package model_test

import (
	"encoding/json"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

// A named participant must survive the JSON boundary unchanged, because the
// caller's message slice is often rebuilt from storage between runs.
func TestMessageNameJSONRoundTrip(t *testing.T) {
	raw := `{"role":"user","content":"常见的十字花科植物有哪些？","name":"researcher"}`
	var message model.Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatal(err)
	}
	if message.Name != "researcher" {
		t.Fatalf("name lost on decode: %q", message.Name)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != raw {
		t.Fatalf("round trip mismatch:\n got %s\nwant %s", encoded, raw)
	}
}

// An unnamed participant must not gain an empty "name" on the wire, so that
// providers with strict request validation do not reject the payload.
func TestMessageNameOmittedWhenEmpty(t *testing.T) {
	encoded, err := json.Marshal(model.Message{Role: model.RoleUser, Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"role":"user","content":"hi"}` {
		t.Fatalf("unexpected encoding: %s", encoded)
	}
}

func TestNamedMessageConstructors(t *testing.T) {
	user := model.NamedUserMessage("researcher", "常见的十字花科植物有哪些？")
	if user.Role != model.RoleUser || user.Name != "researcher" || user.Content != "常见的十字花科植物有哪些？" {
		t.Fatalf("unexpected user message: %+v", user)
	}
	system := model.NamedMessage(model.RoleSystem, "policy", "Stay factual.")
	if system.Role != model.RoleSystem || system.Name != "policy" {
		t.Fatalf("unexpected system message: %+v", system)
	}
	// A named message carries text only; multimodal content must still go
	// through UserMessage so the two fields never disagree.
	if named := model.NamedMessage(model.RoleUser, "researcher", "text"); named.Content == "" || named.Parts != nil {
		t.Fatalf("named message should be text only: %+v", named)
	}
}

func TestUserMessageRemainsUnnamed(t *testing.T) {
	if message := model.UserMessage(model.TextPart("hi")); message.Name != "" {
		t.Fatalf("multimodal user message should stay unnamed: %+v", message)
	}
}
