package plan

import "testing"

func TestOptimizeMergesDuplicateReadSteps(t *testing.T) {
	p := Plan{Version: 1, Steps: []Step{{ID: "a", Capability: "weather", Input: map[string]any{"city": "Shenzhen"}}, {ID: "b", Capability: "weather", Input: map[string]any{"city": "Shenzhen"}}, {ID: "c", Capability: "summary", DependsOn: []string{"b"}}}}
	out, err := Optimize(p)
	if err != nil || len(out.Steps) != 2 || out.Steps[1].DependsOn[0] != "a" {
		t.Fatalf("unexpected optimized plan: %+v %v", out, err)
	}
}
