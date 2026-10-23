package plan

import (
	"errors"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

// RuleFixer 自动修复已知的计划验证错误模式
type RuleFixer struct {
	Rules []FixRule
	Trace func(event string, details map[string]any)
}

// FixRule 定义一个修复规则
type FixRule interface {
	// Name 返回规则名称
	Name() string
	// Match 判断是否匹配此错误类型
	Match(p Plan, err error) bool
	// Apply 应用修复,返回修复后的计划、是否修改、错误
	Apply(p Plan, tools []model.ToolSpec) (Plan, bool, error)
}

// NewRuleFixer 创建默认规则修复器
func NewRuleFixer() *RuleFixer {
	return &RuleFixer{
		Rules: []FixRule{
			&SchemalessBindingFix{},
			&BudgetConflictFix{},
		},
	}
}

// TryFix 尝试用规则修复计划
func (f *RuleFixer) TryFix(p Plan, validationErr error, tools []model.ToolSpec) (Plan, bool, error) {
	if validationErr == nil {
		return p, false, nil
	}

	for _, rule := range f.Rules {
		if !rule.Match(p, validationErr) {
			continue
		}

		f.trace("rule_fixer.attempt", map[string]any{
			"rule":  rule.Name(),
			"error": validationErr.Error(),
		})

		fixed, applied, err := rule.Apply(p, tools)
		if err != nil {
			f.trace("rule_fixer.failed", map[string]any{
				"rule":  rule.Name(),
				"error": err.Error(),
			})
			continue
		}

		if !applied {
			continue
		}

		// 验证修复后的计划
		if err := fixed.Validate(); err != nil {
			f.trace("rule_fixer.invalid", map[string]any{
				"rule":  rule.Name(),
				"error": err.Error(),
			})
			continue
		}

		if err := ValidateAgainstTools(fixed, tools); err != nil {
			f.trace("rule_fixer.invalid_tools", map[string]any{
				"rule":  rule.Name(),
				"error": err.Error(),
			})
			continue
		}

		f.trace("rule_fixer.success", map[string]any{
			"rule":           rule.Name(),
			"original_error": validationErr.Error(),
		})

		return fixed, true, nil
	}

	return p, false, validationErr
}

func (f *RuleFixer) trace(event string, details map[string]any) {
	if f.Trace != nil {
		f.Trace(event, details)
	}
}

// ClassifyError 分类验证错误
func ClassifyError(err error) string {
	if err == nil {
		return "none"
	}

	// StructuredOutputError deliberately hides its cause from Error(), so match
	// against the unwrapped chain as well or every real failure looks "other".
	var msg strings.Builder
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg.WriteString(e.Error())
		msg.WriteString("\n")
	}
	message := msg.String()

	if strings.Contains(message, "declares no output schema") ||
		strings.Contains(message, "absent from") && strings.Contains(message, "output schema") {
		return "schema_missing"
	}
	if strings.Contains(message, "output_schema") && strings.Contains(message, "null") {
		return "schema_null"
	}
	// Range violations and schema violations both mention min/max, so the
	// discriminator has to be the "range" keyword itself.
	if strings.Contains(message, "budget") && strings.Contains(message, "range") {
		return "budget_range"
	}
	if strings.Contains(message, "tool_budget") || (strings.Contains(message, "budget") && strings.Contains(message, "invalid")) {
		return "budget_invalid"
	}
	if strings.Contains(message, "cycle") || strings.Contains(message, "circular") {
		return "circular_dependency"
	}
	if strings.Contains(message, "mode") && strings.Contains(message, "agentic") {
		return "mode_conflict"
	}
	if strings.Contains(message, "unknown capability") {
		return "capability_unknown"
	}
	if strings.Contains(message, "binding") {
		return "binding_invalid"
	}

	return "other"
}

// errorChain flattens an error and its causes. StructuredOutputError and
// ModelError deliberately hide their cause from Error(), so anything matching
// on message text has to walk the chain or every real failure looks opaque.
func errorChain(err error) string {
	var msg strings.Builder
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg.WriteString(e.Error())
		msg.WriteString(" ")
	}
	return msg.String()
}

// Helper functions

func unique(items []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func findToolByCapability(tools []model.ToolSpec, capability string) (model.ToolSpec, bool) {
	for _, tool := range tools {
		if tool.Function.Name == capability {
			return tool, true
		}
		for _, cap := range tool.Capabilities {
			if cap == capability {
				return tool, true
			}
		}
	}
	return model.ToolSpec{}, false
}
