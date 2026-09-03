package model

import (
	"encoding/json"
	"testing"
)

func TestValidateCapabilities(t *testing.T) {
	input := ModelInput{Tools: []ToolSpec{{Type: "function"}}, JSONMode: true}
	if err := ValidateCapabilities(ModelCapabilities{Declared: true, ToolCalling: true, JSONMode: true, Streaming: true}, input, true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCapabilities(ModelCapabilities{Declared: true, JSONMode: true, Streaming: true}, input, true); err == nil {
		t.Fatal("expected tool calling capability error")
	}
	if err := ValidateCapabilities(ModelCapabilities{}, input, true); err != nil {
		t.Fatalf("legacy undeclared adapter should remain compatible: %v", err)
	}
}

func TestValidateOutputAgainstJSONSchema(t *testing.T) {
	input := ModelInput{ResponseFormat: &ResponseFormat{Type: ResponseFormatJSONSchema, Name: "answer", Schema: json.RawMessage(`{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)}}
	if err := ValidateOutput(input, &ModelOutput{Text: `{"ok":true}`}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(input, &ModelOutput{Text: `{"ok":"yes"}`}); err == nil {
		t.Fatal("expected schema validation error")
	}
}

func TestValidateMultimodalCapability(t *testing.T) {
	input := ModelInput{Messages: []Message{UserMessage(TextPart("look"), ImageURLPart("https://example.test/a.png", "high"))}}
	if err := ValidateCapabilities(ModelCapabilities{Declared: true, Images: true}, input, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCapabilities(ModelCapabilities{Declared: true}, input, false); err == nil {
		t.Fatal("expected image capability error")
	}
}

func TestMessageMultimodalPartsSurviveJSONRoundTrip(t *testing.T) {
	original := UserMessage(TextPart("look"), ImageURLPart("https://example.test/a.png", "high"))
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Parts) != 2 || decoded.Parts[1].URL != original.Parts[1].URL {
		t.Fatalf("multimodal content was not preserved: %#v", decoded.Parts)
	}
}
