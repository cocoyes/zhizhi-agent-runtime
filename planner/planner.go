package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/plan"
)

type Planner interface {
	Plan(context.Context, string, []model.ToolSpec) (plan.Plan, error)
}

type ModelPlanner struct {
	Model  model.Model
	Prompt string
}

func NewModelPlanner(m model.Model) *ModelPlanner { return &ModelPlanner{Model: m} }

func (p *ModelPlanner) Plan(ctx context.Context, input string, tools []model.ToolSpec) (plan.Plan, error) {
	if p == nil || p.Model == nil {
		return plan.Plan{}, fmt.Errorf("planner: model is required")
	}
	system := p.Prompt
	if system == "" {
		system = "你是 Agent 的规划组件。理解用户目标，只选择真正有帮助的工具，并把它们编排成依赖安全的执行图。需要根据前置结果做不同处理时，使用条件步骤；避免无关步骤和重复工作。只返回可执行的 JSON，格式为 {version,steps}；每个步骤包含 id、capability、depends_on，可选 input、bindings、condition。condition 包含 source_step、source_path、equals，且 source_step 必须同时出现在 depends_on 中。"
	}
	out, err := p.Model.Generate(ctx, model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: system}, {Role: model.RoleUser, Content: input}}, Tools: tools, JSONMode: true})
	if err != nil {
		return plan.Plan{}, fmt.Errorf("planner model: %w", err)
	}
	text := strings.TrimSpace(out.Text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	var result plan.Plan
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		// One bounded repair attempt is allowed for providers without strict JSON mode.
		repair, repairErr := p.Model.Generate(ctx, model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Return only valid JSON matching {version,steps}. Repair the candidate without changing its intent."}, {Role: model.RoleUser, Content: text}}, JSONMode: true})
		if repairErr != nil {
			return plan.Plan{}, fmt.Errorf("planner: invalid structured output: %w", err)
		}
		text = strings.TrimSpace(repair.Text)
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
		if repairErr = json.Unmarshal([]byte(text), &result); repairErr != nil {
			return plan.Plan{}, fmt.Errorf("planner: invalid structured output after repair: %w", repairErr)
		}
	}
	if result.Version == 0 {
		result.Version = 1
	}
	if err := result.Validate(); err != nil {
		return plan.Plan{}, fmt.Errorf("planner: invalid plan: %w", err)
	}
	return result.Normalize()
}
