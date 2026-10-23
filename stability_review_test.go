package zhizhi

import (
	"context"
	"errors"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type reviewWriteThenReadModel struct{}

func (reviewWriteThenReadModel) ID() string { return "write-then-read" }
func (reviewWriteThenReadModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (reviewWriteThenReadModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, errors.New("unexpected stream")
}
func (reviewWriteThenReadModel) Generate(_ context.Context, in model.ModelInput) (*model.ModelOutput, error) {
	if len(in.Tools) == 0 {
		return &model.ModelOutput{Text: "The write completed, but subsequent work could not be verified."}, nil
	}
	calls := make([]model.ToolCallRequest, 2)
	for i, id := range []string{"write", "read"} {
		calls[i].ID, calls[i].Type = id, "function"
		calls[i].Function.Name, calls[i].Function.Arguments = id, `{}`
	}
	return &model.ModelOutput{ToolCalls: calls}, nil
}

type reviewReplanner struct{ calls int }

func (r *reviewReplanner) Replan(context.Context, plan.Input) (plan.Patch, error) {
	r.calls++
	return plan.Patch{}, errors.New("a completed write must not reach replanning")
}

func TestAgenticFailureAfterSuccessfulWriteDoesNotReplan(t *testing.T) {
	writes, reads := 0, 0
	write := tool.Func("write", "write", func(context.Context, struct{}) (string, error) {
		writes++
		return "saved", nil
	}, tool.WithSideEffect(tool.SideEffectWrite))
	read := tool.Func("read", "read", func(context.Context, struct{}) (string, error) {
		reads++
		return "", errors.New("permanent read failure")
	})
	planner := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
		return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "work", Mode: plan.StepModeAgentic, Goal: "write then verify", SuccessCriteria: "verified", Capabilities: []string{"write", "read"}, ToolBudget: plan.ToolCallBudget{Max: 2}}}}, nil
	})
	replanner := &reviewReplanner{}
	a, err := New(WithModel(reviewWriteThenReadModel{}), WithTools(write, read), WithPlanner(planner), WithReplanner(replanner))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentComplex
	resp, err := a.Run(context.Background(), Request{Input: "write then verify", Mode: &mode})
	if err != nil || resp == nil {
		t.Fatalf("response=%+v error=%v", resp, err)
	}
	if writes != 1 || reads != 1 || replanner.calls != 0 || resp.Outcome != contract.OutcomeDegraded {
		t.Fatalf("write=%d read=%d replans=%d response=%+v", writes, reads, replanner.calls, resp)
	}
	if len(resp.Actions) != 1 || resp.Actions[0].Status != contract.ActionSuccess {
		t.Fatalf("successful write receipt was lost: %+v", resp.Actions)
	}
}

func TestParallelUnknownWriteCannotBeHiddenByPendingConfirmation(t *testing.T) {
	writes, confirmedWrites := 0, 0
	unknown := tool.Func("unknown", "unknown", func(context.Context, struct{}) (string, error) {
		writes++
		return "", errors.New("connection reset after dispatch")
	}, tool.WithSideEffect(tool.SideEffectWrite))
	pending := tool.Func("pending", "pending", func(context.Context, struct{}) (string, error) {
		confirmedWrites++
		return "saved", nil
	}, tool.WithSideEffect(tool.SideEffectWrite), tool.WithConfirmation(tool.ConfirmationAlways))
	planner := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
		return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "unknown", Capability: "unknown"}, {ID: "pending", Capability: "pending"}}}, nil
	})
	a, err := New(WithModel(reviewWriteThenReadModel{}), WithTools(unknown, pending), WithPlanner(planner), WithCheckpointStore(checkpoint.NewMemoryStore()))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentComplex
	resp, err := a.Run(context.Background(), Request{Input: "perform both writes", Mode: &mode})
	if err != nil || resp == nil {
		t.Fatalf("response=%+v error=%v", resp, err)
	}
	if resp.Suspended || resp.Checkpoint != "" || resp.Outcome != contract.OutcomeDegraded || writes != 1 || confirmedWrites != 0 {
		t.Fatalf("unsafe confirmation checkpoint: response=%+v writes=%d confirmed=%d", resp, writes, confirmedWrites)
	}
	foundUnknown := false
	for _, action := range resp.Actions {
		foundUnknown = foundUnknown || action.Status == contract.ActionUnknown
	}
	if !foundUnknown {
		t.Fatalf("unknown write fact was lost: %+v", resp.Actions)
	}
}
