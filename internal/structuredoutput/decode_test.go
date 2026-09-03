package structuredoutput

import "testing"

func TestDecodeRepairsSyntaxAndWeakScalarTypes(t *testing.T) {
	var output struct {
		Version int `json:"version"`
	}
	if err := Decode("```json\n{version: '1'}\n```", &output); err != nil {
		t.Fatal(err)
	}
	if output.Version != 1 {
		t.Fatalf("unexpected version: %d", output.Version)
	}
}

func TestDecodeUsesJSONTags(t *testing.T) {
	var output struct {
		RemoveSteps []string `json:"remove_steps"`
	}
	if err := Decode(`{"remove_steps":["failed"]}`, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.RemoveSteps) != 1 || output.RemoveSteps[0] != "failed" {
		t.Fatalf("unexpected output: %+v", output)
	}
}

func TestDecodeRejectsUnknownWrapper(t *testing.T) {
	var output struct {
		Version int `json:"version"`
	}
	if err := DecodeStrict(`{"plan":{"version":1}}`, &output); err == nil {
		t.Fatal("expected an unknown wrapper field to be rejected")
	}
}
