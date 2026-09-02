package replan

import (
	"context"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/plan"
	"testing"
)

func TestPatchApply(t *testing.T) {
	source := plan.Plan{Version: 1, Steps: []plan.Step{{ID: "weather", Capability: "weather"}, {ID: "poi", Capability: "poi", DependsOn: []string{"weather"}}}}
	out, err := (Patch{Remove: []string{"poi"}, Add: []plan.Step{{ID: "fallback", Capability: "local.poi"}}}).Apply(source)
	if err != nil || out.Version != 2 || len(out.Steps) != 2 {
		t.Fatalf("unexpected patch: %+v %v", out, err)
	}
}

type scripted struct{}

func (scripted) ID() string { return "scripted" }
func (scripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (scripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	return &model.ModelOutput{Text: `{"remove_steps":["failed"]}`}, nil
}
func (scripted) Stream(context.Context, model.ModelInput) (model.Stream, error) { return nil, nil }
func TestModelReplannerParsesPatch(t *testing.T) {
	p, err := NewModelReplanner(scripted{}).Replan(context.Background(), Input{Original: plan.Plan{Version: 1}, Failure: "timeout"})
	if err != nil || len(p.Remove) != 1 {
		t.Fatalf("unexpected patch: %+v %v", p, err)
	}
}
