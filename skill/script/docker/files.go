package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
)

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// CreateWorkspace generates an unguessable task identifier. The owner is a
// trusted run identity, not a user-controlled directory or model argument.
func (e *Executor) CreateWorkspace(ctx context.Context, owner string) (script.Workspace, error) {
	ctx, done, err := e.begin(ctx)
	if err != nil {
		return script.Workspace{}, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return script.Workspace{}, contextFailure(err)
	}
	if owner == "" || len(owner) > 256 {
		return script.Workspace{}, script.Failure(script.CodePermission, script.ErrPermission)
	}
	id, err := randomID()
	if err != nil {
		return script.Workspace{}, err
	}
	w := script.Workspace{ID: id, Owner: owner}
	root := filepath.Join(e.root, "workspaces", id)
	if err = os.Mkdir(root, 0700); err != nil {
		return script.Workspace{}, script.Failure(script.CodePermission, err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(root)
		}
	}()
	for _, dir := range []string{"work", "work/input", "output"} {
		if err = os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			return script.Workspace{}, script.Failure(script.CodePermission, err)
		}
		if err = os.Chmod(filepath.Join(root, dir), 0777); err != nil {
			return script.Workspace{}, script.Failure(script.CodePermission, err)
		}
	}
	b, _ := json.Marshal(w)
	if err = os.WriteFile(filepath.Join(root, "owner.json"), b, 0600); err != nil {
		return script.Workspace{}, script.Failure(script.CodePermission, err)
	}
	success = true
	return w, nil
}

func (e *Executor) workspacePath(ctx context.Context, w script.Workspace) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", contextFailure(err)
	}
	if !validID(w.ID) || w.Owner == "" {
		return "", script.Failure(script.CodePermission, script.ErrPermission)
	}
	root := filepath.Join(e.root, "workspaces", w.ID)
	for _, p := range []string{root, filepath.Join(root, "work"), filepath.Join(root, "output")} {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", script.Failure(script.CodePermission, script.ErrPermission)
		}
	}
	marker := filepath.Join(root, "owner.json")
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", script.Failure(script.CodePermission, script.ErrPermission)
	}
	bytes, err := os.ReadFile(marker)
	if err != nil {
		return "", script.Failure(script.CodePermission, err)
	}
	var recorded script.Workspace
	if json.Unmarshal(bytes, &recorded) != nil || recorded != w {
		return "", script.Failure(script.CodePermission, script.ErrPermission)
	}
	return root, nil
}

// ValidateWorkspace permits checkpoint restoration without granting access to
// another task. Its protected owner marker survives executor process restarts.
func (e *Executor) ValidateWorkspace(ctx context.Context, w script.Workspace) error {
	ctx, done, err := e.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, err = e.workspacePath(ctx, w)
	return err
}

func (e *Executor) workspaceLock(id string) chan struct{} {
	value, _ := e.workspaceLocks.LoadOrStore(id, make(chan struct{}, 1))
	return value.(chan struct{})
}
func acquire(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return contextFailure(ctx.Err())
	}
}

// ReleaseWorkspace applies the configured terminal-task retention policy.
func (e *Executor) ReleaseWorkspace(ctx context.Context, w script.Workspace) error {
	if !e.cfg.CleanupOnRelease {
		return e.ValidateWorkspace(ctx, w)
	}
	return e.DeleteWorkspace(ctx, w)
}

// DeleteWorkspace explicitly removes this task's data. Call only after consumers
// have retrieved output files; no public base directory or image is deleted.
func (e *Executor) DeleteWorkspace(ctx context.Context, w script.Workspace) error {
	ctx, done, err := e.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	lock := e.workspaceLock(w.ID)
	if err := acquire(ctx, lock); err != nil {
		return err
	}
	defer func() { <-lock }()
	root, err := e.workspacePath(ctx, w)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return script.Failure(script.CodeCleanup, err)
	}
	return nil
}

// OpenFile is for trusted application retrieval using an owner-bound reference.
// Only regular, single-link output files can be opened; callers close the reader.
func (e *Executor) OpenFile(ctx context.Context, w script.Workspace, path string) (io.ReadCloser, error) {
	ctx, done, err := e.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	lock := e.workspaceLock(w.ID)
	if err := acquire(ctx, lock); err != nil {
		return nil, err
	}
	held := true
	defer func() {
		if held {
			<-lock
		}
	}()
	if !script.ValidPath(path) || !strings.HasPrefix(path, "output/") {
		return nil, script.Failure(script.CodePermission, script.ErrPermission)
	}
	root, err := e.workspacePath(ctx, w)
	if err != nil {
		return nil, err
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, script.Failure(script.CodePermission, err)
	}
	defer confined.Close()
	info, err := confined.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || multipleLinks(info) {
		return nil, script.Failure(script.CodePermission, script.ErrPermission)
	}
	f, err := openConfined(confined, path, false)
	if err != nil {
		return nil, script.Failure(script.CodePermission, err)
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Size() > e.cfg.OutputMaxBytes {
		f.Close()
		return nil, script.Failure(script.CodePermission, script.ErrPermission)
	}
	held = false
	return &workspaceReader{ReadCloser: f, release: func() { <-lock }}, nil
}

type workspaceReader struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (r *workspaceReader) Close() error {
	var err error
	r.once.Do(func() { err = r.ReadCloser.Close(); r.release() })
	return err
}

// Native stat metadata differs across platforms. A link count, where available,
// catches hardlinks without ever resolving them outside the managed directories.
func multipleLinks(info os.FileInfo) bool {
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	n := v.FieldByName("Nlink")
	return n.IsValid() && n.CanUint() && n.Uint() > 1
}

func (e *Executor) scanWorkspace(ctx context.Context, root string) ([]script.File, error) {
	files := []script.File{}
	var total, output int64
	count := 0
	for _, area := range []string{"work", "output"} {
		confined, err := os.OpenRoot(filepath.Join(root, area))
		if err != nil {
			return nil, script.Failure(script.CodePermission, err)
		}
		var visit func(string) error
		visit = func(relative string) error {
			dir, err := openConfined(confined, relative, true)
			if err != nil {
				return script.Failure(script.CodePermission, err)
			}
			defer dir.Close()
			for {
				entries, readErr := dir.ReadDir(64)
				for _, entry := range entries {
					if err := ctx.Err(); err != nil {
						return contextFailure(err)
					}
					count++
					if count > e.cfg.MaxFiles {
						return script.Failure(script.CodeWorkspaceLimit, script.ErrLimit)
					}
					p := filepath.Join(relative, entry.Name())
					info, err := confined.Lstat(p)
					if err != nil || info.Mode()&os.ModeSymlink != 0 {
						return script.Failure(script.CodePermission, script.ErrPermission)
					}
					if info.IsDir() {
						if err := visit(p); err != nil {
							return err
						}
						continue
					}
					if !info.Mode().IsRegular() || multipleLinks(info) {
						return script.Failure(script.CodePermission, script.ErrPermission)
					}
					size := info.Size()
					if size > e.cfg.WorkspaceMaxBytes-total {
						return script.Failure(script.CodeWorkspaceLimit, script.ErrLimit)
					}
					total += size
					if area == "output" {
						if size > e.cfg.OutputMaxBytes-output {
							return script.Failure(script.CodeOutputLimit, script.ErrLimit)
						}
						output += size
						logical := "output/" + filepath.ToSlash(p)
						if !script.ValidPath(logical) {
							return script.Failure(script.CodePermission, script.ErrPermission)
						}
						files = append(files, script.File{Name: info.Name(), Path: logical, Size: size})
					}
				}
				if readErr == io.EOF {
					return nil
				}
				if readErr != nil {
					return script.Failure(script.CodeWorkspaceLimit, readErr)
				}
			}
		}
		err = visit(".")
		confined.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (e *Executor) snapshot(ctx context.Context, approval script.Approval) (string, error) {
	loaded, err := e.catalog.Load(ctx, approval.Metadata.Name, approval.Metadata.Fingerprint)
	if err != nil {
		return "", script.Failure(script.CodeDrift, script.ErrDrift)
	}
	root, err := os.MkdirTemp(filepath.Join(e.root, "snapshots"), "run-")
	if err != nil {
		return "", script.Failure(script.CodePermission, err)
	}
	success := false
	defer func() {
		if !success {
			_ = removeSnapshot(root)
		}
	}()
	confined, err := os.OpenRoot(loaded.Dir)
	if err != nil {
		return "", script.Failure(script.CodePermission, err)
	}
	defer confined.Close()
	total := int64(0)
	for path, hash := range approval.Files {
		if err := ctx.Err(); err != nil {
			return "", contextFailure(err)
		}
		// Reject symlinks at every component and hardlinks before copying. OpenRoot
		// additionally prevents traversal if a deployed file changes during a read.
		parts := strings.Split(path, "/")
		prefix := ""
		for _, part := range parts {
			prefix = filepath.Join(prefix, part)
			info, err := confined.Lstat(prefix)
			if errors.Is(err, os.ErrNotExist) {
				return "", script.Failure(script.CodeScriptMissing, err)
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return "", script.Failure(script.CodePermission, script.ErrPermission)
			}
		}
		f, err := openConfined(confined, path, false)
		if err != nil {
			return "", script.Failure(script.CodeScriptMissing, err)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || multipleLinks(info) {
			f.Close()
			return "", script.Failure(script.CodePermission, script.ErrPermission)
		}
		bytes, err := io.ReadAll(io.LimitReader(f, (32<<20)-total+1))
		f.Close()
		if err != nil {
			return "", script.Failure(script.CodePermission, err)
		}
		total += int64(len(bytes))
		if total > 32<<20 {
			return "", script.Failure(script.CodePermission, script.ErrLimit)
		}
		if script.Hash(bytes) != hash {
			return "", script.Failure(script.CodeDrift, script.ErrDrift)
		}
		destination := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return "", script.Failure(script.CodePermission, err)
		}
		if err := os.WriteFile(destination, bytes, 0444); err != nil {
			return "", script.Failure(script.CodePermission, err)
		}
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0555)
		}
		return nil
	}); err != nil {
		return "", script.Failure(script.CodePermission, err)
	}
	success = true
	return root, nil
}

func removeSnapshot(root string) error {
	if root == "" {
		return nil
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0700)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(root)
}
