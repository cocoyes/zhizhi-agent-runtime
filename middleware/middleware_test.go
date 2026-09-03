package middleware

import (
	"context"
	"encoding/json"
	"io"
	"testing"

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
