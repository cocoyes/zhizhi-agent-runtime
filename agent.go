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
	"sync"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/checkpoint"
	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/runtime/exec"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/runtime/react"
	runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"
	"github.com/cocoyes/zhizhi-agent-runtime/middleware"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/policy"
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
	model                  model.Model
	systemPrompt           string
	tools                  []tool.Tool
	planner                plan.Planner
	replanner              plan.Replanner
	mcpConfigs             []runtimemcp.Config
	mcpCapabilityProviders []*runtimemcp.CapabilityProvider
	observer               observe.Observer
	budget                 Budget
	plannerRecovery        PlannerRecoveryPolicy
	guard                  Guard
	confirm                ConfirmFunc
	failurePolicy          policy.Runner
	router                 route.Router
	broker                 *tool.Broker
	maxParallel            int
	checkpointStore        checkpoint.Store
	modelMiddleware        []middleware.ModelMiddleware
	modelStreamMiddleware  []middleware.ModelStreamMiddleware
	toolMiddleware         []middleware.ToolMiddleware
	toolStreamMiddleware   []middleware.ToolStreamMiddleware
	plannerMiddleware      []middleware.PlannerMiddleware
	replannerMiddleware    []middleware.ReplannerMiddleware
	agentMiddleware        []AgentMiddleware
	runIDGenerator         RunIDGenerator
}

func WithModel(v model.Model) Option { return func(c *config) error { c.model = v; return nil } }
func WithSystemPrompt(v string) Option {
	return func(c *config) error { c.systemPrompt = v; return nil }
}
func WithTools(v ...tool.Tool) Option {
	return func(c *config) error { c.tools = append(c.tools, v...); return nil }
}
func WithPlanner(v plan.Planner) Option {
	return func(c *config) error { c.planner = v; return nil }
}
func WithReplanner(v plan.Replanner) Option {
	return func(c *config) error { c.replanner = v; return nil }
}
func WithMCP(v runtimemcp.Config) Option {
	return func(c *config) error { c.mcpConfigs = append(c.mcpConfigs, v); return nil }
}

// WithMCPCapabilityProvider enables model-driven MCP server selection. Only
// servers selected for a request are connected and have their tools listed.
func WithMCPCapabilityProvider(v *runtimemcp.CapabilityProvider) Option {
	return func(c *config) error {
		if v == nil {
			return errors.New("zhizhi: MCP capability provider cannot be nil")
		}
		c.mcpCapabilityProviders = append(c.mcpCapabilityProviders, v)
		return nil
	}
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
	return &runtime{cfg: c, registry: registry, mcpProviders: providers, observer: c.observer, broker: tool.NewBroker(registry), maxParallel: c.budget.MaxParallelSteps}, nil
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

func (r *runtime) selectMCPTools(ctx context.Context, selector model.Model, request string) ([]tool.Tool, error) {
	var selected []tool.Tool
	for _, provider := range r.cfg.mcpCapabilityProviders {
		selection, err := provider.Select(ctx, selector, request)
		if err != nil {
			return nil, err
		}
		selected = append(selected, selection.Tools...)
	}
	return selected, nil
}

func catalogFingerprint(registry *tool.Registry) string {
	h := sha256.New()
	for _, value := range registry.Tools() {
		// Confirmation, effects, fallback policy and capabilities are part of
		// resume authorization, alongside the invocation schemas.
		encoded, _ := json.Marshal(value.Spec())
		_, _ = h.Write(encoded)
		_, _ = h.Write([]byte{0})
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
	ToolCalls  int                    `json:"tool_calls,omitempty"`
	Replans    int                    `json:"replans,omitempty"`
	StartedAt  time.Time              `json:"started_at"`
}

type checkpointStepResult struct {
	StepID              string              `json:"step_id"`
	RequestedCapability string              `json:"requested_capability,omitempty"`
	Capability          string              `json:"capability"`
	ToolID              string              `json:"tool_id"`
	Content             any                 `json:"content,omitempty"`
	Receipt             bool                `json:"receipt,omitempty"`
	Action              *tool.ActionReceipt `json:"action,omitempty"`
	Duration            time.Duration       `json:"duration,omitempty"`
	Attempts            int                 `json:"attempts,omitempty"`
	Fallbacks           int                 `json:"fallbacks,omitempty"`
	Skipped             bool                `json:"skipped,omitempty"`
	ConditionUnknown    bool                `json:"condition_unknown,omitempty"`
	Input               json.RawMessage     `json:"input,omitempty"`
	Continuation        *react.Continuation `json:"continuation,omitempty"`
}

func persistStepResults(values []exec.StepResult, pendingStep string) []checkpointStepResult {
	out := make([]checkpointStepResult, 0, len(values))
	for _, value := range values {
		if (value.StepID == pendingStep || value.Err != nil) && value.Continuation == nil {
			continue
		}
		out = append(out, checkpointStepResult{StepID: value.StepID, RequestedCapability: value.RequestedCapability, Capability: value.Capability, ToolID: value.ToolID, Content: value.Content, Receipt: value.Receipt, Action: value.Action, Duration: value.Duration, Attempts: value.Attempts, Fallbacks: value.Fallbacks, Skipped: value.Skipped, ConditionUnknown: value.ConditionUnknown, Input: append(json.RawMessage(nil), value.Input...), Continuation: value.Continuation})
	}
	return out
}

func restoreStepResults(values []checkpointStepResult) map[string]exec.StepResult {
	out := make(map[string]exec.StepResult, len(values))
	for _, value := range values {
		if value.Continuation != nil {
			continue
		}
		out[value.StepID] = exec.StepResult{StepID: value.StepID, RequestedCapability: value.RequestedCapability, Capability: value.Capability, ToolID: value.ToolID, Content: value.Content, Receipt: value.Receipt, Action: value.Action, Duration: value.Duration, Attempts: value.Attempts, Fallbacks: value.Fallbacks, Skipped: value.Skipped, ConditionUnknown: value.ConditionUnknown, Input: append(json.RawMessage(nil), value.Input...)}
	}
	return out
}

func (r *runtime) saveCheckpoint(ctx context.Context, registry *tool.Registry, runID string, state simpleResumeState, pending model.ToolCallRequest) (string, error) {
	state.Kind = "simple"
	if ledger := budget.FromContext(ctx); ledger != nil {
		used := ledger.Snapshot()
		state.ModelCalls, state.ToolCalls = used.ModelCalls, used.ToolCalls
	}
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
	value.Deadline, _ = ctx.Deadline()
	if ledger := budget.FromContext(ctx); ledger != nil {
		limits := ledger.Limits()
		value.Limits = checkpoint.BudgetLimits{MaxModelCalls: limits.ModelCalls, MaxToolCalls: limits.ToolCalls}
	}
	if err := r.cfg.checkpointStore.Save(ctx, value); err != nil {
		return "", err
	}
	r.emit(observe.Event{Type: "run.suspended", RunID: runID, Data: map[string]any{"reason": value.Reason, "checkpoint": id}})
	return id, nil
}

func (r *runtime) saveComplexCheckpoint(ctx context.Context, registry *tool.Registry, runID string, state complexResumeState, pending exec.StepResult) (string, error) {
	state.Kind = "complex"
	if ledger := budget.FromContext(ctx); ledger != nil {
		used := ledger.Snapshot()
		state.ModelCalls, state.ToolCalls = used.ModelCalls, used.ToolCalls
	}
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
	value.Budget = checkpoint.BudgetUsage{ModelCalls: state.ModelCalls, ToolCalls: state.ToolCalls, Replans: state.Replans}
	value.Deadline, _ = ctx.Deadline()
	if ledger := budget.FromContext(ctx); ledger != nil {
		limits := ledger.Limits()
		value.Limits = checkpoint.BudgetLimits{MaxModelCalls: limits.ModelCalls, MaxToolCalls: limits.ToolCalls}
	}
	if err := r.cfg.checkpointStore.Save(ctx, value); err != nil {
		return "", err
	}
	r.emit(observe.Event{Type: "run.suspended", RunID: runID, PlanVersion: state.Plan.Version, StepID: pending.StepID, ToolID: pending.ToolID, Data: map[string]any{"reason": value.Reason, "checkpoint": id}})
	return id, nil
}

func collectCheckpointActions(values []checkpointStepResult) []contract.ActionReceipt {
	out := make([]contract.ActionReceipt, 0)
	for _, value := range values {
		out = append(out, stepActions(exec.StepResult{Content: value.Content, Action: value.Action})...)
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

func (r *runtime) agenticExecutor(cfg runConfig, registry *tool.Registry) exec.AgenticExecutor {
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
		runner := react.Runner{Model: cfg.model, Registry: allowedRegistry, Budget: budget, Continuation: step.Continuation, Guard: react.Guard(func(ctx context.Context, spec tool.Spec) error {
			if r.cfg.guard == nil {
				return nil
			}
			return r.cfg.guard(ctx, spec)
		}), Confirm: confirm}
		messages := []model.Message{{Role: model.RoleSystem, Content: "Achieve the step goal using only the allowed tools. Stop when the success criteria are satisfied."}, {Role: model.RoleUser, Content: "Goal: " + step.Goal + "\nSuccess criteria: " + step.SuccessCriteria + "\nInput: " + string(input)}}
		if original, ok := ctx.Value(requestMessagesKey{}).([]model.Message); ok {
			messages = append(append([]model.Message(nil), original...), messages...)
		}
		result, err := runner.RunInput(ctx, model.ModelInput{Messages: messages, Options: cloneMetadata(cfg.modelOptions)})
		var action *tool.ActionReceipt
		if len(result.Actions) > 0 {
			copyAction := result.Actions[len(result.Actions)-1]
			copyAction.StepID = step.ID
			action = &copyAction
		}
		content := map[string]any{"text": result.Text, "evidence": result.Evidence, "actions": result.Actions}
		return exec.AgenticResult{Content: content, Action: action, ToolCalls: result.ToolCalls, ModelCalls: result.ModelCalls, PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens, TotalTokens: result.Usage.TotalTokens, PendingArguments: result.PendingArguments, Continuation: result.Continuation}, err
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
	if err := value.Validate(); err != nil {
		return nil, err
	}
	if !value.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, value.Deadline)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runCfg.ledger = budget.New(stricterLimit(r.cfg.budget.MaxModelCalls, value.Limits.MaxModelCalls), stricterLimit(r.cfg.budget.MaxToolCalls, value.Limits.MaxToolCalls))
	ctx = budget.WithLedger(ctx, runCfg.ledger)
	if err := runCfg.ledger.Restore(budget.Usage{ModelCalls: value.Budget.ModelCalls, ToolCalls: value.Budget.ToolCalls}); err != nil {
		return nil, err
	}
	runCfg.counter.calls = value.Budget.ModelCalls
	r.observeBudget(ctx, value.RunID)
	if runCfg.observer != nil {
		r.runObservers.Store(value.RunID, runCfg.observer)
		defer r.runObservers.Delete(value.RunID)
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
		state.ModelCalls, state.ToolCalls = value.Budget.ModelCalls, value.Budget.ToolCalls
		runCfg.counter.usage = state.Usage
		ctx = context.WithValue(ctx, requestMessagesKey{}, r.requestMessages(state.Request))
		// Consume authorization before any dispatch so a failed continuation cannot
		// replay a write using the same checkpoint.
		if err := r.cfg.checkpointStore.Delete(ctx, value.ID); err != nil {
			return nil, err
		}
		response, resumeErr := r.resumeComplex(ctx, value, state, registry, runCfg)
		return response, resumeErr
	}
	var state simpleResumeState
	if err := json.Unmarshal(value.State, &state); err != nil {
		return nil, fmt.Errorf("resume: decode state: %w", err)
	}
	if value.Pending == nil || len(state.Remaining) == 0 || state.Remaining[0].Function.Name != value.Pending.ToolID || !sameJSON([]byte(state.Remaining[0].Function.Arguments), value.Pending.Arguments) {
		return nil, errors.New("resume: pending invocation does not match approval")
	}
	state.ModelCalls, state.ToolCalls = value.Budget.ModelCalls, value.Budget.ToolCalls
	runCfg.counter.usage = state.Usage
	if err := r.cfg.checkpointStore.Delete(ctx, value.ID); err != nil {
		return nil, err
	}
	response, err = r.resumeSimple(ctx, value.RunID, state, registry, runCfg.model)
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
	counter := runCfg.counter
	if err := plan.ValidateAgainstTools(state.Plan, registry.ModelSpecs()); err != nil {
		return nil, err
	}
	execution, err := exec.Compile(state.Plan)
	if err != nil {
		return nil, err
	}
	if r.cfg.budget.MaxBatches > 0 && len(execution.Batches) > r.cfg.budget.MaxBatches {
		return nil, errors.New("budget: max batches exceeded")
	}
	for _, saved := range state.Completed {
		if saved.Continuation == nil {
			continue
		}
		for i := range execution.Steps {
			if execution.Steps[i].ID == saved.StepID {
				execution.Steps[i].Continuation = saved.Continuation
			}
		}
		for i := range execution.Batches {
			for j := range execution.Batches[i].Steps {
				if execution.Batches[i].Steps[j].ID == saved.StepID {
					execution.Batches[i].Steps[j].Continuation = saved.Continuation
				}
			}
		}
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
	}, Confirm: r.cfg.confirm, Agentic: r.agenticExecutor(runCfg, registry), Observe: func(event policy.Event) {
		r.emit(observe.Event{Type: event.Type, RunID: checkpointValue.RunID, PlanVersion: state.Plan.Version, ToolID: event.ToolID, Attempt: event.Attempt, Data: map[string]any{"fallback": event.Fallback, "error_kind": event.ErrorKind, "error": errString(event.Err)}})
	}}
	results, err := scheduler.Run(ctx, execution, nil)
	r.emitStepResults(checkpointValue.RunID, state.Plan.Version, results)
	unsafeReplay := hasAmbiguousAction(results) || failedAfterWrite(results)
	if pending, ok := confirmationPending(results); ok && !unsafeReplay {
		modelCalls, usage := counter.snapshot()
		next := complexResumeState{Kind: "complex", Request: state.Request, Plan: state.Plan, Completed: persistStepResults(results, pending.StepID), Usage: usage, ModelCalls: modelCalls, Replans: state.Replans, StartedAt: state.StartedAt}
		id, saveErr := r.saveComplexCheckpoint(ctx, registry, checkpointValue.RunID, next, pending)
		if saveErr != nil {
			return nil, saveErr
		}
		return &Response{RunID: checkpointValue.RunID, Checkpoint: id, Suspended: true, Outcome: contract.OutcomeSuspended, Actions: []contract.ActionReceipt{*pending.Action}, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: pending.Action.Message, StepID: pending.StepID}}}, nil
	}
	if err != nil && !unsafeReplay {
		return nil, err
	}
	started := state.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	evidence := make([]Evidence, 0, len(results))
	actions := make([]contract.ActionReceipt, 0)
	warnings := make([]contract.Warning, 0)
	outcome := contract.OutcomeSuccess
	for _, result := range results {
		if result.Skipped {
			code, message := "STEP_SKIPPED", "step condition was not met"
			if result.ConditionUnknown {
				code, message = "CONDITION_UNKNOWN", "step condition could not be evaluated because its source value was unavailable"
			}
			warnings = append(warnings, contract.Warning{Code: code, Message: message, StepID: result.StepID})
			continue
		}
		for _, action := range stepActions(result) {
			actions = append(actions, action)
			if action.Status != contract.ActionSuccess {
				outcome = contract.OutcomeDegraded
			}
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
		evidence = append(evidence, Evidence{StepID: result.StepID, ToolID: result.ToolID, RequestedCapability: requestedCapability(result), ExecutedCapability: result.Capability, Status: "success", Receipt: result.Receipt, Attempts: result.Attempts, Fallbacks: result.Fallbacks, ProviderID: providerID, Data: result.Content, Content: result.Content, CapturedAt: now, SchemaValid: true})
	}
	structuredEvidence := map[string]any{"goal": state.Request.Input, "outcome": outcome, "execution_facts": executionFacts(results), "evidence": evidence, "warnings": warnings, "actions": actions}
	evidenceJSON, marshalErr := json.Marshal(structuredEvidence)
	if marshalErr != nil {
		return nil, fmt.Errorf("resume: encode final evidence: %w", marshalErr)
	}
	input := model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Answer using only the structured execution facts. step_id is only a plan node name and must never be described as a tool. tool_id is the only executed tool identity. A write action is complete only when status is success and it has a successful receipt. Never infer a missing or unknown value as false."}, {Role: model.RoleUser, Content: string(evidenceJSON)}}, ResponseFormat: runCfg.responseFormat, Options: cloneMetadata(runCfg.modelOptions)}
	input.Messages = append(r.requestMessages(state.Request), input.Messages...)
	if err := model.ValidateCapabilities(counter.Capabilities(), input, false); err != nil {
		return nil, err
	}
	final, err := runCfg.model.Generate(ctx, input)
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
	toolCalls = runCfg.ledger.Snapshot().ToolCalls
	stats := contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successful, FailedTools: failed, Retries: retries, Fallbacks: fallbacks, PlanSteps: len(state.Plan.Steps), Batches: len(execution.Batches), Replans: state.Replans, PromptTokens: totalUsage.PromptTokens, CompletionTokens: totalUsage.CompletionTokens, TotalTokens: totalUsage.TotalTokens, Duration: time.Since(started)}
	return &Response{RunID: checkpointValue.RunID, Text: final.Text, FinishReason: final.FinishReason, Usage: totalUsage, Outcome: outcome, Stats: stats, Warnings: warnings, ToolCalls: toolCalls, Evidence: evidence, PlanSteps: len(state.Plan.Steps), Batches: len(execution.Batches), Replans: state.Replans, Actions: actions}, nil
}

type runtime struct {
	cfg          *config
	registry     *tool.Registry
	broker       *tool.Broker
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

func (r *runtime) run(ctx context.Context, req Request, options ...RunOption) (response *Response, returnErr error) {
	if r.cfg.budget.HardTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.budget.HardTimeout)
		defer cancel()
	}
	if err := r.prepare(ctx); err != nil {
		return nil, err
	}
	runCfg, registry, err := r.newRunConfig(options)
	if err != nil {
		return nil, err
	}
	defer func() {
		if response != nil {
			calls, usage := runCfg.counter.snapshot()
			response.Stats.ModelCalls = calls
			response.Usage = usage
			response.Stats.PromptTokens, response.Stats.CompletionTokens, response.Stats.TotalTokens = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens
			response.Stats.ToolCalls = runCfg.ledger.Snapshot().ToolCalls
			response.ToolCalls = response.Stats.ToolCalls
		}
	}()
	ctx = budget.WithLedger(ctx, runCfg.ledger)
	runID, err := r.nextRunID()
	if err != nil {
		return nil, err
	}
	r.observeBudget(ctx, runID)
	if runCfg.observer != nil {
		r.runObservers.Store(runID, runCfg.observer)
		defer r.runObservers.Delete(runID)
	}
	selected, err := r.selectMCPTools(ctx, runCfg.model, req.Input)
	if err != nil {
		return nil, err
	}
	registry, err = r.addSelectedTools(runCfg, registry, selected)
	if err != nil {
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
	modelCalls, _ := runCfg.counter.snapshot()
	toolCalls := 0
	successfulTools, failedTools := 0, 0
	started := time.Now()
	for i := 0; i < 8; i++ {
		modelCalls++
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
						state := simpleResumeState{Kind: "simple", Request: req, Input: input, Remaining: append([]model.ToolCallRequest(nil), out.ToolCalls[callIndex:]...), Usage: usage, Evidence: evidence, Actions: actions, ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successfulTools, FailedTools: failedTools, StartedAt: started}
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
			toolCalls = runCfg.ledger.Snapshot().ToolCalls
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

func (r *runtime) runComplex(ctx context.Context, req Request, runID string, registry *tool.Registry, runCfg runConfig) (response *Response, returnErr error) {
	ctx = context.WithValue(ctx, requestMessagesKey{}, r.requestMessages(req))
	p := r.cfg.planner
	if p == nil {
		modelPlanner := plan.NewModelPlanner(runCfg.model)
		modelPlanner.MaxRepairAttempts = r.cfg.plannerRecovery.MaxRepairAttempts
		modelPlanner.DisableOutputTool = r.cfg.plannerRecovery.DisableOutputTool
		modelPlanner.Trace = r.plannerTrace("planner", runID, 0)
		p = modelPlanner
	}
	r.emit(observe.Event{Type: "planner.started", RunID: runID, Details: map[string]any{"request": req.Input, "tools": model.PlanningToolCatalog(registry.ModelSpecs())}})

	// 创建规则修复器
	ruleFixer := plan.NewRuleFixer()
	ruleFixer.Trace = r.plannerTrace("rule_fixer", runID, 0)

	// Replan patches go through the same deterministic repairs as the initial plan.
	replanFixer := plan.NewRuleFixer()
	replanFixer.Trace = r.plannerTrace("replan_rule_fixer", runID, 0)

	var planHandler middleware.PlannerHandler = func(ctx context.Context, input middleware.PlannerInput) (plan.Plan, error) {
		// 如果是 ModelPlanner,使用带 fixer 的方法
		if mp, ok := p.(*plan.ModelPlanner); ok {
			return mp.PlanWithFixer(ctx, input.Request, input.Tools, ruleFixer)
		}
		return p.Plan(ctx, input.Request, input.Tools)
	}
	plannerMiddleware := append(append([]middleware.PlannerMiddleware(nil), r.cfg.plannerMiddleware...), runCfg.plannerMiddleware...)
	for i := len(plannerMiddleware) - 1; i >= 0; i-- {
		planHandler = plannerMiddleware[i](planHandler)
	}
	semantic, err := planHandler(ctx, middleware.PlannerInput{Request: req.Input, Tools: registry.ModelSpecs()})
	recovered := false
	if err != nil {
		r.emit(observe.Event{Type: "planner.failed", RunID: runID, Data: map[string]any{"error": "initial_plan_failed"}, Details: map[string]any{"error": err.Error()}})
		var response *Response
		semantic, response, err = r.initialRecovery(ctx, req, runID, registry, runCfg, err)
		if err != nil || response != nil {
			return response, err
		}
		recovered = true
	}
	if recovered {
		defer func() {
			outcome := "failed"
			if returnErr == nil && response != nil {
				outcome = string(response.Outcome)
			}
			r.emit(observe.Event{Type: "planner.recovery.completed", RunID: runID, Data: map[string]any{"strategy": "bounded_agentic", "reason_code": "invalid_initial_plan", "allowed_tool_count": len(registry.Tools()), "remaining_model_calls": runCfg.ledger.Remaining().ModelCalls, "remaining_tool_calls": runCfg.ledger.Remaining().ToolCalls, "outcome": outcome}})
		}()
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
	completed := make(map[string]completedStep)
	executionHistory := make([]exec.StepResult, 0)
	requestedCapabilities := make(map[string]string, len(semantic.Steps))
	for _, step := range semantic.Steps {
		requestedCapabilities[step.ID] = step.Capability
	}
	replans := 0
	maxReplans := r.cfg.budget.MaxReplans
	if maxReplans <= 0 {
		maxReplans = 1
	}
	actions := make([]tool.ActionReceipt, 0)
	// Non-nil when replanning failed and the run fell back to composing from
	// the facts it already has. Checked after the loop to degrade the outcome.
	var replanFailure error
	started := time.Now()
	for {
		if r.cfg.budget.MaxSteps > 0 && len(semantic.Steps) > r.cfg.budget.MaxSteps {
			return nil, errors.New("budget: max steps exceeded")
		}
		execution, err = exec.Compile(semantic)
		if err != nil {
			r.emit(observe.Event{Type: "plan.compile_failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": err.Error()}})
			return nil, err
		}
		if r.cfg.budget.MaxBatches > 0 && len(execution.Batches) > r.cfg.budget.MaxBatches {
			return nil, fmt.Errorf("budget: max batches exceeded")
		}
		for _, step := range execution.Steps {
			if _, exists := requestedCapabilities[step.ID]; !exists {
				requestedCapabilities[step.ID] = step.Capability
			}
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
		fingerprints := executionFingerprints(execution)
		reusable := reusableSteps(execution, fingerprints, completed)
		scheduler := exec.Scheduler{Registry: registry, MaxParallel: parallel, Policy: r.cfg.failurePolicy, Completed: reusable, Observe: func(event policy.Event) {
			r.emit(observe.Event{Type: event.Type, RunID: runID, PlanVersion: semantic.Version, ToolID: event.ToolID, Attempt: event.Attempt, Data: map[string]any{"fallback": event.Fallback, "error_kind": event.ErrorKind, "error": errString(event.Err)}})
		}, Deadline: exec.DeadlinePolicy{TargetLatency: r.cfg.budget.TargetLatency, HardTimeout: r.cfg.budget.HardTimeout, OptionalCutoff: r.cfg.budget.OptionalCutoff}, Barrier: &exec.Barrier{}, Guard: func(ctx context.Context, value tool.Tool) error {
			if r.cfg.guard == nil {
				return nil
			}
			return r.cfg.guard(ctx, value.Spec())
		}, Confirm: r.cfg.confirm, Agentic: r.agenticExecutor(runCfg, registry)}
		// Let the scheduler resolve inputs so bindings can use evidence from
		// completed dependency steps.
		results, err = scheduler.Run(ctx, execution, nil)
		for index := range results {
			result := results[index]
			result.RequestedCapability = requestedCapabilities[result.StepID]
			results[index] = result
			if _, replayed := reusable[result.StepID]; !replayed {
				executionHistory = append(executionHistory, result)
			}
			if result.Err == nil && !result.Skipped {
				completed[result.StepID] = completedStep{Result: result, Fingerprint: fingerprints[result.StepID]}
			}
		}
		r.emitStepResults(runID, semantic.Version, results)
		if hasAmbiguousAction(results) || failedAfterWrite(results) {
			// Retain write facts and stop the graph before any confirmation or
			// replan can turn a partly executed step into a fresh invocation.
			err = nil
			break
		}
		if pending, ok := confirmationPending(results); ok {
			modelCalls, totalUsage := runCfg.counter.snapshot()
			toolCalls, successful, failed, retries, fallbacks := stepStats(results)
			toolCalls = runCfg.ledger.Snapshot().ToolCalls
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
		if errors.Is(err, budget.Exceeded) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if err == nil {
			r.emit(observe.Event{Type: "plan.compiled", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"steps": len(semantic.Steps), "batches": len(execution.Batches)}})
			for _, batch := range execution.Batches {
				r.emit(observe.Event{Type: "batch.completed", RunID: runID, PlanVersion: semantic.Version, BatchIndex: batch.Index})
			}
			break
		}
		if recovered {
			replanFailure = fmt.Errorf("replan: not attempted because the initial plan was recovered: %w", err)
			break
		}
		if replans >= maxReplans {
			replanFailure = fmt.Errorf("budget: max replans exceeded after execution failure: %w", err)
			r.emit(observe.Event{Type: "replan.failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": err.Error(), "reason": "max_replans", "replans": replans, "max_replans": maxReplans}})
			break
		}
		rp := r.cfg.replanner
		if rp == nil {
			modelReplanner := plan.NewModelReplanner(runCfg.model)
			modelReplanner.MaxRepairAttempts = r.cfg.plannerRecovery.MaxRepairAttempts
			modelReplanner.DisableOutputTool = r.cfg.plannerRecovery.DisableOutputTool
			modelReplanner.Trace = r.plannerTrace("replan", runID, semantic.Version)
			rp = modelReplanner
		}
		replanInput := plan.Input{Original: semantic, Failure: err.Error(), Tools: registry.ModelSpecs()}
		r.emit(observe.Event{Type: "replan.started", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": err.Error()}, Details: map[string]any{"request": map[string]any{"original": semantic, "failure": err.Error(), "tools": model.PlanningToolCatalog(replanInput.Tools)}}})
		var replanHandler middleware.ReplannerHandler = rp.Replan
		replannerMiddleware := append(append([]middleware.ReplannerMiddleware(nil), r.cfg.replannerMiddleware...), runCfg.replannerMiddleware...)
		for i := len(replannerMiddleware) - 1; i >= 0; i-- {
			replanHandler = replannerMiddleware[i](replanHandler)
		}
		patch, patchErr := replanHandler(ctx, replanInput)
		if patchErr != nil {
			// A failed replan must not discard the facts already produced. Exit the
			// loop and compose from execution history; the outcome is degraded and
			// the replan failure is surfaced as a warning rather than an outage.
			if errors.Is(patchErr, budget.Exceeded) || errors.Is(patchErr, context.Canceled) || errors.Is(patchErr, context.DeadlineExceeded) {
				return nil, &safeOperationError{message: "replan: execution and replanning failed", cause: errors.Join(err, patchErr)}
			}
			replanFailure = errors.Join(err, patchErr)
			r.emit(observe.Event{Type: "replan.failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": patchErr.Error(), "execution_error": err.Error()}})
			break
		}
		if compatibilityErr := plan.ValidatePatchCompatibility(semantic, patch, registry.ModelSpecs()); compatibilityErr != nil {
			replanFailure = errors.Join(err, fmt.Errorf("replan: incompatible patch: %w", compatibilityErr))
			r.emit(observe.Event{Type: "replan.failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": compatibilityErr.Error(), "execution_error": err.Error(), "reason": "incompatible_patch"}})
			break
		}
		patched, applyErr := patch.Apply(semantic)
		if applyErr != nil {
			replanFailure = errors.Join(err, fmt.Errorf("replan: patch apply failed: %w", applyErr))
			r.emit(observe.Event{Type: "replan.failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": applyErr.Error(), "execution_error": err.Error(), "reason": "patch_apply"}})
			break
		}
		if protectErr := protectCompletedWrites(semantic, patched, completed, registry); protectErr != nil {
			replanFailure = errors.Join(err, fmt.Errorf("replan: unsafe patch: %w", protectErr))
			r.emit(observe.Event{Type: "replan.failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": protectErr.Error(), "execution_error": err.Error(), "reason": "write_protection"}})
			break
		}
		if validateErr := plan.ValidateAgainstTools(patched, registry.ModelSpecs()); validateErr != nil {
			// The patch may be structurally fine but contain the same
			// deterministic defects the planner produced, so give the rule fixer
			// a chance before declaring the replan lost.
			fixed, applied, fixErr := replanFixer.TryFix(patched, validateErr, registry.ModelSpecs())
			if fixErr != nil || !applied {
				replanFailure = errors.Join(err, fmt.Errorf("replan: invalid patched plan: %w", validateErr))
				r.emit(observe.Event{Type: "replan.failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": validateErr.Error(), "execution_error": err.Error(), "reason": "invalid_patched_plan"}})
				break
			}
			if protectErr := protectCompletedWrites(semantic, fixed, completed, registry); protectErr != nil {
				replanFailure = errors.Join(err, fmt.Errorf("replan: unsafe repaired patch: %w", protectErr))
				r.emit(observe.Event{Type: "replan.failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": protectErr.Error(), "execution_error": err.Error(), "reason": "write_protection"}})
				break
			}
			patched = fixed
			r.emit(observe.Event{Type: "replan.repaired", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": validateErr.Error(), "steps": len(patched.Steps)}})
		}
		semantic = patched
		replans++
		r.emit(observe.Event{Type: "replan.completed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"version": semantic.Version, "replans": replans}, Details: map[string]any{"patch": patch, "plan": semantic}})
	}
	evidence := make([]Evidence, 0, len(results))
	warnings := make([]contract.Warning, 0)
	outcome := contract.OutcomeSuccess
	if recovered {
		outcome = contract.OutcomeDegraded
		warnings = append(warnings, contract.Warning{Code: "INITIAL_PLAN_RECOVERED", Message: "The initial plan was invalid; the response reflects bounded recovery facts and may require clarification."})
	}
	if replanFailure != nil {
		outcome = contract.OutcomeDegraded
		warnings = append(warnings, contract.Warning{Code: "REPLAN_FAILED", Message: replanFailure.Error(), Metadata: map[string]any{"replans": replans}})
	}
	currentSuccess := make(map[string]bool, len(results))
	for _, result := range results {
		currentSuccess[result.StepID] = result.Err == nil && !result.Skipped
	}
	for _, historical := range executionHistory {
		if historical.Err != nil && !currentSuccess[historical.StepID] {
			outcome = contract.OutcomeDegraded
			warnings = append(warnings, contract.Warning{Code: "STEP_FAILED_REPLANNED", Message: historical.Err.Error(), StepID: historical.StepID, Metadata: map[string]any{"tool_id": historical.ToolID, "capability": historical.Capability, "attempts": historical.Attempts}})
		}
	}
	for _, result := range results {
		if result.Skipped {
			code, message := "STEP_SKIPPED", "step condition was not met"
			if result.ConditionUnknown {
				code, message = "CONDITION_UNKNOWN", "step condition could not be evaluated because its source value was unavailable"
			}
			warnings = append(warnings, contract.Warning{Code: code, Message: message, StepID: result.StepID})
			continue
		}
		for _, action := range stepActions(result) {
			actions = append(actions, action)
			if action.Status != contract.ActionSuccess {
				outcome = contract.OutcomeDegraded
				code := "ACTION_NOT_COMPLETED"
				if action.Status == contract.ActionUnknown {
					code = "AMBIGUOUS_SIDE_EFFECT"
				}
				warnings = append(warnings, contract.Warning{Code: code, Message: action.Message, StepID: result.StepID})
			}
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
		status := "success"
		evidence = append(evidence, Evidence{StepID: result.StepID, ToolID: result.ToolID, RequestedCapability: requestedCapability(result), ExecutedCapability: result.Capability, Status: status, Receipt: result.Receipt, Attempts: result.Attempts, Fallbacks: result.Fallbacks, ProviderID: providerID, Data: result.Content, Content: result.Content, CapturedAt: now, SchemaValid: true})
	}
	structuredEvidence := map[string]any{"goal": req.Input, "outcome": outcome, "execution_facts": executionFacts(executionHistory), "evidence": evidence, "warnings": warnings, "actions": actions}
	evidenceJSON, marshalErr := json.Marshal(structuredEvidence)
	if marshalErr != nil {
		return nil, fmt.Errorf("final composer evidence: %w", marshalErr)
	}
	finalInput := model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Answer using only the structured execution facts. step_id is only a plan node name and must never be described as a tool. tool_id is the only executed tool identity. A write action is complete only when status is success and it has a successful receipt. Never infer a missing or unknown value as false."}, {Role: model.RoleUser, Content: string(evidenceJSON)}}, ResponseFormat: runCfg.responseFormat, Options: cloneMetadata(runCfg.modelOptions)}
	finalInput.Messages = append(r.requestMessages(req), finalInput.Messages...)
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
	toolCalls, successful, failed, retries, fallbacks := stepStats(executionHistory)
	toolCalls = runCfg.ledger.Snapshot().ToolCalls
	stats := contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, SuccessfulTools: successful, FailedTools: failed, Retries: retries, Fallbacks: fallbacks, PlanSteps: len(semantic.Steps), Batches: len(execution.Batches), Replans: replans, Duration: time.Since(started), TotalTokens: totalUsage.TotalTokens, PromptTokens: totalUsage.PromptTokens, CompletionTokens: totalUsage.CompletionTokens}
	return &Response{RunID: runID, Text: final.Text, FinishReason: final.FinishReason, Usage: totalUsage, Outcome: outcome, Stats: stats, Warnings: warnings, ToolCalls: toolCalls, Evidence: evidence, PlanSteps: len(semantic.Steps), Batches: len(execution.Batches), Replans: replans, Actions: actions}, nil
}

type completedStep struct {
	Result      exec.StepResult
	Fingerprint string
}

func executionFingerprints(execution exec.ExecutionPlan) map[string]string {
	out := make(map[string]string, len(execution.Steps))
	for _, step := range execution.Steps {
		encoded, _ := json.Marshal(step)
		digest := sha256.Sum256(encoded)
		out[step.ID] = hex.EncodeToString(digest[:])
	}
	return out
}

func reusableSteps(execution exec.ExecutionPlan, fingerprints map[string]string, completed map[string]completedStep) map[string]exec.StepResult {
	out := make(map[string]exec.StepResult)
	for _, step := range execution.Steps {
		stored, ok := completed[step.ID]
		if !ok || stored.Fingerprint != fingerprints[step.ID] {
			continue
		}
		dependenciesReusable := true
		for _, dependency := range step.DependsOn {
			if _, ok := out[dependency]; !ok {
				dependenciesReusable = false
				break
			}
		}
		if dependenciesReusable {
			out[step.ID] = stored.Result
		}
	}
	return out
}

func hasAmbiguousAction(results []exec.StepResult) bool {
	for _, result := range results {
		for _, action := range stepActions(result) {
			if action.Status == contract.ActionUnknown {
				return true
			}
		}
	}
	return false
}

func failedAfterWrite(results []exec.StepResult) bool {
	for _, result := range results {
		if result.Err == nil {
			continue
		}
		// A confirmation continuation persists the earlier writes explicitly.
		if result.Continuation != nil && result.Action != nil && result.Action.Status == contract.ActionConfirmationRequired {
			continue
		}
		for _, action := range stepActions(result) {
			if action.Status == contract.ActionSuccess {
				return true
			}
		}
	}
	return false
}

func stepActions(result exec.StepResult) []contract.ActionReceipt {
	if content, ok := result.Content.(map[string]any); ok {
		if raw, ok := content["actions"]; ok {
			encoded, _ := json.Marshal(raw)
			var actions []contract.ActionReceipt
			if json.Unmarshal(encoded, &actions) == nil && len(actions) > 0 {
				return actions
			}
		}
	}
	if result.Action != nil {
		return []contract.ActionReceipt{*result.Action}
	}
	return nil
}

func executionFacts(results []exec.StepResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		status := "success"
		if result.Skipped {
			status = "skipped"
		} else if result.Err != nil {
			status = "failed"
		}
		fact := map[string]any{"step_id": result.StepID, "requested_capability": requestedCapability(result), "executed_capability": result.Capability, "tool_id": result.ToolID, "status": status, "attempts": result.Attempts, "fallbacks": result.Fallbacks, "receipt": result.Receipt}
		if result.Err != nil {
			fact["error"] = result.Err.Error()
		}
		if result.ConditionUnknown {
			fact["condition"] = "unknown"
		}
		if result.Action != nil {
			fact["action"] = result.Action
		}
		out = append(out, fact)
	}
	return out
}

func requestedCapability(result exec.StepResult) string {
	if result.RequestedCapability != "" {
		return result.RequestedCapability
	}
	return result.Capability
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
		toolCalls += calls
		if result.Err == nil && calls > 0 {
			successful++
			failed += calls - 1
		} else {
			failed += calls
		}
		candidateCalls := result.Fallbacks + 1
		if calls > candidateCalls {
			retries += calls - candidateCalls
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
				"requested_capability": requestedCapability(result),
				"executed_capability":  result.Capability,
				"attempts":             result.Attempts,
				"fallbacks":            result.Fallbacks,
				"duration_ms":          result.Duration.Milliseconds(),
				"error":                errString(result.Err),
			},
			Details: map[string]any{"input": result.Input, "output": result.Content},
		})
	}
}

func (r *runtime) emit(event observe.Event) {
	if event.Data != nil {
		if raw, ok := event.Data["error"].(string); ok && raw != "" {
			data := cloneMetadata(event.Data)
			details := cloneMetadata(event.Details)
			if details == nil {
				details = map[string]any{}
			}
			details["error"] = raw
			data["error"] = "operation_failed"
			event.Data, event.Details = data, details
		}
	}
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
	for _, provider := range r.cfg.mcpCapabilityProviders {
		if err := provider.Close(); err != nil {
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
	if err := r.prepare(runCtx); err != nil {
		cancel()
		return nil, err
	}
	runCfg, registry, err := r.newRunConfig(options)
	if err != nil {
		cancel()
		return nil, err
	}
	runCtx = budget.WithLedger(runCtx, runCfg.ledger)
	selected, err := r.selectMCPTools(runCtx, runCfg.model, req.Input)
	if err != nil {
		cancel()
		return nil, err
	}
	registry, err = r.addSelectedTools(runCfg, registry, selected)
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
		owned = false
		return newAsyncRunStream(runCtx, r, req, runCfg, registry, func() { cancel(); r.done() }), nil
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
	r.observeBudget(runCtx, runID)
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
	if event.Response != nil {
		if ledger := budget.FromContext(s.ctx); ledger != nil {
			used := ledger.Snapshot()
			event.Response.ToolCalls = used.ToolCalls
			event.Response.Stats.ToolCalls, event.Response.Stats.ModelCalls = used.ToolCalls, used.ModelCalls
		}
	}
	if raw, ok := event.Data["error"].(string); ok && raw != "" {
		event.Data = cloneMetadata(event.Data)
		event.Data["error"] = "operation_failed"
	}
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
