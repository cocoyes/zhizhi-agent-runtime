package docker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
	"github.com/moby/moby/client"
)

func TestDockerIntegration(t *testing.T) {
	if os.Getenv("ZHIZHI_DOCKER_TEST") != "1" {
		t.Skip("set ZHIZHI_DOCKER_TEST=1 to run actual Docker containers")
	}
	if !safeFileOpening {
		t.Skip("Docker bind execution requires a supported Unix host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	body := `import json, os, socket, sys
assert os.geteuid() != 0
assert not os.path.exists('/var/run/docker.sock')
try:
    open('/skill/scripts/hello.py', 'a')
    raise AssertionError('Skill mount is writable')
except OSError:
    pass
try:
    socket.create_connection(('1.1.1.1', 443), timeout=0.2)
    raise AssertionError('Network is enabled')
except OSError:
    pass
with open('/output/report.txt', 'w') as output:
    output.write('report')
print(json.dumps({'message': 'Hello from zhizhi-agent-runtime', 'success': True}))
print('diagnostic', file=sys.stderr)
`
	files := map[string]string{"SKILL.md": "---\nname: test-skill\ndescription: Execute audited Python\n---\nRun scripts/hello.py", "scripts/hello.py": body, "scripts/fail.py": "import sys\nprint('bad', file=sys.stderr)\nsys.exit(7)", "scripts/slow.py": "import time\ntime.sleep(60)", "scripts/noisy.py": "print('x' * (2 * 1024 * 1024))", "scripts/memory.py": "data = bytearray(256 * 1024 * 1024)"}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalog := skill.NewCatalog(skill.CatalogOptions{})
	if _, err := catalog.LoadRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("ZHIZHI_DOCKER_IMAGE")
	registration := script.Registration{SkillID: "test-skill", Scripts: []script.EntryPoint{{Path: "scripts/hello.py"}, {Path: "scripts/fail.py"}, {Path: "scripts/slow.py"}, {Path: "scripts/noisy.py"}, {Path: "scripts/memory.py"}}}
	executor, err := New(ctx, Config{Enabled: true, Catalog: catalog, Image: image, AutoPull: true, WorkspaceRoot: t.TempDir(), Registrations: []script.Registration{registration}})
	if err != nil {
		var coded *script.Error
		if errors.As(err, &coded) && coded.Code == script.CodeDockerUnavailable {
			t.Skipf("Docker Engine unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer func() {
		if err := executor.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	workspace, err := executor.CreateWorkspace(ctx, "integration-task")
	if err != nil {
		t.Fatal(err)
	}
	req := script.Request{SkillID: "test-skill", SkillFingerprint: catalog.Metadata()[0].Fingerprint, ScriptPath: "scripts/hello.py", Workspace: workspace}
	result, err := executor.Execute(ctx, req)
	if err != nil {
		t.Fatalf("%+v %v", result, err)
	}
	var output struct {
		Message string `json:"message"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &output); err != nil || !output.Success || output.Message != "Hello from zhizhi-agent-runtime" || result.Stderr != "diagnostic\n" || len(result.Files) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	req.ScriptPath = "scripts/fail.py"
	result, err = executor.Execute(ctx, req)
	if err == nil || result.ExitCode != 7 || result.Code != script.CodeScriptFailed {
		t.Fatalf("%+v %v", result, err)
	}
	req.ScriptPath = "scripts/slow.py"
	req.Timeout = 300 * time.Millisecond
	result, err = executor.Execute(ctx, req)
	if err == nil || result.Code != script.CodeTimeout {
		t.Fatalf("%+v %v", result, err)
	}
	req.Timeout = 0
	userCtx, stop := context.WithCancel(ctx)
	stop()
	result, err = executor.Execute(userCtx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%+v %v", result, err)
	}
	sdk := executor.engine.(*sdkEngine)
	// Cancel only after the actual process is running, then verify removal.
	activeCtx, stopActive := context.WithCancel(ctx)
	defer stopActive()
	type completion struct {
		result script.Result
		err    error
	}
	done := make(chan completion, 1)
	go func() { r, err := executor.Execute(activeCtx, req); done <- completion{r, err} }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	running := false
	for !running {
		select {
		case completed := <-done:
			t.Fatalf("execution stopped before cancellation: %+v %v", completed.result, completed.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			containers, err := sdk.client.ContainerList(ctx, client.ContainerListOptions{Filters: client.Filters{"label": map[string]bool{"io.zhizhi.workspace=" + workspace.ID: true}, "status": map[string]bool{"running": true}}})
			if err != nil {
				t.Fatal(err)
			}
			running = len(containers.Items) > 0
		}
	}
	stopActive()
	canceled := <-done
	if !errors.Is(canceled.err, context.Canceled) || canceled.result.Code != script.CodeCanceled {
		t.Fatalf("active cancellation: %+v %v", canceled.result, canceled.err)
	}
	req.ScriptPath = "scripts/noisy.py"
	result, err = executor.Execute(ctx, req)
	if err == nil || result.Code != script.CodeLogLimit || len(result.Stdout)+len(result.Stderr) > 1<<20 {
		t.Fatalf("unbounded logs: %+v %v", result, err)
	}
	// A small separately configured sandbox validates a real OOM termination.
	memoryExecutor, err := New(ctx, Config{Enabled: true, Catalog: catalog, Image: image, AutoPull: true, MemoryMB: 64, WorkspaceRoot: t.TempDir(), Registrations: []script.Registration{registration}})
	if err != nil {
		t.Fatal(err)
	}
	defer memoryExecutor.Close(context.Background())
	memoryWorkspace, err := memoryExecutor.CreateWorkspace(ctx, "memory-task")
	if err != nil {
		t.Fatal(err)
	}
	memoryReq := req
	memoryReq.ScriptPath = "scripts/memory.py"
	memoryReq.Workspace = memoryWorkspace
	result, err = memoryExecutor.Execute(ctx, memoryReq)
	if err == nil || result.Code != script.CodeResourceLimit {
		t.Fatalf("memory limit not observed: %+v %v", result, err)
	}
	remaining, err := sdk.client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{"label": map[string]bool{"io.zhizhi.workspace=" + workspace.ID: true}}})
	if err != nil || len(remaining.Items) != 0 {
		t.Fatalf("owned containers leaked: %+v %v", remaining, err)
	}
}
