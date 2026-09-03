package plan

import "testing"

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
