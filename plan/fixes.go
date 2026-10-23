package plan

import (
	"fmt"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

// SchemalessBindingFix 修复 schema-less 工具的 binding 问题
// 当工具没有 output_schema 时,将依赖该工具的步骤合并为 agentic 步骤
type SchemalessBindingFix struct{}

func (f *SchemalessBindingFix) Name() string {
	return "schemaless_binding"
}

func (f *SchemalessBindingFix) Match(p Plan, err error) bool {
	if err == nil {
		return false
	}

	msg := errorChain(err)
	// 匹配各种 schema-less 工具导致的 binding 错误
	return strings.Contains(msg, "declares no output schema") ||
		strings.Contains(msg, "absent from") && strings.Contains(msg, "output schema") ||
		strings.Contains(msg, "output_schema") && strings.Contains(msg, "null")
}

func (f *SchemalessBindingFix) Apply(p Plan, tools []model.ToolSpec) (Plan, bool, error) {
	stepIndex := make(map[string]Step, len(p.Steps))
	for _, step := range p.Steps {
		stepIndex[step.ID] = step
	}

	// A binding is unusable when the source tool has no output schema at all,
	// or when the referenced path is not in it. Both mean the same thing at
	// runtime: the value cannot be resolved deterministically.
	problematic := make(map[string]bool)
	for _, step := range p.Steps {
		for _, binding := range step.Bindings {
			sourceStep, ok := stepIndex[binding.SourceStep]
			if !ok || sourceStep.Mode == StepModeAgentic {
				continue
			}
			spec, found := findToolByCapability(tools, sourceStep.Capability)
			if !found || outputSchemaMissing(spec) {
				problematic[sourceStep.ID] = true
				problematic[step.ID] = true
				continue
			}
			if _, ok := schemaAt(spec.OutputSchema, binding.SourcePath); !ok {
				problematic[sourceStep.ID] = true
				problematic[step.ID] = true
			}
		}
	}
	if len(problematic) == 0 {
		return p, false, nil
	}

	capabilities := []string{}
	goals := []string{}
	for _, step := range p.Steps {
		if !problematic[step.ID] {
			continue
		}
		if step.Mode == StepModeAgentic {
			capabilities = append(capabilities, step.Capabilities...)
			if step.Goal != "" {
				goals = append(goals, step.Goal)
			}
			continue
		}
		if step.Capability != "" {
			capabilities = append(capabilities, step.Capability)
		}
		if spec, ok := findToolByCapability(tools, step.Capability); ok && spec.Function.Description != "" {
			goals = append(goals, spec.Function.Description)
		} else if step.ID != "" {
			goals = append(goals, stepIDToGoal(step.ID))
		}
	}
	capabilities = unique(capabilities)
	if len(capabilities) == 0 {
		return p, false, nil
	}

	agentic := Step{
		ID:              fmt.Sprintf("agentic-schemaless-%d", p.Version+1),
		Mode:            StepModeAgentic,
		Goal:            mergeGoals(goals, p.Goal),
		SuccessCriteria: "依据工具的真实返回完成该目标;信息不足时说明缺口,不得编造。",
		Capabilities:    capabilities,
		ToolBudget: ToolCallBudget{
			Min:               0,
			Max:               min(len(capabilities)*2, 8),
			RequiredSuccesses: 1,
		},
	}

	steps := make([]Step, 0, len(p.Steps))
	for _, step := range p.Steps {
		if problematic[step.ID] {
			continue
		}
		// Steps that were ordered behind a removed step keep a valid graph by
		// depending on the replacement instead of dangling.
		rewired := step.DependsOn[:0:0]
		for _, dep := range step.DependsOn {
			if problematic[dep] {
				rewired = appendUnique(rewired, agentic.ID)
				continue
			}
			rewired = appendUnique(rewired, dep)
		}
		step.DependsOn = rewired
		steps = append(steps, step)
	}
	steps = append(steps, agentic)

	fixed := Plan{ID: p.ID, Goal: p.Goal, Version: p.Version + 1, Steps: steps}
	normalized, err := fixed.Normalize()
	if err != nil {
		return p, false, err
	}
	return normalized, true, nil
}

func outputSchemaMissing(spec model.ToolSpec) bool {
	raw := strings.TrimSpace(string(spec.OutputSchema))
	return raw == "" || raw == "null"
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

// BudgetConflictFix 修复工具预算冲突
type BudgetConflictFix struct{}

func (f *BudgetConflictFix) Name() string {
	return "budget_conflict"
}

func (f *BudgetConflictFix) Match(p Plan, err error) bool {
	if err == nil {
		return false
	}

	msg := errorChain(err)
	return strings.Contains(msg, "tool_budget") ||
		strings.Contains(msg, "budget") ||
		(strings.Contains(msg, "min") && strings.Contains(msg, "max"))
}

func (f *BudgetConflictFix) Apply(p Plan, tools []model.ToolSpec) (Plan, bool, error) {
	fixed := false

	for i := range p.Steps {
		if p.Steps[i].Mode != StepModeAgentic {
			continue
		}

		budget := &p.Steps[i].ToolBudget

		// 修正负数
		if budget.Min < 0 {
			budget.Min = 0
			fixed = true
		}

		// 修正 max < 1
		if budget.Max < 1 {
			budget.Max = max(len(p.Steps[i].Capabilities)*2, 3)
			fixed = true
		}

		// 修正 min > max
		if budget.Min > budget.Max {
			budget.Min = budget.Max / 2
			if budget.Min < 0 {
				budget.Min = 0
			}
			fixed = true
		}

		// 修正 required_successes > max
		if budget.RequiredSuccesses > budget.Max {
			budget.RequiredSuccesses = budget.Max
			fixed = true
		}

		// 修正 required_successes < 0
		if budget.RequiredSuccesses < 0 {
			budget.RequiredSuccesses = 0
			fixed = true
		}
	}

	if !fixed {
		return p, false, nil
	}

	p.Version++
	normalized, err := p.Normalize()
	return normalized, true, err
}

// Helper functions

func stepIDToGoal(id string) string {
	// 简单的 ID 到目标的转换
	// 例如 "step-1-locate" -> "定位"
	parts := strings.Split(id, "-")
	if len(parts) > 2 {
		return parts[len(parts)-1]
	}
	return id
}

func mergeGoals(goals []string, fallback string) string {
	if len(goals) == 0 {
		if fallback != "" {
			return fallback
		}
		return "完成用户请求"
	}

	if len(goals) == 1 {
		return goals[0]
	}

	// 合并多个目标
	return fmt.Sprintf("依次完成: %s", strings.Join(goals, " → "))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
