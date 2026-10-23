package zhizhi

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type recoveryModel struct {
	calls   int
	failure error
	seen    model.ModelInput
	lookup  bool
}

func (m *recoveryModel) ID() string { return "recovery-script" }
func (m *recoveryModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, Images: true}
}
func (m *recoveryModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, errors.New("unexpected stream call")
}
func (m *recoveryModel) Generate(_ context.Context, in model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if len(in.Tools) > 0 && in.Tools[0].Function.Name == "zhizhi_emit_plan" {
		if m.failure != nil {
			return nil, m.failure
		}
		call := model.ToolCallRequest{ID: "bad", Type: "function"}
		call.Function.Name = "zhizhi_emit_plan"
		call.Function.Arguments = `{"version":1,"steps":[{"id":"private-user-secret","mode":"agentic","capabilities":["lookup"],"success_criteria":"done","tool_budget":{"min":0,"max":1,"required_successes":0}}]}`
		return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{call}}, nil
	}
	if m.calls == 3 {
		m.seen = in
		if len(in.Tools) > 0 {
			call := model.ToolCallRequest{ID: "lookup", Type: "function"}
			call.Function.Name = "lookup"
			call.Function.Arguments = `{}`
			return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{call}}, nil
		}
	}
	return &model.ModelOutput{Text: "Please provide the missing information."}, nil
}

func TestInitialRecoveryPreservesContextAndRequestLimits(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "stream"}[streaming], func(t *testing.T) {
			m := &recoveryModel{}
			var calls int
			lookup := tool.Func("lookup", "lookup", func(context.Context, struct{}) (string, error) { calls++; return "observed", nil })
			a, err := New(WithModel(m), WithSystemPrompt("trusted instruction"), WithTools(lookup), WithBudget(Budget{MaxModelCalls: 5, MaxToolCalls: 1}), WithPlannerRecovery(PlannerRecoveryPolicy{InitialFallback: InitialFallbackBoundedAgentic, MaxFallbackTools: 1}))
			if err != nil {
				t.Fatal(err)
			}
			mode := RunModeAgentComplex
			prior := model.UserMessage(model.TextPart("history"), model.ImageURLPart("https://example.invalid/image", "low"))
			req := Request{Input: "need lookup", Mode: &mode, Messages: []model.Message{prior}}
			var resp *Response
			if streaming {
				s, e := a.Stream(context.Background(), req, WithRunModelOptions(map[string]any{"temperature": 0}))
				if e != nil {
					t.Fatal(e)
				}
				defer s.Close()
				for {
					event, e := s.Recv(context.Background())
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
					if event.Type == EventRunFailed {
						t.Fatalf("stream failed: %+v", event)
					}
					if event.Response != nil {
						resp = event.Response
					}
				}
			} else {
				resp, err = a.Run(context.Background(), req, WithRunModelOptions(map[string]any{"temperature": 0}))
				if err != nil {
					t.Fatal(err)
				}
			}
			if resp == nil || resp.Text == "" || calls != 1 || m.calls != 5 || resp.Stats.ModelCalls != 5 || resp.Stats.ToolCalls != 1 {
				t.Fatalf("response=%+v model=%d tool=%d", resp, m.calls, calls)
			}
			if len(m.seen.Messages) < 3 || m.seen.Messages[0].Role != model.RoleSystem || m.seen.Messages[0].Content != "trusted instruction" || !reflect.DeepEqual(m.seen.Messages[1], prior) || m.seen.Options["temperature"] != 0 {
				t.Fatalf("recovery lost trusted/multimodal context: %+v", m.seen)
			}
		})
	}
}

func TestInitialRecoveryWithoutToolsAndProviderFailure(t *testing.T) {
	for _, providerFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_tools", true: "provider_failure"}[providerFailure], func(t *testing.T) {
			failure := errors.New("provider unavailable")
			m := &recoveryModel{}
			if providerFailure {
				m.failure = failure
			}
			a, err := New(WithModel(m), WithPlannerRecovery(PlannerRecoveryPolicy{InitialFallback: InitialFallbackBoundedAgentic, MaxFallbackTools: 1}), WithBudget(Budget{MaxModelCalls: 3}))
			if err != nil {
				t.Fatal(err)
			}
			mode := RunModeAgentComplex
			resp, err := a.Run(context.Background(), Request{Input: "give advice", Mode: &mode})
			if providerFailure {
				if !errors.Is(err, failure) || m.calls != 1 {
					t.Fatalf("provider failure triggered recovery: %v calls=%d", err, m.calls)
				}
				return
			}
			if err != nil || resp == nil || resp.Outcome != contract.OutcomeDegraded || m.calls != 3 || len(m.seen.Tools) != 0 {
				t.Fatalf("response=%+v err=%v calls=%d", resp, err, m.calls)
			}
		})
	}
}

func TestDefaultDiagnosticsDoNotExposeCandidate(t *testing.T) {
	var records strings.Builder
	a, err := New(WithModel(&recoveryModel{}), WithObserver(observe.NewJSONL(&records)))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentComplex
	_, err = a.Run(context.Background(), Request{Input: "private-user-secret", Mode: &mode})
	if err == nil || strings.Contains(err.Error(), "private-user-secret") || strings.Contains(records.String(), "private-user-secret") {
		t.Fatalf("diagnostics leak: err=%v log=%s", err, records.String())
	}
}

func TestPlanningRecoveryAndCompositionShareModelQuota(t *testing.T) {
	m := &recoveryModel{}
	lookup := tool.Func("lookup", "lookup", func(context.Context, struct{}) (string, error) { return "observed", nil })
	a, err := New(WithModel(m), WithTools(lookup), WithPlannerRecovery(PlannerRecoveryPolicy{InitialFallback: InitialFallbackBoundedAgentic, MaxFallbackTools: 1}), WithBudget(Budget{MaxModelCalls: 4, MaxToolCalls: 1}))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentComplex
	_, err = a.Run(context.Background(), Request{Input: "lookup", Mode: &mode})
	if !errors.Is(err, ErrBudgetExceeded) || m.calls != 4 {
		t.Fatalf("composition bypassed model quota: %v calls=%d", err, m.calls)
	}
}

func TestFailedResumeConsumesApprovalWithoutResettingQuota(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	writes := 0
	write := tool.Func("write", "write", func(context.Context, struct{}) (string, error) { writes++; return "ok", nil }, tool.WithConfirmation(tool.ConfirmationAlways))
	m := &writeModel{}
	a, err := New(WithModel(m), WithTools(write), WithCheckpointStore(store), WithBudget(Budget{MaxModelCalls: 1, MaxToolCalls: 1}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Run(context.Background(), Request{Input: "write"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Resume(context.Background(), ResumeRequest{Checkpoint: resp.Checkpoint, Decision: true})
	if !errors.Is(err, ErrBudgetExceeded) || writes != 1 || m.calls != 1 {
		t.Fatalf("resume reset quota: %v writes=%d model=%d", err, writes, m.calls)
	}
	_, err = a.Resume(context.Background(), ResumeRequest{Checkpoint: resp.Checkpoint, Decision: true})
	if !errors.Is(err, checkpoint.ErrNotFound) || writes != 1 {
		t.Fatalf("approval replayed: %v writes=%d", err, writes)
	}
}

func TestComplexParallelDispatchHonorsRequestBudget(t *testing.T) {
	var dispatched atomic.Int32
	lookup := tool.Func("lookup", "lookup", func(context.Context, struct {
		Key string `json:"key"`
	}) (string, error) {
		dispatched.Add(1)
		return "ok", nil
	})
	p := plannerFunc(func(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
		return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "a", Capability: "lookup", Input: map[string]any{"key": "a"}}, {ID: "b", Capability: "lookup", Input: map[string]any{"key": "b"}}}}, nil
	})
	a, err := New(WithModel(&complexModel{}), WithPlanner(p), WithTools(lookup), WithBudget(Budget{MaxToolCalls: 1}))
	if err != nil {
		t.Fatal(err)
	}
	mode := RunModeAgentComplex
	_, err = a.Run(context.Background(), Request{Input: "lookup", Mode: &mode})
	if !errors.Is(err, ErrBudgetExceeded) || dispatched.Load() != 1 {
		t.Fatalf("dispatches=%d error=%v", dispatched.Load(), err)
	}
}

func TestResumeRetainsOriginalDeadline(t *testing.T) {
	store := checkpoint.NewMemoryStore()
	var writes atomic.Int32
	write := tool.Func("write", "write", func(context.Context, struct{}) (string, error) {
		writes.Add(1)
		return "ok", nil
	}, tool.WithConfirmation(tool.ConfirmationAlways))
	a, err := New(WithModel(&writeModel{}), WithTools(write), WithCheckpointStore(store), WithBudget(Budget{HardTimeout: time.Hour}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Run(context.Background(), Request{Input: "write"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := store.Load(context.Background(), resp.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	// Move the persisted deadline into the past without sleeping.
	cp.Deadline = time.Now().Add(-time.Second)
	if err := store.Save(context.Background(), *cp); err != nil {
		t.Fatal(err)
	}
	_, err = a.Resume(context.Background(), ResumeRequest{Checkpoint: resp.Checkpoint, Decision: true})
	if !errors.Is(err, context.DeadlineExceeded) || writes.Load() != 0 {
		t.Fatalf("expired resume dispatched %d writes: %v", writes.Load(), err)
	}
}
