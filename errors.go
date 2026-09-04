package zhizhi

import (
	"context"
	"errors"
	"io"
	"strings"
)

// ErrorClass identifies the runtime boundary that produced an error.
type ErrorClass string

// RuntimeError is the structured error returned by Agent operations. Use it
// with errors.As while sentinel errors continue to support errors.Is.
type RuntimeError struct {
	Code            string
	Class           ErrorClass
	Operation       string
	Message         string
	Retryable       bool
	AmbiguousEffect bool
	Cause           error
}

func (e *RuntimeError) Error() string { return e.Message }
func (e *RuntimeError) Unwrap() error { return e.Cause }

const (
	ErrorClassConfig     ErrorClass = "config"
	ErrorClassModel      ErrorClass = "model"
	ErrorClassPlan       ErrorClass = "plan"
	ErrorClassCompile    ErrorClass = "compile"
	ErrorClassCapability ErrorClass = "capability"
	ErrorClassTool       ErrorClass = "tool"
	ErrorClassTimeout    ErrorClass = "timeout"
	ErrorClassCancelled  ErrorClass = "cancelled"
	ErrorClassBudget     ErrorClass = "budget"
	ErrorClassRuntime    ErrorClass = "runtime"
)

// Stable sentinel errors support errors.Is, while RuntimeError provides
// structured classification through errors.As.
var (
	ErrBudgetExceeded = errors.New("zhizhi: budget exceeded")
	ErrToolNotFound   = errors.New("zhizhi: tool not found")
	ErrAgentClosed    = errors.New("zhizhi: agent closed")
)

func standardizeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return err
	}
	var structured *RuntimeError
	if errors.As(err, &structured) {
		return err
	}
	class, code := ErrorClassRuntime, "RUNTIME_ERROR"
	cause := err
	switch {
	case errors.Is(err, context.Canceled):
		class, code = ErrorClassCancelled, "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		class, code = ErrorClassTimeout, "DEADLINE_EXCEEDED"
	case strings.Contains(err.Error(), "budget:"):
		class, code, cause = ErrorClassBudget, "BUDGET_EXCEEDED", errors.Join(ErrBudgetExceeded, err)
	case strings.Contains(err.Error(), "tool not found"):
		class, code, cause = ErrorClassTool, "TOOL_NOT_FOUND", errors.Join(ErrToolNotFound, err)
	case strings.Contains(err.Error(), "agent is closed"):
		class, code, cause = ErrorClassRuntime, "AGENT_CLOSED", errors.Join(ErrAgentClosed, err)
	case strings.Contains(err.Error(), "model"):
		class, code = ErrorClassModel, "MODEL_ERROR"
	case strings.Contains(err.Error(), "plan"):
		class, code = ErrorClassPlan, "PLAN_ERROR"
	}
	return &RuntimeError{Code: code, Class: class, Operation: operation, Message: err.Error(), Cause: cause}
}
