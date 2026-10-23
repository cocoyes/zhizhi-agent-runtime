package zhizhi

import (
	"context"
	"fmt"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/middleware"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type metadataContextKey struct{}

func MetadataFromContext(ctx context.Context) map[string]any {
	value, _ := ctx.Value(metadataContextKey{}).(map[string]any)
	return cloneMetadata(value)
}
func contextWithMetadata(ctx context.Context, value map[string]any) context.Context {
	if len(value) == 0 {
		return ctx
	}
	return context.WithValue(ctx, metadataContextKey{}, cloneMetadata(value))
}

type RunToolFilter func(tool.Spec) bool

type RunOption func(*runConfig) error

type runConfig struct {
	tools                 []tool.Tool
	filter                RunToolFilter
	observer              observe.Observer
	metadata              map[string]any
	modelOptions          map[string]any
	toolChoice            model.ToolChoice
	responseFormat        *model.ResponseFormat
	modelMiddleware       []middleware.ModelMiddleware
	modelStreamMiddleware []middleware.ModelStreamMiddleware
	toolMiddleware        []middleware.ToolMiddleware
	toolStreamMiddleware  []middleware.ToolStreamMiddleware
	plannerMiddleware     []middleware.PlannerMiddleware
	replannerMiddleware   []middleware.ReplannerMiddleware
	agentMiddleware       []AgentMiddleware
	model                 model.Model
	counter               *modelCounter
	ledger                *budget.Ledger
}

func WithRunTools(values ...tool.Tool) RunOption {
	return func(c *runConfig) error { c.tools = append(c.tools, values...); return nil }
}
func WithRunToolFilter(filter RunToolFilter) RunOption {
	return func(c *runConfig) error { c.filter = filter; return nil }
}
func WithRunObserver(value observe.Observer) RunOption {
	return func(c *runConfig) error { c.observer = value; return nil }
}
func WithRunMetadata(value map[string]any) RunOption {
	return func(c *runConfig) error { c.metadata = cloneMetadata(value); return nil }
}
func WithRunModelOptions(value map[string]any) RunOption {
	return func(c *runConfig) error { c.modelOptions = cloneMetadata(value); return nil }
}
func WithRunToolChoice(value model.ToolChoice) RunOption {
	return func(c *runConfig) error { c.toolChoice = value; return nil }
}
func WithRunResponseFormat(value model.ResponseFormat) RunOption {
	return func(c *runConfig) error { copyValue := value; c.responseFormat = &copyValue; return nil }
}
func WithRunModelMiddleware(values ...middleware.ModelMiddleware) RunOption {
	return func(c *runConfig) error { c.modelMiddleware = append(c.modelMiddleware, values...); return nil }
}
func WithRunModelStreamMiddleware(values ...middleware.ModelStreamMiddleware) RunOption {
	return func(c *runConfig) error {
		c.modelStreamMiddleware = append(c.modelStreamMiddleware, values...)
		return nil
	}
}
func WithRunToolMiddleware(values ...middleware.ToolMiddleware) RunOption {
	return func(c *runConfig) error { c.toolMiddleware = append(c.toolMiddleware, values...); return nil }
}
func WithRunToolStreamMiddleware(values ...middleware.ToolStreamMiddleware) RunOption {
	return func(c *runConfig) error {
		c.toolStreamMiddleware = append(c.toolStreamMiddleware, values...)
		return nil
	}
}
func WithRunPlannerMiddleware(values ...middleware.PlannerMiddleware) RunOption {
	return func(c *runConfig) error { c.plannerMiddleware = append(c.plannerMiddleware, values...); return nil }
}
func WithRunReplannerMiddleware(values ...middleware.ReplannerMiddleware) RunOption {
	return func(c *runConfig) error { c.replannerMiddleware = append(c.replannerMiddleware, values...); return nil }
}
func WithRunAgentMiddleware(values ...AgentMiddleware) RunOption {
	return func(c *runConfig) error { c.agentMiddleware = append(c.agentMiddleware, values...); return nil }
}

func cloneMetadata(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for k, v := range value {
		out[k] = v
	}
	return out
}

func (r *runtime) newRunConfig(options []RunOption) (runConfig, *tool.Registry, error) {
	var cfg runConfig
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&cfg); err != nil {
			return runConfig{}, nil, err
		}
	}
	values := append(r.registry.Tools(), cfg.tools...)
	if cfg.toolChoice == model.ToolChoiceForbidden {
		values = nil
	}
	if cfg.filter != nil {
		filtered := values[:0]
		for _, value := range values {
			if cfg.filter(value.Spec()) {
				filtered = append(filtered, value)
			}
		}
		values = filtered
	}
	callMiddleware := append(append([]middleware.ToolMiddleware(nil), r.cfg.toolMiddleware...), cfg.toolMiddleware...)
	streamMiddleware := append(append([]middleware.ToolStreamMiddleware(nil), r.cfg.toolStreamMiddleware...), cfg.toolStreamMiddleware...)
	wrapped := make([]tool.Tool, 0, len(values))
	for _, value := range values {
		wrapped = append(wrapped, middleware.WrapTool(value, callMiddleware, streamMiddleware))
	}
	registry := tool.NewRegistry(wrapped...)
	if err := registry.Validate(); err != nil {
		return runConfig{}, nil, fmt.Errorf("run tools: %w", err)
	}
	cfg.counter = &modelCounter{base: r.cfg.model}
	cfg.ledger = budget.New(r.cfg.budget.MaxModelCalls, r.cfg.budget.MaxToolCalls)
	cfg.model = middleware.WrapModel(cfg.counter,
		append(append([]middleware.ModelMiddleware(nil), r.cfg.modelMiddleware...), cfg.modelMiddleware...),
		append(append([]middleware.ModelStreamMiddleware(nil), r.cfg.modelStreamMiddleware...), cfg.modelStreamMiddleware...))
	return cfg, registry, nil
}

// addSelectedTools applies the same request filter and middleware to tools
// discovered after the request model configuration has been constructed.
func (r *runtime) addSelectedTools(cfg runConfig, registry *tool.Registry, selected []tool.Tool) (*tool.Registry, error) {
	if cfg.toolChoice == model.ToolChoiceForbidden {
		return registry, nil
	}
	values := append([]tool.Tool(nil), registry.Tools()...)
	calls := append(append([]middleware.ToolMiddleware(nil), r.cfg.toolMiddleware...), cfg.toolMiddleware...)
	streams := append(append([]middleware.ToolStreamMiddleware(nil), r.cfg.toolStreamMiddleware...), cfg.toolStreamMiddleware...)
	for _, value := range selected {
		if value == nil {
			continue
		}
		if cfg.filter != nil && !cfg.filter(value.Spec()) {
			continue
		}
		values = append(values, middleware.WrapTool(value, calls, streams))
	}
	registry = tool.NewRegistry(values...)
	if err := registry.Validate(); err != nil {
		return nil, fmt.Errorf("run tools: %w", err)
	}
	return registry, nil
}
