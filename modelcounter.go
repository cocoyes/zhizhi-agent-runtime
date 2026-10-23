package zhizhi

import (
	"context"
	"sync"

	"github.com/cocoyes/zhizhi-agent-runtime/internal/budget"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

type modelCounter struct {
	base  model.Model
	mu    sync.Mutex
	calls int
	usage model.Usage
}

// modelCallError preserves the provider error while distinguishing a failed
// request from validation errors that merely mention a model.
type modelCallError struct{ cause error }

func (e *modelCallError) Error() string { return e.cause.Error() }
func (e *modelCallError) Unwrap() error { return e.cause }

func (c *modelCounter) ID() string                            { return c.base.ID() }
func (c *modelCounter) Capabilities() model.ModelCapabilities { return c.base.Capabilities() }
func (c *modelCounter) Generate(ctx context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := budget.FromContext(ctx).ReserveModel("model.generate"); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	out, err := c.base.Generate(ctx, input)
	c.mu.Lock()
	if out != nil {
		c.usage.PromptTokens += out.Usage.PromptTokens
		c.usage.CompletionTokens += out.Usage.CompletionTokens
		c.usage.TotalTokens += out.Usage.TotalTokens
	}
	c.mu.Unlock()
	if err != nil {
		return out, &modelCallError{cause: err}
	}
	return out, err
}
func (c *modelCounter) Stream(ctx context.Context, input model.ModelInput) (model.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := budget.FromContext(ctx).ReserveModel("model.stream"); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	stream, err := c.base.Stream(ctx, input)
	if err != nil {
		return stream, &modelCallError{cause: err}
	}
	return stream, err
}
func (c *modelCounter) snapshot() (int, model.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.usage
}
