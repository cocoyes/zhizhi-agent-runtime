package exec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
	"go.uber.org/goleak"
)

func TestSchedulerRunsBatchConcurrently(t *testing.T) {
	defer goleak.VerifyNone(t)
	weather := tool.Func("weather", "weather", func(context.Context, struct{}) (string, error) {
		time.Sleep(40 * time.Millisecond)
		return "sunny", nil
	}, tool.WithCapabilities("weather"))
	location := tool.Func("location", "location", func(context.Context, struct{}) (string, error) { time.Sleep(40 * time.Millisecond); return "city", nil }, tool.WithCapabilities("location"))
	p := plan.Plan{Steps: []plan.Step{{ID: "weather", Capability: "weather"}, {ID: "location", Capability: "location"}}}
	execution, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	results, err := (Scheduler{Registry: tool.NewRegistry(weather, location), MaxParallel: 2}).Run(context.Background(), execution, nil)
	if err != nil || len(results) != 2 {
		t.Fatalf("unexpected results: %+v %v", results, err)
	}
	if time.Since(started) >= 75*time.Millisecond {
		t.Fatalf("steps did not run concurrently: %s", time.Since(started))
	}
}

func TestSchedulerOptionalFailureDoesNotAbort(t *testing.T) {
	failing := tool.Func("optional", "optional", func(context.Context, struct{}) (string, error) { return "", errors.New("unavailable") }, tool.WithCapabilities("optional"))
	succeeding := tool.Func("required", "required", func(context.Context, struct{}) (string, error) { return "ok", nil }, tool.WithCapabilities("required"))
	execution, err := Compile(plan.Plan{Steps: []plan.Step{{ID: "optional", Capability: "optional", Optional: true}, {ID: "required", Capability: "required"}}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := (Scheduler{Registry: tool.NewRegistry(failing, succeeding)}).Run(context.Background(), execution, func(Step) json.RawMessage { return json.RawMessage(`{}`) })
	if err != nil || len(results) != 2 {
		t.Fatalf("optional failure aborted run: %+v %v", results, err)
	}
}

func TestSchedulerBindsPreviousEvidence(t *testing.T) {
	location := tool.Func("location", "location", func(context.Context, struct{}) (map[string]any, error) { return map[string]any{"lat": 22.5}, nil }, tool.WithCapabilities("location"))
	poi := tool.Func("poi", "poi", func(_ context.Context, input struct {
		Latitude float64 `json:"latitude"`
	}) (float64, error) {
		return input.Latitude, nil
	}, tool.WithCapabilities("poi"))
	execution, err := Compile(plan.Plan{Steps: []plan.Step{{ID: "location", Capability: "location"}, {ID: "poi", Capability: "poi", DependsOn: []string{"location"}, Bindings: []plan.InputBinding{{SourceStep: "location", SourcePath: "lat", TargetPath: "latitude"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := (Scheduler{Registry: tool.NewRegistry(location, poi)}).Run(context.Background(), execution, nil)
	if err != nil || len(results) != 2 || results[1].Content.(float64) != 22.5 {
		t.Fatalf("binding failed: %+v %v", results, err)
	}
}

func TestSchedulerExecutesOnlyMatchingCondition(t *testing.T) {
	calledB, calledC := false, false
	a := tool.Func("a", "a", func(context.Context, struct{}) (map[string]any, error) {
		return map[string]any{"approved": true}, nil
	}, tool.WithCapabilities("a"))
	b := tool.Func("b", "b", func(context.Context, struct{}) (string, error) {
		calledB = true
		return "accepted", nil
	}, tool.WithCapabilities("b"))
	c := tool.Func("c", "c", func(context.Context, struct{}) (string, error) {
		calledC = true
		return "rejected", nil
	}, tool.WithCapabilities("c"))
	execution, err := Compile(plan.Plan{Steps: []plan.Step{
		{ID: "a", Capability: "a"},
		{ID: "b", Capability: "b", DependsOn: []string{"a"}, Condition: &plan.Condition{SourceStep: "a", SourcePath: "approved", Equals: true}},
		{ID: "c", Capability: "c", DependsOn: []string{"a"}, Condition: &plan.Condition{SourceStep: "a", SourcePath: "approved", Equals: false}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := (Scheduler{Registry: tool.NewRegistry(a, b, c)}).Run(context.Background(), execution, nil)
	if err != nil || len(results) != 3 || !calledB || calledC || !results[2].Skipped {
		t.Fatalf("condition branch failed: %+v err=%v calledB=%v calledC=%v", results, err, calledB, calledC)
	}
}

func TestSchedulerRequiresConfirmationBeforeWrite(t *testing.T) {
	called := false
	write := tool.Func("write", "write", func(context.Context, struct{}) (string, error) {
		called = true
		return "done", nil
	}, tool.WithCapabilities("write"), tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyIdempotent), tool.WithConfirmation(tool.ConfirmationAlways))
	execution, err := Compile(plan.Plan{Steps: []plan.Step{{ID: "write", Capability: "write"}}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := (Scheduler{Registry: tool.NewRegistry(write), Confirm: func(context.Context, tool.Spec, json.RawMessage) (bool, error) { return false, nil }}).Run(context.Background(), execution, nil)
	if err != nil || len(results) != 1 || called || results[0].Action == nil || results[0].Action.Status != contract.ActionConfirmationRequired {
		t.Fatalf("confirmation gate failed: %+v err=%v called=%v", results, err, called)
	}
}
