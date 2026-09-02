package zhizhi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zhizhi-ai/zhizhi-agent-runtime/capability"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/contract"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/exec"
	runtimemcp "github.com/zhizhi-ai/zhizhi-agent-runtime/mcp"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/observe"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/plan"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/planner"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/policy"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/replan"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/route"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
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
	RunID, Text string
	Usage       model.Usage
	Outcome     contract.RunOutcome
	Stats       contract.RunStats
	Warnings    []contract.Warning
	ToolCalls   int
	Evidence    []contract.Evidence
	PlanSteps   int
	Batches     int
	Replans     int
	Actions     []contract.ActionReceipt
}
type Evidence = contract.Evidence
type Event struct {
	TextDelta string
	ToolCall  *model.ToolCallDelta
	Response  *Response
}
type EventStream interface {
	Recv(context.Context) (Event, error)
	Close() error
}
type Agent interface {
	Run(context.Context, Request) (*Response, error)
	Stream(context.Context, Request) (EventStream, error)
	Close(context.Context) error
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
type config struct {
	model         model.Model
	systemPrompt  string
	tools         []tool.Tool
	planner       planner.Planner
	replanner     replan.Replanner
	mcpConfigs    []runtimemcp.Config
	observer      observe.Observer
	budget        Budget
	guard         Guard
	confirm       ConfirmFunc
	failurePolicy policy.Runner
	router        route.Router
	broker        *capability.Broker
	maxParallel   int
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
	registry := tool.NewRegistry(c.tools...)
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	providers := make([]*runtimemcp.Provider, 0, len(c.mcpConfigs))
	for _, config := range c.mcpConfigs {
		provider, err := runtimemcp.Connect(context.Background(), config)
		if err != nil {
			for _, opened := range providers {
				_ = opened.Close()
			}
			return nil, err
		}
		provider.AddToRegistry(registry)
		providers = append(providers, provider)
	}
	return &runtime{cfg: c, registry: registry, mcpProviders: providers, observer: c.observer, broker: capability.NewBroker(registry), maxParallel: c.budget.MaxParallelSteps}, nil
}

type runtime struct {
	cfg          *config
	registry     *tool.Registry
	broker       *capability.Broker
	maxParallel  int
	mcpProviders []*runtimemcp.Provider
	observer     observe.Observer
}

func (r *runtime) Run(ctx context.Context, req Request) (*Response, error) {
	if r.cfg.budget.HardTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.budget.HardTimeout)
		defer cancel()
	}
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	r.emit(observe.Event{Type: "run.started", RunID: runID, Data: map[string]any{"input_length": len(req.Input)}})
	if req.Mode == nil {
		router := r.cfg.router
		if router == nil {
			router = route.DefaultRouter{}
		}
		intent, routeErr := router.Route(ctx, req.Input, r.registry.ModelSpecs())
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
		resp, err := r.runComplex(ctx, req, runID)
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
	input := model.ModelInput{Messages: msgs, Tools: r.registry.ModelSpecs()}
	var evidence []Evidence
	var usage model.Usage
	modelCalls, toolCalls := 0, 0
	started := time.Now()
	for i := 0; i < 8; i++ {
		modelCalls++
		if r.cfg.budget.MaxModelCalls > 0 && modelCalls > r.cfg.budget.MaxModelCalls {
			r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "max model calls exceeded"}})
			return nil, fmt.Errorf("budget: max model calls exceeded")
		}
		out, err := r.cfg.model.Generate(ctx, input)
		if err != nil {
			r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": err.Error()}})
			return nil, err
		}
		usage = out.Usage
		r.emit(observe.Event{Type: "model.completed", RunID: runID, Data: map[string]any{"tool_calls": len(out.ToolCalls), "total_tokens": out.Usage.TotalTokens}})
		if len(out.ToolCalls) == 0 {
			resp := &Response{RunID: runID, Text: out.Text, Usage: usage, ToolCalls: len(evidence), Evidence: evidence, Outcome: contract.OutcomeSuccess, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, TotalTokens: usage.TotalTokens, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, Duration: time.Since(started)}}
			r.emit(observe.Event{Type: "run.completed", RunID: runID})
			return resp, nil
		}
		input.Messages = append(input.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: out.ToolCalls})
		for _, call := range out.ToolCalls {
			toolCalls++
			if r.cfg.budget.MaxToolCalls > 0 && toolCalls > r.cfg.budget.MaxToolCalls {
				r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "max tool calls exceeded"}})
				return nil, fmt.Errorf("budget: max tool calls exceeded")
			}
			t, ok := r.registry.Get(call.Function.Name)
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
					return &Response{RunID: runID, Outcome: contract.OutcomeDegraded, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: receipt.Message, StepID: spec.ID}}, Actions: []contract.ActionReceipt{receipt}, Stats: contract.RunStats{ModelCalls: modelCalls, ToolCalls: toolCalls, Duration: time.Since(started)}}, nil
				}
			}
			result, err := t.Call(ctx, json.RawMessage(call.Function.Arguments))
			if err != nil {
				r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": err.Error(), "tool_id": call.Function.Name}})
				return nil, fmt.Errorf("tool %s: %w", call.Function.Name, err)
			}
			evidence = append(evidence, Evidence{ToolID: call.Function.Name, Content: result.Content})
			if result.Action != nil {
				r.emit(observe.Event{Type: "action.completed", RunID: runID, Data: map[string]any{"tool_id": result.Action.ToolID, "status": result.Action.Status}})
			}
			r.emit(observe.Event{Type: "tool.completed", RunID: runID, Data: map[string]any{"tool_id": call.Function.Name, "receipt": result.Receipt}})
			b, _ := json.Marshal(result.Content)
			input.Messages = append(input.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: string(b)})
		}
	}
	r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "tool call limit exceeded"}})
	return nil, errors.New("agent: tool call limit exceeded")
}

func (r *runtime) runComplex(ctx context.Context, req Request, runID string) (*Response, error) {
	p := r.cfg.planner
	if p == nil {
		p = planner.NewModelPlanner(r.cfg.model)
	}
	r.emit(observe.Event{Type: "planner.started", RunID: runID})
	semantic, err := p.Plan(ctx, req.Input, r.registry.ModelSpecs())
	if err != nil {
		r.emit(observe.Event{Type: "planner.failed", RunID: runID, Data: map[string]any{"error": err.Error()}})
		return nil, err
	}
	r.emit(observe.Event{Type: "planner.completed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"steps": len(semantic.Steps)}})
	semantic, err = plan.Optimize(semantic)
	if err != nil {
		return nil, err
	}
	if r.cfg.budget.MaxSteps > 0 && len(semantic.Steps) > r.cfg.budget.MaxSteps {
		return nil, fmt.Errorf("budget: max steps exceeded")
	}
	for _, step := range semantic.Steps {
		if _, resolveErr := r.broker.Resolve(step.Capability); resolveErr != nil {
			return nil, resolveErr
		}
	}
	var execution exec.ExecutionPlan
	var results []exec.StepResult
	replans := 0
	actions := make([]tool.ActionReceipt, 0)
	started := time.Now()
	for {
		execution, err = exec.Compile(semantic)
		if err != nil {
			r.emit(observe.Event{Type: "plan.compile_failed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"error": err.Error()}})
			return nil, err
		}
		if r.cfg.budget.MaxBatches > 0 && len(execution.Batches) > r.cfg.budget.MaxBatches {
			r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": "max batches exceeded"}})
			return nil, fmt.Errorf("budget: max batches exceeded")
		}
		for _, batch := range execution.Batches {
			r.emit(observe.Event{Type: "batch.started", RunID: runID, PlanVersion: semantic.Version, BatchIndex: batch.Index, Data: map[string]any{"steps": len(batch.Steps)}})
		}
		parallel := r.maxParallel
		if parallel < 1 {
			parallel = 4
		}
		scheduler := exec.Scheduler{Registry: r.registry, MaxParallel: parallel, Policy: r.cfg.failurePolicy, Observe: func(event policy.Event) {
			r.emit(observe.Event{Type: event.Type, RunID: runID, PlanVersion: semantic.Version, ToolID: event.ToolID, Attempt: event.Attempt, Data: map[string]any{"fallback": event.Fallback, "error_kind": event.ErrorKind, "error": errString(event.Err)}})
		}, Deadline: exec.DeadlinePolicy{TargetLatency: r.cfg.budget.TargetLatency, HardTimeout: r.cfg.budget.HardTimeout, OptionalCutoff: r.cfg.budget.OptionalCutoff}, Barrier: &exec.Barrier{}, Guard: func(ctx context.Context, value tool.Tool) error {
			if r.cfg.guard == nil {
				return nil
			}
			return r.cfg.guard(ctx, value.Spec())
		}, Confirm: r.cfg.confirm}
		// Let the scheduler resolve inputs so bindings can use evidence from
		// completed dependency steps.
		results, err = scheduler.Run(ctx, execution, nil)
		if err == nil {
			r.emit(observe.Event{Type: "plan.compiled", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"steps": len(semantic.Steps), "batches": len(execution.Batches)}})
			for _, batch := range execution.Batches {
				r.emit(observe.Event{Type: "batch.completed", RunID: runID, PlanVersion: semantic.Version, BatchIndex: batch.Index})
			}
			break
		}
		if replans >= 1 {
			r.emit(observe.Event{Type: "run.failed", RunID: runID, Data: map[string]any{"error": err.Error()}})
			return nil, err
		}
		if r.cfg.budget.MaxReplans > 0 && replans >= r.cfg.budget.MaxReplans {
			return nil, fmt.Errorf("budget: max replans exceeded")
		}
		rp := r.cfg.replanner
		if rp == nil {
			rp = replan.NewModelReplanner(r.cfg.model)
		}
		patch, patchErr := rp.Replan(ctx, replan.Input{Original: semantic, Failure: err.Error(), Tools: r.registry.ModelSpecs()})
		if patchErr != nil {
			return nil, fmt.Errorf("replan: execution failed: %v; replanner failed: %w", err, patchErr)
		}
		semantic, err = patch.Apply(semantic)
		if err != nil {
			return nil, err
		}
		replans++
		r.emit(observe.Event{Type: "replan.completed", RunID: runID, PlanVersion: semantic.Version, Data: map[string]any{"version": semantic.Version, "replans": replans}})
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
		evidence = append(evidence, Evidence{ToolID: result.ToolID, Content: result.Content})
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
		b, _ := json.Marshal(result.Content)
		evidenceText.WriteString(result.StepID + ": " + string(b) + "\n")
	}
	finalInput := model.ModelInput{Messages: []model.Message{{Role: model.RoleSystem, Content: "Answer using only the provided evidence. Do not claim an action succeeded without a receipt."}, {Role: model.RoleUser, Content: req.Input + "\nEvidence:\n" + evidenceText.String()}}}
	final, err := r.cfg.model.Generate(ctx, finalInput)
	if err != nil {
		return nil, fmt.Errorf("final composer: %w", err)
	}
	stats := contract.RunStats{ModelCalls: 2 + replans, ToolCalls: len(results), PlanSteps: len(semantic.Steps), Batches: len(execution.Batches), Replans: replans, Duration: time.Since(started), TotalTokens: final.Usage.TotalTokens, PromptTokens: final.Usage.PromptTokens, CompletionTokens: final.Usage.CompletionTokens}
	return &Response{Text: final.Text, Usage: final.Usage, Outcome: outcome, Stats: stats, Warnings: warnings, ToolCalls: len(evidence), Evidence: evidence, PlanSteps: len(semantic.Steps), Batches: len(execution.Batches), Replans: replans, Actions: actions}, nil
}

func (r *runtime) emit(event observe.Event) {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if r.observer != nil {
		r.observer.Observe(event)
	}
}
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func semanticInput(p plan.Plan, id string) json.RawMessage {
	for _, step := range p.Steps {
		if step.ID == id && step.Input != nil {
			b, _ := json.Marshal(step.Input)
			return b
		}
	}
	return nil
}
func (r *runtime) Close(context.Context) error {
	for _, provider := range r.mcpProviders {
		if err := provider.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (r *runtime) Stream(ctx context.Context, req Request) (EventStream, error) {
	msgs := append([]model.Message{}, req.Messages...)
	if r.cfg.systemPrompt != "" {
		msgs = append([]model.Message{{Role: model.RoleSystem, Content: r.cfg.systemPrompt}}, msgs...)
	}
	msgs = append(msgs, model.Message{Role: model.RoleUser, Content: req.Input})
	s, err := r.cfg.model.Stream(ctx, model.ModelInput{Messages: msgs, Tools: r.registry.ModelSpecs()})
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("model returned a nil stream")
	}
	return &eventStream{stream: s, runtime: r, input: model.ModelInput{Messages: msgs, Tools: r.registry.ModelSpecs()}, calls: make(map[int]model.ToolCallRequest)}, nil
}

type eventStream struct {
	stream    model.Stream
	runtime   *runtime
	input     model.ModelInput
	calls     map[int]model.ToolCallRequest
	processed bool
	closed    bool
}

func (s *eventStream) Recv(ctx context.Context) (Event, error) {
	if s.closed {
		return Event{}, io.EOF
	}
	ev, err := s.stream.Recv(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) && len(s.calls) > 0 && !s.processed {
			return s.finishToolCalls(ctx)
		}
		return Event{}, err
	}
	result := Event{TextDelta: ev.TextDelta}
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
		}
	}
	return result, nil
}
func (s *eventStream) finishToolCalls(ctx context.Context) (Event, error) {
	s.processed = true
	ordered := make([]model.ToolCallRequest, 0, len(s.calls))
	for i := 0; i < len(s.calls); i++ {
		if call, ok := s.calls[i]; ok {
			ordered = append(ordered, call)
		}
	}
	s.input.Messages = append(s.input.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: ordered})
	actions := make([]contract.ActionReceipt, 0)
	evidence := make([]contract.Evidence, 0, len(ordered))
	for _, call := range ordered {
		t, ok := s.runtime.registry.Get(call.Function.Name)
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
				return Event{Response: &Response{Outcome: contract.OutcomeDegraded, Actions: []contract.ActionReceipt{receipt}, Warnings: []contract.Warning{{Code: "CONFIRMATION_REQUIRED", Message: receipt.Message}}}}, nil
			}
		}
		result, err := t.Call(ctx, json.RawMessage(call.Function.Arguments))
		if err != nil {
			return Event{}, fmt.Errorf("tool %s: %w", call.Function.Name, err)
		}
		if result.Action != nil {
			actions = append(actions, *result.Action)
		}
		evidence = append(evidence, contract.Evidence{ToolID: call.Function.Name, Content: result.Content, CapturedAt: time.Now().UTC()})
		encoded, _ := json.Marshal(result.Content)
		s.input.Messages = append(s.input.Messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: string(encoded)})
	}
	final, err := s.runtime.cfg.model.Generate(ctx, s.input)
	if err != nil {
		return Event{}, fmt.Errorf("stream final composer: %w", err)
	}
	outcome := contract.OutcomeSuccess
	for _, action := range actions {
		if action.Status != contract.ActionSuccess {
			outcome = contract.OutcomeDegraded
			break
		}
	}
	return Event{Response: &Response{Text: final.Text, Usage: final.Usage, ToolCalls: len(ordered), Actions: actions, Evidence: evidence, Outcome: outcome, Stats: contract.RunStats{ModelCalls: 2, ToolCalls: len(ordered), TotalTokens: final.Usage.TotalTokens}}}, nil
}
func (s *eventStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.stream.Close()
}
