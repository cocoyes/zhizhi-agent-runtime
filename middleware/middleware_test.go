package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type streamTool struct{ tool.Tool }
type oneResultStream struct{ done bool }

func (streamTool) Stream(context.Context, json.RawMessage) (tool.ToolStream, error) {
	return &oneResultStream{}, nil
}
func (s *oneResultStream) Recv(context.Context) (tool.StreamEvent, error) {
	if s.done {
		return tool.StreamEvent{}, io.EOF
	}
	s.done = true
	return tool.StreamEvent{Result: &tool.Result{Content: "ok"}}, nil
}
func (*oneResultStream) Close() error { return nil }

func TestToolStreamMiddlewareRunsDuringCollection(t *testing.T) {
	base := tool.Func("stream", "stream", func(context.Context, struct{}) (string, error) { return "fallback", nil })
	called := 0
	mw := func(next ToolStreamHandler) ToolStreamHandler {
		return func(ctx context.Context, invocation ToolInvocation) (tool.ToolStream, error) {
			called++
			return next(ctx, invocation)
		}
	}
	wrapped := WrapTool(streamTool{Tool: base}, nil, []ToolStreamMiddleware{mw})
	result, err := tool.CallAndCollect(context.Background(), wrapped, json.RawMessage(`{}`), nil)
	if err != nil || result.Content != "ok" || called != 1 {
		t.Fatalf("unexpected collection: %+v called=%d err=%v", result, called, err)
	}
}

func TestToolMiddlewareRetriesReserveEachActualDispatch(t *testing.T) {
	ledger := budget.New(0, 1)
	ctx := budget.WithLedger(context.Background(), ledger)
	dispatches := 0
	base := tool.Func("read", "read", func(context.Context, struct{}) (string, error) { dispatches++; return "", errors.New("temporary") })
	retry := func(next ToolHandler) ToolHandler {
		return func(ctx context.Context, in ToolInvocation) (tool.Result, error) {
			if out, err := next(ctx, in); err == nil {
				return out, nil
			}
			return next(ctx, in)
		}
	}
	wrapped := WrapTool(WrapTool(base, []ToolMiddleware{retry}, nil), []ToolMiddleware{func(next ToolHandler) ToolHandler { return next }}, nil)
	_, err := tool.CallAndCollect(ctx, wrapped, json.RawMessage(`{}`), nil)
	if !errors.Is(err, budget.Exceeded) || dispatches != 1 || ledger.Snapshot().ToolCalls != 1 {
		t.Fatalf("dispatches=%d usage=%+v err=%v", dispatches, ledger.Snapshot(), err)
	}
}
