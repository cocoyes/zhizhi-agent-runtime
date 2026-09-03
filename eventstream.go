package zhizhi

import (
	"context"
	"io"
	"sync"

	"github.com/cocoyes/zhizhi-agent-runtime/observe"
)

type asyncRunStream struct {
	events chan Event
	cancel context.CancelFunc
	once   sync.Once
	onDone func()
}

func newAsyncRunStream(ctx context.Context, r *runtime, req Request, options []RunOption, observer observe.Observer, onDone func()) EventStream {
	runCtx, cancel := context.WithCancel(ctx)
	result := &asyncRunStream{events: make(chan Event, 64), cancel: cancel, onDone: onDone}
	bridge := observe.Func(func(value observe.Event) {
		eventType := EventType(value.Type)
		switch value.Type {
		case "planner.completed":
			eventType = EventPlanCreated
		case "model.request", "planner.model.request", "replan.model.request", "final_composer.started":
			eventType = EventModelStarted
		}
		event := Event{Type: eventType, RunID: value.RunID, Data: value.Data}
		select {
		case result.events <- event:
		case <-runCtx.Done():
		}
	})
	runOptions := append(append([]RunOption(nil), options...), WithRunObserver(observe.Multi(observer, bridge)))
	go func() {
		defer result.once.Do(result.onDone)
		defer close(result.events)
		response, err := r.Run(runCtx, req, runOptions...)
		if err != nil {
			select {
			case result.events <- Event{Type: EventRunFailed, Data: map[string]any{"error": err.Error()}}:
			case <-runCtx.Done():
			}
			return
		}
		select {
		case result.events <- Event{Type: EventRunCompleted, RunID: response.RunID, Response: response}:
		case <-runCtx.Done():
		}
	}()
	return result
}
func (s *asyncRunStream) Recv(ctx context.Context) (event Event, returnErr error) {
	defer func() { returnErr = standardizeError("agent.stream.recv", returnErr) }()
	select {
	case value, ok := <-s.events:
		if !ok {
			return Event{}, io.EOF
		}
		return value, nil
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}
func (s *asyncRunStream) Close() error { s.cancel(); s.once.Do(s.onDone); return nil }
