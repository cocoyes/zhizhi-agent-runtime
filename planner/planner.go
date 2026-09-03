package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/structuredoutput"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type Planner interface {
	Plan(context.Context, string, []model.ToolSpec) (plan.Plan, error)
}

type ModelPlanner struct {
	Model  model.Model
	Prompt string
	Trace  func(event string, details map[string]any)
}

const planOutputToolName = "zhizhi_emit_plan"

func NewModelPlanner(m model.Model) *ModelPlanner { return &ModelPlanner{Model: m} }

func (p *ModelPlanner) Plan(ctx context.Context, input string, tools []model.ToolSpec) (plan.Plan, error) {
	if p == nil || p.Model == nil {
		return plan.Plan{}, fmt.Errorf("planner: model is required")
	}
	system := p.Prompt
	if system == "" {
		system = "你是 Agent 的规划组件。理解用户目标，只选择真正有帮助的工具，并把它们编排成依赖安全的执行图。需要根据前置结果做不同处理时，使用条件步骤；避免无关步骤和重复工作。只返回可执行的 JSON，格式为 {version,steps}；每个步骤包含 id、capability、depends_on，可选 input、bindings、condition。每个工具 input_schema 中的必填字段都必须由静态 input 或 bindings 提供。引用前置结果必须使用 bindings，source_path 必须来自对应工具的 output_schema，target_path 必须来自当前工具的 input_schema；不要把 step.path 引用写进 input 字符串。condition 包含 source_step、source_path，并且必须在 equals 或 not_equals 中选择一个；比较值必须严格使用 source output_schema 定义的原始值，不能改写成自然语言。source_step 必须同时出现在 depends_on 中。用户表达“否则”时，为互斥分支分别使用 equals 和 not_equals。不要预先添加仅在某一步执行失败后才使用的 fallback 步骤；运行时重规划器会在真实故障后选择替代能力。"
	}
	catalog, err := json.Marshal(model.PlanningToolCatalog(tools))
	if err != nil {
		return plan.Plan{}, fmt.Errorf("planner: encode tool catalog: %w", err)
	}
	// The planner needs tool metadata, not permission to call tools. Supplying
	// API-level tools here lets many models return a tool call instead of the
	// requested plan JSON, leaving the planner with empty text.
	user := input + "\n可用工具目录（仅用于规划，不要调用）：\n" + string(catalog)
	outputTool, err := structuredoutput.ToolFor[plan.Plan](planOutputToolName, "Return the complete executable plan. The arguments must exactly match this schema.")
	if err != nil {
		return plan.Plan{}, fmt.Errorf("planner: build output schema: %w", err)
	}
	useToolOutput := p.Model.Capabilities().ToolCalling
	modelInput := structuredModelInput([]model.Message{{Role: model.RoleSystem, Content: system}, {Role: model.RoleUser, Content: user}}, outputTool, useToolOutput)
	p.trace("model.request", map[string]any{"messages": modelInput.Messages, "output_tool": outputTool, "tool_choice": modelInput.ToolChoice})
	out, err := p.Model.Generate(ctx, modelInput)
	if err != nil {
		return plan.Plan{}, fmt.Errorf("planner model: %w", err)
	}
	if out == nil {
		return plan.Plan{}, fmt.Errorf("planner model returned nil output")
	}
	p.trace("model.completed", map[string]any{"content": out.Text, "reasoning_content": out.ReasoningContent, "tool_call_requests": out.ToolCalls})
	candidate, candidateErr := structuredoutput.Candidate(out, planOutputToolName, useToolOutput)
	for repairAttempt := 0; ; repairAttempt++ {
		var result plan.Plan
		decodeErr := candidateErr
		if decodeErr == nil {
			result, decodeErr = decodePlan(candidate, tools)
		}
		if decodeErr == nil {
			return result, nil
		}
		if repairAttempt >= 2 {
			return plan.Plan{}, fmt.Errorf("planner: invalid structured output after repair: %w", decodeErr)
		}
		repairRequest := user + "\nInvalid candidate:\n" + candidate + "\nValidation error:\n" + decodeErr.Error()
		p.trace("repair.request", map[string]any{"content": repairRequest})
		repairMessages := []model.Message{{Role: model.RoleSystem, Content: "Produce the required plan tool call again after correcting the validation error. Preserve intent. All steps need non-empty id and capability fields, dependencies must form an acyclic graph, and every required tool input must be covered by static input or bindings. Never put {{step.path}} templates in input; represent every dependency value with a bindings entry. Do not pre-plan failure-only fallback steps; runtime replanning handles actual failures."}, {Role: model.RoleUser, Content: repairRequest}}
		repair, repairErr := p.Model.Generate(ctx, structuredModelInput(repairMessages, outputTool, useToolOutput))
		if repairErr != nil {
			return plan.Plan{}, fmt.Errorf("planner: invalid structured output: %w", decodeErr)
		}
		if repair == nil {
			return plan.Plan{}, fmt.Errorf("planner repair model returned nil output")
		}
		p.trace("repair.completed", map[string]any{"content": repair.Text, "reasoning_content": repair.ReasoningContent, "tool_call_requests": repair.ToolCalls})
		candidate, candidateErr = structuredoutput.Candidate(repair, planOutputToolName, useToolOutput)
	}
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

func decodePlan(text string, tools []model.ToolSpec) (plan.Plan, error) {
	var result plan.Plan
	if err := structuredoutput.DecodeStrict(text, &result); err != nil {
		return plan.Plan{}, err
	}
	if result.Version == 0 {
		result.Version = 1
	}
	normalized, err := result.Normalize()
	if err != nil {
		return plan.Plan{}, fmt.Errorf("invalid plan: %w", err)
	}
	if len(normalized.Steps) == 0 {
		return plan.Plan{}, fmt.Errorf("invalid plan: initial plan must contain at least one executable step")
	}
	if err := plan.ValidateAgainstTools(normalized, tools); err != nil {
		return plan.Plan{}, fmt.Errorf("invalid plan: %w", err)
	}
	for _, step := range normalized.Steps {
		encoded, err := json.Marshal(step.Input)
		if err != nil {
			return plan.Plan{}, fmt.Errorf("encode input for step %q: %w", step.ID, err)
		}
		if bytes.Contains(encoded, []byte("{{")) {
			return plan.Plan{}, fmt.Errorf("invalid plan: step %q contains a template reference in input; use bindings instead", step.ID)
		}
		if len(step.Bindings) == 0 && step.Input != nil {
			for _, spec := range tools {
				if !providesCapability(spec, step.Capability) || len(spec.Function.Parameters) == 0 {
					continue
				}
				if err := tool.ValidateValue(spec.Function.Name, spec.Function.Parameters, step.Input); err != nil {
					return plan.Plan{}, fmt.Errorf("invalid plan input for step %q: %w; dependency values must use bindings", step.ID, err)
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
