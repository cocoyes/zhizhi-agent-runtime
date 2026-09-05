package plan

import (
	"context"
	"encoding/json"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"testing"
)

func TestPatchAcceptsObjectRemoveEntriesAndSingleStepObjects(t *testing.T) {
	patch, err := decodePatch("```json\n{remove_steps: [{id: 'failed'}], add_steps: [{id: 'fallback', capability: 'cached'}]}\n```")
	if err != nil || len(patch.Remove) != 1 || patch.Remove[0] != "failed" || len(patch.Add) != 1 || patch.Add[0].ID != "fallback" {
		t.Fatalf("unexpected tolerant patch: %+v err=%v", patch, err)
	}
}

type replanToolCallingScripted struct{ input model.ModelInput }

func (s *replanToolCallingScripted) ID() string { return "tool-calling-scripted" }
func (s *replanToolCallingScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{ToolCalling: true}
}
func (s *replanToolCallingScripted) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	s.input = input
	var call model.ToolCallRequest
	call.Type = "function"
	call.Function.Name = patchOutputToolName
	call.Function.Arguments = `{"replace_steps":[{"id":"failed","capability":"cached"}]}`
	return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{call}}, nil
}
func (s *replanToolCallingScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestModelReplannerUsesForcedTypedPatchToolLikeEino(t *testing.T) {
	m := &replanToolCallingScripted{}
	patch, err := NewModelReplanner(m).Replan(context.Background(), Input{Original: Plan{Version: 1, Steps: []Step{{ID: "failed", Capability: "remote"}}}, Failure: "failed"})
	if err != nil || len(patch.Replace) != 1 {
		t.Fatalf("unexpected patch: %+v err=%v", patch, err)
	}
	if m.input.ToolChoice != model.ToolChoiceForced || len(m.input.Tools) != 1 || m.input.JSONMode {
		t.Fatalf("expected one forced structured-output tool: %+v", m.input)
	}
	var schema map[string]any
	if err := json.Unmarshal(m.input.Tools[0].Function.Parameters, &schema); err != nil || schema["type"] != "object" {
		t.Fatalf("invalid patch tool schema: %v err=%v", schema, err)
	}
}

func TestPatchAcceptsStringRemoveEntriesThroughTargetShape(t *testing.T) {
	patch, err := decodePatch(`{"remove_steps":["failed"]}`)
	if err != nil || len(patch.Remove) != 1 || patch.Remove[0] != "failed" {
		t.Fatalf("unexpected normalized patch: %+v err=%v", patch, err)
	}
}

func TestDecodePatchRejectsReplacementWithoutID(t *testing.T) {
	if _, err := decodePatch(`{"replace_steps":[{"capability":"cached"}]}`); err == nil {
		t.Fatal("expected replacement without id to be rejected")
	}
}

func TestPatchApply(t *testing.T) {
	source := Plan{Version: 1, Steps: []Step{{ID: "weather", Capability: "weather"}, {ID: "poi", Capability: "poi", DependsOn: []string{"weather"}}}}
	out, err := (Patch{Remove: []string{"poi"}, Add: []Step{{ID: "fallback", Capability: "local.poi"}}}).Apply(source)
	if err != nil || out.Version != 2 || len(out.Steps) != 2 {
		t.Fatalf("unexpected patch: %+v %v", out, err)
	}
}

type replanScripted struct{}

func (replanScripted) ID() string { return "scripted" }
func (replanScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (replanScripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	return &model.ModelOutput{Text: `{"remove_steps":["failed"]}`}, nil
}
func (replanScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}
func TestModelReplannerParsesPatch(t *testing.T) {
	p, err := NewModelReplanner(replanScripted{}).Replan(context.Background(), Input{Original: Plan{Version: 1, Steps: []Step{{ID: "failed", Capability: "remote"}}}, Failure: "timeout"})
	if err != nil || len(p.Remove) != 1 {
		t.Fatalf("unexpected patch: %+v %v", p, err)
	}
}

type replanRepairScripted struct{ calls int }

func (s *replanRepairScripted) ID() string { return "repair-scripted" }
func (s *replanRepairScripted) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (s *replanRepairScripted) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	s.calls++
	if s.calls == 1 {
		return &model.ModelOutput{Text: `{}`}, nil
	}
	return &model.ModelOutput{Text: `{"replace_steps":[{"id":"failed","capability":"cached"}]}`}, nil
}
func (s *replanRepairScripted) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestModelReplannerRepairsEmptyPatchInsteadOfReusingPlan(t *testing.T) {
	m := &replanRepairScripted{}
	patch, err := NewModelReplanner(m).Replan(context.Background(), Input{Original: Plan{Version: 1, Steps: []Step{{ID: "failed", Capability: "remote"}}}, Failure: "tool failed"})
	if err != nil || m.calls != 2 || len(patch.Replace) != 1 || patch.Replace[0].Capability != "cached" {
		t.Fatalf("expected a repaired non-empty patch after two calls: patch=%+v calls=%d err=%v", patch, m.calls, err)
	}
}

func TestValidatePatchCompatibilityRejectsUnrelatedCapability(t *testing.T) {
	source := Plan{Steps: []Step{{ID: "create", Capability: "reminder.create"}}}
	patch := Patch{Replace: []Step{{ID: "create", Capability: "web.search"}}}
	tools := []model.ToolSpec{
		{Function: model.FunctionSpec{Name: "reminder", Parameters: json.RawMessage(`{"type":"object"}`)}, Capabilities: []string{"reminder.create"}, SemanticGroup: "reminder", Fallbacks: []string{"reminder.create.backup"}, SideEffect: "write_non_idempotent"},
		{Function: model.FunctionSpec{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}, Capabilities: []string{"web.search"}, SemanticGroup: "search", SideEffect: "read"},
	}
	if err := ValidatePatchCompatibility(source, patch, tools); err == nil {
		t.Fatal("expected incompatible replacement to be rejected")
	}
}

func TestValidatePatchCompatibilityAllowsExplicitSameGroupFallback(t *testing.T) {
	source := Plan{Steps: []Step{{ID: "create", Capability: "reminder.create"}}}
	patch := Patch{Replace: []Step{{ID: "create", Capability: "reminder.create.backup"}}}
	tools := []model.ToolSpec{
		{Function: model.FunctionSpec{Name: "reminder", Parameters: json.RawMessage(`{"type":"object"}`)}, Capabilities: []string{"reminder.create"}, SemanticGroup: "reminder", Fallbacks: []string{"reminder.create.backup"}, SideEffect: "write_non_idempotent"},
		{Function: model.FunctionSpec{Name: "reminder_backup", Parameters: json.RawMessage(`{"type":"object"}`)}, Capabilities: []string{"reminder.create.backup"}, SemanticGroup: "reminder", SideEffect: "write_non_idempotent"},
	}
	if err := ValidatePatchCompatibility(source, patch, tools); err != nil {
		t.Fatalf("explicit fallback rejected: %v", err)
	}
}
