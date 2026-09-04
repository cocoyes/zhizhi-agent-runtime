// Package react implements the runtime's private bounded model-tool loop.
package react

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type Budget struct {
	MinToolCalls      int
	MaxToolCalls      int
	RequiredSuccesses int
	MaxIterations     int
	MaxModelCalls     int
}
type Guard func(context.Context, tool.Spec) error
type Confirm func(context.Context, tool.Spec, json.RawMessage) (bool, error)
type Result struct {
	Text             string
	Usage            model.Usage
	ModelCalls       int
	ToolCalls        int
	SuccessfulTools  int
	FailedTools      int
	Evidence         []contract.Evidence
	Actions          []contract.ActionReceipt
	PendingArguments json.RawMessage
}

type Runner struct {
	Model    model.Model
	Registry *tool.Registry
	Guard    Guard
	Confirm  Confirm
	Budget   Budget
}

func (r Runner) Run(ctx context.Context, messages []model.Message) (Result, error) {
	if r.Model == nil || r.Registry == nil {
		return Result{}, errors.New("react: model and registry are required")
	}
	budget := r.Budget
	if budget.MaxIterations <= 0 {
		budget.MaxIterations = 8
	}
	if budget.MaxToolCalls <= 0 {
		budget.MaxToolCalls = 32
	}
	if budget.MaxModelCalls <= 0 {
		budget.MaxModelCalls = budget.MaxIterations
	}
	input := model.ModelInput{Messages: append([]model.Message(nil), messages...), Tools: r.Registry.ModelSpecs()}
	if err := model.ValidateCapabilities(r.Model.Capabilities(), input, false); err != nil {
		return Result{}, err
	}
	var result Result
	for iteration := 0; iteration < budget.MaxIterations; iteration++ {
		if result.ModelCalls >= budget.MaxModelCalls {
			return result, errors.New("react: model call budget exceeded")
		}
		out, err := r.Model.Generate(ctx, input)
		if err != nil {
			return result, err
		}
		if out == nil {
			return result, errors.New("react: model returned nil output")
		}
		result.ModelCalls++
		addUsage(&result.Usage, out.Usage)
		if len(out.ToolCalls) == 0 {
			if result.ToolCalls < budget.MinToolCalls || result.SuccessfulTools < budget.RequiredSuccesses {
				return result, errors.New("react: success criteria budget was not satisfied")
			}
			result.Text = out.Text
			return result, nil
		}
		input.Messages = append(input.Messages, model.Message{Role: model.RoleAssistant, Content: out.Text, ReasoningContent: out.ReasoningContent, ToolCalls: out.ToolCalls})
		for _, call := range out.ToolCalls {
			if result.ToolCalls >= budget.MaxToolCalls {
				return result, errors.New("react: tool call budget exceeded")
			}
			value, ok := r.Registry.Get(call.Function.Name)
			if !ok {
				return result, fmt.Errorf("react: tool %q is not allowed", call.Function.Name)
			}
			if r.Guard != nil {
				if err := r.Guard(ctx, value.Spec()); err != nil {
					return result, err
				}
			}
			if value.Spec().RequiresConfirmation() {
				approved := false
				if r.Confirm != nil {
					approved, err = r.Confirm(ctx, value.Spec(), json.RawMessage(call.Function.Arguments))
					if err != nil {
						return result, err
					}
				}
				if !approved {
					now := time.Now().UTC()
					receipt := contract.ActionReceipt{ToolID: value.Spec().ID, Status: contract.ActionConfirmationRequired, ExecutedAt: now, Timestamp: now, Message: "explicit confirmation is required before execution"}
					result.Actions = append(result.Actions, receipt)
					result.PendingArguments = append(json.RawMessage(nil), call.Function.Arguments...)
					return result, errors.New("react: confirmation required")
				}
			}
			invoked, err := tool.CallAndCollect(ctx, value, json.RawMessage(call.Function.Arguments), nil)
			result.ToolCalls++
			if err != nil {
				result.FailedTools++
				return result, fmt.Errorf("react: tool %s: %w", call.Function.Name, err)
			}
			result.SuccessfulTools++
			evidence := contract.Evidence{ToolID: value.Spec().ID, ProviderID: value.Spec().ProviderID, Data: invoked.Content, Content: invoked.Content, SchemaValid: true, CapturedAt: time.Now().UTC()}
			result.Evidence = append(result.Evidence, evidence)
			if invoked.Action != nil {
				result.Actions = append(result.Actions, *invoked.Action)
			}
			encoded, err := json.Marshal(invoked.Content)
			if err != nil {
				return result, err
			}
			input.Messages = append(input.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: string(encoded)})
		}
	}
	return result, errors.New("react: iteration budget exceeded")
}
func addUsage(total *model.Usage, value model.Usage) {
	total.PromptTokens += value.PromptTokens
	total.CompletionTokens += value.CompletionTokens
	total.TotalTokens += value.TotalTokens
}
