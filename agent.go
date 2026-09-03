package zhizhi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/capability"
	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/exec"
	runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"
	"github.com/cocoyes/zhizhi-agent-runtime/middleware"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/planner"
	"github.com/cocoyes/zhizhi-agent-runtime/policy"
	"github.com/cocoyes/zhizhi-agent-runtime/react"
	"github.com/cocoyes/zhizhi-agent-runtime/replan"
	"github.com/cocoyes/zhizhi-agent-runtime/route"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type RunMode string

const (
	RunModeChat         RunMode = "chat"
	RunModeAgentSimple  RunMode = "agent.simple"
	RunModeAgentComplex RunMode = "agent.complex"
)

type Request struct {
	Input             string
	Messages          []model.Message
	UserID, SessionID string
	Mode              *RunMode
	Metadata          map[string]any
}
type Response struct {
	RunID, Text  string
	FinishReason model.FinishReason
	Checkpoint   string
	Suspended    bool
	Usage        model.Usage
	Outcome      contract.RunOutcome
	Stats        contract.RunStats
	Warnings     []contract.Warning
	ToolCalls    int
	Evidence     []contract.Evidence
	PlanSteps    int
	Batches      int
	Replans      int
	Actions      []contract.ActionReceipt
}
type Evidence = contract.Evidence
type EventType string

const (
	EventRunStarted     EventType = "run.started"
	EventModelStarted   EventType = "model.started"
	EventTextDelta      EventType = "model.text.delta"
	EventReasoningDelta EventType = "model.reasoning.delta"
	EventToolCallDelta  EventType = "model.tool_call.delta"
	EventToolStarted    EventType = "tool.started"
	EventToolDelta      EventType = "tool.delta"
	EventToolCompleted  EventType = "tool.completed"
	EventToolFailed     EventType = "tool.failed"
	EventPlanCreated    EventType = "plan.created"
	EventStepStarted    EventType = "step.started"
	EventStepCompleted  EventType = "step.completed"
	EventRunSuspended   EventType = "run.suspended"
	EventRunCompleted   EventType = "run.completed"
	EventRunFailed      EventType = "run.failed"
)

type Event struct {
	Type           EventType
	RunID          string
	TextDelta      string
	ReasoningDelta string
	ToolCall       *model.ToolCallDelta
	ToolID         string
	ToolDelta      any
	Response       *Response
	Data           map[string]any
}
type EventStream interface {
	Recv(context.Context) (Event, error)
	Close() error
}
type Agent interface {
	Run(context.Context, Request, ...RunOption) (*Response, error)
	Stream(context.Context, Request, ...RunOption) (EventStream, error)
	Resume(context.Context, ResumeRequest, ...RunOption) (*Response, error)
	Close(context.Context) error
}
type ResumeRequest struct {
	RunID      string
	Checkpoint string
	Decision   any
}
type Option func(*config) error
type Budget struct {
	MaxSteps         int
	MaxBatches       int
	MaxModelCalls    int
	MaxToolCalls     int
	MaxReplans       int
	MaxParallelSteps int
	TargetLatency    time.Duration
	OptionalCutoff   time.Duration
	HardTimeout      time.Duration
}
type Guard func(context.Context, tool.Spec) error

// ConfirmFunc authorizes a tool invocation after policy and guard checks but
// before the side effect occurs. A false result is represented as a
// CONFIRMATION_REQUIRED action receipt and is never executed.
type ConfirmFunc func(context.Context, tool.Spec, json.RawMessage) (bool, error)
type RunIDGenerator func() (string, error)
type AgentHandler func(context.Context, Request, ...RunOption) (*Response, error)
type AgentMiddleware func(AgentHandler) AgentHandler
type config struct {
	model                 model.Model
	systemPrompt          string
	tools                 []tool.Tool
	planner               planner.Planner
	replanner             replan.Replanner
	mcpConfigs            []runtimemcp.Config
	observer              observe.Observer
	budget                Budget
	guard                 Guard
	confirm               ConfirmFunc
	failurePolicy         policy.Runner
	router                route.Router
	broker                *capability.Broker
	maxParallel           int
	checkpointStore       checkpoint.Store
	modelMiddleware       []middleware.ModelMiddleware
	modelStreamMiddleware []middleware.ModelStreamMiddleware
	toolMiddleware        []middleware.ToolMiddleware
	toolStreamMiddleware  []middleware.ToolStreamMiddleware
	plannerMiddleware     []middleware.PlannerMiddleware
	replannerMiddleware   []middleware.ReplannerMiddleware
	agentMiddleware       []AgentMiddleware
	runIDGenerator        RunIDGenerator
}

func WithModel(v model.Model) Option { return func(c *config) error { c.model = v; return nil } }
func WithSystemPrompt(v string) Option {
	return func(c *config) error { c.systemPrompt = v; return nil }
}
func WithTools(v ...tool.Tool) Option {
	return func(c *config) error { c.tools = append(c.tools, v...); return nil }
}
func WithPlanner(v planner.Planner) Option {
	return func(c *config) error { c.planner = v; return nil }
}
func WithReplanner(v replan.Replanner) Option {
	return func(c *config) error { c.replanner = v; return nil }
}
func WithMCP(v runtimemcp.Config) Option {
	return func(c *config) error { c.mcpConfigs = append(c.mcpConfigs, v); return nil }
}
func WithObserver(v observe.Observer) Option {
	return func(c *config) error { c.observer = v; return nil }
}
func WithBudget(v Budget) Option { return func(c *config) error { c.budget = v; return nil } }
func WithGuard(v Guard) Option   { return func(c *config) error { c.guard = v; return nil } }
func WithConfirmation(v ConfirmFunc) Option {
	return func(c *config) error { c.confirm = v; return nil }
}
func WithCheckpointStore(v checkpoint.Store) Option {
	return func(c *config) error { c.checkpointStore = v; return nil }
}
func WithModelMiddleware(values ...middleware.ModelMiddleware) Option {
	return func(c *config) error { c.modelMiddleware = append(c.modelMiddleware, values...); return nil }
}
func WithModelStreamMiddleware(values ...middleware.ModelStreamMiddleware) Option {
	return func(c *config) error {
		c.modelStreamMiddleware = append(c.modelStreamMiddleware, values...)
		return nil
	}
}
func WithToolMiddleware(values ...middleware.ToolMiddleware) Option {
	return func(c *config) error { c.toolMiddleware = append(c.toolMiddleware, values...); return nil }
}
func WithToolStreamMiddleware(values ...middleware.ToolStreamMiddleware) Option {
	return func(c *config) error { c.toolStreamMiddleware = append(c.toolStreamMiddleware, values...); return nil }
}
func WithRunIDGenerator(value RunIDGenerator) Option {
	return func(c *config) error {
		if value == nil {
			return errors.New("run id generator cannot be nil")
		}
		c.runIDGenerator = value
		return nil
	}
}
func WithPlannerMiddleware(values ...middleware.PlannerMiddleware) Option {
	return func(c *config) error { c.plannerMiddleware = append(c.plannerMiddleware, values...); return nil }
}
func WithReplannerMiddleware(values ...middleware.ReplannerMiddleware) Option {
	return func(c *config) error { c.replannerMiddleware = append(c.replannerMiddleware, values...); return nil }
}
func WithAgentMiddleware(values ...AgentMiddleware) Option {
	return func(c *config) error { c.agentMiddleware = append(c.agentMiddleware, values...); return nil }
}
func WithRouter(v route.Router) Option { return func(c *config) error { c.router = v; return nil } }
func WithFailurePolicy(v policy.Runner) Option {
	return func(c *config) error { c.failurePolicy = v; return nil }
}
func New(options ...Option) (Agent, error) {
	c := &config{}
	for _, o := range options {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	if c.model == nil {
		return nil, errors.New("zhizhi: model is required")
	}
	if c.checkpointStore == nil {
		c.checkpointStore = checkpoint.NewMemoryStore()
	}
	if c.runIDGenerator == nil {
		c.runIDGenerator = defaultRunID
	}
	c.model = middleware.WrapModel(c.model, c.modelMiddleware, c.modelStreamMiddleware)
	registry := tool.NewRegistry(c.tools...)
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	providers := make([]*runtimemcp.Provider, 0, len(c.mcpConfigs))
	for _, config := range c.mcpConfigs {
		provider, err := runtimemcp.NewProvider(config)
		if err != nil {
			for _, opened := range providers {
				_ = opened.Close()
			}
			return nil, err
		}
		if config.ConnectionStrategy == runtimemcp.ConnectionEager {
			if err := provider.Ensure(context.Background()); err != nil {
				if config.Required {
					return nil, err
				}
			} else {
				provider.AddToRegistry(registry)
			}
		}
		providers = append(providers, provider)
	}
	return &runtime{cfg: c, registry: registry, mcpProviders: providers, observer: c.observer, broker: capability.NewBroker(registry), maxParallel: c.budget.MaxParallelSteps}, nil
}

func defaultRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return "run-" + hex.EncodeToString(value[:]), nil
}

func (r *runtime) prepare(ctx context.Context) error {
	r.mcpMu.Lock()
	defer r.mcpMu.Unlock()
	for i, provider := range r.mcpProviders {
		if provider.Healthy() {
			continue
		}
		if err := provider.Ensure(ctx); err != nil {
			if r.cfg.mcpConfigs[i].Required {
				return err
			}
			r.emit(observe.Event{Type: "mcp.unavailable", Data: map[string]any{"provider_id": r.cfg.mcpConfigs[i].ID, "error": err.Error()}})
			continue
		}
		provider.AddToRegistry(r.registry)
	}
	return r.registry.Validate()
}

func catalogFingerprint(registry *tool.Registry) string {
	h := sha256.New()
	for _, spec := range registry.ModelSpecs() {
		_, _ = h.Write([]byte(spec.Function.Name + "\x00" + spec.Function.Description + "\x00" + string(spec.Function.Parameters) + "\x00" + string(spec.OutputSchema)))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

type simpleResumeState struct {
	Kind            string                   `json:"kind"`
	Request         Request                  `json:"request"`
	Input           model.ModelInput         `json:"input"`
	Remaining       []model.ToolCallRequest  `json:"remaining"`
	Usage           model.Usage              `json:"usage"`
	Evidence        []Evidence               `json:"evidence"`
	Actions         []contract.ActionReceipt `json:"actions,omitempty"`
	ModelCalls      int                      `json:"model_calls"`
	ToolCalls       int                      `json:"tool_calls"`
	SuccessfulTools int                      `json:"successful_tools,omitempty"`
	FailedTools     int                      `json:"failed_tools,omitempty"`
	StartedAt       time.Time                `json:"started_at"`
}

type complexResumeState struct {
	Kind       string                 `json:"kind"`
	Request    Request                `json:"request"`
	Plan       plan.Plan              `json:"plan"`
	Completed  []checkpointStepResult `json:"completed"`
	Usage      model.Usage            `json:"usage,omitempty"`
	ModelCalls int                    `json:"model_calls,omitempty"`
	Replans    int                    `json:"replans,omitempty"`
	StartedAt  time.Time              `json:"started_at"`
}

type checkpointStepResult struct {
	StepID     string              `json:"step_id"`
	Capability string              `json:"capability"`
	ToolID     string              `json:"tool_id"`
	Content    any                 `json:"content,omitempty"`
	Receipt    bool                `json:"receipt,omitempty"`
	Action     *tool.ActionReceipt `json:"action,omitempty"`
	Duration   time.Duration       `json:"duration,omitempty"`
	Attempts   int                 `json:"attempts,omitempty"`
	Fallbacks  int                 `json:"fallbacks,omitempty"`
	Skipped    bool                `json:"skipped,omitempty"`
	Input      json.RawMessage     `json:"input,omitempty"`
}

func persistStepResults(values []exec.StepResult, pendingStep string) []checkpointStepResult {
	out := make([]checkpointStepResult, 0, len(values))
	for _, value := range values {
		if value.StepID == pendingStep || value.Err != nil {
			continue
		}
		out = append(out, checkpointStepResult{StepID: value.StepID, Capability: value.Capability, ToolID: value.ToolID, Content: value.Content, Receipt: value.Receipt, Action: value.Action, Duration: value.Duration, Attempts: value.Attempts, Fallbacks: value.Fallbacks, Skipped: value.Skipped, Input: append(json.RawMessage(nil), value.Input...)})
	}
	return out
}

func restoreStepResults(values []checkpointStepResult) map[string]exec.StepResult {
	out := make(map[string]exec.StepResult, len(values))
	for _, value := range values {
		out[value.StepID] = exec.StepResult{StepID: value.StepID, Capability: value.Capability, ToolID: value.ToolID, Content: value.Content, Receipt: value.Receipt, Action: value.Action, Duration: value.Duration, Attempts: value.Attempts, Fallbacks: value.Fallbacks, Skipped: value.Skipped, Input: append(json.RawMessage(nil), value.Input...)}
	}
	return out
}

func (r *runtime) saveCheckpoint(ctx context.Context, registry *tool.Registry, runID string, state simpleResumeState, pending model.ToolCallRequest) (string, error) {
	state.Kind = "simple"
	id, err := checkpoint.NewID()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("checkpoint: encode runtime state: %w", err)
	}
	now := time.Now().UTC()
	value := checkpoint.Checkpoint{Version: checkpoint.CurrentVersion, ID: id, RunID: runID, Reason: checkpoint.ReasonConfirmation, CatalogFingerprint: catalogFingerprint(registry), Messages: state.Input.Messages, Evidence: state.Evidence, Actions: state.Actions, Pending: &checkpoint.PendingInvocation{CallID: pending.ID, ToolID: pending.Function.Name, Arguments: json.RawMessage(pending.Function.Arguments)}, Budget: checkpoint.BudgetUsage{ModelCalls: state.ModelCalls, ToolCalls: state.ToolCalls}, State: encoded, CreatedAt: now, UpdatedAt: now}
	if err := r.cfg.checkpointStore.Save(ctx, value); err != nil {
		return "", err
	}
	r.emit(observe.Event{Type: "run.suspended", RunID: runID, Data: map[string]any{"reason": value.Reason, "checkpoint": id}})
	return id, nil
}

func (r *runtime) saveComplexCheckpoint(ctx context.Context, registry *tool.Registry, runID string, state complexResumeState, pending exec.StepResult) (string, error) {
	state.Kind = "complex"
	id, err := checkpoint.NewID()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("checkpoint: encode complex state: %w", err)
	}
	now := time.Now().UTC()
	arguments := append(json.RawMessage(nil), pending.Input...)
	value := checkpoint.Checkpoint{Version: checkpoint.CurrentVersion, ID: id, RunID: runID, Reason: checkpoint.ReasonConfirmation, CatalogFingerprint: catalogFingerprint(registry), Actions: collectCheckpointActions(state.Completed), Pending: &checkpoint.PendingInvocation{ToolID: pending.ToolID, StepID: pending.StepID, Arguments: arguments}, State: encoded, CreatedAt: now, UpdatedAt: now}
	if err := r.cfg.checkpointStore.Save(ctx, value); err != nil {
		return "", err
	}
	r.emit(observe.Event{Type: "run.suspended", RunID: runID, PlanVersion: state.Plan.Version, StepID: pending.StepID, ToolID: pending.ToolID, Data: map[string]any{"reason": value.Reason, "checkpoint": id}})
	return id, nil
}

func collectCheckpointActions(values []checkpointStepResult) []contract.ActionReceipt {
	out := make([]contract.ActionReceipt, 0)
	for _, value := range values {
		if value.Action != nil {
			out = append(out, *value.Action)
		}
	}
	return out
}

func approvedDecision(value any) (bool, bool) {
	switch decision := value.(type) {
	case bool:
		return decision, true
	case map[string]any:
		approved, ok := decision["approved"].(bool)
		return approved, ok
	default:
		return false, false
	}
}

func outcomeForActions(actions []contract.ActionReceipt) contract.RunOutcome {
	for _, action := range actions {
		if action.Status != contract.ActionSuccess {
			return contract.OutcomeDegraded
		}
	}
	return contract.OutcomeSuccess
}

func (r *runtime) agenticExecutor(activeModel model.Model, registry *tool.Registry) exec.AgenticExecutor {
	return func(ctx context.Context, step exec.Step, input json.RawMessage) (exec.AgenticResult, error) {
		allowed := make([]tool.Tool, 0)
		seen := map[string]struct{}{}
		for _, capability := range step.Capabilities {
			for _, value := range registry.ResolveCapability(capability) {
				if _, ok := seen[value.Spec().ID]; !ok {
					seen[value.Spec().ID] = struct{}{}
					allowed = append(allowed, value)
				}
			}
		}
		allowedRegistry := tool.NewRegistry(allowed...)
		if err := allowedRegistry.Validate(); err != nil {
			return exec.AgenticResult{}, err
		}
		if len(allowed) == 0 {
			return exec.AgenticResult{}, fmt.Errorf("agentic step %s has no allowed tools", step.ID)
		}
		budget := react.Budget{MinToolCalls: step.ToolBudget.Min, MaxToolCalls: step.ToolBudget.Max, RequiredSuccesses: step.ToolBudget.RequiredSuccesses, MaxIterations: r.cfg.budget.MaxModelCalls, MaxModelCalls: r.cfg.budget.MaxModelCalls}
		confirm := react.Confirm(r.cfg.confirm)
		if step.ApprovedToolID != "" {
			approvedPendingCall := false
			confirm = func(_ context.Context, spec tool.Spec, arguments json.RawMessage) (bool, error) {
				if approvedPendingCall {
					return false, nil
				}
				if spec.ID != step.ApprovedToolID || !sameJSON(arguments, step.ApprovedInput) {
					return false, nil
				}
				approvedPendingCall = true
				return true, nil
			}
		}
		runner := react.Runner{Model: activeModel, Registry: allowedRegistry, Budget: budget, Guard: react.Guard(func(ctx context.Context, spec tool.Spec) error {
			if r.cfg.guard == nil {
				return nil
			}
			return r.cfg.guard(ctx, spec)
		}), Confirm: confirm}
		messages := []model.Message{{Role: model.RoleSystem, Content: "Achieve the step goal using only the allowed tools. Stop when the success criteria are satisfied."}, {Role: model.RoleUser, Content: "Goal: " + step.Goal + "\nSuccess criteria: " + step.SuccessCriteria + "\nInput: " + string(input)}}
		result, err := runner.Run(ctx, messages)
		var action *tool.ActionReceipt
		if len(result.Actions) > 0 {
			copyAction := result.Actions[len(result.Actions)-1]
			copyAction.StepID = step.ID
			action = &copyAction
		}
		content := map[string]any{"text": result.Text, "evidence": result.Evidence, "actions": result.Actions}
		return exec.AgenticResult{Content: content, Action: action, ToolCalls: result.ToolCalls, ModelCalls: result.ModelCalls, PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens, TotalTokens: result.Usage.TotalTokens, PendingArguments: result.PendingArguments}, err
	}
}

func sameJSON(left, right []byte) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return string(left) == string(right)
	}
	return reflect.DeepEqual(a, b)
}

func (r *runtime) Resume(ctx context.Context, req ResumeRequest, options ...RunOption) (response *Response, returnErr error) {
	defer func() { returnErr = standardizeError("agent.resume", returnErr) }()
	if err := r.begin(); err != nil {
		return nil, err
	}
	defer r.done()
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()
	if r.cfg.checkpointStore == nil {
		return nil, errors.New("resume: checkpoint store is not configured")
	}
	if err := r.prepare(ctx); err != nil {
		return nil, err
	}
	runCfg, registry, err := r.newRunConfig(options)
	if err != nil {
		return nil, err
	}
	value, err := r.cfg.checkpointStore.Load(ctx, req.Checkpoint)
	if err != nil {
		return nil, err
	}
	if req.RunID != "" && req.RunID != value.RunID {
		return nil, errors.New("resume: run id does not match checkpoint")
	}
	if value.CatalogFingerprint != catalogFingerprint(registry) {
		return nil, checkpoint.ErrCatalogDrift
	}
	for _, action := range value.Actions {
		if action.Status == contract.ActionUnknown {
			return nil, fmt.Errorf("resume: ambiguous side effect for tool %s cannot be retried", action.ToolID)
		}
	}
	approved, ok := approvedDecision(req.Decision)
	if !ok {
		return nil, errors.New("resume: decision must be a boolean or contain boolean approved")
	}
	if !approved {
		if err := r.cfg.checkpointStore.Delete(ctx, value.ID); err != nil {
			return nil, err
		}
		return &Response{RunID: value.RunID, Outcome: contract.OutcomeDegraded, Warnings: []contract.Warning{{Code: "CONFIRMATION_REJECTED", Message: "tool execution was rejected"}}, Actions: value.Actions}, nil
	}
	var envelope struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(value.State, &envelope); err != nil {
		return nil, fmt.Errorf("resume: decode state: %w", err)
	}
	if envelope.Kind == "complex" {
		if value.Pending == nil || value.Pending.StepID == "" {
			return nil, errors.New("resume: complex checkpoint has no pending step")
		}
		var state complexResumeState
		if err := json.Unmarshal(value.State, &state); err != nil {
			return nil, fmt.Errorf("resume: decode complex state: %w", err)
		}
		response, resumeErr := r.resumeComplex(ctx, value, state, registry, runCfg)
		if resumeErr == nil {
			_ = r.cfg.checkpointStore.Delete(ctx, value.ID)
		}
		return response, resumeErr
	}
	var state simpleResumeState
	if err := json.Unmarshal(value.State, &state); err != nil {
		return nil, fmt.Errorf("resume: decode state: %w", err)
	}
	response, err = r.resumeSimple(ctx, value.RunID, state, registry, runCfg.model)
	if err == nil {
		_ = r.cfg.checkpointStore.Delete(ctx, value.ID)
	}
	return response, err
}

func (r *runtime) resumeSimple(ctx context.Context, runID string, state simpleResumeState, registry *tool.Registry, runModel model.Model) (*Response, error) {
	started := state.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	input, usage, evidence, actions := state.Input, state.Usage, state.Evidence, state.Actions
	modelCalls, toolCalls := state.ModelCalls, state.ToolCalls
	successfulTools, failedTools := state.SuccessfulTools, state.FailedTools
	remaining := state.Remaining
	for iteration := 0; iteration < 8; iteration++ {
		for index, call := range remaining {
			if r.cfg.budget.MaxToolCalls > 0 && toolCalls >= r.cfg.budget.MaxToolCalls {
				return nil, errors.New("budget: max tool calls exceeded")
			}
			value, found := registry.Get(call.Function.Name)
			if !found {
				return nil, fmt.Errorf("tool not found: %s", call.Function.Name)
			}
			if r.cfg.guard != nil {
				if err := r.cfg.guard(ctx, value.Spec()); err != nil {
					return nil, err
				}
			}
			// The first invocation is the one explicitly approved by Resume.
			if index > 0 && value.Spec().RequiresConfirmation() {
				next := state
				next.Input = input
				next.Remaining = append([]model.ToolCallRequest(nil), remaining[index:]...)
				next.Usage = usage
				next.Evidence = evidence
				next.Actions = actions
				next.ModelCalls = modelCalls
				next.ToolCalls = toolCalls
				next.StartedAt = started
				id, err := r.saveCheckpoint(ctx, registry, runID, next, call)
				if err != nil {
					return nil, err
				}
				return &Response{RunID: runID, Checkpoint: id, Suspended: true, Outcome: contract.OutcomeSuspended, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: "explicit confirmation is required before execution"}}, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, Duration: time.Since(started)}}, nil
			}
			result, err := tool.CallAndCollect(ctx, value, json.RawMessage(call.Function.Arguments), nil)
			if err != nil {
				failedTools++
				return &Response{RunID: runID, Usage: usage, Outcome: contract.OutcomeFailed, Evidence: evidence, Actions: actions, ToolCalls: toolCalls + 1, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls + 1, SuccessfulTools: successfulTools, FailedTools: failedTools, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, Duration: time.Since(started)}}, fmt.Errorf("tool %s: %w", call.Function.Name, err)
			}
			toolCalls++
			successfulTools++
			now := time.Now().UTC()
			evidence = append(evidence, Evidence{ToolID: call.Function.Name, ProviderID: value.Spec().ProviderID, Content: result.Content, Data: result.Content, CapturedAt: now, SchemaValid: true})
			if result.Action != nil {
				actions = append(actions, *result.Action)
			}
			encoded, err := json.Marshal(result.Content)
			if err != nil {
				return nil, fmt.Errorf("tool %s result: %w", call.Function.Name, err)
			}
			input.Messages = append(input.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: string(encoded)})
		}
		remaining = nil
		if r.cfg.budget.MaxModelCalls > 0 && modelCalls >= r.cfg.budget.MaxModelCalls {
			return nil, errors.New("budget: max model calls exceeded")
		}
		out, err := runModel.Generate(ctx, input)
		if err != nil {
			return nil, err
		}
		if out == nil {
			return nil, errors.New("model returned nil output")
		}
		modelCalls++
		usage.PromptTokens += out.Usage.PromptTokens
		usage.CompletionTokens += out.Usage.CompletionTokens
		usage.TotalTokens += out.Usage.TotalTokens
		if len(out.ToolCalls) == 0 {
			if err := model.ValidateOutput(input, out); err != nil {
				return nil, err
			}
			return &Response{RunID: runID, Text: out.Text, FinishReason: out.FinishReason, Usage: usage, Outcome: outcomeForActions(actions), Evidence: evidence, Actions: actions, ToolCalls: toolCalls, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successfulTools, FailedTools: failedTools, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, Duration: time.Since(started)}}, nil
		}
		input.Messages = append(input.Messages, model.Message{Role: model.RoleAssistant, Content: out.Text, ReasoningContent: out.ReasoningContent, ToolCalls: out.ToolCalls})
		remaining = out.ToolCalls
		state = simpleResumeState{Kind: "simple", Request: state.Request, Input: input, Remaining: remaining, Usage: usage, Evidence: evidence, Actions: actions, ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successfulTools, FailedTools: failedTools, StartedAt: started}
		if first := remaining[0]; func() bool {
			t, ok := registry.Get(first.Function.Name)
			return ok && t.Spec().RequiresConfirmation()
		}() {
			id, err := r.saveCheckpoint(ctx, registry, runID, state, first)
			if err != nil {
				return nil, err
			}
			return &Response{RunID: runID, Checkpoint: id, Suspended: true, Outcome: contract.OutcomeSuspended, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: "explicit confirmation is required before execution"}}}, nil
		}
	}
	return nil, errors.New("agent: tool call limit exceeded")
}

func (r *runtime) resumeComplex(ctx context.Context, checkpointValue *checkpoint.Checkpoint, state complexResumeState, registry *tool.Registry, runCfg runConfig) (*Response, error) {
	counter := &modelCounter{base: runCfg.model, calls: state.ModelCalls, usage: state.Usage}
	if err := plan.ValidateAgainstTools(state.Plan, registry.ModelSpecs()); err != nil {
		return nil, err
	}
	execution, err := exec.Compile(state.Plan)
	if err != nil {
		return nil, err
	}
	if r.cfg.budget.MaxSteps > 0 && len(state.Plan.Steps) > r.cfg.budget.MaxSteps {
		return nil, errors.New("budget: max steps exceeded")
	}
	for _, step := range state.Plan.Steps {
		if err := validateStepTools(registry, step); err != nil {
			return nil, err
		}
	}
	parallel := r.maxParallel
	if parallel < 1 {
		parallel = 4
	}
	scheduler := exec.Scheduler{Registry: registry, MaxParallel: parallel, Policy: r.cfg.failurePolicy, Deadline: exec.DeadlinePolicy{TargetLatency: r.cfg.budget.TargetLatency, HardTimeout: r.cfg.budget.HardTimeout, OptionalCutoff: r.cfg.budget.OptionalCutoff}, Barrier: &exec.Barrier{}, Completed: restoreStepResults(state.Completed), Approvals: map[string]exec.Approval{checkpointValue.Pending.StepID: {ToolID: checkpointValue.Pending.ToolID, Arguments: checkpointValue.Pending.Arguments}}, Guard: func(ctx context.Context, value tool.Tool) error {
		if r.cfg.guard == nil {
			return nil
		}
		return r.cfg.guard(ctx, value.Spec())
	}, Confirm: r.cfg.confirm, Agentic: r.agenticExecutor(counter, registry), Observe: func(event policy.Event) {
		r.emit(observe.Event{Type: event.Type, RunID: checkpointValue.RunID, PlanVersion: state.Plan.Version, ToolID: event.ToolID, Attempt: event.Attempt, Data: map[string]any{"fallback": event.Fallback, "error_kind": event.ErrorKind, "error": errString(event.Err)}})
	}}
	results, err := scheduler.Run(ctx, execution, nil)
	r.emitStepResults(checkpointValue.RunID, state.Plan.Version, results)
	if err != nil {
		return nil, err
	}
	if pending, ok := confirmationPending(results); ok {
		modelCalls, usage := counter.snapshot()
		next := complexResumeState{Kind: "complex", Request: state.Request, Plan: state.Plan, Completed: persistStepResults(results, pending.StepID), Usage: usage, ModelCalls: modelCalls, Replans: state.Replans, StartedAt: state.StartedAt}
		id, saveErr := r.saveComplexCheckpoint(ctx, registry, checkpointValue.RunID, next, pending)
		if saveErr != nil {
			return nil, saveErr
		}
		return &Response{RunID: checkpointValue.RunID, Checkpoint: id, Suspended: true, Outcome: contract.OutcomeSuspended, Actions: []contract.ActionReceipt{*pending.Action}, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: pending.Action.Message, StepID: pending.StepID}}}, nil
	}
	started := state.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	evidence := make([]Evidence, 0, len(results))
	actions := make([]contract.ActionReceipt, 0)
	warnings := make([]contract.Warning, 0)
	outcome := contract.OutcomeSuccess
	var evidenceText strings.Builder
	for _, result := range results {
		if result.Skipped {
			warnings = append(warnings, contract.Warning{Code: "STEP_SKIPPED", Message: "step condition was not met", StepID: result.StepID})
			continue
		}
		if result.Err != nil {
			outcome = contract.OutcomeDegraded
			warnings = append(warnings, contract.Warning{Code: "STEP_FAILED", Message: result.Err.Error(), StepID: result.StepID})
			continue
		}
		spec, _ := registry.Get(result.ToolID)
		providerID := ""
		if spec != nil {
			providerID = spec.Spec().ProviderID
		}
		now := time.Now().UTC()
		evidence = append(evidence, Evidence{StepID: result.StepID, ToolID: result.ToolID, ProviderID: providerID, Data: result.Content, Content: result.Content, CapturedAt: now, SchemaValid: true})
		if result.Action != nil {
			actions = append(actions, *result.Action)
			if result.Action.Status != contract.ActionSuccess {
				outcome = contract.OutcomeDegraded
			}
		}
		encoded, marshalErr := json.Marshal(result.Content)
		if marshalErr != nil {
			return nil, fmt.Errorf("resume: encode evidence for step %s: %w", result.StepID, marshalErr)
		}
		evidenceText.WriteString(result.StepID + ": " + string(encoded) + "\n")
	}
	input := model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Answer using only the provided evidence. Do not claim an action succeeded without a receipt."}, {Role: model.RoleUser, Content: state.Request.Input + "\nEvidence:\n" + evidenceText.String()}}, ResponseFormat: runCfg.responseFormat, Options: cloneMetadata(runCfg.modelOptions)}
	if err := model.ValidateCapabilities(counter.Capabilities(), input, false); err != nil {
		return nil, err
	}
	final, err := counter.Generate(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("final composer: %w", err)
	}
	if final == nil {
		return nil, errors.New("final composer returned nil output")
	}
	if err := model.ValidateOutput(input, final); err != nil {
		return nil, err
	}
	modelCalls, totalUsage := counter.snapshot()
	toolCalls, successful, failed, retries, fallbacks := stepStats(results)
	stats := contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successful, FailedTools: failed, Retries: retries, Fallbacks: fallbacks, PlanSteps: len(state.Plan.Steps), Batches: len(execution.Batches), Replans: state.Replans, PromptTokens: totalUsage.PromptTokens, CompletionTokens: totalUsage.CompletionTokens, TotalTokens: totalUsage.TotalTokens, Duration: time.Since(started)}
	return &Response{RunID: checkpointValue.RunID, Text: final.Text, FinishReason: final.FinishReason, Usage: totalUsage, Outcome: outcome, Stats: stats, Warnings: warnings, ToolCalls: toolCalls, Evidence: evidence, PlanSteps: len(state.Plan.Steps), Batches: len(execution.Batches), Replans: state.Replans, Actions: actions}, nil
}

type runtime struct {
	cfg          *config
	registry     *tool.Registry
	broker       *capability.Broker
	maxParallel  int
	mcpProviders []*runtimemcp.Provider
	observer     observe.Observer
	mcpMu        sync.Mutex
	resumeMu     sync.Mutex
	runObservers sync.Map
	issuedRunIDs sync.Map
	lifecycleMu  sync.Mutex
	closed       bool
	active       sync.WaitGroup
}

func (r *runtime) nextRunID() (string, error) {
	id, err := r.cfg.runIDGenerator()
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("run id generator returned an empty id")
	}
	if _, loaded := r.issuedRunIDs.LoadOrStore(id, struct{}{}); loaded {
		return "", fmt.Errorf("run id generator returned duplicate id %q", id)
	}
	return id, nil
}
func (r *runtime) begin() error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.closed {
		return errors.New("agent is closed")
	}
	r.active.Add(1)
	return nil
}
func (r *runtime) done() { r.active.Done() }

func (r *runtime) Run(ctx context.Context, req Request, options ...RunOption) (*Response, error) {
	if err := r.begin(); err != nil {
		return nil, standardizeError("agent.run", err)
	}
	defer r.done()
	var runCfg runConfig
	for _, option := range options {
		if option != nil {
			if err := option(&runCfg); err != nil {
				return nil, err
			}
		}
	}
	var next AgentHandler = r.run
	values := append(append([]AgentMiddleware(nil), r.cfg.agentMiddleware...), runCfg.agentMiddleware...)
	for i := len(values) - 1; i >= 0; i-- {
		next = values[i](next)
	}
	response, err := next(ctx, req, options...)
	return response, standardizeError("agent.run", err)
}

func (r *runtime) run(ctx context.Context, req Request, options ...RunOption) (*Response, error) {
	if err := r.prepare(ctx); err != nil {
		return nil, err
	}
	runCfg, registry, err := r.newRunConfig(options)
	if err != nil {
		return nil, err
	}
	if r.cfg.budget.HardTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.budget.HardTimeout)
		defer cancel()
	}
	runID, err := r.nextRunID()
	if err != nil {
		return nil, err
	}
	if runCfg.observer != nil {
		r.runObservers.Store(runID, runCfg.observer)
		defer r.runObservers.Delete(runID)
	}
	if len(runCfg.metadata) > 0 {
		if req.Metadata == nil {
			req.Metadata = map[string]any{}
		} else {
			req.Metadata = cloneMetadata(req.Metadata)
		}
		for key, value := range runCfg.metadata {
			req.Metadata[key] = value
		}
	}
	ctx = contextWithMetadata(ctx, req.Metadata)
	r.emit(observe.Event{Type: "run.started", RunID: runID, Data: map[string]any{"input_length": len(req.Input)}})
	if req.Mode == nil {
		router := r.cfg.router
		if router == nil {
			router = route.DefaultRouter{}
		}
		intent, routeErr := router.Route(ctx, req.Input, registry.ModelSpecs())
		if routeErr != nil {
			r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": routeErr.Error()}})
			return nil, routeErr
		}
		if intent.Mode == route.ModeComplex {
			mode := RunModeAgentComplex
			req.Mode = &mode
		}
	}
	if req.Mode != nil && *req.Mode == RunModeAgentComplex {
		resp, err := r.runComplex(ctx, req, runID, registry, runCfg)
		if resp != nil {
			resp.RunID = runID
		}
		eventType := "run.completed"
		if err != nil {
			eventType = "run.failed"
		}
		r.emit(observe.Event{Type: eventType, RunID: runID, Data: map[string]any{"error": errString(err)}})
		return resp, err
	}
	msgs := append([]model.Message{}, req.Messages...)
	if r.cfg.systemPrompt != "" {
		msgs = append([]model.Message{{Role: model.RoleSystem, Content: r.cfg.systemPrompt}}, msgs...)
	}
	msgs = append(msgs, model.Message{Role: model.RoleUser, Content: req.Input})
	input := model.ModelInput{Messages: msgs, Tools: registry.ModelSpecs(), ToolChoice: runCfg.toolChoice, ResponseFormat: runCfg.responseFormat, Options: cloneMetadata(runCfg.modelOptions)}
	if err := model.ValidateCapabilities(runCfg.model.Capabilities(), input, false); err != nil {
		return nil, err
	}
	var evidence []Evidence
	var actions []contract.ActionReceipt
	var usage model.Usage
	modelCalls, toolCalls := 0, 0
	successfulTools, failedTools := 0, 0
	started := time.Now()
	for i := 0; i < 8; i++ {
		modelCalls++
		if r.cfg.budget.MaxModelCalls > 0 && modelCalls > r.cfg.budget.MaxModelCalls {
			r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "max model calls exceeded"}})
			return nil, fmt.Errorf("budget: max model calls exceeded")
		}
		r.emit(observe.Event{Type: "model.request", RunID: runID, Data: map[string]any{"call": modelCalls}, Details: map[string]any{"messages": input.Messages, "tools": input.Tools}})
		out, err := runCfg.model.Generate(ctx, input)
		if err != nil {
			r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": err.Error()}})
			return nil, err
		}
		if out == nil {
			return nil, errors.New("model returned nil output")
		}
		usage.PromptTokens += out.Usage.PromptTokens
		usage.CompletionTokens += out.Usage.CompletionTokens
		usage.TotalTokens += out.Usage.TotalTokens
		r.emit(observe.Event{Type: "model.completed", RunID: runID, Data: map[string]any{"tool_calls": len(out.ToolCalls), "total_tokens": out.Usage.TotalTokens}, Details: map[string]any{"content": out.Text, "tool_call_requests": out.ToolCalls}})
		if len(out.ToolCalls) == 0 {
			if err := model.ValidateOutput(input, out); err != nil {
				return nil, err
			}
			resp := &Response{RunID: runID, Text: out.Text, FinishReason: out.FinishReason, Usage: usage, ToolCalls: len(evidence), Evidence: evidence, Actions: actions, Outcome: outcomeForActions(actions), Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successfulTools, FailedTools: failedTools, TotalTokens: usage.TotalTokens, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, Duration: time.Since(started)}}
			r.emit(observe.Event{Type: "run.completed", RunID: runID})
			return resp, nil
		}
		input.Messages = append(input.Messages, model.Message{Role: model.RoleAssistant, Content: out.Text, ReasoningContent: out.ReasoningContent, ToolCalls: out.ToolCalls})
		for callIndex, call := range out.ToolCalls {
			toolCalls++
			if r.cfg.budget.MaxToolCalls > 0 && toolCalls > r.cfg.budget.MaxToolCalls {
				r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "max tool calls exceeded"}})
				return nil, fmt.Errorf("budget: max tool calls exceeded")
			}
			t, ok := registry.Get(call.Function.Name)
			if !ok {
				r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "tool not found", "tool_id": call.Function.Name}})
				return nil, fmt.Errorf("tool not found: %s", call.Function.Name)
			}
			if r.cfg.guard != nil {
				if guardErr := r.cfg.guard(ctx, t.Spec()); guardErr != nil {
					r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": guardErr.Error(), "tool_id": call.Function.Name}})
					return nil, guardErr
				}
			}
			if spec := t.Spec(); spec.RequiresConfirmation() {
				approved := false
				if r.cfg.confirm != nil {
					approved, err = r.cfg.confirm(ctx, spec, json.RawMessage(call.Function.Arguments))
					if err != nil {
						r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": err.Error(), "tool_id": call.Function.Name}})
						return nil, err
					}
				}
				if !approved {
					now := time.Now().UTC()
					receipt := contract.ActionReceipt{ToolID: spec.ID, Status: contract.ActionConfirmationRequired, ExecutedAt: now, Timestamp: now, Message: "explicit confirmation is required before execution"}
					responseActions := append(append([]contract.ActionReceipt(nil), actions...), receipt)
					resp := &Response{RunID: runID, Outcome: contract.OutcomeSuspended, Suspended: true, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: receipt.Message, StepID: spec.ID}}, Actions: responseActions, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, Duration: time.Since(started)}}
					if r.cfg.checkpointStore != nil {
						state := simpleResumeState{Kind: "simple", Request: req, Input: input, Remaining: append([]model.ToolCallRequest(nil), out.ToolCalls[callIndex:]...), Usage: usage, Evidence: evidence, Actions: actions, ModelCalls: modelCalls, ToolCalls: toolCalls - 1, SuccessfulTools: successfulTools, FailedTools: failedTools, StartedAt: started}
						id, saveErr := r.saveCheckpoint(ctx, registry, runID, state, call)
						if saveErr != nil {
							return nil, saveErr
						}
						resp.Checkpoint = id
					}
					return resp, nil
				}
			}
			result, err := tool.CallAndCollect(ctx, t, json.RawMessage(call.Function.Arguments), nil)
			if err != nil {
				failedTools++
				r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": err.Error(), "tool_id": call.Function.Name}})
				return &Response{RunID: runID, Usage: usage, Outcome: contract.OutcomeFailed, Evidence: evidence, Actions: actions, ToolCalls: toolCalls, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successfulTools, FailedTools: failedTools, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens, Duration: time.Since(started)}}, fmt.Errorf("tool %s: %w", call.Function.Name, err)
			}
			successfulTools++
			now := time.Now().UTC()
			evidence = append(evidence, Evidence{ToolID: call.Function.Name, ProviderID: t.Spec().ProviderID, Data: result.Content, Content: result.Content, CapturedAt: now, SchemaValid: true})
			if result.Action != nil {
				actions = append(actions, *result.Action)
				r.emit(observe.Event{Type: "action.completed", RunID: runID, Data: map[string]any{"tool_id": result.Action.ToolID, "status": result.Action.Status}})
			}
			r.emit(observe.Event{Type: "tool.completed", RunID: runID, Data: map[string]any{"tool_id": call.Function.Name, "receipt": result.Receipt}})
			b, marshalErr := json.Marshal(result.Content)
			if marshalErr != nil {
				return nil, fmt.Errorf("tool %s result: %w", call.Function.Name, marshalErr)
			}
			input.Messages = append(input.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: string(b)})
		}
	}
	r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "tool call limit exceeded"}})
	return nil, errors.New("agent: tool call limit exceeded")
}

func (r *runtime) runComplex(ctx context.Context, req Request, runID string, registry *tool.Registry, runCfg runConfig) (*Response, error) {
	p := r.cfg.planner
	if p == nil {
		modelPlanner := planner.NewModelPlanner(runCfg.model)
		modelPlanner.Trace = func(event string, details map[string]any) {
			r.emit(observe.Event{Type: "planner." + event, RunID: runID, Details: details})
		}
		p = modelPlanner
	}
	r.emit(observe.Event{Type: "planner.started", RunID: runID, Details: map[string]any{"request": req.Input, "tools": model.PlanningToolCatalog(registry.ModelSpecs())}})
	var planHandler middleware.PlannerHandler = func(ctx context.Context, input middleware.PlannerInput) (plan.Plan, error) {
		return p.Plan(ctx, input.Request, input.Tools)
	}
	plannerMiddleware := append(append([]middleware.PlannerMiddleware(nil), r.cfg.plannerMiddleware...), runCfg.plannerMiddleware...)
	for i := len(plannerMiddleware) - 1; i >= 0; i-- {
		planHandler = plannerMiddleware[i](planHandler)
	}
	semantic, err := planHandler(ctx, middleware.PlannerInput{Request: req.Input, Tools: registry.ModelSpecs()})
	if err != nil {
		r.emit(observe.Event{Type: "planner.failed", RunID: runID, Data: map[string]any{"error": err.Error()}})
		return nil, err
	}
	r.emit(observe.Event{Type: "planner.completed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"steps": len(semantic.Steps)}, Details: map[string]any{"plan": semantic}})
	semantic, err = plan.Optimize(semantic)
	if err != nil {
		return nil, err
	}
	if err := plan.ValidateAgainstTools(semantic, registry.ModelSpecs()); err != nil {
		return nil, err
	}
	if r.cfg.budget.MaxSteps > 0 && len(semantic.Steps) > r.cfg.budget.MaxSteps {
		return nil, fmt.Errorf("budget: max steps exceeded")
	}
	for _, step := range semantic.Steps {
		if err := validateStepTools(registry, step); err != nil {
			return nil, err
		}
	}
	var execution exec.ExecutionPlan
	var results []exec.StepResult
	replans := 0
	maxReplans := r.cfg.budget.MaxReplans
	if maxReplans <= 0 {
		maxReplans = 1
	}
	actions := make([]tool.ActionReceipt, 0)
	started := time.Now()
	for {
		execution, err = exec.Compile(semantic)
		if err != nil {
			r.emit(observe.Event{Type: "plan.compile_failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": err.Error()}})
			return nil, err
		}
		if r.cfg.budget.MaxBatches > 0 && len(execution.Batches) > r.cfg.budget.MaxBatches {
			return nil, fmt.Errorf("budget: max batches exceeded")
		}
		for _, batch := range execution.Batches {
			r.emit(observe.Event{Type: "batch.started", RunID: runID, PlanVersion: semantic.Version, BatchIndex: batch.Index, Data: map[string]any{"steps": len(batch.Steps)}, Details: map[string]any{"step_definitions": batch.Steps}})
			for _, step := range batch.Steps {
				r.emit(observe.Event{Type: "step.started", RunID: runID, PlanVersion: semantic.Version, BatchIndex: batch.Index, StepID: step.ID, Data: map[string]any{"mode": step.Mode, "capability": step.Capability}})
			}
		}
		parallel := r.maxParallel
		if parallel < 1 {
			parallel = 4
		}
		scheduler := exec.Scheduler{Registry: registry, MaxParallel: parallel, Policy: r.cfg.failurePolicy, Observe: func(event policy.Event) {
			r.emit(observe.Event{Type: event.Type, RunID: runID, PlanVersion: semantic.Version, ToolID: event.ToolID, Attempt: event.Attempt, Data: map[string]any{"fallback": event.Fallback, "error_kind": event.ErrorKind, "error": errString(event.Err)}})
		}, Deadline: exec.DeadlinePolicy{TargetLatency: r.cfg.budget.TargetLatency, HardTimeout: r.cfg.budget.HardTimeout, OptionalCutoff: r.cfg.budget.OptionalCutoff}, Barrier: &exec.Barrier{}, Guard: func(ctx context.Context, value tool.Tool) error {
			if r.cfg.guard == nil {
				return nil
			}
			return r.cfg.guard(ctx, value.Spec())
		}, Confirm: r.cfg.confirm, Agentic: r.agenticExecutor(runCfg.model, registry)}
		// Let the scheduler resolve inputs so bindings can use evidence from
		// completed dependency steps.
		results, err = scheduler.Run(ctx, execution, nil)
		r.emitStepResults(runID, semantic.Version, results)
		if pending, ok := confirmationPending(results); ok {
			modelCalls, totalUsage := runCfg.counter.snapshot()
			toolCalls, successful, failed, retries, fallbacks := stepStats(results)
			resp := &Response{RunID: runID, Suspended: true, Outcome: contract.OutcomeSuspended, Actions: []contract.ActionReceipt{*pending.Action}, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: pending.Action.Message, StepID: pending.StepID}}, Usage: totalUsage, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successful, FailedTools: failed, Retries: retries, Fallbacks: fallbacks, PlanSteps: len(semantic.Steps), Batches: len(execution.Batches), Replans: replans, PromptTokens: totalUsage.PromptTokens, CompletionTokens: totalUsage.CompletionTokens, TotalTokens: totalUsage.TotalTokens, Duration: time.Since(started)}}
			if r.cfg.checkpointStore != nil {
				state := complexResumeState{Kind: "complex", Request: req, Plan: semantic, Completed: persistStepResults(results, pending.StepID), Usage: totalUsage, ModelCalls: modelCalls, Replans: replans, StartedAt: started}
				id, saveErr := r.saveComplexCheckpoint(ctx, registry, runID, state, pending)
				if saveErr != nil {
					return nil, saveErr
				}
				resp.Checkpoint = id
			}
			return resp, nil
		}
		if err == nil {
			r.emit(observe.Event{Type: "plan.compiled", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"steps": len(semantic.Steps), "batches": len(execution.Batches)}})
			for _, batch := range execution.Batches {
				r.emit(observe.Event{Type: "batch.completed", RunID: runID, PlanVersion: semantic.Version, BatchIndex: batch.Index})
			}
			break
		}
		if replans >= maxReplans {
			return nil, fmt.Errorf("budget: max replans exceeded after execution failure: %w", err)
		}
		rp := r.cfg.replanner
		if rp == nil {
			modelReplanner := replan.NewModelReplanner(runCfg.model)
			modelReplanner.Trace = func(event string, details map[string]any) {
				r.emit(observe.Event{Type: "replan." + event, RunID: runID, PlanVersion: semantic.Version, Details: details})
			}
			rp = modelReplanner
		}
		replanInput := replan.Input{Original: semantic, Failure: err.Error(), Tools: registry.ModelSpecs()}
		r.emit(observe.Event{Type: "replan.started", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": err.Error()}, Details: map[string]any{"request": map[string]any{"original": semantic, "failure": err.Error(), "tools": model.PlanningToolCatalog(replanInput.Tools)}}})
		var replanHandler middleware.ReplannerHandler = rp.Replan
		replannerMiddleware := append(append([]middleware.ReplannerMiddleware(nil), r.cfg.replannerMiddleware...), runCfg.replannerMiddleware...)
		for i := len(replannerMiddleware) - 1; i >= 0; i-- {
			replanHandler = replannerMiddleware[i](replanHandler)
		}
		patch, patchErr := replanHandler(ctx, replanInput)
		if patchErr != nil {
			return nil, fmt.Errorf("replan: execution failed: %v; replanner failed: %w", err, patchErr)
		}
		semantic, err = patch.Apply(semantic)
		if err != nil {
			return nil, err
		}
		if err := plan.ValidateAgainstTools(semantic, registry.ModelSpecs()); err != nil {
			return nil, fmt.Errorf("replan: invalid patched plan: %w", err)
		}
		replans++
		r.emit(observe.Event{Type: "replan.completed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"version": semantic.Version, "replans": replans}, Details: map[string]any{"patch": patch, "plan": semantic}})
	}
	evidence := make([]Evidence, 0, len(results))
	var evidenceText strings.Builder
	warnings := make([]contract.Warning, 0)
	outcome := contract.OutcomeSuccess
	for _, result := range results {
		if result.Skipped {
			warnings = append(warnings, contract.Warning{Code: "STEP_SKIPPED", Message: "step condition was not met", StepID: result.StepID})
			continue
		}
		if result.Err != nil {
			outcome = contract.OutcomeDegraded
			warnings = append(warnings, contract.Warning{Code: "STEP_FAILED_OR_SKIPPED", Message: result.Err.Error(), StepID: result.StepID})
			continue
		}
		providerID := ""
		if value, ok := registry.Get(result.ToolID); ok {
			providerID = value.Spec().ProviderID
		}
		now := time.Now().UTC()
		evidence = append(evidence, Evidence{StepID: result.StepID, ToolID: result.ToolID, ProviderID: providerID, Data: result.Content, Content: result.Content, CapturedAt: now, SchemaValid: true})
		if result.Action != nil {
			actions = append(actions, *result.Action)
			if result.Action.Status == contract.ActionConfirmationRequired || result.Action.Status == contract.ActionUnknown {
				outcome = contract.OutcomeDegraded
				code := "CONFIRMATION_REQUIRED"
				if result.Action.Status == contract.ActionUnknown {
					code = "AMBIGUOUS_SIDE_EFFECT"
				}
				warnings = append(warnings, contract.Warning{Code: code, Message: result.Action.Message, StepID: result.StepID})
			}
		}
		b, marshalErr := json.Marshal(result.Content)
		if marshalErr != nil {
			return nil, fmt.Errorf("step %s result: %w", result.StepID, marshalErr)
		}
		evidenceText.WriteString(result.StepID + ": " + string(b) + "\n")
	}
	finalInput := model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Answer using only the provided evidence. Do not claim an action succeeded without a receipt."}, {Role: model.RoleUser, Content: req.Input + "\nEvidence:\n" + evidenceText.String()}}, ResponseFormat: runCfg.responseFormat, Options: cloneMetadata(runCfg.modelOptions)}
	r.emit(observe.Event{Type: "final_composer.started", RunID: runID, Details: map[string]any{"messages": finalInput.Messages}})
	final, err := runCfg.model.Generate(ctx, finalInput)
	if err != nil {
		return nil, fmt.Errorf("final composer: %w", err)
	}
	if final == nil {
		return nil, errors.New("final composer returned nil output")
	}
	if err := model.ValidateOutput(finalInput, final); err != nil {
		return nil, err
	}
	r.emit(observe.Event{Type: "final_composer.completed", RunID: runID, Data: map[string]any{"total_tokens": final.Usage.TotalTokens}, Details: map[string]any{"content": final.Text}})
	modelCalls, totalUsage := runCfg.counter.snapshot()
	toolCalls, successful, failed, retries, fallbacks := stepStats(results)
	stats := contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successful, FailedTools: failed, Retries: retries, Fallbacks: fallbacks, PlanSteps: len(semantic.Steps), Batches: len(execution.Batches), Replans: replans, Duration: time.Since(started), TotalTokens: totalUsage.TotalTokens, PromptTokens: totalUsage.PromptTokens, CompletionTokens: totalUsage.CompletionTokens}
	return &Response{RunID: runID, Text: final.Text, FinishReason: final.FinishReason, Usage: totalUsage, Outcome: outcome, Stats: stats, Warnings: warnings, ToolCalls: toolCalls, Evidence: evidence, PlanSteps: len(semantic.Steps), Batches: len(execution.Batches), Replans: replans, Actions: actions}, nil
}

func stepStats(results []exec.StepResult) (toolCalls, successful, failed, retries, fallbacks int) {
	for _, result := range results {
		if result.Skipped {
			continue
		}
		if result.Action != nil && result.Action.Status == contract.ActionConfirmationRequired {
			continue
		}
		calls := result.Attempts
		if calls == 0 && result.ModelCalls == 0 {
			calls = 1
		}
		toolCalls += calls
		if result.Err == nil {
			successful += calls
		} else {
			failed += calls
		}
		if calls > 1 {
			retries += calls - 1
		}
		fallbacks += result.Fallbacks
	}
	return
}

func validateStepTools(registry *tool.Registry, step plan.Step) error {
	if step.Mode == plan.StepModeAgentic {
		for _, capability := range step.Capabilities {
			if len(registry.ResolveCapability(capability)) == 0 {
				return fmt.Errorf("agentic step %s capability %q has no registered tool", step.ID, capability)
			}
		}
		return nil
	}
	if len(registry.ResolveCapability(step.Capability)) == 0 {
		return fmt.Errorf("capability %q has no registered tool", step.Capability)
	}
	return nil
}

func confirmationPending(results []exec.StepResult) (exec.StepResult, bool) {
	for _, result := range results {
		if result.Action != nil && result.Action.Status == contract.ActionConfirmationRequired {
			return result, true
		}
	}
	return exec.StepResult{}, false
}

func (r *runtime) emitStepResults(runID string, planVersion int, results []exec.StepResult) {
	for _, result := range results {
		eventType := "step.completed"
		if result.Skipped {
			eventType = "step.skipped"
		} else if result.Err != nil {
			eventType = "step.failed"
		}
		r.emit(observe.Event{
			Type:        eventType,
			RunID:       runID,
			PlanVersion: planVersion,
			StepID:      result.StepID,
			ToolID:      result.ToolID,
			Data: map[string]any{
				"capability":  result.Capability,
				"attempts":    result.Attempts,
				"fallbacks":   result.Fallbacks,
				"duration_ms": result.Duration.Milliseconds(),
				"error":       errString(result.Err),
			},
			Details: map[string]any{"input": result.Input, "output": result.Content},
		})
	}
}

func (r *runtime) emit(event observe.Event) {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if r.observer != nil {
		r.observer.Observe(event)
	}
	if event.RunID != "" {
		if value, ok := r.runObservers.Load(event.RunID); ok {
			value.(observe.Observer).Observe(event)
		}
	}
}
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (r *runtime) Close(ctx context.Context) error {
	r.lifecycleMu.Lock()
	r.closed = true
	r.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() { r.active.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, provider := range r.mcpProviders {
		if err := provider.CloseContext(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *runtime) Stream(ctx context.Context, req Request, options ...RunOption) (result EventStream, returnErr error) {
	defer func() { returnErr = standardizeError("agent.stream", returnErr) }()
	if err := r.begin(); err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			r.done()
		}
	}()
	runCtx := ctx
	var cancel context.CancelFunc
	if r.cfg.budget.HardTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, r.cfg.budget.HardTimeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	if err := r.prepare(ctx); err != nil {
		cancel()
		return nil, err
	}
	runCfg, registry, err := r.newRunConfig(options)
	if err != nil {
		cancel()
		return nil, err
	}
	if len(runCfg.metadata) > 0 {
		if req.Metadata == nil {
			req.Metadata = map[string]any{}
		} else {
			req.Metadata = cloneMetadata(req.Metadata)
		}
		for key, value := range runCfg.metadata {
			req.Metadata[key] = value
		}
	}
	ctx = contextWithMetadata(ctx, req.Metadata)
	runCtx = contextWithMetadata(runCtx, req.Metadata)
	mode := req.Mode
	if mode == nil {
		router := r.cfg.router
		if router == nil {
			router = route.DefaultRouter{}
		}
		intent, routeErr := router.Route(runCtx, req.Input, registry.ModelSpecs())
		if routeErr != nil {
			cancel()
			return nil, routeErr
		}
		if intent.Mode == route.ModeComplex {
			selected := RunModeAgentComplex
			mode = &selected
			req.Mode = mode
		}
	}
	if mode != nil && *mode == RunModeAgentComplex {
		cancel()
		owned = false
		return newAsyncRunStream(ctx, r, req, options, runCfg.observer, r.done), nil
	}
	msgs := append([]model.Message{}, req.Messages...)
	if r.cfg.systemPrompt != "" {
		msgs = append([]model.Message{{Role: model.RoleSystem, Content: r.cfg.systemPrompt}}, msgs...)
	}
	msgs = append(msgs, model.Message{Role: model.RoleUser, Content: req.Input})
	streamInput := model.ModelInput{Messages: msgs, Tools: registry.ModelSpecs(), ToolChoice: runCfg.toolChoice, ResponseFormat: runCfg.responseFormat, Options: cloneMetadata(runCfg.modelOptions)}
	if err := model.ValidateCapabilities(runCfg.model.Capabilities(), streamInput, true); err != nil {
		cancel()
		return nil, err
	}
	s, err := runCfg.model.Stream(runCtx, streamInput)
	if err != nil {
		cancel()
		return nil, err
	}
	if s == nil {
		cancel()
		return nil, errors.New("model returned a nil stream")
	}
	runID, err := r.nextRunID()
	if err != nil {
		_ = s.Close()
		cancel()
		return nil, err
	}
	if runCfg.observer != nil {
		r.runObservers.Store(runID, runCfg.observer)
	}
	onDone := func() { r.runObservers.Delete(runID); r.done() }
	owned = false
	return &eventStream{stream: s, runtime: r, model: runCfg.model, registry: registry, input: streamInput, calls: make(map[int]model.ToolCallRequest), runID: runID, request: req, modelCalls: 1, started: time.Now(), pending: []Event{{Type: EventRunStarted, RunID: runID}, {Type: EventModelStarted, RunID: runID}}, ctx: runCtx, cancel: cancel, onDone: onDone}, nil
}

type eventStream struct {
	stream           model.Stream
	runtime          *runtime
	model            model.Model
	registry         *tool.Registry
	input            model.ModelInput
	calls            map[int]model.ToolCallRequest
	closed           bool
	assistantText    string
	reasoningContent string
	pending          []Event
	runID            string
	request          Request
	usage            model.Usage
	evidence         []contract.Evidence
	actions          []contract.ActionReceipt
	modelCalls       int
	toolCalls        int
	successfulTools  int
	failedTools      int
	started          time.Time
	finished         bool
	ctx              context.Context
	cancel           context.CancelFunc
	terminalErr      error
	finishReason     model.FinishReason
	onDone           func()
	doneOnce         sync.Once
}

func (s *eventStream) deliver(event Event) (Event, error) {
	data := event.Data
	if data == nil {
		data = map[string]any{}
	}
	if event.TextDelta != "" {
		data["delta_length"] = len(event.TextDelta)
	}
	s.runtime.emit(observe.Event{Type: string(event.Type), RunID: s.runID, ToolID: event.ToolID, Data: data})
	return event, nil
}

func (s *eventStream) Recv(ctx context.Context) (event Event, returnErr error) {
	defer func() { returnErr = standardizeError("agent.stream.recv", returnErr) }()
	if s.closed || s.finished {
		return Event{}, io.EOF
	}
	if len(s.pending) > 0 {
		result := s.pending[0]
		s.pending = s.pending[1:]
		return s.deliver(result)
	}
	if s.terminalErr != nil {
		err := s.terminalErr
		s.terminalErr = nil
		s.finished = true
		_ = s.stream.Close()
		if s.cancel != nil {
			s.cancel()
		}
		s.doneOnce.Do(s.onDone)
		return Event{}, err
	}
	if err := s.ctx.Err(); err != nil {
		return s.deliver(s.failureEvent(err))
	}
	ev, err := s.stream.Recv(s.ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if len(s.calls) > 0 {
				event, callErr := s.finishToolCalls(ctx)
				if callErr != nil {
					return s.deliver(s.failureEvent(callErr))
				}
				return event, nil
			}
			s.finished = true
			if err := model.ValidateOutput(s.input, &model.ModelOutput{Text: s.assistantText, Usage: s.usage}); err != nil {
				return s.deliver(s.failureEvent(err))
			}
			_ = s.stream.Close()
			if s.cancel != nil {
				s.cancel()
			}
			s.doneOnce.Do(s.onDone)
			response := &Response{RunID: s.runID, Text: s.assistantText, FinishReason: s.finishReason, Usage: s.usage, Outcome: outcomeForActions(s.actions), Evidence: s.evidence, Actions: s.actions, ToolCalls: s.toolCalls, Stats: contract.RunStats{ModelCalls: s.modelCalls, ToolCalls: s.toolCalls, SuccessfulTools: s.successfulTools, FailedTools: s.failedTools, PromptTokens: s.usage.PromptTokens, CompletionTokens: s.usage.CompletionTokens, TotalTokens: s.usage.TotalTokens, Duration: time.Since(s.started)}}
			return s.deliver(Event{Type: EventRunCompleted, RunID: s.runID, Response: response})
		}
		return s.deliver(s.failureEvent(err))
	}
	if ev.Usage != nil {
		s.usage.PromptTokens += ev.Usage.PromptTokens
		s.usage.CompletionTokens += ev.Usage.CompletionTokens
		s.usage.TotalTokens += ev.Usage.TotalTokens
	}
	if ev.FinishReason != "" {
		s.finishReason = ev.FinishReason
	}
	result := Event{RunID: s.runID, TextDelta: ev.TextDelta, ReasoningDelta: ev.ReasoningDelta}
	if ev.TextDelta != "" {
		result.Type = EventTextDelta
	} else if ev.ReasoningDelta != "" {
		result.Type = EventReasoningDelta
	}
	s.assistantText += ev.TextDelta
	s.reasoningContent += ev.ReasoningDelta
	for i := range ev.ToolCallDeltas {
		delta := ev.ToolCallDeltas[i]
		call := s.calls[delta.Index]
		if delta.ID != "" {
			call.ID = delta.ID
		}
		call.Type = "function"
		if delta.Name != "" {
			call.Function.Name = delta.Name
		}
		call.Function.Arguments += delta.Arguments
		s.calls[delta.Index] = call
		if result.ToolCall == nil {
			result.ToolCall = &delta
			result.Type = EventToolCallDelta
		} else {
			copyDelta := delta
			s.pending = append(s.pending, Event{Type: EventToolCallDelta, RunID: s.runID, ToolCall: &copyDelta})
		}
	}
	if result.Type == "" {
		return s.Recv(ctx)
	}
	return s.deliver(result)
}
func (s *eventStream) finishToolCalls(ctx context.Context) (Event, error) {
	ctx = s.ctx
	ordered := make([]model.ToolCallRequest, 0, len(s.calls))
	indexes := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		ordered = append(ordered, s.calls[index])
	}
	s.input.Messages = append(s.input.Messages, model.Message{Role: model.RoleAssistant, Content: s.assistantText, ReasoningContent: s.reasoningContent, ToolCalls: ordered})
	for callIndex, call := range ordered {
		if s.runtime.cfg.budget.MaxToolCalls > 0 && s.toolCalls >= s.runtime.cfg.budget.MaxToolCalls {
			return Event{}, errors.New("budget: max tool calls exceeded")
		}
		t, ok := s.registry.Get(call.Function.Name)
		if !ok {
			return Event{}, fmt.Errorf("tool not found: %s", call.Function.Name)
		}
		if s.runtime.cfg.guard != nil {
			if err := s.runtime.cfg.guard(ctx, t.Spec()); err != nil {
				return Event{}, err
			}
		}
		if spec := t.Spec(); spec.RequiresConfirmation() {
			approved := false
			if s.runtime.cfg.confirm != nil {
				var confirmErr error
				approved, confirmErr = s.runtime.cfg.confirm(ctx, spec, json.RawMessage(call.Function.Arguments))
				if confirmErr != nil {
					return Event{}, confirmErr
				}
			}
			if !approved {
				now := time.Now().UTC()
				receipt := contract.ActionReceipt{ToolID: spec.ID, Status: contract.ActionConfirmationRequired, ExecutedAt: now, Timestamp: now, Message: "explicit confirmation is required before execution"}
				state := simpleResumeState{Kind: "simple", Request: s.request, Input: s.input, Remaining: append([]model.ToolCallRequest(nil), ordered[callIndex:]...), Usage: s.usage, Evidence: s.evidence, Actions: s.actions, ModelCalls: s.modelCalls, ToolCalls: s.toolCalls, StartedAt: s.started}
				id, saveErr := s.runtime.saveCheckpoint(ctx, s.registry, s.runID, state, call)
				if saveErr != nil {
					return Event{}, saveErr
				}
				s.finished = true
				_ = s.stream.Close()
				if s.cancel != nil {
					s.cancel()
				}
				s.doneOnce.Do(s.onDone)
				return s.deliver(Event{Type: EventRunSuspended, RunID: s.runID, Response: &Response{RunID: s.runID, Checkpoint: id, Suspended: true, Outcome: contract.OutcomeSuspended, Actions: append(append([]contract.ActionReceipt(nil), s.actions...), receipt), Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: receipt.Message}}}})
			}
		}
		s.pending = append(s.pending, Event{Type: EventToolStarted, RunID: s.runID, ToolID: call.Function.Name})
		result, err := tool.CallAndCollect(ctx, t, json.RawMessage(call.Function.Arguments), func(delta any) {
			s.pending = append(s.pending, Event{Type: EventToolDelta, RunID: s.runID, ToolID: call.Function.Name, ToolDelta: delta})
		})
		if err != nil {
			s.toolCalls++
			s.failedTools++
			s.pending = append(s.pending, Event{Type: EventToolFailed, RunID: s.runID, ToolID: call.Function.Name, Data: map[string]any{"error": err.Error()}})
			s.pending = append(s.pending, s.failureEvent(fmt.Errorf("tool %s: %w", call.Function.Name, err)))
			s.terminalErr = fmt.Errorf("tool %s: %w", call.Function.Name, err)
			result := s.pending[0]
			s.pending = s.pending[1:]
			return s.deliver(result)
		}
		s.toolCalls++
		s.successfulTools++
		if result.Action != nil {
			s.actions = append(s.actions, *result.Action)
		}
		s.evidence = append(s.evidence, contract.Evidence{ToolID: call.Function.Name, ProviderID: t.Spec().ProviderID, Data: result.Content, Content: result.Content, CapturedAt: time.Now().UTC(), SchemaValid: true})
		encoded, marshalErr := json.Marshal(result.Content)
		if marshalErr != nil {
			return Event{}, marshalErr
		}
		s.input.Messages = append(s.input.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: string(encoded)})
		s.pending = append(s.pending, Event{Type: EventToolCompleted, RunID: s.runID, ToolID: call.Function.Name})
	}
	_ = s.stream.Close()
	if s.runtime.cfg.budget.MaxModelCalls > 0 && s.modelCalls >= s.runtime.cfg.budget.MaxModelCalls {
		return Event{}, errors.New("budget: max model calls exceeded")
	}
	next, err := s.model.Stream(s.ctx, s.input)
	if err != nil {
		return Event{}, err
	}
	if next == nil {
		return Event{}, errors.New("model returned a nil continuation stream")
	}
	s.stream = next
	s.calls = make(map[int]model.ToolCallRequest)
	s.assistantText = ""
	s.reasoningContent = ""
	s.modelCalls++
	s.pending = append(s.pending, Event{Type: EventModelStarted, RunID: s.runID})
	if len(s.pending) > 0 {
		result := s.pending[0]
		s.pending = s.pending[1:]
		return s.deliver(result)
	}
	return s.Recv(ctx)
}

func (s *eventStream) failureEvent(err error) Event {
	s.terminalErr = err
	_ = s.stream.Close()
	if s.cancel != nil {
		s.cancel()
	}
	s.doneOnce.Do(s.onDone)
	response := &Response{RunID: s.runID, Usage: s.usage, Outcome: contract.OutcomeFailed, Evidence: s.evidence, Actions: s.actions, ToolCalls: s.toolCalls, Stats: contract.RunStats{ModelCalls: s.modelCalls, ToolCalls: s.toolCalls, SuccessfulTools: s.successfulTools, FailedTools: s.failedTools, PromptTokens: s.usage.PromptTokens, CompletionTokens: s.usage.CompletionTokens, TotalTokens: s.usage.TotalTokens, Duration: time.Since(s.started)}}
	return Event{Type: EventRunFailed, RunID: s.runID, Response: response, Data: map[string]any{"error": err.Error()}}
}
func (s *eventStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.doneOnce.Do(s.onDone)
	return s.stream.Close()
}
