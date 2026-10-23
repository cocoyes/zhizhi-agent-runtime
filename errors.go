package zhizhi

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
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

type safeOperationError struct {
	message string
	cause   error
}

func (e *safeOperationError) Error() string { return e.message }
func (e *safeOperationError) Unwrap() error { return e.cause }

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
	var structuredOutput *plan.StructuredOutputError
	var modelFailure *modelCallError
	var planningModelFailure *plan.ModelError
	switch {
	case errors.Is(err, context.Canceled):
		class, code = ErrorClassCancelled, "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		class, code = ErrorClassTimeout, "DEADLINE_EXCEEDED"
	case errors.Is(err, budget.Exceeded), errors.Is(err, ErrBudgetExceeded), strings.Contains(err.Error(), "budget:"):
		class, code, cause = ErrorClassBudget, "BUDGET_EXCEEDED", errors.Join(ErrBudgetExceeded, err)
	case strings.Contains(err.Error(), "tool not found"):
		class, code, cause = ErrorClassTool, "TOOL_NOT_FOUND", errors.Join(ErrToolNotFound, err)
	case strings.Contains(err.Error(), "agent is closed"):
		class, code, cause = ErrorClassRuntime, "AGENT_CLOSED", errors.Join(ErrAgentClosed, err)
	case errors.As(err, &modelFailure), errors.As(err, &planningModelFailure):
		class, code = ErrorClassModel, "MODEL_ERROR"
	case errors.As(err, &structuredOutput):
		class, code = ErrorClassPlan, "PLAN_STRUCTURED_OUTPUT"
	case strings.Contains(err.Error(), "model"):
		class, code = ErrorClassModel, "MODEL_ERROR"
	case strings.Contains(err.Error(), "plan"):
		class, code = ErrorClassPlan, "PLAN_ERROR"
	case strings.Contains(err.Error(), "tool "):
		class, code = ErrorClassTool, "TOOL_ERROR"
	}
	message := err.Error()
	switch class {
	case ErrorClassModel:
		message = "model request failed"
	case ErrorClassTool:
		message = "tool invocation failed"
	case ErrorClassPlan:
		message = "plan validation or execution failed"
		if structuredOutput != nil {
			message = structuredOutput.Error()
		}
	case ErrorClassBudget:
		message = "budget exceeded"
	case ErrorClassCancelled:
		message = "request cancelled"
	case ErrorClassTimeout:
		message = "request deadline exceeded"
	}
	return &RuntimeError{Code: code, Class: class, Operation: operation, Message: message, Cause: cause}
}
