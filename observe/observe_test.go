package observe

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestJSONLSanitizesSecrets(t *testing.T) {
	var b bytes.Buffer
	NewJSONL(&b).Observe(Event{Type: "tool.completed", Data: map[string]any{"api_key": "secret", "nested": map[string]any{"token": "x"}}})
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	data := got["data"].(map[string]any)
	if data["api_key"] != "[REDACTED]" {
		t.Fatalf("secret not redacted: %v", data)
	}
}
func TestStats(t *testing.T) {
	s := new(Stats)
	s.Observe(Event{Type: "tool.retry"})
	s.Observe(Event{Type: "replan.completed"})
	if s.Snapshot().Retries != 1 || s.Snapshot().Replans != 1 {
		t.Fatal(s.Snapshot())
	}
}
