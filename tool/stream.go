package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// CollectingTool customizes aggregation while preserving streaming deltas.
// Middleware wrappers use this to intercept a logical invocation once.
type CollectingTool interface {
	Tool
	CallAndCollect(context.Context, json.RawMessage, func(any)) (Result, error)
}

// CallAndCollect invokes either kind of tool and aggregates a streaming tool
// for non-streaming callers. onDelta may be nil.
func CallAndCollect(ctx context.Context, value Tool, input json.RawMessage, onDelta func(any)) (Result, error) {
	if collecting, ok := value.(CollectingTool); ok {
		return collecting.CallAndCollect(ctx, input, onDelta)
	}
	streamable, ok := value.(StreamableTool)
	if !ok {
		return value.Call(ctx, input)
	}
	stream, err := streamable.Stream(ctx, input)
	if err != nil {
		return Result{}, err
	}
	if stream == nil {
		return Result{}, fmt.Errorf("tool %s returned a nil stream", value.Spec().ID)
	}
	return CollectStream(ctx, stream, onDelta)
}

// CollectStream drains a tool stream and returns its final result.
func CollectStream(ctx context.Context, stream ToolStream, onDelta func(any)) (Result, error) {
	if stream == nil {
		return Result{}, errors.New("tool returned a nil stream")
	}
	defer stream.Close()
	var final *Result
	for {
		event, recvErr := stream.Recv(ctx)
		if recvErr != nil {
			if recvErr == io.EOF {
				break
			}
			return Result{}, recvErr
		}
		if event.Delta != nil && onDelta != nil {
			onDelta(event.Delta)
		}
		if event.Result != nil {
			copyResult := *event.Result
			final = &copyResult
		}
	}
	if final == nil {
		return Result{}, errors.New("tool stream ended without a final result")
	}
	return *final, nil
}

type singleResultStream struct {
	result Result
	done   bool
}

func (s *singleResultStream) Recv(ctx context.Context) (StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return StreamEvent{}, err
	}
	if s.done {
		return StreamEvent{}, io.EOF
	}
	s.done = true
	return StreamEvent{Result: &s.result}, nil
}
func (s *singleResultStream) Close() error { s.done = true; return nil }

// Stream wraps an ordinary tool result in a one-event stream.
func Stream(ctx context.Context, value Tool, input json.RawMessage) (ToolStream, error) {
	if streamable, ok := value.(StreamableTool); ok {
		return streamable.Stream(ctx, input)
	}
	result, err := value.Call(ctx, input)
	if err != nil {
		return nil, err
	}
	return &singleResultStream{result: result}, nil
}
