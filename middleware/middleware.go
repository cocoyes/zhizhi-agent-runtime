// Package middleware contains composable model and tool invocation wrappers.
package middleware

import (
	"context"
	"encoding/json"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/cocoyes/zhizhi-agent-runtime/replan"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type ModelHandler func(context.Context, model.ModelInput) (*model.ModelOutput, error)
type ModelMiddleware func(ModelHandler) ModelHandler
type ModelStreamHandler func(context.Context, model.ModelInput) (model.Stream, error)
type ModelStreamMiddleware func(ModelStreamHandler) ModelStreamHandler

type ToolInvocation struct {
	Tool  tool.Tool
	Input json.RawMessage
}
type ToolHandler func(context.Context, ToolInvocation) (tool.Result, error)
type ToolMiddleware func(ToolHandler) ToolHandler
type ToolStreamHandler func(context.Context, ToolInvocation) (tool.ToolStream, error)
type ToolStreamMiddleware func(ToolStreamHandler) ToolStreamHandler
type PlannerInput struct {
	Request string
	Tools   []model.ToolSpec
}
type PlannerHandler func(context.Context, PlannerInput) (plan.Plan, error)
type PlannerMiddleware func(PlannerHandler) PlannerHandler
type ReplannerHandler func(context.Context, replan.Input) (replan.Patch, error)
type ReplannerMiddleware func(ReplannerHandler) ReplannerHandler

func WrapModel(base model.Model, generate []ModelMiddleware, streams []ModelStreamMiddleware) model.Model {
	if len(generate) == 0 && len(streams) == 0 {
		return base
	}
	return &wrappedModel{base: base, generate: append([]ModelMiddleware(nil), generate...), streams: append([]ModelStreamMiddleware(nil), streams...)}
}

type wrappedModel struct {
	base     model.Model
	generate []ModelMiddleware
	streams  []ModelStreamMiddleware
}

func (w *wrappedModel) ID() string                            { return w.base.ID() }
func (w *wrappedModel) Capabilities() model.ModelCapabilities { return w.base.Capabilities() }
func (w *wrappedModel) Generate(ctx context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	var next ModelHandler = w.base.Generate
	for i := len(w.generate) - 1; i >= 0; i-- {
		next = w.generate[i](next)
	}
	return next(ctx, input)
}
func (w *wrappedModel) Stream(ctx context.Context, input model.ModelInput) (model.Stream, error) {
	var next ModelStreamHandler = w.base.Stream
	for i := len(w.streams) - 1; i >= 0; i-- {
		next = w.streams[i](next)
	}
	return next(ctx, input)
}

func WrapTool(base tool.Tool, calls []ToolMiddleware, streams []ToolStreamMiddleware) tool.Tool {
	if len(calls) == 0 && len(streams) == 0 {
		return base
	}
	return &wrappedTool{base: base, calls: append([]ToolMiddleware(nil), calls...), streams: append([]ToolStreamMiddleware(nil), streams...)}
}

type wrappedTool struct {
	base    tool.Tool
	calls   []ToolMiddleware
	streams []ToolStreamMiddleware
}

func (w *wrappedTool) Spec() tool.Spec           { return w.base.Spec() }
func (w *wrappedTool) ModelSpec() model.ToolSpec { return w.base.ModelSpec() }
func (w *wrappedTool) Call(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	var next ToolHandler = func(ctx context.Context, in ToolInvocation) (tool.Result, error) { return in.Tool.Call(ctx, in.Input) }
	for i := len(w.calls) - 1; i >= 0; i-- {
		next = w.calls[i](next)
	}
	return next(ctx, ToolInvocation{Tool: w.base, Input: input})
}
func (w *wrappedTool) CallAndCollect(ctx context.Context, input json.RawMessage, onDelta func(any)) (tool.Result, error) {
	var next ToolHandler = func(ctx context.Context, in ToolInvocation) (tool.Result, error) {
		if _, streamable := in.Tool.(tool.StreamableTool); streamable && len(w.streams) > 0 {
			stream, err := w.Stream(ctx, in.Input)
			if err != nil {
				return tool.Result{}, err
			}
			return tool.CollectStream(ctx, stream, onDelta)
		}
		return tool.CallAndCollect(ctx, in.Tool, in.Input, onDelta)
	}
	for i := len(w.calls) - 1; i >= 0; i-- {
		next = w.calls[i](next)
	}
	return next(ctx, ToolInvocation{Tool: w.base, Input: input})
}
func (w *wrappedTool) Stream(ctx context.Context, input json.RawMessage) (tool.ToolStream, error) {
	var next ToolStreamHandler = func(ctx context.Context, in ToolInvocation) (tool.ToolStream, error) {
		return tool.Stream(ctx, in.Tool, in.Input)
	}
	for i := len(w.streams) - 1; i >= 0; i-- {
		next = w.streams[i](next)
	}
	return next(ctx, ToolInvocation{Tool: w.base, Input: input})
}
