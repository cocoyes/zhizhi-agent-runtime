package zhizhi

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type complexModel struct{ calls int }

func (m *complexModel) ID() string { return "complex-scripted" }
func (m *complexModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, JSONMode: true}
}

type writePlanner struct{}

func (writePlanner) Plan(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
	return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "send", Capability: "message.send", Input: map[string]any{"text": "hello"}}}}, nil
}

func TestComplexSuspendResumeSkipsCompletedSteps(t *testing.T) {
	reads, writes := 0, 0
	read := tool.Func("read", "read", func(context.Context, struct{}) (string, error) { reads++; return "ready", nil }, tool.WithCapabilities("state.read"))
	write := tool.Func("send", "send", func(context.Context, struct {
		Text string `json:"text"`
	}) (string, error) {
		writes++
		return "sent", nil
	}, tool.WithCapabilities("message.send"), tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencyIdempotent), tool.WithConfirmation(tool.ConfirmationAlways))
	planner := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
		return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "read", Capability: "state.read"}, {ID: "send", Capability: "message.send", DependsOn: []string{"read"}, Input: map[string]any{"text": "hello"}}}}, nil
	})
	store := checkpoint.NewMemoryStore()
	mode := RunModeAgentComplex
	a, err := New(WithModel(&complexModel{}), WithTools(read, write), WithPlanner(planner), WithCheckpointStore(store))
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := a.Run(context.Background(), Request{Input: "send", Mode: &mode})
	if err != nil || !suspended.Suspended || reads != 1 || writes != 0 {
		t.Fatalf("unexpected suspend: %+v reads=%d writes=%d err=%v", suspended, reads, writes, err)
	}
	resumed, err := a.Resume(context.Background(), ResumeRequest{RunID: suspended.RunID, Checkpoint: suspended.Checkpoint, Decision: true})
	if err != nil || resumed.Suspended || reads != 1 || writes != 1 {
		t.Fatalf("unexpected resume: %+v reads=%d writes=%d err=%v", resumed, reads, writes, err)
	}
	if resumed.Stats.ModelCalls != 1 || resumed.Stats.ToolCalls != 2 || resumed.Stats.SuccessfulTools != 2 {
		t.Fatalf("resume statistics were not accumulated: %+v", resumed.Stats)
	}
}

type plannerFunc func(context.Context, string, []model.ToolSpec) (plan.Plan, error)

func (f plannerFunc) Plan(ctx context.Context, input string, tools []model.ToolSpec) (plan.Plan, error) {
	return f(ctx, input, tools)
}
func (m *complexModel) Generate(_ context.Context, in model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if m.calls == 1 {
		return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"weather","capability":"weather.current","input":{"city":"Shenzhen"}}]}`}, nil
	}
	return &model.ModelOutput{Text: "今晚深圳天气晴朗。"}, nil
}
func (m *complexModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

type fixedPlanner struct{}

func (fixedPlanner) Plan(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
	return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "weather", Capability: "weather.current", Input: map[string]any{"city": "Shenzhen"}}}}, nil
}
func TestComplexRunUsesCompiledPlan(t *testing.T) {
	weather := tool.Func("weather", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "sunny", nil
	}, tool.WithCapabilities("weather.current"))
	m := &complexModel{}
	mode := RunModeAgentComplex
	a, err := New(WithModel(m), WithTools(weather), WithPlanner(fixedPlanner{}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), Request{Input: "安排今晚", Mode: &mode})
	if err != nil || out.PlanSteps != 1 || out.Batches != 1 || out.Text == "" {
		t.Fatalf("unexpected response: %+v %v", out, err)
	}
}

func TestComplexStreamEmitsStepAndFinalEvents(t *testing.T) {
	weather := tool.Func("weather", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "sunny", nil
	}, tool.WithCapabilities("weather.current"))
	mode := RunModeAgentComplex
	a, err := New(WithModel(&complexModel{}), WithTools(weather), WithPlanner(fixedPlanner{}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.Stream(context.Background(), Request{Input: "plan", Mode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	stepSeen, finalSeen := false, false
	for {
		event, recvErr := stream.Recv(context.Background())
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if event.Type == EventStepCompleted {
			stepSeen = true
		}
		if event.Response != nil {
			finalSeen = true
		}
	}
	if !stepSeen || !finalSeen {
		t.Fatalf("missing complex events: step=%v final=%v", stepSeen, finalSeen)
	}
}

type recoveryPlanner struct{ called bool }

func (p *recoveryPlanner) Replan(context.Context, plan.Input) (plan.Patch, error) {
	p.called = true
	return plan.Patch{Remove: []string{"weather"}}, nil
}
func TestComplexRunReplansOnceAfterRequiredFailure(t *testing.T) {
	failing := tool.Func("weather", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "", errors.New("required read failed")
	}, tool.WithCapabilities("weather.current"))
	m := &complexModel{}
	rp := &recoveryPlanner{}
	mode := RunModeAgentComplex
	a, err := New(WithModel(m), WithTools(failing), WithPlanner(fixedPlanner{}), WithReplanner(rp))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), Request{Input: "安排今晚", Mode: &mode})
	if err != nil || !rp.called || out.Replans != 1 || out.PlanSteps != 0 {
		t.Fatalf("unexpected replan response: %+v %v", out, err)
	}
}

func TestComplexCancelledStepDoesNotReplan(t *testing.T) {
	failing := tool.Func("weather", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "", context.Canceled
	}, tool.WithCapabilities("weather.current"))
	rp := &recoveryPlanner{}
	a, err := New(WithModel(&complexModel{}), WithTools(failing), WithPlanner(fixedPlanner{}), WithReplanner(rp))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentComplex
	_, err = a.Run(context.Background(), Request{Input: "plan", Mode: &mode})
	if !errors.Is(err, context.Canceled) || rp.called {
		t.Fatalf("cancelled execution replanned: %v replan=%v", err, rp.called)
	}
}

type noopReplanner struct{}

func (noopReplanner) Replan(context.Context, plan.Input) (plan.Patch, error) {
	return plan.Patch{}, nil
}

func TestComplexFailureEmitsRunFailedOnce(t *testing.T) {
	failing := tool.Func("weather", "weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "", errors.New("unavailable")
	}, tool.WithCapabilities("weather.current"))
	mode := RunModeAgentComplex
	failedEvents := 0
	replanFailedEvents := 0
	a, err := New(
		WithModel(&complexModel{}),
		WithTools(failing),
		WithPlanner(fixedPlanner{}),
		WithReplanner(noopReplanner{}),
		WithObserver(observe.Func(func(event observe.Event) {
			switch event.Type {
			case "run.failed":
				failedEvents++
			case "replan.failed":
				replanFailedEvents++
			}
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	// A replan that cannot produce a usable patch degrades the run instead of
	// failing it: the facts already gathered are still composed into an answer.
	out, err := a.Run(context.Background(), Request{Input: "fail", Mode: &mode})
	if err != nil {
		t.Fatalf("replan failure should degrade, not fail: %v", err)
	}
	if out == nil || out.Outcome != contract.OutcomeDegraded {
		t.Fatalf("expected degraded outcome, got %+v", out)
	}
	if !hasWarning(out.Warnings, "REPLAN_FAILED") {
		t.Fatalf("expected REPLAN_FAILED warning, got %+v", out.Warnings)
	}
	if replanFailedEvents != 1 {
		t.Fatalf("expected one replan.failed event, got %d", replanFailedEvents)
	}
	if failedEvents != 0 {
		t.Fatalf("expected no run.failed event, got %d", failedEvents)
	}
}

type erroringReplanner struct{ calls int }

func (r *erroringReplanner) Replan(context.Context, plan.Input) (plan.Patch, error) {
	r.calls++
	return plan.Patch{}, errors.New("replan model output was unusable")
}

// A replan failure after work has already succeeded must keep that work and
// still answer. The run degrades; it does not discard facts or re-run steps.
func TestComplexReplanFailureKeepsPartialResults(t *testing.T) {
	reads, failures := 0, 0
	read := tool.Func("read", "read", func(context.Context, struct{}) (string, error) {
		reads++
		return "attraction: 万绿湖", nil
	}, tool.WithCapabilities("state.read"))
	fail := tool.Func("fail", "fail", func(context.Context, struct{}) (string, error) {
		failures++
		return "", errors.New("permanent failure")
	}, tool.WithCapabilities("state.fail"))
	planner := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
		return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "read", Capability: "state.read"}, {ID: "fail", Capability: "state.fail", DependsOn: []string{"read"}}}}, nil
	})
	m := &complexModel{}
	rp := &erroringReplanner{}
	mode := RunModeAgentComplex
	a, err := New(WithModel(m), WithTools(read, fail), WithPlanner(planner), WithReplanner(rp))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), Request{Input: "河源", Mode: &mode})
	if err != nil {
		t.Fatalf("replan failure should degrade, not fail: %v", err)
	}
	if out.Outcome != contract.OutcomeDegraded || !hasWarning(out.Warnings, "REPLAN_FAILED") {
		t.Fatalf("expected degraded outcome with REPLAN_FAILED, got %+v", out)
	}
	if out.Text == "" {
		t.Fatalf("degraded run produced no answer: %+v", out)
	}
	if rp.calls != 1 || reads != 1 || failures != 1 {
		t.Fatalf("replan or execution repeated: replans=%d reads=%d failures=%d", rp.calls, reads, failures)
	}
	if out.Stats.SuccessfulTools != 1 || out.Stats.FailedTools != 1 {
		t.Fatalf("partial results were lost: %+v", out.Stats)
	}
}

func hasWarning(warnings []contract.Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

type removeFailedReplanner struct{}

func (removeFailedReplanner) Replan(context.Context, plan.Input) (plan.Patch, error) {
	return plan.Patch{Remove: []string{"fail"}}, nil
}

func TestComplexReplanReusesSuccessfulStep(t *testing.T) {
	reads, failures := 0, 0
	read := tool.Func("read", "read", func(context.Context, struct{}) (string, error) {
		reads++
		return "ok", nil
	}, tool.WithCapabilities("state.read"))
	fail := tool.Func("fail", "fail", func(context.Context, struct{}) (string, error) {
		failures++
		return "", errors.New("permanent failure")
	}, tool.WithCapabilities("state.fail"))
	planner := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
		return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "read", Capability: "state.read"}, {ID: "fail", Capability: "state.fail"}}}, nil
	})
	mode := RunModeAgentComplex
	a, err := New(WithModel(&complexModel{}), WithTools(read, fail), WithPlanner(planner), WithReplanner(removeFailedReplanner{}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), Request{Input: "run", Mode: &mode})
	if err != nil || reads != 1 || failures != 1 || out.Stats.ToolCalls != 2 || out.Stats.SuccessfulTools != 1 || out.Stats.FailedTools != 1 {
		t.Fatalf("successful step was repeated or stats lost: out=%+v err=%v reads=%d failures=%d", out, err, reads, failures)
	}
}
