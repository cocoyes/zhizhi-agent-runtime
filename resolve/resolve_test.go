package resolve

import "testing"

func TestApplyBindings(t *testing.T) {
	out, err := ApplyBindings(map[string]any{}, map[string]any{"location": map[string]any{"lat": 1}}, []Binding{{SourceStep: "location", SourcePath: "lat", TargetPath: "latitude"}})
	if err != nil || out["latitude"] != 1 {
		t.Fatalf("unexpected binding: %+v %v", out, err)
	}
}

func TestApplyBindingsReadsJSONFieldsFromStruct(t *testing.T) {
	type result struct {
		Score int `json:"score"`
	}
	out, err := ApplyBindings(map[string]any{}, map[string]any{"check": result{Score: 92}}, []Binding{{SourceStep: "check", SourcePath: "score", TargetPath: "score"}})
	if err != nil || out["score"] != 92 {
		t.Fatalf("unexpected struct binding: %+v %v", out, err)
	}
}

func TestApplyBindingsUsesJSONPointerEscaping(t *testing.T) {
	out, err := ApplyBindings(
		map[string]any{"payload": map[string]any{"temperature": 0}},
		map[string]any{"weather": map[string]any{"air/temperature": 27}},
		[]Binding{{SourceStep: "weather", SourcePath: "/air~1temperature", TargetPath: "/payload/temperature"}},
	)
	payload, _ := out["payload"].(map[string]any)
	if err != nil || payload["temperature"] != 27 {
		t.Fatalf("unexpected pointer binding: %+v %v", out, err)
	}
}
