package replan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zhizhi-ai/zhizhi-agent-runtime/internal/structuredoutput"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/plan"
)

type Input struct {
	Original plan.Plan
	Failure  string
	Tools    []model.ToolSpec
}

type Replanner interface {
	Replan(context.Context, Input) (Patch, error)
}

type Patch struct {
	Add     []plan.Step `json:"add_steps,omitempty" jsonschema:"description=Complete new step objects to add. Do not include steps that already exist."`
	Replace []plan.Step `json:"replace_steps,omitempty" jsonschema:"description=Complete replacement step objects directly. Each id must identify an existing step. Never use from/to wrapper objects."`
	Remove  []string    `json:"remove_steps,omitempty" jsonschema:"description=IDs of existing steps to remove. All remaining dependencies must stay valid."`
}

type patchStepReference struct {
	ID string `json:"id"`
}

type patchOutput struct {
	Add     []plan.Step          `json:"add_steps"`
	Replace []plan.Step          `json:"replace_steps"`
	Remove  []patchStepReference `json:"remove_steps"`
}

func decodePatch(text string) (Patch, error) {
	var decoded patchOutput
	if err := structuredoutput.Decode(text, &decoded); err != nil {
		return Patch{}, err
	}
	patch := Patch{Add: decoded.Add, Replace: decoded.Replace, Remove: make([]string, 0, len(decoded.Remove))}
	for _, reference := range decoded.Remove {
		if reference.ID == "" {
			return Patch{}, fmt.Errorf("remove_steps contains an empty step id")
		}
		patch.Remove = append(patch.Remove, reference.ID)
	}
	if len(patch.Add) == 0 && len(patch.Replace) == 0 && len(patch.Remove) == 0 {
		return Patch{}, fmt.Errorf("patch contains no changes")
	}
	for _, group := range []struct {
		name  string
		steps []plan.Step
	}{{name: "add_steps", steps: patch.Add}, {name: "replace_steps", steps: patch.Replace}} {
		for _, step := range group.steps {
			if step.ID == "" {
				return Patch{}, fmt.Errorf("%s contains a step with an empty id", group.name)
			}
			if step.Capability == "" {
				return Patch{}, fmt.Errorf("%s step %q has an empty capability", group.name, step.ID)
			}
		}
	}
	return patch, nil
}

func (p Patch) Apply(source plan.Plan) (plan.Plan, error) {
	steps := make(map[string]plan.Step, len(source.Steps))
	for _, step := range source.Steps {
		steps[step.ID] = step
	}
	for _, id := range p.Remove {
		if _, ok := steps[id]; !ok {
			return plan.Plan{}, fmt.Errorf("replan: cannot remove unknown step %q", id)
		}
		delete(steps, id)
	}
	for _, step := range p.Replace {
		if _, ok := steps[step.ID]; !ok {
			return plan.Plan{}, fmt.Errorf("replan: cannot replace unknown step %q", step.ID)
		}
		steps[step.ID] = step
	}
	for _, step := range p.Add {
		if _, ok := steps[step.ID]; ok {
			return plan.Plan{}, fmt.Errorf("replan: duplicate added step %q", step.ID)
		}
		steps[step.ID] = step
	}
	out := plan.Plan{Version: source.Version + 1, Steps: make([]plan.Step, 0, len(steps))}
	for _, step := range steps {
		out.Steps = append(out.Steps, step)
	}
	return out.Normalize()
}

type ModelReplanner struct {
	Model  model.Model
	Prompt string
	Trace  func(event string, details map[string]any)
}

const patchOutputToolName = "zhizhi_emit_plan_patch"

func NewModelReplanner(m model.Model) *ModelReplanner { return &ModelReplanner{Model: m} }
func (r *ModelReplanner) Replan(ctx context.Context, input Input) (Patch, error) {
	if r == nil || r.Model == nil {
		return Patch{}, fmt.Errorf("replanner: model is required")
	}
	prompt := r.Prompt
	if prompt == "" {
		prompt = "Repair the execution plan after a required step failed. Return only JSON with add_steps, replace_steps, and remove_steps. Preserve successful work and all valid downstream steps. Prefer replacing the failed step with a compatible fallback capability; update downstream dependencies to the replacement when needed. Do not remove final composition steps merely because an upstream tool failed. References must use bindings whose source_path exists in output_schema and target_path exists in input_schema. remove_steps must contain step ID strings only. Do not add retry steps."
	}
	payload := struct {
		Original plan.Plan                `json:"original"`
		Failure  string                   `json:"failure"`
		Tools    []model.PlanningToolSpec `json:"tools"`
	}{Original: input.Original, Failure: input.Failure, Tools: model.PlanningToolCatalog(input.Tools)}
	b, _ := json.Marshal(payload)
	outputTool, err := structuredoutput.ToolFor[Patch](patchOutputToolName, "Return a patch for the existing plan. add_steps and replace_steps contain complete step objects directly; remove_steps contains existing step ID strings.")
	if err != nil {
		return Patch{}, fmt.Errorf("replanner: build output schema: %w", err)
	}
	useToolOutput := r.Model.Capabilities().ToolCalling
	messages := []model.Message{{Role: model.RoleSystem, Content: prompt}, {Role: model.RoleUser, Content: string(b)}}
	// The only callable tool here is the synthetic typed output channel. Domain
	// tools remain catalog data and cannot be executed during replanning.
	r.trace("model.request", map[string]any{"messages": messages, "output_tool": outputTool})
	out, err := r.Model.Generate(ctx, replanModelInput(messages, outputTool, useToolOutput))
	if err != nil {
		return Patch{}, fmt.Errorf("replanner model: %w", err)
	}
	r.trace("model.completed", map[string]any{"content": out.Text, "reasoning_content": out.ReasoningContent, "tool_call_requests": out.ToolCalls})
	candidate, candidateErr := structuredoutput.Candidate(out, patchOutputToolName, useToolOutput)
	for repairAttempt := 0; ; repairAttempt++ {
		var patch Patch
		decodeErr := candidateErr
		if decodeErr == nil {
			patch, decodeErr = decodePatch(candidate)
		}
		if decodeErr == nil {
			_, decodeErr = patch.Apply(input.Original)
		}
		if decodeErr == nil {
			return patch, nil
		}
		if repairAttempt >= 2 {
			return Patch{}, fmt.Errorf("replanner: invalid patch after repair: %w; candidate=%q", decodeErr, compactCandidate(candidate))
		}
		repairRequest := string(b) + "\nInvalid candidate:\n" + candidate + "\nValidation error:\n" + decodeErr.Error()
		r.trace("repair.request", map[string]any{"content": repairRequest})
		repairMessages := []model.Message{{Role: model.RoleSystem, Content: "Call the required patch output tool again after correcting the validation error. add_steps and replace_steps contain complete step objects directly, never {from,to} wrappers. remove_steps contains existing step ID strings. The patched plan must preserve valid dependencies."}, {Role: model.RoleUser, Content: repairRequest}}
		repair, repairErr := r.Model.Generate(ctx, replanModelInput(repairMessages, outputTool, useToolOutput))
		if repairErr != nil {
			return Patch{}, fmt.Errorf("replanner: invalid patch: %w", decodeErr)
		}
		r.trace("repair.completed", map[string]any{"content": repair.Text, "reasoning_content": repair.ReasoningContent, "tool_call_requests": repair.ToolCalls})
		candidate, candidateErr = structuredoutput.Candidate(repair, patchOutputToolName, useToolOutput)
	}
}

func replanModelInput(messages []model.Message, outputTool model.ToolSpec, useTool bool) model.ModelInput {
	if useTool {
		return model.ModelInput{Messages: messages, Tools: []model.ToolSpec{outputTool}, ToolChoice: model.ToolChoiceForced}
	}
	return model.ModelInput{Messages: messages, JSONMode: true}
}

func (r *ModelReplanner) trace(event string, details map[string]any) {
	if r.Trace != nil {
		r.Trace(event, details)
	}
}

func compactCandidate(value string) string {
	const limit = 2048
	if len(value) > limit {
		return value[:limit] + "..."
	}
	return value
}
