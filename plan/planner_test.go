package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

type scripted struct{}

func (scripted) ID() string { return "scripted" }
func (scripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}

func schemaLessToolCatalog() []model.ToolSpec {
	return []model.ToolSpec{
		{Type: "function", Function: model.FunctionSpec{Name: "locate", Description: "Locate the current city", Parameters: json.RawMessage(`{"type":"object","properties":{"ip":{"type":"string"}},"required":["ip"]}`)}},
		{Type: "function", Function: model.FunctionSpec{Name: "weather", Description: "Get weather for a city", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}},
	}
}

type schemaLessAwareScripted struct {
	calls      int
	firstValid bool
}

func (s *schemaLessAwareScripted) ID() string { return "schema-less-aware-scripted" }
func (s *schemaLessAwareScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (s *schemaLessAwareScripted) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	s.calls++
	system := input.Messages[0].Content
	knowsFallback := strings.Contains(system, "output_schema") && strings.Contains(system, "agentic")
	if (s.calls == 1 && s.firstValid && knowsFallback) || (s.calls > 1 && knowsFallback) {
		return &model.ModelOutput{Text: `{"version":1,"goal":"Get today's local weather","steps":[{"id":"locate-and-weather","mode":"agentic","goal":"Locate the user and get weather for the observed city","success_criteria":"Current weather is available","capabilities":["locate","weather"],"tool_budget":{"min":2,"max":2,"required_successes":2}}]}`}, nil
	}
	return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"locate","capability":"locate","input":{"ip":"127.0.0.1"}},{"id":"weather","capability":"weather","depends_on":["locate"],"bindings":[{"source_step":"locate","source_path":"/city","target_path":"/city"}]}]}`}, nil
}
func (s *schemaLessAwareScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestModelPlannerUsesAgenticStepForSchemaLessToolChain(t *testing.T) {
	m := &schemaLessAwareScripted{firstValid: true}
	value, err := NewModelPlanner(m).Plan(context.Background(), "today's weather", schemaLessToolCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 || len(value.Steps) != 1 || value.Steps[0].Mode != StepModeAgentic {
		t.Fatalf("expected an initial agentic plan: plan=%+v calls=%d", value, m.calls)
	}
	step := value.Steps[0]
	if len(step.Bindings) != 0 || len(step.Capabilities) != 2 || step.ToolBudget.Max != 2 {
		t.Fatalf("expected a bounded schema-less fallback: %+v", step)
	}
}

func TestModelPlannerRepairsSchemaLessBindingAsAgenticStep(t *testing.T) {
	m := &schemaLessAwareScripted{}
	value, err := NewModelPlanner(m).Plan(context.Background(), "today's weather", schemaLessToolCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 2 || len(value.Steps) != 1 || value.Steps[0].Mode != StepModeAgentic {
		t.Fatalf("expected one repair to produce an agentic plan: plan=%+v calls=%d", value, m.calls)
	}
}

type toolCallingScripted struct{ input model.ModelInput }

func (s *toolCallingScripted) ID() string { return "tool-calling-scripted" }
func (s *toolCallingScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (s *toolCallingScripted) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	s.input = input
	var call model.ToolCallRequest
	call.Type = "function"
	call.Function.Name = planOutputToolName
	call.Function.Arguments = `{"version":1,"steps":[{"id":"weather","capability":"weather.current"}]}`
	return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{call}}, nil
}
func (s *toolCallingScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestModelPlannerUsesForcedTypedOutputToolLikeEino(t *testing.T) {
	m := &toolCallingScripted{}
	p, err := NewModelPlanner(m).Plan(context.Background(), "weather", nil)
	if err != nil || len(p.Steps) != 1 {
		t.Fatalf("unexpected plan: %+v err=%v", p, err)
	}
	if m.input.ToolChoice != model.ToolChoiceForced || len(m.input.Tools) != 1 || m.input.JSONMode {
		t.Fatalf("expected one forced structured-output tool: %+v", m.input)
	}
	var schema map[string]any
	if err := json.Unmarshal(m.input.Tools[0].Function.Parameters, &schema); err != nil || schema["type"] != "object" {
		t.Fatalf("invalid output tool schema: %v err=%v", schema, err)
	}
}

type wrappedPlanScripted struct{ calls int }

func (s *wrappedPlanScripted) ID() string { return "wrapped-plan-scripted" }
func (s *wrappedPlanScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (s *wrappedPlanScripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	s.calls++
	if s.calls == 1 {
		return &model.ModelOutput{Text: `{"plan":{"version":1,"steps":[{"id":"weather","capability":"weather.current"}]}}`}, nil
	}
	return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"weather","capability":"weather.current"}]}`}, nil
}
func (s *wrappedPlanScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestModelPlannerRepairsUnexpectedPlanWrapper(t *testing.T) {
	m := &wrappedPlanScripted{}
	p, err := NewModelPlanner(m).Plan(context.Background(), "weather", nil)
	if err != nil || m.calls != 2 || len(p.Steps) != 1 {
		t.Fatalf("expected wrapped plan to be repaired: plan=%+v calls=%d err=%v", p, m.calls, err)
	}
}

func TestDecodePlanRejectsEmptyInitialPlan(t *testing.T) {
	if _, err := decodePlan(`{"version":1,"steps":[]}`, nil); err == nil {
		t.Fatal("expected empty initial plan to be rejected")
	}
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

type repairScripted struct{ calls int }

func (s *repairScripted) ID() string { return "repair-scripted" }
func (s *repairScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (s *repairScripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	s.calls++
	if s.calls == 1 {
		return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"weather"}]}`}, nil
	}
	return &model.ModelOutput{Text: "```json\n{version: 1, steps: [{id: 'weather', capability: 'weather.current'}]}\n```"}, nil
}
func (s *repairScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestModelPlannerRepairsSemanticallyInvalidOutput(t *testing.T) {
	m := &repairScripted{}
	p, err := NewModelPlanner(m).Plan(context.Background(), "weather", nil)
	if err != nil || m.calls != 2 || len(p.Steps) != 1 || p.Steps[0].Capability != "weather.current" {
		t.Fatalf("expected repaired plan after two calls: plan=%+v calls=%d err=%v", p, m.calls, err)
	}
}

type bindingRepairScripted struct{ calls int }

func (s *bindingRepairScripted) ID() string { return "binding-repair-scripted" }
func (s *bindingRepairScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (s *bindingRepairScripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	s.calls++
	if s.calls == 1 {
		return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"weather","capability":"weather"},{"id":"activity","capability":"activity","depends_on":["weather"],"input":{"temperature":"{{weather.temperature}}"}}]}`}, nil
	}
	return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"weather","capability":"weather"},{"id":"activity","capability":"activity","depends_on":["weather"],"bindings":[{"source_step":"weather","source_path":"temperature","target_path":"temperature"}]}]}`}, nil
}
func (s *bindingRepairScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestModelPlannerRejectsTemplateReferencesAndRequestsBindings(t *testing.T) {
	m := &bindingRepairScripted{}
	p, err := NewModelPlanner(m).Plan(context.Background(), "activity", nil)
	bindings := 0
	for _, step := range p.Steps {
		bindings += len(step.Bindings)
	}
	if err != nil || m.calls != 2 || bindings != 1 {
		t.Fatalf("expected a binding-based repaired plan: plan=%+v calls=%d err=%v", p, m.calls, err)
	}
}
