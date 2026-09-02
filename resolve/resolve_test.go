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
	if err != nil || out["score"] != float64(92) {
		t.Fatalf("unexpected struct binding: %+v %v", out, err)
	}
}
