package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/structuredoutput"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type Planner interface {
	Plan(context.Context, string, []model.ToolSpec) (Plan, error)
}

type ModelPlanner struct {
	// DisableOutputTool supports providers that cannot accept union tool schemas.
	DisableOutputTool bool
	// MaxRepairAttempts defaults to one when nil; an explicit zero disables repair.
	MaxRepairAttempts *int
	Model             model.Model
	Prompt            string
	Trace             func(event string, details map[string]any)
}

const planOutputToolName = "zhizhi_emit_plan"

func NewModelPlanner(m model.Model) *ModelPlanner { return &ModelPlanner{Model: m} }

func (p *ModelPlanner) Plan(ctx context.Context, input string, tools []model.ToolSpec) (Plan, error) {
	if p == nil || p.Model == nil {
		return Plan{}, fmt.Errorf("planner: model is required")
	}
	system := p.Prompt
	if system == "" {
		system = "你是 Agent 的规划组件。理解用户目标，只选择真正有帮助的工具，并把它们编排成依赖安全的执行图。需要根据前置结果做不同处理时，使用条件步骤；避免无关步骤和重复工作。只返回可执行的 JSON，格式为 {version,steps}。确定性工具步骤包含 id、capability、depends_on，可选 input、bindings、condition。每个工具 input_schema 中的必填字段都必须由静态 input 或 bindings 提供。引用前置结果必须使用 bindings，且只有来源工具的 output_schema 明确定义了所需路径时才能绑定；output_schema 缺失、为 null 或没有所需路径时，禁止猜测 source_path。若多个工具必须依据前一工具的真实运行时观察串联，把它们合并为一个 mode: \"agentic\" 的步骤，填写明确的 goal 和 success_criteria，仅在 capabilities 中列出完成该目标所需的最小工具集合，并设置有限的 tool_budget（min、max、required_successes）；agentic 步骤不使用 bindings。能用静态输入直接调用的工具仍优先使用确定性步骤。target_path 必须来自当前工具的 input_schema；不要把 step.path 引用写进 input 字符串。condition 包含 source_step、source_path，并且必须在 equals 或 not_equals 中选择一个；比较值必须严格使用 source output_schema 定义的原始值，不能改写成自然语言。source_step 必须同时出现在 depends_on 中。用户表达“否则”时，为互斥分支分别使用 equals 和 not_equals。不要预先添加仅在某一步执行失败后才使用的 fallback 步骤；运行时重规划器会在真实故障后选择替代能力。"
	}
	catalog, err := json.Marshal(model.PlanningToolCatalog(tools))
	if err != nil {
		return Plan{}, fmt.Errorf("planner: encode tool catalog: %w", err)
	}
	// The planner needs tool metadata, not permission to call tools. Supplying
	// API-level tools here lets many models return a tool call instead of the
	// requested plan JSON, leaving the planner with empty text.
	user := input + "\n可用工具目录（仅用于规划，不要调用）：\n" + string(catalog)
	outputTool, err := outputTool[Plan](planOutputToolName, "Return the complete executable plan. The arguments must exactly match this schema.")
	if err != nil {
		return Plan{}, fmt.Errorf("planner: build output schema: %w", err)
	}
	useToolOutput := p.Model.Capabilities().ToolCalling && !p.DisableOutputTool
	modelInput := structuredModelInput([]model.Message{{Role: model.RoleSystem, Content: system}, {Role: model.RoleUser, Content: user}}, outputTool, useToolOutput)
	p.trace("model.request", map[string]any{"messages": modelInput.Messages, "output_tool": outputTool, "tool_choice": modelInput.ToolChoice})
	return generateCandidate(ctx, p.Model, "initial_plan", p.MaxRepairAttempts, useToolOutput, modelInput.Messages, outputTool, p.trace, func(candidate string) (Plan, error) { return decodePlan(candidate, tools) })
}

func structuredModelInput(messages []model.Message, outputTool model.ToolSpec, useTool bool) model.ModelInput {
	if useTool {
		return model.ModelInput{Messages: messages, Tools: []model.ToolSpec{outputTool}, ToolChoice: model.ToolChoiceForced}
	}
	return model.ModelInput{Messages: messages, JSONMode: true}
}

func (p *ModelPlanner) trace(event string, details map[string]any) {
	if p.Trace != nil {
		p.Trace(event, details)
	}
}

func decodePlan(text string, tools []model.ToolSpec) (Plan, error) {
	var result Plan
	if err := structuredoutput.DecodeStrict(text, &result); err != nil {
		return Plan{}, invalid("decode", err)
	}
	if err := validateCandidateSchema[Plan](text); err != nil {
		return Plan{}, err
	}
	if result.Version == 0 {
		result.Version = 1
	}
	normalized, err := result.Normalize()
	if err != nil {
		return Plan{}, fmt.Errorf("invalid plan: %w", err)
	}
	if len(normalized.Steps) == 0 {
		return Plan{}, fmt.Errorf("invalid plan: initial plan must contain at least one executable step")
	}
	if err := ValidateAgainstTools(normalized, tools); err != nil {
		return Plan{}, fmt.Errorf("invalid plan: %w", err)
	}
	for _, step := range normalized.Steps {
		encoded, err := json.Marshal(step.Input)
		if err != nil {
			return Plan{}, fmt.Errorf("encode input for step %q: %w", step.ID, err)
		}
		if bytes.Contains(encoded, []byte("{{")) {
			return Plan{}, fmt.Errorf("invalid plan: step %q contains a template reference in input; use bindings instead", step.ID)
		}
		if len(step.Bindings) == 0 && step.Input != nil {
			for _, spec := range tools {
				if !providesCapability(spec, step.Capability) || len(spec.Function.Parameters) == 0 {
					continue
				}
				if err := tool.ValidateValue(spec.Function.Name, spec.Function.Parameters, step.Input); err != nil {
					return Plan{}, fmt.Errorf("invalid plan input for step %q: %w; dependency values must use bindings", step.ID, err)
				}
				break
			}
		}
	}
	return normalized, nil
}

func providesCapability(spec model.ToolSpec, capability string) bool {
	if spec.Function.Name == capability {
		return true
	}
	for _, value := range spec.Capabilities {
		if value == capability {
			return true
		}
	}
	return false
}
