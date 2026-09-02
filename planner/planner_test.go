package planner

import (
	"context"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"testing"
)

type scripted struct{}

func (scripted) ID() string { return "scripted" }
func (scripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (scripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"weather","capability":"weather.current","input":{"city":"Shenzhen"}}]}`}, nil
}
func (scripted) Stream(context.Context, model.ModelInput) (model.Stream, error) { return nil, nil }
func TestModelPlannerParsesAndNormalizes(t *testing.T) {
	p, err := NewModelPlanner(scripted{}).Plan(context.Background(), "weather", nil)
	if err != nil || len(p.Steps) != 1 || p.Version != 1 {
		t.Fatalf("unexpected plan: %+v %v", p, err)
	}
}
