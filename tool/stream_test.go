package tool

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

type testStreamTool struct{ Tool }

func (t testStreamTool) Stream(context.Context, json.RawMessage) (ToolStream, error) {
	return &testToolStream{events: []StreamEvent{{Delta: "a"}, {Delta: "b", Result: &Result{Content: "done"}}}}, nil
}

type testToolStream struct {
	events []StreamEvent
	index  int
}

func (s *testToolStream) Recv(context.Context) (StreamEvent, error) {
	if s.index >= len(s.events) {
		return StreamEvent{}, io.EOF
	}
	value := s.events[s.index]
	s.index++
	return value, nil
}
func (s *testToolStream) Close() error { return nil }

func TestCallAndCollectStreamingTool(t *testing.T) {
	base := Func("stream", "stream", func(context.Context, struct{}) (string, error) { return "ordinary", nil })
	var deltas []any
	result, err := CallAndCollect(context.Background(), testStreamTool{Tool: base}, json.RawMessage(`{}`), func(value any) { deltas = append(deltas, value) })
	if err != nil || result.Content != "done" || len(deltas) != 2 {
		t.Fatalf("unexpected aggregate: %+v %#v %v", result, deltas, err)
	}
}

func TestStreamWrapsOrdinaryTool(t *testing.T) {
	base := Func("ordinary", "ordinary", func(context.Context, struct{}) (string, error) { return "done", nil })
	stream, err := Stream(context.Background(), base, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv(context.Background())
	if err != nil || event.Result == nil || event.Result.Content != "done" {
		t.Fatalf("unexpected event: %+v %v", event, err)
	}
}
