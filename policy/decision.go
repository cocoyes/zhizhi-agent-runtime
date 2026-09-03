package policy

import (
	"context"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type FailureAction string

const (
	ActionRetry    FailureAction = "retry"
	ActionFallback FailureAction = "fallback"
	ActionSkip     FailureAction = "skip"
	ActionReplan   FailureAction = "replan"
	ActionAbort    FailureAction = "abort"
)

type FailurePolicy struct {
	MaxRetries  int
	AllowSkip   bool
	OnExhausted FailureAction
	RetryOn     []ErrorKind
	Backoff     time.Duration
}

type Observation struct {
	Err       error
	Tool      tool.Spec
	Optional  bool
	Attempts  int
	Remaining time.Duration
}
type Decision struct {
	Action FailureAction
	Reason string
}

type DecisionEngine struct{ Default FailurePolicy }

func (e DecisionEngine) Decide(_ context.Context, observation Observation) Decision {
	if observation.Err == nil {
		return Decision{Reason: "success"}
	}
	classified := Classify(observation.Err)
	policy := e.Default
	maxRetries := policy.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if classified.Retryable && observation.Tool.Retryable && observation.Tool.Idempotency == tool.IdempotencySafe && !observation.Tool.IsWrite() && observation.Attempts <= maxRetries && observation.Remaining > 0 {
		return Decision{Action: ActionRetry, Reason: "retryable idempotent transient failure"}
	}
	if observation.Optional && policy.AllowSkip {
		return Decision{Action: ActionSkip, Reason: "optional step failed"}
	}
	if policy.OnExhausted != "" {
		return Decision{Action: policy.OnExhausted, Reason: "failure policy exhausted"}
	}
	return Decision{Action: ActionAbort, Reason: "required step failed"}
}
