package plan

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

func TestRuleFixerSchemalessBinding(t *testing.T) {
	// 创建 schema-less 工具
	tools := []model.ToolSpec{
		{
			Type: "function",
			Function: model.FunctionSpec{
				Name:        "locate",
				Description: "获取位置",
				Parameters:  nil, // schema-less
			},
		},
		{
			Type: "function",
			Function: model.FunctionSpec{
				Name:        "weather",
				Description: "查天气",
				Parameters: mustMarshal(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []string{"city"},
				}),
			},
		},
	}

	// 创建有问题的计划(binding 引用 schema-less 工具)
	invalidPlan := Plan{
		ID:      "test",
		Goal:    "查询天气",
		Version: 1,
		Steps: []Step{
			{
				ID:         "locate",
				Mode:       StepModeTool,
				Capability: "locate",
			},
			{
				ID:         "weather",
				Mode:       StepModeTool,
				Capability: "weather",
				DependsOn:  []string{"locate"},
				Bindings: []InputBinding{
					{
						SourceStep: "locate",
						SourcePath: "/city",
						TargetPath: "/city",
					},
				},
			},
		},
	}

	// 模拟验证错误
	validationErr := ValidateAgainstTools(invalidPlan, tools)
	if validationErr == nil {
		t.Fatal("expected validation error for schema-less binding")
	}

	// 应用规则修复
	fixer := NewRuleFixer()
	fixed, applied, err := fixer.TryFix(invalidPlan, validationErr, tools)

	if err != nil {
		t.Fatalf("fix failed: %v", err)
	}

	if !applied {
		t.Fatal("fix was not applied")
	}

	// 验证修复后的计划
	if err := fixed.Validate(); err != nil {
		t.Fatalf("fixed plan is invalid: %v", err)
	}

	if err := ValidateAgainstTools(fixed, tools); err != nil {
		t.Fatalf("fixed plan failed tool validation: %v", err)
	}

	// 验证已转换为 agentic 步骤
	if len(fixed.Steps) != 1 {
		t.Fatalf("expected 1 agentic step, got %d steps", len(fixed.Steps))
	}

	step := fixed.Steps[0]
	if step.Mode != StepModeAgentic {
		t.Fatalf("expected agentic mode, got %s", step.Mode)
	}

	if len(step.Capabilities) != 2 {
		t.Fatalf("expected 2 capabilities, got %d", len(step.Capabilities))
	}

	if step.ToolBudget.Max < 1 {
		t.Fatal("invalid tool budget")
	}
}

func TestRuleFixerBudgetConflict(t *testing.T) {
	// 创建有预算冲突的计划
	invalidPlan := Plan{
		ID:      "test",
		Goal:    "测试",
		Version: 1,
		Steps: []Step{
			{
				ID:              "test",
				Mode:            StepModeAgentic,
				Goal:            "测试目标",
				SuccessCriteria: "成功",
				Capabilities:    []string{"tool1"},
				ToolBudget: ToolCallBudget{
					Min:               5,
					Max:               2,  // min > max (错误)
					RequiredSuccesses: 10, // > max (错误)
				},
			},
		},
	}

	validationErr := invalidPlan.Validate()
	if validationErr == nil {
		t.Fatal("expected validation error for budget conflict")
	}

	// 应用规则修复
	fixer := &BudgetConflictFix{}
	fixed, applied, err := fixer.Apply(invalidPlan, nil)

	if err != nil {
		t.Fatalf("fix failed: %v", err)
	}

	if !applied {
		t.Fatal("fix was not applied")
	}

	// 验证修复后的预算
	budget := fixed.Steps[0].ToolBudget
	if budget.Min > budget.Max {
		t.Fatalf("min still > max: min=%d max=%d", budget.Min, budget.Max)
	}

	if budget.RequiredSuccesses > budget.Max {
		t.Fatalf("required_successes > max: required=%d max=%d", budget.RequiredSuccesses, budget.Max)
	}

	if budget.Max < 1 {
		t.Fatal("max should be >= 1")
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		error    string
		expected string
	}{
		{
			error:    "binding source path \"/city\" is absent from step output schema",
			expected: "schema_missing",
		},
		{
			error:    "output_schema is null",
			expected: "schema_null",
		},
		{
			error:    "tool_budget invalid: min > max",
			expected: "budget_invalid",
		},
		{
			error:    "invalid budget range: min 5 > max 2",
			expected: "budget_range",
		},
		{
			error:    "circular dependency detected",
			expected: "circular_dependency",
		},
		{
			error:    "unknown error",
			expected: "other",
		},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := ClassifyError(&mockError{msg: tt.error})
			if result != tt.expected {
				t.Errorf("ClassifyError(%q) = %q, want %q", tt.error, result, tt.expected)
			}
		})
	}
}

type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type schemalessModel struct{ calls int }

func (m *schemalessModel) ID() string { return "schemaless" }
func (m *schemalessModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{Declared: true}
}
func (m *schemalessModel) Generate(context.Context, model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"locate","mode":"tool","capability":"locate"},{"id":"weather","mode":"tool","capability":"weather","depends_on":["locate"],"bindings":[{"source_step":"locate","source_path":"/city","target_path":"/city"}]}]}`}, nil
}
func (m *schemalessModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

// The fixer must be reachable from the real planner path, not only when called
// directly with a hand-built plan.
func TestPlannerRuleFixerRepairsSchemalessBinding(t *testing.T) {
	tools := []model.ToolSpec{
		{Type: "function", Function: model.FunctionSpec{Name: "locate", Description: "获取城市"}, Capabilities: []string{"locate"}},
		{Type: "function", Function: model.FunctionSpec{Name: "weather", Description: "查天气", Parameters: mustMarshal(map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}})}, Capabilities: []string{"weather"}},
	}
	zero := 0
	planner := NewModelPlanner(&schemalessModel{})
	planner.MaxRepairAttempts = &zero
	got, err := planner.PlanWithFixer(context.Background(), "查天气", tools, NewRuleFixer())
	if err != nil {
		t.Fatalf("fixer did not rescue the plan: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].Mode != StepModeAgentic {
		t.Fatalf("expected one merged agentic step, got %+v", got.Steps)
	}
}
