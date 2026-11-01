package script

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/skill"
)

func TestApprovalPinsOnlyExplicitFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{"SKILL.md": "---\nname: test\ndescription: test scripts\n---\nrun scripts/hello.py", "scripts/hello.py": "print('hello')", "secret.txt": "not approved"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalog := skill.NewCatalog(skill.CatalogOptions{})
	if _, err := catalog.LoadRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	approved, err := Approve(context.Background(), catalog, Registration{SkillID: "test", Scripts: []EntryPoint{{Path: "scripts/hello.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(approved.Files) != 1 || approved.Files["scripts/hello.py"] == "" {
		t.Fatalf("%+v", approved)
	}
	if _, ok := approved.Files["secret.txt"]; ok {
		t.Fatal("unapproved file copied")
	}
	for _, path := range []string{"/etc/passwd", "../secret", "scripts/../hello.py", "scripts\\hello.py"} {
		if ValidPath(path) {
			t.Fatalf("unsafe path %q", path)
		}
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(root, "scripts", "escape.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(context.Background(), catalog, Registration{SkillID: "test", Scripts: []EntryPoint{{Path: "scripts/escape.py"}}}); !errors.Is(err, ErrPermission) {
		t.Fatalf("%v", err)
	}
}
