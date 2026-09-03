package zhizhi

import (
	"context"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/replan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type complexModel struct{ calls int }

func (m *complexModel) ID() string { return "complex-scripted" }
func (m *complexModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true, JSONMode: true}
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

type recoveryPlanner struct{ called bool }

func (p *recoveryPlanner) Replan(context.Context, replan.Input) (replan.Patch, error) {
	p.called = true
	return replan.Patch{Remove: []string{"weather"}}, nil
}
func TestComplexRunReplansOnceAfterRequiredFailure(t *testing.T) {
	failing := tool.Func("weather", "weather", func(context.Context, struct{}) (string, error) { return "", context.Canceled }, tool.WithCapabilities("weather.current"))
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
