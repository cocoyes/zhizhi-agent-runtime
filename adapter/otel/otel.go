// Package otel provides optional OpenTelemetry middleware for the runtime.
// The core packages do not depend on OpenTelemetry.
package otel

import (
	"context"
	"io"
	"sync"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/middleware"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/replan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
	api "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type Instrumentation struct{ Tracer trace.Tracer }

func New(provider trace.TracerProvider) Instrumentation {
	if provider == nil {
		provider = api.GetTracerProvider()
	}
	return Instrumentation{Tracer: provider.Tracer("github.com/cocoyes/zhizhi-agent-runtime")}
}

func finish(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (i Instrumentation) AgentMiddleware(next zhizhi.AgentHandler) zhizhi.AgentHandler {
	return func(ctx context.Context, request zhizhi.Request, options ...zhizhi.RunOption) (*zhizhi.Response, error) {
		ctx, span := i.Tracer.Start(ctx, "agent.run")
		response, err := next(ctx, request, options...)
		if response != nil {
			span.SetAttributes(attribute.String("agent.run_id", response.RunID), attribute.String("agent.outcome", string(response.Outcome)))
		}
		finish(span, err)
		return response, err
	}
}

func (i Instrumentation) ModelMiddleware(next middleware.ModelHandler) middleware.ModelHandler {
	return func(ctx context.Context, input model.ModelInput) (*model.ModelOutput, error) {
		ctx, span := i.Tracer.Start(ctx, "model.generate", trace.WithAttributes(attribute.Int("model.message_count", len(input.Messages)), attribute.Int("model.tool_count", len(input.Tools))))
		output, err := next(ctx, input)
		finish(span, err)
		return output, err
	}
}

func (i Instrumentation) ModelStreamMiddleware(next middleware.ModelStreamHandler) middleware.ModelStreamHandler {
	return func(ctx context.Context, input model.ModelInput) (model.Stream, error) {
		ctx, span := i.Tracer.Start(ctx, "model.stream")
		stream, err := next(ctx, input)
		if err != nil {
			finish(span, err)
			return nil, err
		}
		return &modelStream{Stream: stream, span: span}, nil
	}
}

type modelStream struct {
	model.Stream
	span trace.Span
	once sync.Once
}

func (s *modelStream) Recv(ctx context.Context) (model.StreamEvent, error) {
	event, err := s.Stream.Recv(ctx)
	if err != nil {
		s.once.Do(func() { finish(s.span, nonEOF(err)) })
	}
	return event, err
}
func (s *modelStream) Close() error {
	err := s.Stream.Close()
	s.once.Do(func() { finish(s.span, err) })
	return err
}

func (i Instrumentation) ToolMiddleware(next middleware.ToolHandler) middleware.ToolHandler {
	return func(ctx context.Context, invocation middleware.ToolInvocation) (tool.Result, error) {
		ctx, span := i.Tracer.Start(ctx, "tool.call", trace.WithAttributes(attribute.String("tool.id", invocation.Tool.Spec().ID)))
		result, err := next(ctx, invocation)
		finish(span, err)
		return result, err
	}
}

func (i Instrumentation) ToolStreamMiddleware(next middleware.ToolStreamHandler) middleware.ToolStreamHandler {
	return func(ctx context.Context, invocation middleware.ToolInvocation) (tool.ToolStream, error) {
		ctx, span := i.Tracer.Start(ctx, "tool.stream", trace.WithAttributes(attribute.String("tool.id", invocation.Tool.Spec().ID)))
		stream, err := next(ctx, invocation)
		if err != nil {
			finish(span, err)
			return nil, err
		}
		return &toolStream{ToolStream: stream, span: span}, nil
	}
}

type toolStream struct {
	tool.ToolStream
	span trace.Span
	once sync.Once
}

func (s *toolStream) Recv(ctx context.Context) (tool.StreamEvent, error) {
	event, err := s.ToolStream.Recv(ctx)
	if err != nil {
		s.once.Do(func() { finish(s.span, nonEOF(err)) })
	}
	return event, err
}
func (s *toolStream) Close() error {
	err := s.ToolStream.Close()
	s.once.Do(func() { finish(s.span, err) })
	return err
}

func (i Instrumentation) PlannerMiddleware(next middleware.PlannerHandler) middleware.PlannerHandler {
	return func(ctx context.Context, input middleware.PlannerInput) (plan.Plan, error) {
		ctx, span := i.Tracer.Start(ctx, "planner.plan")
		result, err := next(ctx, input)
		finish(span, err)
		return result, err
	}
}

func (i Instrumentation) ReplannerMiddleware(next middleware.ReplannerHandler) middleware.ReplannerHandler {
	return func(ctx context.Context, input replan.Input) (replan.Patch, error) {
		ctx, span := i.Tracer.Start(ctx, "planner.replan")
		result, err := next(ctx, input)
		finish(span, err)
		return result, err
	}
}

// Observer records lifecycle events on lightweight event spans. Use the
// middleware above for parent-aware execution spans.
func (i Instrumentation) Observer() observe.Observer {
	return observe.Func(func(event observe.Event) {
		_, span := i.Tracer.Start(context.Background(), "agent.event", trace.WithAttributes(
			attribute.String("agent.event.type", event.Type),
			attribute.String("agent.run_id", event.RunID),
			attribute.String("agent.step_id", event.StepID),
			attribute.String("tool.id", event.ToolID),
		))
		span.End()
	})
}

func nonEOF(err error) error {
	if err == io.EOF {
		return nil
	}
	return err
}
