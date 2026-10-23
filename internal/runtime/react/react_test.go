package react

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type scripted struct{ calls int }

func (m *scripted) ID() string { return "scripted" }
func (m *scripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}

type sequenceModel struct {
	outputs []*model.ModelOutput
	inputs  []model.ModelInput
}

func (m *sequenceModel) ID() string                            { return "sequence" }
func (m *sequenceModel) Capabilities() model.ModelCapabilities { return model.ModelCapabilities{} }
func (m *sequenceModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}
func (m *sequenceModel) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	m.inputs = append(m.inputs, input)
	if len(m.outputs) == 0 {
		return nil, errors.New("unexpected model call")
	}
	out := m.outputs[0]
	m.outputs = m.outputs[1:]
	return out, nil
}
func namedCall(id, name string) model.ToolCallRequest {
	var call model.ToolCallRequest
	call.ID = id
	call.Type = "function"
	call.Function.Name = name
	call.Function.Arguments = `{}`
	return call
}

func TestConfirmationContinuationDoesNotReplayEarlierWrite(t *testing.T) {
	firstCalls, secondCalls := 0, 0
	first := tool.Func("first", "first", func(context.Context, struct{}) (string, error) { firstCalls++; return "written", nil }, tool.WithSideEffect(tool.SideEffectWrite))
	second := tool.Func("second", "second", func(context.Context, struct{}) (string, error) { secondCalls++; return "written", nil }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithConfirmation(tool.ConfirmationAlways))
	m := &sequenceModel{outputs: []*model.ModelOutput{{ToolCalls: []model.ToolCallRequest{namedCall("1", "first"), namedCall("2", "second")}}, {Text: "done"}}}
	runner := Runner{Model: m, Registry: tool.NewRegistry(first, second), Budget: Budget{MaxToolCalls: 2, MaxModelCalls: 2}}
	initial, err := runner.RunInput(context.Background(), model.ModelInput{Messages: []model.Message{model.SystemMessage("trusted"), model.TextMessage(model.RoleUser, "write")}, Options: map[string]any{"temperature": 0}})
	if err == nil || initial.Continuation == nil || firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("missing pause: %+v %v", initial, err)
	}
	// Round trip the same continuation representation used by checkpoints.
	encoded, _ := json.Marshal(initial.Continuation)
	var saved Continuation
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	runner.Continuation = &saved
	runner.Confirm = func(_ context.Context, spec tool.Spec, args json.RawMessage) (bool, error) {
		return spec.ID == "second" && string(args) == `{}`, nil
	}
	resumed, err := runner.Run(context.Background(), nil)
	if err != nil || firstCalls != 1 || secondCalls != 1 || resumed.ToolCalls != 2 || resumed.ModelCalls != 2 || len(resumed.Actions) != 2 {
		t.Fatalf("unsafe resume: %+v %v first=%d second=%d", resumed, err, firstCalls, secondCalls)
	}
	last := m.inputs[len(m.inputs)-1]
	if last.Messages[0].Role != model.RoleSystem || len(last.Messages) != 5 || last.Options["temperature"] == nil {
		t.Fatalf("lost model context: %+v", last)
	}
}

func TestDisabledToolsAndUnknownWrite(t *testing.T) {
	called := 0
	w := tool.Func("write", "write", func(context.Context, struct{}) (string, error) { called++; return "", errors.New("connection dropped") }, tool.WithSideEffect(tool.SideEffectWrite))
	for _, disabled := range []bool{true, false} {
		m := &sequenceModel{outputs: []*model.ModelOutput{{ToolCalls: []model.ToolCallRequest{namedCall("1", "write")}}}}
		result, err := (Runner{Model: m, Registry: tool.NewRegistry(w), DisableTools: disabled}).Run(context.Background(), nil)
		if err == nil {
			t.Fatal("expected rejection")
		}
		if disabled && (called != 0 || len(m.inputs[0].Tools) != 0) {
			t.Fatal("disabled tools executed")
		}
		if !disabled && (called != 1 || len(result.Actions) != 1 || result.Actions[0].Status != contract.ActionUnknown) {
			t.Fatalf("lost unknown write receipt: %+v", result)
		}
	}
}

func TestUnknownSuccessfulTransportStopsRemainingCalls(t *testing.T) {
	unknownCalls, readCalls := 0, 0
	unknown := tool.Func("unknown", "unknown side effects", func(context.Context, struct{}) (string, error) { unknownCalls++; return "transport completed", nil }, tool.WithSideEffect(tool.SideEffectUnknown))
	read := tool.Func("read", "later failing read", func(context.Context, struct{}) (string, error) { readCalls++; return "", errors.New("read failed") })
	m := &sequenceModel{outputs: []*model.ModelOutput{{ToolCalls: []model.ToolCallRequest{namedCall("1", "unknown"), namedCall("2", "read")}}}}
	result, err := (Runner{Model: m, Registry: tool.NewRegistry(unknown, read)}).Run(context.Background(), nil)
	if err == nil || unknownCalls != 1 || readCalls != 0 || len(m.inputs) != 1 || len(result.Actions) != 1 || result.Actions[0].Status != contract.ActionUnknown || len(result.Evidence) != 1 {
		t.Fatalf("unknown effect lost or execution continued: %+v err=%v unknown=%d read=%d", result, err, unknownCalls, readCalls)
	}
}
func (m *scripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if m.calls == 1 {
		var call model.ToolCallRequest
		call.ID = "1"
		call.Type = "function"
		call.Function.Name = "lookup"
		call.Function.Arguments = `{}`
		return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{call}}, nil
	}
	return &model.ModelOutput{Text: "complete"}, nil
}
func (m *scripted) Stream(context.Context, model.ModelInput) (model.Stream, error) { return nil, nil }

func TestRunnerUsesAllowlistedToolsWithinBudget(t *testing.T) {
	called := 0
	lookup := tool.Func("lookup", "lookup", func(context.Context, struct{}) (string, error) { called++; return "evidence", nil })
	result, err := (Runner{Model: &scripted{}, Registry: tool.NewRegistry(lookup), Budget: Budget{MaxToolCalls: 1, RequiredSuccesses: 1}}).Run(context.Background(), []model.Message{{Role: model.RoleUser, Content: "lookup"}})
	if err != nil || result.Text != "complete" || called != 1 || result.ModelCalls != 2 || result.ToolCalls != 1 {
		t.Fatalf("unexpected result: %+v called=%d err=%v", result, called, err)
	}
}

func TestRunnerRejectsToolOutsideAllowlist(t *testing.T) {
	_, err := (Runner{Model: &scripted{}, Registry: tool.NewRegistry(), Budget: Budget{MaxToolCalls: 1}}).Run(context.Background(), []model.Message{{Role: model.RoleUser, Content: "lookup"}})
	if err == nil {
		t.Fatal("expected allowlist rejection")
	}
}
