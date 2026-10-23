package zhizhi

import (
	"context"
	"errors"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
)

func TestCounterReservesBeforeModelDispatch(t *testing.T) {
	base := &scriptedModel{}
	c := &modelCounter{base: base}
	ledger := budget.New(1, 0)
	ctx := budget.WithLedger(context.Background(), ledger)
	if _, err := c.Generate(ctx, model.ModelInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stream(ctx, model.ModelInput{}); !errors.Is(err, budget.Exceeded) {
		t.Fatalf("second call = %v", err)
	}
	calls, _ := c.snapshot()
	if calls != 1 || base.calls != 1 {
		t.Fatalf("counter=%d provider=%d", calls, base.calls)
	}
}

func TestStructuredErrorClassification(t *testing.T) {
	validation := &plan.StructuredOutputError{Phase: "initial_plan", Kind: "semantic", Recoverable: true, Cause: errors.New("model candidate is invalid")}
	var classified *RuntimeError
	if err := standardizeError("run", validation); !errors.As(err, &classified) || classified.Class != ErrorClassPlan {
		t.Fatalf("classification = %v", err)
	}
	exhausted := &plan.StructuredOutputError{Phase: "initial_plan", Kind: "semantic", Cause: &budget.Error{Dimension: "model_calls", Limit: 1, Used: 1}}
	err := standardizeError("run", exhausted)
	if !errors.As(err, &classified) || classified.Class != ErrorClassBudget || !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("budget classification = %v", err)
	}
}
