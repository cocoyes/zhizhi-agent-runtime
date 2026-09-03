package exec

import (
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"testing"
)

func TestCompileGroupsIndependentSteps(t *testing.T) {
	p := plan.Plan{Steps: []plan.Step{{ID: "weather", Capability: "weather.current"}, {ID: "location", Capability: "location.current"}, {ID: "restaurant", Capability: "poi.search", DependsOn: []string{"location"}}}}
	out, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Batches) != 2 || len(out.Batches[0].Steps) != 2 || out.Batches[1].Steps[0].ID != "restaurant" {
		t.Fatalf("unexpected batches: %+v", out.Batches)
	}
}
