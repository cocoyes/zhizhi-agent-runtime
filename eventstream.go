package zhizhi

import (
	"context"
	"io"
	"sync"

	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type asyncRunStream struct {
	events         chan Event
	cancel         context.CancelFunc
	once           sync.Once
	onDone         func()
	terminalErr    error
	errorDelivered bool
}

func newAsyncRunStream(ctx context.Context, r *runtime, req Request, cfg runConfig, registry *tool.Registry, onDone func()) EventStream {
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
	go func() {
		defer result.once.Do(result.onDone)
		defer close(result.events)
		runID, err := r.nextRunID()
		if err == nil {
			r.runObservers.Store(runID, observe.Multi(cfg.observer, bridge))
			defer r.runObservers.Delete(runID)
			r.observeBudget(runCtx, runID)
			r.emit(observe.Event{Type: "run.started", RunID: runID})
		}
		var response *Response
		if err == nil {
			response, err = r.runComplex(runCtx, req, runID, registry, cfg)
		}
		if err != nil {
			err = standardizeError("agent.stream", err)
			result.terminalErr = err
			code := "RUNTIME_ERROR"
			if typed, ok := err.(*RuntimeError); ok {
				code = typed.Code
			}
			select {
			case result.events <- Event{Type: EventRunFailed, RunID: runID, Data: map[string]any{"error": "operation_failed", "code": code}}:
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
			if s.terminalErr != nil && !s.errorDelivered {
				s.errorDelivered = true
				return Event{}, s.terminalErr
			}
			return Event{}, io.EOF
		}
		return value, nil
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}
func (s *asyncRunStream) Close() error { s.cancel(); s.once.Do(s.onDone); return nil }
