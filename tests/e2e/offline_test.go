package e2e

import (
	"context"
	"testing"

	zhizhi "github.com/zhizhi-ai/zhizhi-agent-runtime"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/plan"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

type modelStub struct{ calls int }

func (m *modelStub) ID() string { return "offline" }
func (m *modelStub) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, JSONMode: true, Streaming: true}
}
func (m *modelStub) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	if m.calls == 1 {
		return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{{ID: "1", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "echo", Arguments: `{"value":"ok"}`}}}}, nil
	}
	return &model.ModelOutput{Text: "ok"}, nil
}
func (m *modelStub) Stream(context.Context, model.ModelInput) (model.Stream, error) { return nil, nil }

func TestPublicSimpleAgentOffline(t *testing.T) {
	m := new(modelStub)
	echo := tool.Func("echo", "echo", func(context.Context, struct {
		Value string `json:"value"`
	}) (string, error) {
		return "ok", nil
	})
	a, err := zhizhi.New(zhizhi.WithModel(m), zhizhi.WithTools(echo))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Run(context.Background(), zhizhi.Request{Input: "echo"})
	if err != nil || resp.Text != "ok" || resp.ToolCalls != 1 {
		t.Fatalf("unexpected response: %+v %v", resp, err)
	}
}

type complexStub struct{}

func (complexStub) ID() string { return "complex" }
func (complexStub) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (complexStub) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	return &model.ModelOutput{Text: "final"}, nil
}
func (complexStub) Stream(context.Context, model.ModelInput) (model.Stream, error) { return nil, nil }

type fixedPlanner struct{}

func (fixedPlanner) Plan(context.Context, string, []model.ToolSpec) (plan.Plan, error) {
	return plan.Plan{Version: 1, Steps: []plan.Step{{ID: "echo", Capability: "echo", Input: map[string]any{"value": "ok"}}}}, nil
}
func TestPublicComplexAgentOffline(t *testing.T) {
	echo := tool.Func("echo", "echo", func(context.Context, struct {
		Value string `json:"value"`
	}) (string, error) {
		return "ok", nil
	}, tool.WithCapabilities("echo"))
	a, err := zhizhi.New(zhizhi.WithModel(complexStub{}), zhizhi.WithPlanner(fixedPlanner{}), zhizhi.WithTools(echo))
	if err != nil {
		t.Fatal(err)
	}
	mode := zhizhi.RunModeAgentComplex
	resp, err := a.Run(context.Background(), zhizhi.Request{Input: "complex", Mode: &mode})
	if err != nil || resp.PlanSteps != 1 || resp.Batches != 1 || len(resp.Evidence) != 1 {
		t.Fatalf("unexpected response: %+v %v", resp, err)
	}
}
