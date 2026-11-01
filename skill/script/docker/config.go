package docker

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
)

// Config is trusted application configuration, never model input. Enabling it
// checks Docker connectivity but does not pull images until first use.
type Config struct {
	Enabled           bool
	Catalog           *skill.Catalog
	Registrations     []script.Registration
	Image             string
	AutoPull          bool
	MemoryMB          int64
	CPUs              float64
	PIDsLimit         int64
	MaxParallel       int
	Timeout           time.Duration
	ConnectTimeout    time.Duration
	CleanupTimeout    time.Duration
	NetworkEnabled    bool
	WorkspaceRoot     string
	LogMaxBytes       int64
	WorkspaceMaxBytes int64
	OutputMaxBytes    int64
	MaxFiles          int
	// CleanupOnRelease deletes task data when the Agent completes. Default false
	// retains outputs for trusted application retrieval and explicit deletion.
	CleanupOnRelease bool
	// Engine is optional dependency injection; nil selects the official SDK.
	Engine Engine
}

type imageFlight struct {
	done chan struct{}
	id   string
	err  error
}
type ownedContainer struct{ snapshot string }

// Executor owns a client and disposable containers, not the Docker daemon or
// shared images. Close cancels active executions and waits for cleanup.
type Executor struct {
	cfg            Config
	catalog        *skill.Catalog
	approvals      map[string]script.Approval
	engine         Engine
	root           string
	fingerprint    string
	slots          chan struct{}
	workspaceLocks sync.Map
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	closed         bool
	active         sync.WaitGroup
	closing        chan struct{}
	closeErr       error
	images         map[string]*imageFlight
	owned          map[string]ownedContainer
}

// New validates trust manifests and checks Docker only when explicitly enabled.
// WorkspaceRoot must be private to the service; no host environment enters tasks.
func New(ctx context.Context, cfg Config) (*Executor, error) {
	if !cfg.Enabled {
		return nil, script.Failure(script.CodeDisabled, nil)
	}
	if !safeFileOpening {
		return nil, script.Failure(script.CodeUnsupportedHost, nil)
	}
	if cfg.Catalog == nil || len(cfg.Registrations) == 0 {
		return nil, script.Failure(script.CodePermission, script.ErrPermission)
	}
	if cfg.Image == "" {
		cfg.Image = "python:3.12-slim"
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = 384
	}
	if cfg.CPUs == 0 {
		cfg.CPUs = .5
	}
	if cfg.PIDsLimit == 0 {
		cfg.PIDsLimit = 64
	}
	if cfg.MaxParallel == 0 {
		cfg.MaxParallel = 1
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.CleanupTimeout == 0 {
		cfg.CleanupTimeout = 10 * time.Second
	}
	if cfg.LogMaxBytes == 0 {
		cfg.LogMaxBytes = 1 << 20
	}
	if cfg.WorkspaceMaxBytes == 0 {
		cfg.WorkspaceMaxBytes = 100 << 20
	}
	if cfg.OutputMaxBytes == 0 {
		cfg.OutputMaxBytes = 50 << 20
	}
	if cfg.MaxFiles == 0 {
		cfg.MaxFiles = 128
	}
	if cfg.MemoryMB < 16 || cfg.MemoryMB > 1<<40 || cfg.CPUs <= 0 || math.IsNaN(cfg.CPUs) || math.IsInf(cfg.CPUs, 0) || cfg.CPUs > 1024 || cfg.PIDsLimit < 1 || cfg.MaxParallel < 1 || cfg.Timeout <= 0 || cfg.ConnectTimeout <= 0 || cfg.CleanupTimeout <= 0 || cfg.LogMaxBytes < 1 || cfg.WorkspaceMaxBytes < 1 || cfg.OutputMaxBytes < 1 || cfg.MaxFiles < 1 {
		return nil, script.Failure(script.CodePermission, script.ErrPermission)
	}
	e := &Executor{cfg: cfg, catalog: cfg.Catalog, approvals: map[string]script.Approval{}, images: map[string]*imageFlight{}, owned: map[string]ownedContainer{}, slots: make(chan struct{}, cfg.MaxParallel), closing: make(chan struct{})}
	for _, registration := range cfg.Registrations {
		approval, err := script.Approve(ctx, cfg.Catalog, registration)
		if err != nil {
			return nil, err
		}
		if _, ok := e.approvals[registration.SkillID]; ok {
			return nil, script.Failure(script.CodePermission, script.ErrPermission)
		}
		for path := range approval.Scripts {
			if !strings.HasSuffix(path, ".py") {
				return nil, script.Failure(script.CodePermission, script.ErrPermission)
			}
		}
		if approval.Image == "" {
			approval.Image = cfg.Image
		}
		e.approvals[registration.SkillID] = approval
	}
	// Include every effective trust/resource option in checkpoint authorization.
	// Host paths and SDK injection are not authority to execute different code.
	policy := struct {
		Approvals              map[string]script.Approval
		Memory                 int64
		CPU                    float64
		PIDs                   int64
		Parallel               int
		Timeout                time.Duration
		Network                bool
		Log, Workspace, Output int64
		Files                  int
	}{e.approvals, cfg.MemoryMB, cfg.CPUs, cfg.PIDsLimit, cfg.MaxParallel, cfg.Timeout, cfg.NetworkEnabled, cfg.LogMaxBytes, cfg.WorkspaceMaxBytes, cfg.OutputMaxBytes, cfg.MaxFiles}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, script.Failure(script.CodePermission, err)
	}
	e.fingerprint = script.Hash(encoded)
	engine := cfg.Engine
	if engine == nil {
		engine, err = newEngine()
		if err != nil {
			return nil, script.Failure(script.CodeDockerUnavailable, err)
		}
	}
	check, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	err = engine.Ping(check)
	err = classifyEngineError(err)
	cancel()
	if err != nil {
		_ = engine.Close()
		var coded *script.Error
		if errors.As(err, &coded) && coded.Code == script.CodePermission {
			return nil, err
		}
		return nil, script.Failure(script.CodeDockerUnavailable, err)
	}
	e.engine = engine
	root := cfg.WorkspaceRoot
	if root == "" {
		root, err = os.MkdirTemp("", "zhizhi-scripts-")
	} else {
		root, err = filepath.Abs(root)
		if err == nil && root == filepath.VolumeName(root)+string(filepath.Separator) {
			err = errors.New("unsafe workspace root")
		}
		if err == nil {
			// Treat the configured directory as a parent. Own a private child
			// instead of requiring or changing the parent's existing permissions.
			root = filepath.Join(root, ".zhizhi-scripts")
			err = os.MkdirAll(root, 0700)
		}
	}
	if err == nil {
		if info, statErr := os.Lstat(root); statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			err = errors.New("unsafe workspace root")
		}
	}
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err == nil {
		info, statErr := os.Stat(root)
		err = statErr
		if err == nil && (!info.IsDir() || info.Mode().Perm()&0077 != 0) {
			err = errors.New("workspace root must be private")
		}
	}
	if err == nil {
		for _, name := range []string{"workspaces", "snapshots"} {
			p := filepath.Join(root, name)
			if err = os.MkdirAll(p, 0700); err != nil {
				break
			}
			info, statErr := os.Lstat(p)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				err = errors.New("unsafe workspace layout")
				break
			}
		}
	}
	if err != nil {
		_ = engine.Close()
		return nil, script.Failure(script.CodePermission, err)
	}
	e.root = root
	e.ctx, e.cancel = context.WithCancel(context.Background())
	return e, nil
}

// Fingerprint binds suspended calls to exact script approvals and runtime limits.
func (e *Executor) Fingerprint() string { return e.fingerprint }

// WorkspaceRoot exposes the managed root only to trusted application code.
func (e *Executor) WorkspaceRoot() string { return e.root }

func (e *Executor) begin(ctx context.Context) (context.Context, func(), error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, nil, script.ErrClosed
	}
	e.active.Add(1)
	e.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.ctx, cancel)
	return child, func() { stop(); cancel(); e.active.Done() }, nil
}

// EnsureImage inspects first. Only a specific not-found result permits pulling;
// concurrent callers share one inspect/pull operation for the same image.
func (e *Executor) EnsureImage(ctx context.Context, image string) error {
	ctx, done, err := e.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, err = e.ensureImage(ctx, image)
	return err
}
func (e *Executor) ensureImage(ctx context.Context, image string) (string, error) {
	e.mu.Lock()
	if flight := e.images[image]; flight != nil {
		e.mu.Unlock()
		select {
		case <-flight.done:
			return flight.id, flight.err
		case <-ctx.Done():
			return "", contextFailure(ctx.Err())
		}
	}
	flight := &imageFlight{done: make(chan struct{})}
	e.images[image] = flight
	e.mu.Unlock()
	id, err := e.engine.ImageInspect(ctx, image)
	err = classifyEngineError(err)
	if errors.Is(err, ErrImageNotFound) {
		if !e.cfg.AutoPull {
			err = script.Failure(script.CodeImageMissing, err)
		} else if pullErr := e.engine.ImagePull(ctx, image); pullErr != nil {
			err = script.Failure(script.CodeImagePull, pullErr)
		} else {
			id, err = e.engine.ImageInspect(ctx, image)
			if err != nil {
				err = script.Failure(script.CodeImagePull, err)
			}
		}
	} else if err != nil {
		var coded *script.Error
		if !errors.As(err, &coded) || coded.Code != script.CodePermission {
			err = script.Failure(script.CodeDockerUnavailable, err)
		}
	}
	if ctx.Err() != nil {
		err = contextFailure(ctx.Err())
	}
	if err == nil && id == "" {
		err = script.Failure(script.CodeDockerUnavailable, nil)
	}
	e.mu.Lock()
	flight.id, flight.err = id, err
	delete(e.images, image)
	close(flight.done)
	e.mu.Unlock()
	return id, err
}

func contextFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return script.Failure(script.CodeTimeout, err)
	}
	return script.Failure(script.CodeCanceled, err)
}

// Close is idempotent and cancellation-aware. Only containers created by this
// executor are removed. Images and retained task outputs are never deleted.
func (e *Executor) Close(ctx context.Context) error {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		e.cancel()
		go func() {
			e.active.Wait()
			var cleanupErr error
			for id, owned := range e.owned {
				cleanup, cancel := context.WithTimeout(context.Background(), e.cfg.CleanupTimeout)
				err := e.engine.Remove(cleanup, id)
				cancel()
				if err != nil {
					cleanupErr = errors.Join(cleanupErr, script.Failure(script.CodeCleanup, err))
				} else {
					if err := removeSnapshot(owned.snapshot); err != nil {
						cleanupErr = errors.Join(cleanupErr, script.Failure(script.CodeCleanup, err))
					}
				}
			}
			if err := e.engine.Close(); err != nil {
				cleanupErr = errors.Join(cleanupErr, script.Failure(script.CodeCleanup, err))
			}
			e.mu.Lock()
			e.closeErr = cleanupErr
			close(e.closing)
			e.mu.Unlock()
		}()
	}
	e.mu.Unlock()
	select {
	case <-e.closing:
		e.mu.Lock()
		err := e.closeErr
		e.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
