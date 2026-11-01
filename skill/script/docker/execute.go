package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

type waitResult struct {
	code int
	oom  bool
	err  error
}
type logResult struct {
	stdout, stderr string
	err            error
}

// Execute runs a registered entrypoint in a fresh temporary container. It returns
// bounded partial results on errors and never falls back to host execution.
func (e *Executor) Execute(ctx context.Context, req script.Request) (result script.Result, returnErr error) {
	started := time.Now()
	req.Args = append([]string(nil), req.Args...)
	result = script.Result{ExitCode: -1, Files: []script.File{}, WorkspaceID: req.Workspace.ID}
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if returnErr != nil {
			var coded *script.Error
			if errors.As(returnErr, &coded) {
				result.Code = coded.Code
			} else {
				result.Code = script.CodeContainer
				returnErr = script.Failure(result.Code, returnErr)
			}
		} else {
			result.Success = true
			result.Code = script.CodeOK
		}
	}()
	ctx, done, err := e.begin(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	timeout := e.cfg.Timeout
	if req.Timeout > 0 && req.Timeout < timeout {
		timeout = req.Timeout
	}
	task, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := script.ValidateArgs(req.Args); err != nil {
		return result, err
	}
	approval, ok := e.approvals[req.SkillID]
	if !ok {
		return result, script.Failure(script.CodePermission, script.ErrPermission)
	}
	if req.SkillFingerprint != approval.Metadata.Fingerprint {
		return result, script.Failure(script.CodeDrift, script.ErrDrift)
	}
	entry, ok := approval.Scripts[req.ScriptPath]
	if !ok || !script.ValidPath(req.ScriptPath) {
		return result, script.Failure(script.CodePermission, script.ErrPermission)
	}
	if entry.ValidateArgs != nil {
		if err := entry.ValidateArgs(append([]string(nil), req.Args...)); err != nil {
			return result, script.Failure(script.CodeInvalidArgs, script.ErrPermission)
		}
	}
	root, err := e.workspacePath(task, req.Workspace)
	if err != nil {
		return result, err
	}
	if err := acquire(task, e.slots); err != nil {
		return result, err
	}
	defer func() { <-e.slots }()
	lock := e.workspaceLock(req.Workspace.ID)
	if err := acquire(task, lock); err != nil {
		return result, err
	}
	defer func() { <-lock }()
	if _, err := e.scanWorkspace(task, root); err != nil {
		return result, err
	}
	snapshot, err := e.snapshot(task, approval)
	if err != nil {
		return result, err
	}
	snapshotOwned := false
	defer func() {
		if !snapshotOwned {
			if err := removeSnapshot(snapshot); err != nil && returnErr == nil {
				returnErr = script.Failure(script.CodeCleanup, err)
			}
		}
	}()
	image, err := e.ensureImage(task, approval.Image)
	if err != nil {
		return result, err
	}
	spec := e.containerSpec(req, approval.Env, image, snapshot, root)
	name, err := randomID()
	if err != nil {
		return result, err
	}
	spec.Name = "zhizhi-script-" + name
	id, createErr := e.engine.Create(task, spec)
	if id == "" {
		id = spec.Name
	}
	e.mu.Lock()
	e.owned[id] = ownedContainer{snapshot: snapshot}
	e.mu.Unlock()
	snapshotOwned = true
	// Cleanup has its own deadline so a canceled request cannot skip removal.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), e.cfg.CleanupTimeout)
		err := e.engine.Remove(cleanup, id)
		stop()
		if err != nil {
			returnErr = script.Failure(script.CodeCleanup, errors.Join(returnErr, err))
			return
		}
		e.mu.Lock()
		delete(e.owned, id)
		e.mu.Unlock()
		snapshotOwned = false
	}()
	if createErr != nil {
		return result, containerFailure(task, createErr)
	}
	logs, err := e.engine.Logs(task, id)
	if err != nil {
		return result, containerFailure(task, err)
	}
	defer logs.Close()
	result.Started = true // A start transport failure has an ambiguous execution outcome.
	if err := e.engine.Start(task, id); err != nil {
		return result, containerFailure(task, err)
	}
	logDone := make(chan logResult, 1)
	go func() { logDone <- readLogs(logs, e.cfg.LogMaxBytes) }()
	waitDone := make(chan waitResult, 1)
	go func() { code, oom, err := e.engine.Wait(task, id); waitDone <- waitResult{code, oom, err} }()
	defer func() { cancel(); _ = logs.Close() }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	logChannel := logDone
	waitChannel := waitDone
	var executionErr error
	for waitChannel != nil || logChannel != nil {
		select {
		case waited := <-waitChannel:
			waitChannel = nil
			result.ExitCode = waited.code
			if waited.err != nil {
				if executionErr == nil {
					if task.Err() != nil {
						executionErr = contextFailure(task.Err())
					} else {
						executionErr = script.Failure(script.CodeContainer, waited.err)
					}
				}
			} else if waited.oom {
				executionErr = script.Failure(script.CodeResourceLimit, script.ErrLimit)
			} else if waited.code != 0 {
				executionErr = script.Failure(script.CodeScriptFailed, nil)
			}
			if waited.err != nil {
				cancel()
				_ = logs.Close()
			}
		case logged := <-logChannel:
			logChannel = nil
			result.Stdout, result.Stderr = logged.stdout, logged.stderr
			if logged.err != nil {
				if executionErr == nil {
					if task.Err() != nil {
						executionErr = contextFailure(task.Err())
					} else {
						executionErr = logged.err
					}
				}
				cancel()
			}
		case <-task.Done():
			if executionErr == nil {
				executionErr = contextFailure(task.Err())
			}
			cancel()
			_ = logs.Close()
			if waitChannel != nil {
				<-waitChannel
				waitChannel = nil
			}
			if logChannel != nil {
				logged := <-logChannel
				result.Stdout, result.Stderr = logged.stdout, logged.stderr
				logChannel = nil
			}
		case <-ticker.C:
			if executionErr != nil {
				continue
			}
			if _, err := e.scanWorkspace(task, root); err != nil {
				executionErr = err
				cancel()
				_ = logs.Close()
			}
		}
	}
	// Stop the process before final file inspection on error, rather than trust a
	// still-running container while validating output paths.
	if executionErr != nil {
		cancel()
	}
	if task.Err() != nil && executionErr == nil {
		executionErr = contextFailure(task.Err())
	}
	if executionErr == nil || result.ExitCode >= 0 {
		files, err := e.scanWorkspace(ctx, root)
		if err != nil {
			if executionErr == nil {
				executionErr = err
			}
		} else {
			result.Files = files
		}
	}
	return result, executionErr
}

func containerFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return contextFailure(ctx.Err())
	}
	return script.Failure(script.CodeContainer, err)
}

func (e *Executor) containerSpec(req script.Request, env map[string]string, image, snapshot, root string) ContainerSpec {
	uid, gid := os.Getuid(), os.Getgid()
	if uid <= 0 {
		uid, gid = 65532, 65532
	}
	environment := map[string]string{"HOME": "/tmp", "PYTHONNOUSERSITE": "1", "PYTHONDONTWRITEBYTECODE": "1", "TMPDIR": "/tmp"}
	for k, v := range env {
		environment[k] = v
	}
	keys := make([]string, 0, len(environment))
	for k := range environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, k := range keys {
		values = append(values, k+"="+environment[k])
	}
	network := container.NetworkMode("none")
	if e.cfg.NetworkEnabled {
		network = "bridge"
	}
	init := true
	pids := e.cfg.PIDsLimit
	memory := e.cfg.MemoryMB << 20
	return ContainerSpec{
		Config: &container.Config{
			Image: image, User: strconv.Itoa(uid) + ":" + strconv.Itoa(gid),
			WorkingDir: "/workspace", Entrypoint: []string{"python3", "-B", "-u", "/skill/" + req.ScriptPath},
			Cmd: append([]string(nil), req.Args...), Env: values, NetworkDisabled: !e.cfg.NetworkEnabled,
			Labels: map[string]string{"io.zhizhi.skill-script": "true", "io.zhizhi.workspace": req.Workspace.ID},
		},
		Host: &container.HostConfig{
			ReadonlyRootfs: true, NetworkMode: network, CapDrop: []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges:true"}, Init: &init,
			Resources: container.Resources{Memory: memory, MemorySwap: memory, NanoCPUs: int64(e.cfg.CPUs * 1e9), PidsLimit: &pids},
			Tmpfs:     map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=16777216,mode=1777"},
			// Live attach owns bounded output collection; do not retain a second,
			// potentially secret-bearing log stream on the Docker daemon's disk.
			LogConfig: container.LogConfig{Type: "none"},
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: snapshot, Target: "/skill", ReadOnly: true},
				{Type: mount.TypeBind, Source: filepath.Join(root, "work"), Target: "/workspace"},
				{Type: mount.TypeBind, Source: filepath.Join(root, "output"), Target: "/output"},
			},
		},
	}
}

// readLogs demultiplexes Docker frames without allocating advertised frame sizes.
// The combined stdout/stderr budget bounds retained memory and stops excess logs.
func readLogs(reader io.Reader, limit int64) logResult {
	var stdout, stderr bytes.Buffer
	remaining := limit
	result := func(err error) logResult { return logResult{stdout.String(), stderr.String(), err} }
	for {
		var header [8]byte
		_, err := io.ReadFull(reader, header[:])
		if err == io.EOF {
			return result(nil)
		}
		if err != nil {
			return result(script.Failure(script.CodeContainer, err))
		}
		length := int64(binary.BigEndian.Uint32(header[4:]))
		var writer io.Writer
		switch header[0] {
		case 1:
			writer = &stdout
		case 2:
			writer = &stderr
		default:
			return result(script.Failure(script.CodeContainer, nil))
		}
		amount := length
		if amount > remaining {
			amount = remaining
		}
		if _, err := io.CopyN(writer, reader, amount); err != nil {
			return result(script.Failure(script.CodeContainer, err))
		}
		remaining -= amount
		if amount < length {
			return result(script.Failure(script.CodeLogLimit, script.ErrLimit))
		}
	}
}
