package plan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/structuredoutput"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

type Input struct {
	Original Plan
	Failure  string
	Tools    []model.ToolSpec
}

type Replanner interface {
	Replan(context.Context, Input) (Patch, error)
}

type Patch struct {
	Add     []Step   `json:"add_steps,omitempty" jsonschema:"description=Complete new step objects to add. Do not include steps that already exist."`
	Replace []Step   `json:"replace_steps,omitempty" jsonschema:"description=Complete replacement step objects directly. Each id must identify an existing step. Never use from/to wrapper objects."`
	Remove  []string `json:"remove_steps,omitempty" jsonschema:"description=IDs of existing steps to remove. All remaining dependencies must stay valid."`
}

func decodePatch(text string) (Patch, error) {
	var patch Patch
	if err := structuredoutput.DecodeStrict(text, &patch); err != nil {
		return Patch{}, invalid("decode", err)
	}
	if err := validateCandidateSchema[Patch](text); err != nil {
		return Patch{}, err
	}
	if len(patch.Add) == 0 && len(patch.Replace) == 0 && len(patch.Remove) == 0 {
		return Patch{}, fmt.Errorf("patch contains no changes")
	}
	for _, group := range []struct {
		name  string
		steps []Step
	}{{name: "add_steps", steps: patch.Add}, {name: "replace_steps", steps: patch.Replace}} {
		for _, step := range group.steps {
			if step.ID == "" {
				return Patch{}, fmt.Errorf("%s contains a step with an empty id", group.name)
			}
			if err := validateStepMode(step); err != nil {
				return Patch{}, err
			}
		}
	}
	for i := range patch.Add {
		patch.Add[i] = normalizeStep(patch.Add[i])
	}
	for i := range patch.Replace {
		patch.Replace[i] = normalizeStep(patch.Replace[i])
	}
	return patch, nil
}

func (p Patch) Apply(source Plan) (Plan, error) {
	if err := source.Validate(); err != nil {
		return Plan{}, err
	}
	seen := map[string]bool{}
	ids := append([]string(nil), p.Remove...)
	for _, group := range [][]Step{p.Add, p.Replace} {
		for _, step := range group {
			ids = append(ids, step.ID)
		}
	}
	for _, id := range ids {
		if id == "" || seen[id] {
			return Plan{}, fmt.Errorf("replan: empty or conflicting patch step IDs")
		}
		seen[id] = true
	}
	steps := make(map[string]Step, len(source.Steps))
	for _, step := range source.Steps {
		steps[step.ID] = step
	}
	for _, id := range p.Remove {
		if _, ok := steps[id]; !ok {
			return Plan{}, fmt.Errorf("replan: cannot remove unknown step %q", id)
		}
		delete(steps, id)
	}
	for _, step := range p.Replace {
		if _, ok := steps[step.ID]; !ok {
			return Plan{}, fmt.Errorf("replan: cannot replace unknown step %q", step.ID)
		}
		steps[step.ID] = step
	}
	for _, step := range p.Add {
		if _, ok := steps[step.ID]; ok {
			return Plan{}, fmt.Errorf("replan: duplicate added step %q", step.ID)
		}
		steps[step.ID] = step
	}
	out := Plan{ID: source.ID, Goal: source.Goal, Version: source.Version + 1, Steps: make([]Step, 0, len(steps))}
	for _, step := range steps {
		out.Steps = append(out.Steps, step)
	}
	return out.Normalize()
}

// ValidatePatchCompatibility rejects semantic substitutions that the catalog
// has not explicitly authorized. A model-generated patch is never itself
// evidence that two capabilities are interchangeable.
func ValidatePatchCompatibility(source Plan, patch Patch, tools []model.ToolSpec) error {
	before := make(map[string]Step, len(source.Steps))
	for _, step := range source.Steps {
		before[step.ID] = step
	}
	for _, replacement := range patch.Replace {
		original, ok := before[replacement.ID]
		if !ok {
			continue
		}
		if effectiveMode(original.Mode) != effectiveMode(replacement.Mode) {
			return fmt.Errorf("REPLAN_INCOMPATIBLE_MODE_REPLACEMENT")
		}
		if effectiveMode(original.Mode) == StepModeAgentic {
			for _, capability := range replacement.Capabilities {
				allowed := containsCapability(original.Capabilities, capability, capability)
				if !allowed && len(tools) > 0 {
					to, ok := findTool(tools, capability)
					if ok {
						for _, old := range original.Capabilities {
							from, found := findTool(tools, old)
							if found && from.Function.Name == to.Function.Name {
								allowed = true
							}
						}
					}
				}
				if !allowed {
					return fmt.Errorf("REPLAN_INCOMPATIBLE_AGENTIC_ALLOWLIST")
				}
			}
			continue
		}
		if original.Capability == replacement.Capability || len(tools) == 0 {
			continue
		}
		from, fromOK := findTool(tools, original.Capability)
		to, toOK := findTool(tools, replacement.Capability)
		if !fromOK || !toOK || from.SemanticGroup == "" || from.SemanticGroup != to.SemanticGroup || !containsCapability(from.Fallbacks, replacement.Capability, to.Function.Name) || weakensEffect(from.SideEffect, to.SideEffect) {
			return fmt.Errorf("REPLAN_INCOMPATIBLE_CAPABILITY_REPLACEMENT: step %q cannot replace capability %q with incompatible capability %q", replacement.ID, original.Capability, replacement.Capability)
		}
	}
	return nil
}

func containsCapability(values []string, capability, toolID string) bool {
	for _, value := range values {
		if value == capability || value == toolID {
			return true
		}
	}
	return false
}

func weakensEffect(before, after string) bool {
	isWrite := func(value string) bool {
		return value == "write" || value == "write_idempotent" || value == "write_non_idempotent" || value == "destructive"
	}
	return isWrite(before) && !isWrite(after)
}

type ModelReplanner struct {
	// DisableOutputTool supports providers that cannot accept union tool schemas.
	DisableOutputTool bool
	// MaxRepairAttempts defaults to one when nil; an explicit zero disables repair.
	MaxRepairAttempts *int
	Model             model.Model
	Prompt            string
	Trace             func(event string, details map[string]any)
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
		Original Plan                     `json:"original"`
		Failure  string                   `json:"failure"`
		Tools    []model.PlanningToolSpec `json:"tools"`
	}{Original: input.Original, Failure: input.Failure, Tools: model.PlanningToolCatalog(input.Tools)}
	b, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return Patch{}, fmt.Errorf("replanner: encode input: %w", marshalErr)
	}
	outputTool, err := outputTool[Patch](patchOutputToolName, "Return a patch for the existing plan. add_steps and replace_steps contain complete step objects directly; remove_steps contains existing step ID strings.")
	if err != nil {
		return Patch{}, fmt.Errorf("replanner: build output schema: %w", err)
	}
	messages := []model.Message{{Role: model.RoleSystem, Content: prompt}, {Role: model.RoleUser, Content: string(b)}}
	// The only callable tool here is the synthetic typed output channel. Domain
	// tools remain catalog data and cannot be executed during replanning.
	r.trace("model.request", map[string]any{"messages": messages, "output_tool": outputTool})
	return generateCandidate(ctx, r.Model, "replan", r.MaxRepairAttempts, r.Model.Capabilities().ToolCalling && !r.DisableOutputTool, messages, outputTool, r.trace, func(candidate string) (Patch, error) {
		patch, err := decodePatch(candidate)
		if err == nil {
			err = ValidatePatchCompatibility(input.Original, patch, input.Tools)
		}
		if err == nil {
			var patched Plan
			patched, err = patch.Apply(input.Original)
			if err == nil {
				err = ValidateAgainstTools(patched, input.Tools)
			}
		}
		return patch, err
	})
}

func (r *ModelReplanner) trace(event string, details map[string]any) {
	if r.Trace != nil {
		r.Trace(event, details)
	}
}
