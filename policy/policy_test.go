package policy

import (
	"context"
	"errors"
	"fmt"
	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
	"testing"
)

func TestRequestStopNeverRetriesOrFallsBack(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, budget.Exceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			first := tool.Func("first", "first", func(context.Context, struct{}) (string, error) {
				calls++
				return "", fmt.Errorf("temporary timeout: %w", cause)
			})
			backup := tool.Func("backup", "backup", func(context.Context, struct{}) (string, error) { calls++; return "ok", nil })
			_, err := (Runner{Config: Config{MaxAttempts: 3}}).Execute(context.Background(), []tool.Tool{first, backup}, []byte(`{}`))
			if !errors.Is(err, cause) || calls != 1 {
				t.Fatalf("stop lost: %v calls=%d", err, calls)
			}
		})
	}
}

func TestRetryReadOnlyTransientFailure(t *testing.T) {
	attempts := 0
	v := tool.Func("read", "read", func(context.Context, struct{}) (string, error) {
		attempts++
		if attempts < 2 {
			return "", errors.New("temporary timeout")
		}
		return "ok", nil
	}, tool.WithIdempotency(tool.IdempotencySafe))
	out, err := (Runner{}).Execute(context.Background(), []tool.Tool{v}, []byte(`{}`))
	if err != nil || out.Attempts != 2 || attempts != 2 {
		t.Fatalf("unexpected retry result: %+v %v attempts=%d", out, err, attempts)
	}
}
func TestDoesNotRetryWrite(t *testing.T) {
	attempts := 0
	v := tool.Func("write", "write", func(context.Context, struct{}) (string, error) { attempts++; return "", errors.New("timeout") }, tool.WithSideEffect(tool.SideEffectWrite), tool.WithIdempotency(tool.IdempotencySafe))
	_, err := (Runner{}).Execute(context.Background(), []tool.Tool{v}, []byte(`{}`))
	if err == nil || attempts != 1 {
		t.Fatalf("write was retried: err=%v attempts=%d", err, attempts)
	}
}

func TestUnknownEffectCannotRetryOrFallback(t *testing.T) {
	calls, backupCalls := 0, 0
	unknown := tool.Func("unknown", "unknown effect", func(context.Context, struct{}) (string, error) {
		calls++
		return "", errors.New("temporary unavailable")
	}, tool.WithSideEffect(tool.SideEffectUnknown), tool.WithIdempotency(tool.IdempotencySafe), tool.WithRetryable(true))
	backup := tool.Func("backup", "backup", func(context.Context, struct{}) (string, error) { backupCalls++; return "ok", nil })
	out, err := (Runner{Config: Config{MaxAttempts: 3}}).Execute(context.Background(), []tool.Tool{unknown, backup}, []byte(`{}`))
	if err == nil || calls != 1 || backupCalls != 0 || out.ErrorKind != Ambiguous {
		t.Fatalf("unknown effect replayed: %+v err=%v calls=%d backup=%d", out, err, calls, backupCalls)
	}
	decision := (DecisionEngine{Default: FailurePolicy{OnExhausted: ActionReplan}}).Decide(context.Background(), Observation{Err: err, Tool: unknown.Spec()})
	if decision.Action != ActionAbort {
		t.Fatalf("unknown effect may replan: %+v", decision)
	}
}
func TestFallbackCandidate(t *testing.T) {
	first := tool.Func("first", "first", func(context.Context, struct{}) (string, error) { return "", errors.New("unavailable") }, tool.WithCapabilities("x"))
	second := tool.Func("second", "second", func(context.Context, struct{}) (string, error) { return "ok", nil }, tool.WithCapabilities("x"))
	out, err := (Runner{Config: Config{MaxAttempts: 1}}).Execute(context.Background(), []tool.Tool{first, second}, []byte(`{}`))
	if err != nil || out.ToolID != "second" || out.Fallbacks != 1 {
		t.Fatalf("unexpected fallback: %+v %v", out, err)
	}
}

func TestGuardRunsForFallbackCandidate(t *testing.T) {
	first := tool.Func("first", "first", func(context.Context, struct{}) (string, error) { return "", errors.New("unavailable") }, tool.WithCapabilities("x"))
	second := tool.Func("second", "second", func(context.Context, struct{}) (string, error) { return "ok", nil }, tool.WithCapabilities("x"))
	checked := 0
	runner := Runner{Config: Config{MaxAttempts: 1}, BeforeCall: func(tool.Tool) error { checked++; return nil }}
	out, err := runner.Execute(context.Background(), []tool.Tool{first, second}, []byte(`{}`))
	if err != nil || out.ToolID != "second" || checked != 2 {
		t.Fatalf("guard did not run for fallback: %+v err=%v checked=%d", out, err, checked)
	}
}

func TestRetryOnRestrictsFailureClasses(t *testing.T) {
	attempts := 0
	v := tool.Func("read", "read", func(context.Context, struct{}) (string, error) {
		attempts++
		return "", errors.New("temporary timeout")
	})
	_, _ = (Runner{Config: Config{MaxAttempts: 3, RetryOn: []ErrorKind{Permanent}}}).Execute(context.Background(), []tool.Tool{v}, []byte(`{}`))
	if attempts != 1 {
		t.Fatalf("unexpected retries for disallowed class: %d", attempts)
	}
}

func TestFailurePreservesInvocationMetadata(t *testing.T) {
	v := tool.Func("broken", "broken", func(context.Context, struct{}) (string, error) {
		return "", errors.New("permanent failure")
	})
	out, err := (Runner{Config: Config{MaxAttempts: 1}}).Execute(context.Background(), []tool.Tool{v}, []byte(`{}`))
	if err == nil || out.ToolID != "broken" || out.Attempts != 1 || len(out.AttemptsLog) != 1 {
		t.Fatalf("lost failed invocation metadata: out=%+v err=%v", out, err)
	}
}

func TestAmbiguousFailureDoesNotFallback(t *testing.T) {
	secondCalled := false
	first := tool.Func("first", "first", func(context.Context, struct{}) (string, error) {
		return "", errors.New("connection lost after write; unknown effect")
	})
	second := tool.Func("second", "second", func(context.Context, struct{}) (string, error) {
		secondCalled = true
		return "ok", nil
	})
	out, err := (Runner{Config: Config{MaxAttempts: 1}}).Execute(context.Background(), []tool.Tool{first, second}, []byte(`{}`))
	if err == nil || secondCalled || out.ToolID != "first" || out.Attempts != 1 {
		t.Fatalf("ambiguous call fell back: out=%+v err=%v second=%v", out, err, secondCalled)
	}
}
