package plan

import (
	"encoding/json"
	"testing"
)

func TestValidateRejectsCycle(t *testing.T) {
	p := Plan{Steps: []Step{{ID: "a", Capability: "one", DependsOn: []string{"b"}}, {ID: "b", Capability: "two", DependsOn: []string{"a"}}}}
	if err := p.Validate(); err == nil {
		t.Fatal("expected dependency cycle")
	}
}
func TestNormalizeIsDeterministic(t *testing.T) {
	p, err := (Plan{Version: 1, Steps: []Step{{ID: "b", Capability: "two"}, {ID: "a", Capability: "one"}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].ID != "a" {
		t.Fatalf("unexpected order: %+v", p.Steps)
	}
}

func TestValidateConditionRequiresDependency(t *testing.T) {
	p := Plan{Steps: []Step{
		{ID: "a", Capability: "check"},
		{ID: "b", Capability: "branch", Condition: &Condition{SourceStep: "a", SourcePath: "approved", Equals: true}},
	}}
	if err := p.Validate(); err == nil {
		t.Fatal("expected condition dependency validation error")
	}
}

func TestUnmarshalAcceptsQuotedVersion(t *testing.T) {
	var p Plan
	if err := json.Unmarshal([]byte(`{"version":"1","steps":[]}`), &p); err != nil || p.Version != 1 {
		t.Fatalf("expected quoted version to parse: %+v %v", p, err)
	}
}

func TestUnmarshalAcceptsCompactBindingObject(t *testing.T) {
	var p Plan
	data := []byte(`{"version":1,"steps":[{"id":"next","capability":"next","depends_on":["weather"],"bindings":{"temperature":"weather.temperature"}},{"id":"weather","capability":"weather"}]}`)
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Steps[0].Bindings) != 1 || p.Steps[0].Bindings[0].SourceStep != "weather" || p.Steps[0].Bindings[0].TargetPath != "temperature" {
		t.Fatalf("unexpected compact binding: %+v", p.Steps[0].Bindings)
	}
}
