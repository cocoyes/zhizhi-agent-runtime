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

func TestJSONLKeepsTokenUsageMetrics(t *testing.T) {
	var b bytes.Buffer
	NewJSONL(&b).Observe(Event{Type: "model.completed", Data: map[string]any{"total_tokens": 42, "access_token": "secret"}})
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	data := got["data"].(map[string]any)
	if data["total_tokens"] != float64(42) || data["access_token"] != "[REDACTED]" {
		t.Fatalf("unexpected token sanitization: %v", data)
	}
}

func TestJSONLDetailsAreOptIn(t *testing.T) {
	event := Event{Type: "step.completed", Data: map[string]any{"attempts": 1}, Details: map[string]any{"input": map[string]any{"city": "Shenzhen"}}}
	var normal, detailed bytes.Buffer
	NewJSONL(&normal).Observe(event)
	NewJSONL(&detailed, WithDetails()).Observe(event)
	if bytes.Contains(normal.Bytes(), []byte(`"city"`)) || !bytes.Contains(detailed.Bytes(), []byte(`"city":"Shenzhen"`)) {
		t.Fatalf("unexpected detail output: normal=%s detailed=%s", normal.String(), detailed.String())
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
