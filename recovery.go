package zhizhi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/runtime/exec"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/runtime/react"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type InitialFallbackMode string

const (
	InitialFallbackDisabled       InitialFallbackMode = "disabled"
	InitialFallbackBoundedAgentic InitialFallbackMode = "bounded_agentic"
)

// PlannerRecoveryPolicy applies to the built-in planners. A nil repair limit
// defaults to one; a pointer to zero disables repair. Initial fallback is opt-in.
type PlannerRecoveryPolicy struct {
	MaxRepairAttempts *int                `yaml:"max_repair_attempts"`
	InitialFallback   InitialFallbackMode `yaml:"initial_fallback"`
	MaxFallbackTools  int                 `yaml:"max_fallback_tools"`
	// DisableOutputTool uses schema-guided text/JSON output for providers which
	// cannot accept the union schema on a synthetic function tool.
	DisableOutputTool bool `yaml:"disable_output_tool"`
}

func protectCompletedWrites(source, next plan.Plan, completed map[string]completedStep, registry *tool.Registry) error {
	execution, err := exec.Compile(next)
	if err != nil {
		return err
	}
	reusable := reusableSteps(execution, executionFingerprints(execution), completed)
	hasCompletedWrite := false
	for id, prior := range completed {
		protected := prior.Result.Receipt || (prior.Result.Action != nil && prior.Result.Action.Status == contract.ActionSuccess)
		if value, ok := registry.Get(prior.Result.ToolID); ok && (value.Spec().IsWrite() || value.Spec().SideEffect == tool.SideEffectUnknown) {
			protected = true
		}
		if !protected {
			if content, ok := prior.Result.Content.(map[string]any); ok {
				if actions, ok := content["actions"].([]contract.ActionReceipt); ok {
					for _, action := range actions {
						if action.Status == contract.ActionSuccess {
							protected = true
						}
					}
				}
			}
		}
		if protected {
			hasCompletedWrite = true
			if _, ok := reusable[id]; !ok {
				return errors.New("replan: completed write cannot be removed, changed, or replayed")
			}
		}
	}
	if hasCompletedWrite {
		originalIDs := map[string]bool{}
		for _, step := range source.Steps {
			originalIDs[step.ID] = true
		}
		for _, step := range next.Steps {
			if originalIDs[step.ID] {
				continue
			}
			capabilities := step.Capabilities
			if step.Mode != plan.StepModeAgentic {
				capabilities = []string{step.Capability}
			}
			for _, capability := range capabilities {
				for _, value := range registry.ResolveCapability(capability) {
					if value.Spec().IsWrite() || value.Spec().SideEffect == tool.SideEffectUnknown {
						return errors.New("replan: new writes after completed work require a new host-authorized request")
					}
				}
			}
		}
	}
	return nil
}

func WithPlannerRecovery(policy PlannerRecoveryPolicy) Option {
	return func(c *config) error {
		if policy.MaxRepairAttempts != nil {
			if *policy.MaxRepairAttempts < 0 {
				return errors.New("config: negative repair limit")
			}
			copyLimit := *policy.MaxRepairAttempts
			policy.MaxRepairAttempts = &copyLimit
		}
		if policy.InitialFallback != "" && policy.InitialFallback != InitialFallbackDisabled && policy.InitialFallback != InitialFallbackBoundedAgentic {
			return errors.New("config: invalid initial fallback mode")
		}
		if policy.MaxFallbackTools < 0 || (policy.InitialFallback == InitialFallbackBoundedAgentic && policy.MaxFallbackTools == 0) {
			return errors.New("config: bounded fallback requires a positive tool limit")
		}
		c.plannerRecovery = policy
		return nil
	}
}

type requestMessagesKey struct{}

func stricterLimit(current, original int) int {
	if current <= 0 {
		return original
	}
	if original <= 0 || current < original {
		return current
	}
	return original
}

func (r *runtime) requestMessages(req Request) []model.Message {
	messages := append([]model.Message(nil), req.Messages...)
	if r.cfg.systemPrompt != "" {
		messages = append([]model.Message{{Role: model.RoleSystem, Content: r.cfg.systemPrompt}}, messages...)
	}
	return append(messages, model.Message{Role: model.RoleUser, Content: req.Input})
}

func (r *runtime) observeBudget(ctx context.Context, runID string) {
	if ledger := budget.FromContext(ctx); ledger != nil {
		ledger.SetOnExhausted(func(err *budget.Error) {
			r.emit(observe.Event{Type: "budget.exhausted", RunID: runID, Data: map[string]any{"dimension": err.Dimension, "limit": err.Limit, "used": err.Used, "operation": err.Operation}})
		})
	}
}

// plannerTrace keeps payload-bearing legacy traces in Details; only fixed safe
// fields for validation and repair events are promoted into ordinary events.
func (r *runtime) plannerTrace(prefix, runID string, version int) func(string, map[string]any) {
	return func(name string, details map[string]any) {
		event := observe.Event{Type: prefix + "." + name, RunID: runID, PlanVersion: version, Details: details}
		if name == "candidate.invalid" || name == "repair.started" || name == "repair.completed" {
			event.Data = map[string]any{}
			for _, key := range []string{"attempt", "kind", "path", "model_id", "latency_ms", "repair_attempt", "validation_succeeded"} {
				if v, ok := details[key]; ok {
					event.Data[key] = v
				}
			}
		}
		r.emit(event)
	}
}

func (r *runtime) initialRecovery(ctx context.Context, req Request, runID string, registry *tool.Registry, cfg runConfig, cause error) (plan.Plan, *Response, error) {
	var invalid *plan.StructuredOutputError
	if r.cfg.plannerRecovery.InitialFallback != InitialFallbackBoundedAgentic || !errors.As(cause, &invalid) || invalid.Phase != "initial_plan" || !invalid.Recoverable {
		return plan.Plan{}, nil, cause
	}
	var modelFailure *modelCallError
	if errors.As(cause, &modelFailure) || errors.Is(cause, budget.Exceeded) || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return plan.Plan{}, nil, cause
	}
	switch invalid.Kind {
	case "candidate_missing", "decode", "schema", "semantic", "capability_reference":
	default:
		return plan.Plan{}, nil, cause
	}
	if err := ctx.Err(); err != nil {
		return plan.Plan{}, nil, err
	}
	ledger := budget.FromContext(ctx)
	if ledger == nil || ledger.Snapshot().ToolCalls != 0 || strings.TrimSpace(req.Input) == "" {
		return plan.Plan{}, nil, cause
	}
	remaining := ledger.Remaining()
	tools := registry.Tools()
	if cfg.toolChoice == model.ToolChoiceForbidden {
		tools = nil
	}
	maxTools := r.cfg.plannerRecovery.MaxFallbackTools
	if remaining.ToolCalls >= 0 && remaining.ToolCalls < maxTools {
		maxTools = remaining.ToolCalls
	}
	// A tool-enabled recovery needs one loop response and final composition.
	needed := 2
	if len(tools) == 0 || maxTools == 0 {
		needed = 1
	}
	if remaining.ModelCalls >= 0 && remaining.ModelCalls < needed {
		return plan.Plan{}, nil, fmt.Errorf("budget: insufficient model calls for initial recovery")
	}
	data := map[string]any{"strategy": "bounded_agentic", "reason_code": invalid.Kind, "allowed_tool_count": len(tools), "remaining_model_calls": remaining.ModelCalls, "remaining_tool_calls": remaining.ToolCalls}
	r.emit(observe.Event{Type: "planner.recovery.started", RunID: runID, Data: data})
	if len(tools) == 0 || maxTools == 0 {
		runner := react.Runner{Model: cfg.model, Registry: tool.NewRegistry(), DisableTools: true, Budget: react.Budget{MaxModelCalls: 1, MaxIterations: 1}}
		messages := append(r.requestMessages(req), model.Message{Role: model.RoleSystem, Content: "No tools have executed and tools are disabled. Answer only from the conversation or ask for missing information. Do not claim a lookup or write succeeded."})
		result, err := runner.RunInput(ctx, model.ModelInput{Messages: messages, Options: cloneMetadata(cfg.modelOptions), ResponseFormat: cfg.responseFormat})
		outcome := "answered"
		if err != nil {
			outcome = "failed"
		}
		data = cloneMetadata(data)
		data["outcome"] = outcome
		r.emit(observe.Event{Type: "planner.recovery.completed", RunID: runID, Data: data})
		if err != nil {
			return plan.Plan{}, nil, err
		}
		calls, usage := cfg.counter.snapshot()
		return plan.Plan{}, &Response{RunID: runID, Text: result.Text, Outcome: contract.OutcomeDegraded, Usage: usage, Stats: contract.RunStats{ModelCalls: calls}, Warnings: []contract.Warning{{Code: "INITIAL_PLAN_RECOVERED", Message: "The initial plan was invalid; returned a bounded answer or clarification."}}}, nil
	}
	allowed := make([]string, 0, len(tools))
	for _, value := range tools {
		allowed = append(allowed, value.Spec().ID)
	}
	recovered := plan.Plan{ID: "initial-recovery", Version: 1, Goal: req.Input, Steps: []plan.Step{{ID: "initial-recovery", Mode: plan.StepModeAgentic, Goal: req.Input, SuccessCriteria: "Address the request using observed facts only; ask for missing information when needed, and never claim an action without its successful receipt.", Capabilities: allowed, ToolBudget: plan.ToolCallBudget{Max: maxTools}}}}
	if err := recovered.Validate(); err != nil {
		return plan.Plan{}, nil, err
	}
	if err := plan.ValidateAgainstTools(recovered, registry.ModelSpecs()); err != nil {
		return plan.Plan{}, nil, err
	}
	// Completion is emitted after execution; constructing a fallback is not success.
	return recovered, nil, nil
}
