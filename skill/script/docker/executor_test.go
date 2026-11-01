package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/skill"
	"github.com/cocoyes/zhizhi-agent-runtime/skill/script"
)

type fakeEngine struct {
	mu                                      sync.Mutex
	exists                                  bool
	inspectErr                              error
	pingErr                                 error
	pullErr                                 error
	createErr, startErr, logsErr, removeErr error
	start                                   func(context.Context) error
	pullCount, created, removed             int
	code                                    int
	output                                  []byte
	spec                                    ContainerSpec
	wait                                    func(context.Context) (int, bool, error)
}

func (f *fakeEngine) Ping(context.Context) error { return f.pingErr }
func (f *fakeEngine) ImageInspect(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return "", f.inspectErr
	}
	if !f.exists {
		return "", ErrImageNotFound
	}
	return "sha256:fixed", nil
}
func (f *fakeEngine) ImagePull(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullCount++
	if f.pullErr != nil {
		return f.pullErr
	}
	f.exists = true
	return nil
}
func (f *fakeEngine) Create(_ context.Context, s ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	f.spec = s
	return fmt.Sprintf("owned-%d", f.created), f.createErr
}
func (f *fakeEngine) Start(ctx context.Context, _ string) error {
	if f.start != nil {
		return f.start(ctx)
	}
	return f.startErr
}
func (f *fakeEngine) Wait(ctx context.Context, _ string) (int, bool, error) {
	if f.wait != nil {
		return f.wait(ctx)
	}
	return f.code, false, nil
}
func (f *fakeEngine) Logs(context.Context, string) (io.ReadCloser, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	return io.NopCloser(bytes.NewReader(f.output)), nil
}
func (f *fakeEngine) Remove(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed++
	return f.removeErr
}
func (f *fakeEngine) Close() error { return nil }
func mux(kind byte, s string) []byte {
	b := make([]byte, 8)
	b[0] = kind
	binary.BigEndian.PutUint32(b[4:], uint32(len(s)))
	return append(b, []byte(s)...)
}
func fixture(t *testing.T) (*skill.Catalog, script.Registration) {
	t.Helper()
	if !safeFileOpening {
		t.Skip("Docker bind execution requires Unix nonblocking file primitives")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"SKILL.md": "---\nname: test\ndescription: test script\n---\nRun scripts/hello.py", "scripts/hello.py": "print('hello')"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := skill.NewCatalog(skill.CatalogOptions{})
	if _, err := c.LoadRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return c, script.Registration{SkillID: "test", Scripts: []script.EntryPoint{{Path: "scripts/hello.py"}}}
}
func newFake(t *testing.T, f *fakeEngine) (*Executor, script.Workspace, script.Request) {
	t.Helper()
	c, r := fixture(t)
	e, err := New(context.Background(), Config{Enabled: true, Catalog: c, Registrations: []script.Registration{r}, Engine: f, WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	w, err := e.CreateWorkspace(context.Background(), "task-owner")
	if err != nil {
		t.Fatal(err)
	}
	return e, w, script.Request{SkillID: "test", SkillFingerprint: c.Metadata()[0].Fingerprint, ScriptPath: "scripts/hello.py", Workspace: w}
}

func TestImagePreparationDistinguishesErrorsAndDeduplicates(t *testing.T) {
	for _, tc := range []struct {
		name                string
		exists, auto        bool
		inspectErr, pullErr error
		want                script.Code
		pulls               int
	}{
		{name: "reuse", exists: true}, {name: "pull", auto: true, pulls: 1}, {name: "missing", want: script.CodeImageMissing}, {name: "inspect", auto: true, inspectErr: errors.New("permission denied /sensitive"), want: script.CodeDockerUnavailable}, {name: "pull-error", auto: true, pullErr: errors.New("token secret"), want: script.CodeImagePull, pulls: 1},
		{name: "inspect-permission", auto: true, inspectErr: os.ErrPermission, want: script.CodePermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeEngine{exists: tc.exists, inspectErr: tc.inspectErr, pullErr: tc.pullErr}
			c, r := fixture(t)
			e, err := New(context.Background(), Config{Enabled: true, Catalog: c, Registrations: []script.Registration{r}, Engine: f, AutoPull: tc.auto, WorkspaceRoot: t.TempDir()})
			if err != nil {
				t.Fatalf("%v cause=%v", err, errors.Unwrap(err))
			}
			defer e.Close(context.Background())
			err = e.EnsureImage(context.Background(), "python:3.12-slim")
			var coded *script.Error
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &coded) || coded.Code != tc.want {
				t.Fatalf("%v", err)
			}
			if f.pullCount != tc.pulls {
				t.Fatalf("pulls=%d", f.pullCount)
			}
		})
	}
	f := &fakeEngine{}
	c, r := fixture(t)
	e, err := New(context.Background(), Config{Enabled: true, Catalog: c, Registrations: []script.Registration{r}, Engine: f, AutoPull: true, WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.EnsureImage(context.Background(), "python:3.12-slim"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.pullCount != 1 {
		t.Fatalf("duplicate pulls=%d", f.pullCount)
	}
}

func TestExecutionLogsSecurityAndCleanup(t *testing.T) {
	f := &fakeEngine{exists: true, output: append(mux(1, "hello"), mux(2, "warning")...)}
	e, _, req := newFake(t, f)
	out, err := e.Execute(context.Background(), req)
	if err != nil || !out.Success || out.Stdout != "hello" || out.Stderr != "warning" || out.ExitCode != 0 || !out.Started {
		t.Fatalf("%+v %v", out, err)
	}
	if f.created != 1 || f.removed != 1 {
		t.Fatalf("lifecycle=%d/%d", f.created, f.removed)
	}
	s := f.spec
	if s.Host.Privileged || !s.Host.ReadonlyRootfs || s.Config.User == "0" || s.Config.User == "" || s.Host.NetworkMode != "none" || s.Host.Memory != 384<<20 || s.Host.NanoCPUs != 500000000 || s.Host.PidsLimit == nil || *s.Host.PidsLimit != 64 {
		t.Fatalf("unsafe container: %+v %+v", s.Config, s.Host)
	}
	if len(s.Host.Mounts) != 3 || !s.Host.Mounts[0].ReadOnly || s.Config.Image != "sha256:fixed" {
		t.Fatalf("mount/image config: %+v", s)
	}
	if _, err := e.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if f.pullCount != 0 || f.removed != 2 {
		t.Fatal("image not reused or container leaked")
	}
}

func TestExecutionFailureTimeoutCancelAndLogLimitCleanup(t *testing.T) {
	for _, name := range []string{"exit", "timeout", "cancel", "log-limit", "cancel-start", "timeout-start"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeEngine{exists: true}
			e, _, req := newFake(t, f)
			ctx := context.Background()
			want := script.CodeScriptFailed
			switch name {
			case "cancel-start":
				want = script.CodeCanceled
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.start = func(ctx context.Context) error { cancel(); return ctx.Err() }
			case "timeout-start":
				want = script.CodeTimeout
				req.Timeout = 100 * time.Millisecond
				f.start = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
			case "exit":
				f.code = 7
				f.output = mux(2, "traceback")
			case "timeout":
				req.Timeout = 10 * time.Millisecond
				want = script.CodeTimeout
				f.wait = func(ctx context.Context) (int, bool, error) { <-ctx.Done(); return -1, false, ctx.Err() }
			case "cancel":
				want = script.CodeCanceled
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				f.wait = func(ctx context.Context) (int, bool, error) { cancel(); <-ctx.Done(); return -1, false, ctx.Err() }
			case "log-limit":
				want = script.CodeLogLimit
				f.output = mux(1, string(bytes.Repeat([]byte("x"), (1<<20)+1)))
			}
			out, err := e.Execute(ctx, req)
			var coded *script.Error
			if !errors.As(err, &coded) || coded.Code != want || out.Success || out.Code != want {
				t.Fatalf("%+v %v want=%s", out, err, want)
			}
			if f.removed != 1 {
				t.Fatalf("leaked container: %d", f.removed)
			}
		})
	}
}

func TestUnauthorizedPathAndWorkspaceNeverCreateContainer(t *testing.T) {
	f := &fakeEngine{exists: true}
	e, _, req := newFake(t, f)
	for _, p := range []string{"../../etc/passwd", "/etc/passwd", "scripts/unknown.py", "scripts/../../../secret.py"} {
		r := req
		r.ScriptPath = p
		if _, err := e.Execute(context.Background(), r); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	req.Workspace.Owner = "other-user"
	if _, err := e.Execute(context.Background(), req); err == nil {
		t.Fatal("cross-owner access accepted")
	}
	if f.created != 0 {
		t.Fatal("unauthorized container created")
	}
}

func TestOutputCollectionAndRejection(t *testing.T) {
	for _, name := range []string{"file", "symlink", "hardlink", "size", "live-size", "workspace-size", "count"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeEngine{exists: true}
			e, w, req := newFake(t, f)
			e.cfg.OutputMaxBytes = 32
			e.cfg.WorkspaceMaxBytes = 64
			e.cfg.MaxFiles = 8
			f.wait = func(ctx context.Context) (int, bool, error) {
				output := f.spec.Host.Mounts[2].Source
				switch name {
				case "file":
					if err := os.WriteFile(filepath.Join(output, "report.txt"), []byte("report"), 0644); err != nil {
						t.Error(err)
					}
				case "symlink":
					if err := os.Symlink("/etc/passwd", filepath.Join(output, "escape")); err != nil {
						t.Error(err)
					}
				case "hardlink":
					external := filepath.Join(t.TempDir(), "external")
					if err := os.WriteFile(external, []byte("external"), 0600); err != nil {
						t.Error(err)
					}
					if err := os.Link(external, filepath.Join(output, "escape")); err != nil {
						t.Error(err)
					}
				case "size", "live-size":
					if err := os.WriteFile(filepath.Join(output, "large"), bytes.Repeat([]byte("x"), 33), 0644); err != nil {
						t.Error(err)
					}
					if name == "live-size" {
						<-ctx.Done()
						return -1, false, ctx.Err()
					}
				case "workspace-size":
					if err := os.WriteFile(filepath.Join(f.spec.Host.Mounts[1].Source, "large"), bytes.Repeat([]byte("x"), 65), 0644); err != nil {
						t.Error(err)
					}
				case "count":
					for i := 0; i < 9; i++ {
						if err := os.Mkdir(filepath.Join(output, fmt.Sprint(i)), 0755); err != nil {
							t.Error(err)
						}
					}
				}
				return 0, false, nil
			}
			out, err := e.Execute(context.Background(), req)
			if name == "file" {
				if err != nil || len(out.Files) != 1 || out.Files[0].Path != "output/report.txt" || out.Files[0].Size != 6 {
					t.Fatalf("%+v %v", out, err)
				}
				reader, err := e.OpenFile(context.Background(), w, out.Files[0].Path)
				if err != nil {
					t.Fatal(err)
				}
				content, _ := io.ReadAll(reader)
				reader.Close()
				if string(content) != "report" {
					t.Fatalf("content=%q", content)
				}
			} else {
				if err == nil || out.Success || len(out.Files) > 0 {
					t.Fatalf("unsafe output accepted: %+v %v", out, err)
				}
			}
			if f.removed != 1 {
				t.Fatal("container leaked")
			}
		})
	}
}

func TestApprovalDriftAndSnapshotExcludeUnregisteredFiles(t *testing.T) {
	f := &fakeEngine{exists: true}
	e, _, req := newFake(t, f)
	approval := e.approvals["test"]
	if err := os.WriteFile(filepath.Join(approval.Metadata.Dir, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	f.wait = func(context.Context) (int, bool, error) {
		snapshot := f.spec.Host.Mounts[0].Source
		if _, err := os.Stat(filepath.Join(snapshot, "secret.txt")); !os.IsNotExist(err) {
			t.Error("copied unapproved file")
		}
		return 0, false, nil
	}
	if _, err := e.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	snapshots, err := os.ReadDir(filepath.Join(e.root, "snapshots"))
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("snapshots leaked: %v %v", snapshots, err)
	}
	if err := os.WriteFile(filepath.Join(approval.Metadata.Dir, "scripts/hello.py"), []byte("print('changed')"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := e.Execute(context.Background(), req)
	if !errors.Is(err, script.ErrDrift) || out.Code != script.CodeDrift || f.created != 1 {
		t.Fatalf("%+v %v created=%d", out, err, f.created)
	}
}

func TestContainerErrorsAlwaysRemoveOnlyOwnedContainer(t *testing.T) {
	for _, stage := range []string{"create", "attach", "start", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			f := &fakeEngine{exists: true}
			e, _, req := newFake(t, f)
			failure := errors.New("daemon secret /host/path")
			switch stage {
			case "create":
				f.createErr = failure
			case "attach":
				f.logsErr = failure
			case "start":
				f.startErr = failure
			case "cleanup":
				f.removeErr = failure
			}
			out, err := e.Execute(context.Background(), req)
			if err == nil || out.Success || f.removed != 1 {
				t.Fatalf("%+v %v removed=%d", out, err, f.removed)
			}
			if bytes.Contains([]byte(err.Error()), []byte("secret")) {
				t.Fatal("backend error leaked")
			}
			f.removeErr = nil // Close retries any cleanup that could not reach the daemon.
		})
	}
}

func TestCloseCancelsActiveExecutionAndReleasesSlots(t *testing.T) {
	f := &fakeEngine{exists: true}
	e, _, req := newFake(t, f)
	entered := make(chan struct{})
	f.wait = func(ctx context.Context) (int, bool, error) {
		close(entered)
		<-ctx.Done()
		return -1, false, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := e.Execute(context.Background(), req); done <- err }()
	<-entered
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("Close did not cancel active execution")
	}
	if f.removed != 1 {
		t.Fatalf("container leak: %d", f.removed)
	}
	if _, err := e.Execute(context.Background(), req); !errors.Is(err, script.ErrClosed) {
		t.Fatalf("%v", err)
	}
}

func TestDisabledAndUnavailableDoNotCreateWorkspace(t *testing.T) {
	f := &fakeEngine{pingErr: errors.New("connection refused")}
	if _, err := New(context.Background(), Config{Engine: f}); err == nil {
		t.Fatal("disabled executor initialized")
	}
	c, r := fixture(t)
	_, err := New(context.Background(), Config{Enabled: true, Catalog: c, Registrations: []script.Registration{r}, Engine: f})
	var coded *script.Error
	if !errors.As(err, &coded) || coded.Code != script.CodeDockerUnavailable {
		t.Fatalf("%v", err)
	}
}
