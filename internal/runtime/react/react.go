// Package react implements the runtime's private bounded model-tool loop.
package react

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	requestbudget "github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
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
	Continuation     *Continuation
}

// Continuation retains exact unexecuted calls and facts from earlier calls.
type Continuation struct {
	Input        model.ModelInput
	PendingCalls []model.ToolCallRequest
	Result       Result
}

type Runner struct {
	Model        model.Model
	Registry     *tool.Registry
	Guard        Guard
	Confirm      Confirm
	Budget       Budget
	DisableTools bool
	Continuation *Continuation
}

func (r Runner) Run(ctx context.Context, messages []model.Message) (Result, error) {
	return r.RunInput(ctx, model.ModelInput{Messages: messages})
}

func (r Runner) RunInput(ctx context.Context, input model.ModelInput) (Result, error) {
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
	var result Result
	var pending []model.ToolCallRequest
	if r.Continuation != nil {
		input = r.Continuation.Input
		pending = append([]model.ToolCallRequest(nil), r.Continuation.PendingCalls...)
		result = r.Continuation.Result
		result.Continuation = nil
		result.PendingArguments = nil
		for len(result.Actions) > 0 && result.Actions[len(result.Actions)-1].Status == contract.ActionConfirmationRequired {
			result.Actions = result.Actions[:len(result.Actions)-1]
		}
	}
	input.Messages = append([]model.Message(nil), input.Messages...)
	input.Tools = r.Registry.ModelSpecs()
	if r.DisableTools {
		input.Tools = nil
		input.ToolChoice = model.ToolChoiceForbidden
		if len(pending) > 0 {
			return result, errors.New("react: tools are disabled")
		}
	}
	if err := model.ValidateCapabilities(r.Model.Capabilities(), input, false); err != nil {
		return Result{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(pending) == 0 {
			if result.ModelCalls >= budget.MaxIterations {
				return result, errors.New("react: iteration budget exceeded")
			}
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
			if err := model.ValidateOutput(input, out); err != nil {
				return result, err
			}
			if r.DisableTools && len(out.ToolCalls) > 0 {
				return result, errors.New("react: model requested tools while disabled")
			}
			addUsage(&result.Usage, out.Usage)
			if len(out.ToolCalls) == 0 {
				if result.ToolCalls < budget.MinToolCalls || result.SuccessfulTools < budget.RequiredSuccesses {
					return result, errors.New("react: success criteria budget was not satisfied")
				}
				result.Text = out.Text
				return result, nil
			}
			input.Messages = append(input.Messages, model.Message{Role: model.RoleAssistant, Content: out.Text, ReasoningContent: out.ReasoningContent, ToolCalls: out.ToolCalls})
			pending = out.ToolCalls
		}
		for callIndex, call := range pending {
			if result.ToolCalls >= budget.MaxToolCalls {
				return result, errors.New("react: tool call budget exceeded")
			}
			value, ok := r.Registry.Get(call.Function.Name)
			if !ok {
				return result, fmt.Errorf("react: tool %q is not allowed", call.Function.Name)
			}
			if err := tool.ValidateInput(value.Spec(), json.RawMessage(call.Function.Arguments)); err != nil {
				return result, err
			}
			if r.Guard != nil {
				if err := r.Guard(ctx, value.Spec()); err != nil {
					return result, err
				}
			}
			if value.Spec().RequiresConfirmation() {
				approved := false
				if r.Confirm != nil {
					var err error
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
					prior := result
					prior.Continuation = nil
					result.Continuation = &Continuation{Input: input, PendingCalls: append([]model.ToolCallRequest(nil), pending[callIndex:]...), Result: prior}
					return result, errors.New("react: confirmation required")
				}
			}
			invoked, err := tool.CallAndCollect(ctx, value, json.RawMessage(call.Function.Arguments), nil)
			result.ToolCalls++
			if value.Spec().SideEffect == tool.SideEffectUnknown && invoked.Action == nil && !errors.Is(err, requestbudget.Exceeded) {
				now := time.Now().UTC()
				invoked.Action = &contract.ActionReceipt{ToolID: value.Spec().ID, Status: contract.ActionUnknown, ExecutedAt: now, Timestamp: now, Message: "external effect is unknown; automatic execution is blocked"}
			}
			if invoked.Action != nil {
				result.Actions = append(result.Actions, *invoked.Action)
			}
			if err == nil && invoked.Action != nil && invoked.Action.Status == contract.ActionUnknown {
				result.Evidence = append(result.Evidence, contract.Evidence{ToolID: value.Spec().ID, ProviderID: value.Spec().ProviderID, Data: invoked.Content, Content: invoked.Content, SchemaValid: true, CapturedAt: time.Now().UTC()})
				return result, errors.New("react: external effect is unknown; automatic execution is blocked")
			}
			if err != nil {
				if (value.Spec().IsWrite() || value.Spec().SideEffect == tool.SideEffectUnknown) && invoked.Action == nil && !errors.Is(err, requestbudget.Exceeded) {
					now := time.Now().UTC()
					result.Actions = append(result.Actions, contract.ActionReceipt{ToolID: value.Spec().ID, Status: contract.ActionUnknown, ExecutedAt: now, Timestamp: now, Message: "write outcome is unknown; automatic replay is blocked"})
				}
				result.FailedTools++
				return result, fmt.Errorf("react: tool %s: %w", call.Function.Name, err)
			}
			result.SuccessfulTools++
			evidence := contract.Evidence{ToolID: value.Spec().ID, ProviderID: value.Spec().ProviderID, Data: invoked.Content, Content: invoked.Content, SchemaValid: true, CapturedAt: time.Now().UTC()}
			result.Evidence = append(result.Evidence, evidence)
			encoded, err := json.Marshal(invoked.Content)
			if err != nil {
				return result, err
			}
			input.Messages = append(input.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: string(encoded)})
		}
		pending = nil
	}
}
func addUsage(total *model.Usage, value model.Usage) {
	total.PromptTokens += value.PromptTokens
	total.CompletionTokens += value.CompletionTokens
	total.TotalTokens += value.TotalTokens
}
