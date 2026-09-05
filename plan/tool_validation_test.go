package plan

import (
	"context"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

func validationCatalog() []model.ToolSpec {
	source := tool.Func("source", "source", func(context.Context, struct{}) (struct {
		Temperature int `json:"temperature"`
	}, error) {
		return struct {
			Temperature int `json:"temperature"`
		}{}, nil
	}, tool.WithCapabilities("source"))
	target := tool.Func("target", "target", func(context.Context, struct {
		Temperature int `json:"temperature"`
	}) (string, error) {
		return "", nil
	}, tool.WithCapabilities("target"))
	return []model.ToolSpec{source.ModelSpec(), target.ModelSpec()}
}

func TestValidateAgainstToolsRejectsUncoveredRequiredInput(t *testing.T) {
	value := Plan{Steps: []Step{{ID: "source", Capability: "source"}, {ID: "target", Capability: "target", DependsOn: []string{"source"}}}}
	if err := ValidateAgainstTools(value, validationCatalog()); err == nil {
		t.Fatal("expected uncovered required input error")
	}
}

func TestValidateAgainstToolsAcceptsRequiredBinding(t *testing.T) {
	value := Plan{Steps: []Step{{ID: "source", Capability: "source"}, {ID: "target", Capability: "target", DependsOn: []string{"source"}, Bindings: []InputBinding{{SourceStep: "source", SourcePath: "/temperature", TargetPath: "/temperature"}}}}}
	if err := ValidateAgainstTools(value, validationCatalog()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAgainstToolsRejectsMissingConditionPath(t *testing.T) {
	value := Plan{Steps: []Step{
		{ID: "source", Capability: "source"},
		{ID: "target", Capability: "target", DependsOn: []string{"source"}, Input: map[string]any{"temperature": 1}, Condition: &Condition{SourceStep: "source", SourcePath: "/missing", NotEquals: "x"}},
	}}
	if err := ValidateAgainstTools(value, validationCatalog()); err == nil {
		t.Fatal("expected missing condition output path to be rejected")
	}
}

func TestValidateAgainstToolsRejectsConditionTypeMismatch(t *testing.T) {
	value := Plan{Steps: []Step{
		{ID: "source", Capability: "source"},
		{ID: "target", Capability: "target", DependsOn: []string{"source"}, Input: map[string]any{"temperature": 1}, Condition: &Condition{SourceStep: "source", SourcePath: "/temperature", Equals: "hot"}},
	}}
	if err := ValidateAgainstTools(value, validationCatalog()); err == nil {
		t.Fatal("expected condition type mismatch to be rejected")
	}
}
