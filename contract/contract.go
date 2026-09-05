package contract

import "time"

type RunOutcome string

const (
	OutcomeSuccess        RunOutcome = "SUCCESS"
	OutcomePartialSuccess RunOutcome = "PARTIAL_SUCCESS"
	OutcomeDegraded       RunOutcome = "DEGRADED"
	OutcomeFailed         RunOutcome = "FAILED"
	OutcomeCancelled      RunOutcome = "CANCELLED"
	OutcomeSuspended      RunOutcome = "SUSPENDED"
)

type Warning struct {
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	StepID   string         `json:"step_id,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Evidence struct {
	ID                  string         `json:"id,omitempty"`
	StepID              string         `json:"step_id,omitempty"`
	ProviderID          string         `json:"provider_id,omitempty"`
	ToolID              string         `json:"tool_id,omitempty"`
	RequestedCapability string         `json:"requested_capability,omitempty"`
	ExecutedCapability  string         `json:"executed_capability,omitempty"`
	Status              string         `json:"status,omitempty"`
	Receipt             bool           `json:"receipt,omitempty"`
	Attempts            int            `json:"attempts,omitempty"`
	Fallbacks           int            `json:"fallbacks,omitempty"`
	Data                any            `json:"data,omitempty"`
	Content             any            `json:"content,omitempty"` // Compatibility alias for early SDK releases.
	CapturedAt          time.Time      `json:"captured_at"`
	FreshUntil          *time.Time     `json:"fresh_until,omitempty"`
	TrustLevel          string         `json:"trust_level,omitempty"`
	SchemaValid         bool           `json:"schema_valid"`
	Metadata            map[string]any `json:"metadata,omitempty"`
}

type ActionStatus string

const (
	ActionSuccess              ActionStatus = "SUCCESS"
	ActionFailed               ActionStatus = "FAILED"
	ActionUnknown              ActionStatus = "UNKNOWN"
	ActionConfirmationRequired ActionStatus = "CONFIRMATION_REQUIRED"
)

type ActionReceipt struct {
	StepID         string       `json:"step_id,omitempty"`
	ToolID         string       `json:"tool_id"`
	Status         ActionStatus `json:"status"`
	ExternalID     string       `json:"external_id,omitempty"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
	ExecutedAt     time.Time    `json:"executed_at,omitempty"`
	Timestamp      time.Time    `json:"timestamp,omitempty"` // Compatibility alias for early SDK releases.
	Message        string       `json:"message,omitempty"`
}

type RunStats struct {
	ModelCalls       int           `json:"model_calls"`
	ToolCalls        int           `json:"tool_calls"`
	SuccessfulTools  int           `json:"successful_tools"`
	FailedTools      int           `json:"failed_tools"`
	PlanSteps        int           `json:"plan_steps"`
	Batches          int           `json:"batches"`
	ParallelSteps    int           `json:"parallel_steps"`
	Retries          int           `json:"retries"`
	Fallbacks        int           `json:"fallbacks"`
	Skips            int           `json:"skips"`
	Replans          int           `json:"replans"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	TotalTokens      int           `json:"total_tokens"`
	Duration         time.Duration `json:"duration"`
}

type ErrorClass string

const (
	ErrorTimeout             ErrorClass = "timeout"
	ErrorNetwork             ErrorClass = "network"
	ErrorRateLimit           ErrorClass = "rate_limit"
	ErrorUnavailable         ErrorClass = "unavailable"
	ErrorInvalidInput        ErrorClass = "invalid_input"
	ErrorSchema              ErrorClass = "schema_error"
	ErrorUnauthorized        ErrorClass = "unauthorized"
	ErrorForbidden           ErrorClass = "forbidden"
	ErrorNotFound            ErrorClass = "not_found"
	ErrorRemote              ErrorClass = "remote_error"
	ErrorProviderUnhealthy   ErrorClass = "provider_unhealthy"
	ErrorToolNotFound        ErrorClass = "tool_not_found"
	ErrorToolDrift           ErrorClass = "tool_drift"
	ErrorAmbiguousSideEffect ErrorClass = "ambiguous_side_effect"
	ErrorCancelled           ErrorClass = "cancelled"
	ErrorDeadlineExceeded    ErrorClass = "deadline_exceeded"
	ErrorBudgetExceeded      ErrorClass = "budget_exceeded"
	ErrorInternal            ErrorClass = "internal"
	ErrorUnknown             ErrorClass = "unknown"
)

type RuntimeError struct {
	Code            string         `json:"code"`
	Class           ErrorClass     `json:"class"`
	Operation       string         `json:"operation,omitempty"`
	Message         string         `json:"message"`
	Retryable       bool           `json:"retryable"`
	AmbiguousEffect bool           `json:"ambiguous_effect"`
	Cause           error          `json:"-"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

func (e *RuntimeError) Error() string { return e.Code + ": " + e.Message }
func (e *RuntimeError) Unwrap() error { return e.Cause }

type Event struct {
	ID                 string         `json:"id"`
	RunID              string         `json:"run_id"`
	TraceID            string         `json:"trace_id,omitempty"`
	EventType          string         `json:"event_type"`
	PlanVersion        int            `json:"plan_version,omitempty"`
	BatchIndex         int            `json:"batch_index,omitempty"`
	StepID             string         `json:"step_id,omitempty"`
	ModelID            string         `json:"model_id,omitempty"`
	ProviderID         string         `json:"provider_id,omitempty"`
	MCPID              string         `json:"mcp_id,omitempty"`
	ToolID             string         `json:"tool_id,omitempty"`
	Attempt            int            `json:"attempt,omitempty"`
	DurationMs         int64          `json:"duration_ms,omitempty"`
	InputTokens        int64          `json:"input_tokens,omitempty"`
	OutputTokens       int64          `json:"output_tokens,omitempty"`
	TotalTokens        int64          `json:"total_tokens,omitempty"`
	ErrorClass         ErrorClass     `json:"error_class,omitempty"`
	ErrorCode          string         `json:"error_code,omitempty"`
	PromptVersion      string         `json:"prompt_version,omitempty"`
	ToolVersion        string         `json:"tool_version,omitempty"`
	CatalogFingerprint string         `json:"catalog_fingerprint,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
}
