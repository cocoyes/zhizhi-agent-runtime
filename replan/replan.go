package replan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	Add     []plan.Step `json:"add_steps,omitempty"`
	Replace []plan.Step `json:"replace_steps,omitempty"`
	Remove  []string    `json:"remove_steps,omitempty"`
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
}

func NewModelReplanner(m model.Model) *ModelReplanner { return &ModelReplanner{Model: m} }
func (r *ModelReplanner) Replan(ctx context.Context, input Input) (Patch, error) {
	if r == nil || r.Model == nil {
		return Patch{}, fmt.Errorf("replanner: model is required")
	}
	prompt := r.Prompt
	if prompt == "" {
		prompt = "Repair the execution plan after a required step failed. Return only JSON with add_steps, replace_steps, and remove_steps. Preserve successful work and do not add retry steps."
	}
	b, _ := json.Marshal(input)
	out, err := r.Model.Generate(ctx, model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: prompt}, {Role: model.RoleUser, Content: string(b)}}, Tools: input.Tools, JSONMode: true})
	if err != nil {
		return Patch{}, fmt.Errorf("replanner model: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	var patch Patch
	if err := json.Unmarshal([]byte(text), &patch); err != nil {
		repair, repairErr := r.Model.Generate(ctx, model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Return only valid JSON with add_steps, replace_steps, remove_steps."}, {Role: model.RoleUser, Content: text}}, JSONMode: true})
		if repairErr != nil {
			return Patch{}, fmt.Errorf("replanner: invalid patch: %w", err)
		}
		text = strings.TrimSpace(repair.Text)
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
		if repairErr = json.Unmarshal([]byte(text), &patch); repairErr != nil {
			return Patch{}, fmt.Errorf("replanner: invalid patch after repair: %w", repairErr)
		}
	}
	return patch, nil
}
