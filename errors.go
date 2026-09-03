package zhizhi

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/runtimeerr"
)

// Stable sentinel errors support errors.Is, while runtimeerr.Error provides
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
	var structured *runtimeerr.Error
	if errors.As(err, &structured) {
		return err
	}
	class, code := runtimeerr.ClassRuntime, "RUNTIME_ERROR"
	cause := err
	switch {
	case errors.Is(err, context.Canceled):
		class, code = runtimeerr.ClassCancelled, "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		class, code = runtimeerr.ClassTimeout, "DEADLINE_EXCEEDED"
	case strings.Contains(err.Error(), "budget:"):
		class, code, cause = runtimeerr.ClassBudget, "BUDGET_EXCEEDED", errors.Join(ErrBudgetExceeded, err)
	case strings.Contains(err.Error(), "tool not found"):
		class, code, cause = runtimeerr.ClassTool, "TOOL_NOT_FOUND", errors.Join(ErrToolNotFound, err)
	case strings.Contains(err.Error(), "agent is closed"):
		class, code, cause = runtimeerr.ClassRuntime, "AGENT_CLOSED", errors.Join(ErrAgentClosed, err)
	case strings.Contains(err.Error(), "model"):
		class, code = runtimeerr.ClassModel, "MODEL_ERROR"
	case strings.Contains(err.Error(), "plan"):
		class, code = runtimeerr.ClassPlan, "PLAN_ERROR"
	}
	return runtimeerr.New(code, class, operation, err.Error(), cause)
}
