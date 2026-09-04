package react

import (
	"context"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type scripted struct{ calls int }

func (m *scripted) ID() string { return "scripted" }
func (m *scripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
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
