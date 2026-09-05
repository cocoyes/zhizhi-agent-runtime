// Package tool defines validated tools, registries, and capability resolution.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/invopop/jsonschema"
	jsonschema6 "github.com/santhosh-tekuri/jsonschema/v6"
)

type SideEffectClass string

const (
	SideEffectNone               SideEffectClass = "none"
	SideEffectRead               SideEffectClass = "read"
	SideEffectWriteIdempotent    SideEffectClass = "write_idempotent"
	SideEffectWriteNonIdempotent SideEffectClass = "write_non_idempotent"
	SideEffectDestructive        SideEffectClass = "destructive"
	SideEffectUnknown            SideEffectClass = "unknown"
	SideEffectWrite              SideEffectClass = SideEffectWriteNonIdempotent // compatibility alias
)

type Idempotency string

const (
	IdempotencySafe          Idempotency = "safe"
	IdempotencyIdempotent    Idempotency = "idempotent"
	IdempotencyNonIdempotent Idempotency = "non_idempotent"
	IdempotencyUnknown       Idempotency = "unknown"
)

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type ConfirmationPolicy string

const (
	ConfirmationNever  ConfirmationPolicy = "never"
	ConfirmationOnRisk ConfirmationPolicy = "on_risk"
	ConfirmationAlways ConfirmationPolicy = "always"
)

type Spec struct {
	ID           string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Capabilities []string
	// SemanticGroup identifies capabilities that can satisfy the same business
	// intent. Fallbacks is an explicit allowlist; the runtime never infers
	// replacement compatibility from names or descriptions.
	SemanticGroup  string
	Fallbacks      []string
	SideEffect     SideEffectClass
	Idempotency    Idempotency
	Timeout        time.Duration
	Version        string
	ProviderID     string
	Retryable      bool
	ConcurrencyKey string
	RiskLevel      RiskLevel
	Confirmation   ConfirmationPolicy
	DataClasses    []string
	TrustLevel     string
}

type Result struct {
	Content any
	Receipt bool
	Action  *ActionReceipt
}

type ActionReceipt = contract.ActionReceipt

type Tool interface {
	Spec() Spec
	ModelSpec() model.ToolSpec
	Call(context.Context, json.RawMessage) (Result, error)
}

// StreamEvent is one item produced by a streaming tool. Delta is suitable for
// immediate delivery to a caller; Result, when non-nil, is the authoritative
// final result and must be the last item in the stream.
type StreamEvent struct {
	Delta  any
	Result *Result
}

// ToolStream is intentionally pull based so cancellation and backpressure are
// inherited from the caller's context.
type ToolStream interface {
	Recv(context.Context) (StreamEvent, error)
	Close() error
}

// StreamableTool is optional. Ordinary Tool implementations remain valid.
type StreamableTool interface {
	Tool
	Stream(context.Context, json.RawMessage) (ToolStream, error)
}

type FuncOption func(*Spec)

func WithCapabilities(values ...string) FuncOption {
	return func(s *Spec) { s.Capabilities = append(s.Capabilities, values...) }
}
func WithSemanticGroup(value string) FuncOption { return func(s *Spec) { s.SemanticGroup = value } }
func WithFallbacks(values ...string) FuncOption {
	return func(s *Spec) { s.Fallbacks = append(s.Fallbacks, values...) }
}
func WithSideEffect(v SideEffectClass) FuncOption      { return func(s *Spec) { s.SideEffect = v } }
func WithIdempotency(v Idempotency) FuncOption         { return func(s *Spec) { s.Idempotency = v } }
func WithTimeout(v time.Duration) FuncOption           { return func(s *Spec) { s.Timeout = v } }
func WithVersion(v string) FuncOption                  { return func(s *Spec) { s.Version = v } }
func WithProviderID(v string) FuncOption               { return func(s *Spec) { s.ProviderID = v } }
func WithRetryable(v bool) FuncOption                  { return func(s *Spec) { s.Retryable = v } }
func WithConcurrencyKey(v string) FuncOption           { return func(s *Spec) { s.ConcurrencyKey = v } }
func WithRisk(v RiskLevel) FuncOption                  { return func(s *Spec) { s.RiskLevel = v } }
func WithConfirmation(v ConfirmationPolicy) FuncOption { return func(s *Spec) { s.Confirmation = v } }
func WithDataClasses(v ...string) FuncOption {
	return func(s *Spec) { s.DataClasses = append(s.DataClasses, v...) }
}

type funcTool[I any, O any] struct {
	spec         Spec
	fn           func(context.Context, I) (O, error)
	outputSchema json.RawMessage
}

func Func[I any, O any](name, description string, fn func(context.Context, I) (O, error), options ...FuncOption) Tool {
	// Function-calling APIs require the parameters schema itself to expose its
	// type. DoNotReference keeps struct schemas inline instead of emitting only
	// a top-level $ref, while still supporting legacy scalar tool types here so
	// they can fail with a clear adapter error if sent to a model.
	r := &jsonschema.Reflector{DoNotReference: true}
	in := r.Reflect(new(I))
	input, inputErr := json.Marshal(in)
	out := r.Reflect(new(O))
	output, outputErr := json.Marshal(out)
	if inputErr != nil {
		input = json.RawMessage(`[`)
	}
	if outputErr != nil {
		output = json.RawMessage(`[`)
	}
	s := Spec{ID: name, Description: description, InputSchema: input, SideEffect: SideEffectRead, Idempotency: IdempotencySafe, Retryable: true, RiskLevel: RiskLow, Confirmation: ConfirmationNever, TrustLevel: "trusted"}
	for _, option := range options {
		option(&s)
	}
	s.OutputSchema = output
	return &funcTool[I, O]{spec: s, fn: fn, outputSchema: output}
}

func (s Spec) IsWrite() bool {
	return s.SideEffect == SideEffectWrite || s.SideEffect == SideEffectWriteIdempotent || s.SideEffect == SideEffectWriteNonIdempotent || s.SideEffect == SideEffectDestructive
}
func (s Spec) RequiresConfirmation() bool {
	return s.Confirmation == ConfirmationAlways || (s.Confirmation == ConfirmationOnRisk && (s.RiskLevel == RiskHigh || s.RiskLevel == RiskCritical || s.IsWrite()))
}
func (s Spec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("tool: id is required")
	}
	if s.Description == "" {
		return fmt.Errorf("tool %s: description is required", s.ID)
	}
	if len(s.InputSchema) > 0 && !json.Valid(s.InputSchema) {
		return fmt.Errorf("tool %s: input schema is invalid JSON", s.ID)
	}
	if len(s.OutputSchema) > 0 && !json.Valid(s.OutputSchema) {
		return fmt.Errorf("tool %s: output schema is invalid JSON", s.ID)
	}
	if s.IsWrite() && s.Idempotency == IdempotencyUnknown {
		return fmt.Errorf("tool %s: write idempotency must be declared", s.ID)
	}
	return nil
}

func (t *funcTool[I, O]) Spec() Spec { return t.spec }
func (t *funcTool[I, O]) ModelSpec() model.ToolSpec {
	return model.ToolSpec{Type: "function", Function: model.FunctionSpec{Name: t.spec.ID, Description: t.spec.Description, Parameters: t.spec.InputSchema}, Capabilities: append([]string(nil), t.spec.Capabilities...), OutputSchema: t.spec.OutputSchema, SemanticGroup: t.spec.SemanticGroup, Fallbacks: append([]string(nil), t.spec.Fallbacks...), SideEffect: string(t.spec.SideEffect), Idempotency: string(t.spec.Idempotency)}
}
func (t *funcTool[I, O]) Call(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ValidateInput(t.spec, raw); err != nil {
		return Result{}, err
	}
	var args I
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, fmt.Errorf("tool %s: invalid arguments: %w", t.spec.ID, err)
	}
	if t.spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.spec.Timeout)
		defer cancel()
	}
	v, err := t.fn(ctx, args)
	if err != nil {
		return Result{}, err
	}
	encodedOutput, err := json.Marshal(v)
	if err != nil {
		return Result{}, fmt.Errorf("tool %s: encode output: %w", t.spec.ID, err)
	}
	var genericOutput any
	_ = json.Unmarshal(encodedOutput, &genericOutput)
	if err := ValidateValue(t.spec.ID, t.outputSchema, genericOutput); err != nil {
		return Result{}, err
	}
	isWrite := t.spec.IsWrite()
	result := Result{Content: v, Receipt: isWrite}
	if isWrite {
		now := time.Now().UTC()
		result.Action = &ActionReceipt{ToolID: t.spec.ID, Status: contract.ActionSuccess, ExecutedAt: now, Timestamp: now}
	}
	return result, nil
}

func ValidateInput(spec Spec, raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("tool %s: invalid arguments: %w", spec.ID, err)
	}
	return ValidateValue(spec.ID, spec.InputSchema, value)
}
func ValidateValue(id string, schemaJSON json.RawMessage, value any) error {
	if len(schemaJSON) == 0 || string(schemaJSON) == "null" {
		return nil
	}
	var schema any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return fmt.Errorf("tool %s: invalid schema: %w", id, err)
	}
	compiler := jsonschema6.NewCompiler()
	if err := compiler.AddResource("mem://tool-schema.json", schema); err != nil {
		return fmt.Errorf("tool %s: register schema: %w", id, err)
	}
	compiled, err := compiler.Compile("mem://tool-schema.json")
	if err != nil {
		return fmt.Errorf("tool %s: compile schema: %w", id, err)
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("tool %s: value violates schema: %w", id, err)
	}
	return nil
}

type Registry struct {
	mu         sync.RWMutex
	tools      map[string]Tool
	duplicates map[string]struct{}
}

func NewRegistry(values ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool), duplicates: make(map[string]struct{})}
	for _, v := range values {
		r.Add(v)
	}
	return r
}
func (r *Registry) Add(v Tool) {
	if v != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		id := v.Spec().ID
		if _, ok := r.tools[id]; ok {
			r.duplicates[id] = struct{}{}
			return
		}
		r.tools[id] = v
	}
}
func (r *Registry) Validate() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id := range r.duplicates {
		return fmt.Errorf("tool: duplicate id %q", id)
	}
	for id, value := range r.tools {
		if err := value.Spec().Validate(); err != nil {
			return fmt.Errorf("tool %s: %w", id, err)
		}
	}
	return nil
}
func (r *Registry) Get(id string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.tools[id]
	return v, ok
}
func (r *Registry) ResolveCapability(capability string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0)
	for id, value := range r.tools {
		wireID := normalizeToolName(id)
		if id == capability || wireID == capability {
			ids = append(ids, id)
			continue
		}
		for _, candidate := range value.Spec().Capabilities {
			if candidate == capability {
				ids = append(ids, id)
				break
			}
		}
	}
	sort.Strings(ids)
	out := make([]Tool, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.tools[id])
	}
	return out
}

// normalizeToolName mirrors the function-name restrictions used by the
// OpenAI-compatible wire protocol. It lets a planner refer to a tool by its
// sanitized function name while the registry keeps the original ID.
func normalizeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
func (r *Registry) ModelSpecs() []model.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.tools))
	for id := range r.tools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]model.ToolSpec, 0, len(r.tools))
	for _, id := range ids {
		value := r.tools[id]
		modelSpec := value.ModelSpec()
		spec := value.Spec()
		modelSpec.Capabilities = append([]string(nil), spec.Capabilities...)
		modelSpec.OutputSchema = append(json.RawMessage(nil), spec.OutputSchema...)
		modelSpec.SemanticGroup = spec.SemanticGroup
		modelSpec.Fallbacks = append([]string(nil), spec.Fallbacks...)
		modelSpec.SideEffect = string(spec.SideEffect)
		modelSpec.Idempotency = string(spec.Idempotency)
		out = append(out, modelSpec)
	}
	return out
}

// Tools returns a deterministic immutable snapshot of registered tools.
func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.tools))
	for id := range r.tools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Tool, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.tools[id])
	}
	return out
}
