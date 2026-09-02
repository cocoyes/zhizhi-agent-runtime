package exec

import (
	"context"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/plan"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
	"testing"
	"time"
)

func TestSchedulerSkipsOptionalNearDeadline(t *testing.T) {
	optional := tool.Func("optional", "optional", func(context.Context, struct{}) (string, error) { return "ran", nil }, tool.WithCapabilities("optional"))
	execution, _ := Compile(plan.Plan{Steps: []plan.Step{{ID: "optional", Capability: "optional", Optional: true}}})
	results, err := (Scheduler{Registry: tool.NewRegistry(optional), Deadline: DeadlinePolicy{HardTimeout: 100 * time.Millisecond, OptionalCutoff: time.Second}}).Run(context.Background(), execution, nil)
	if err != nil || len(results) != 1 || !results[0].Skipped {
		t.Fatalf("expected optional skip: %+v %v", results, err)
	}
}
