package zhizhi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigExpandsEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte("model:\n  base_url: ${TEST_BASE_URL}\n  model: test\nagent:\n  system_prompt: hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_BASE_URL", "http://127.0.0.1:1/v1")
	a, err := Load(path)
	if err != nil || a == nil {
		t.Fatalf("load failed: %v", err)
	}
}
