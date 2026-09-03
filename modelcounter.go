package zhizhi

import (
	"context"
	"sync"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

type modelCounter struct {
	base  model.Model
	mu    sync.Mutex
	calls int
	usage model.Usage
}

func (c *modelCounter) ID() string                            { return c.base.ID() }
func (c *modelCounter) Capabilities() model.ModelCapabilities { return c.base.Capabilities() }
func (c *modelCounter) Generate(ctx context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	out, err := c.base.Generate(ctx, input)
	c.mu.Lock()
	c.calls++
	if out != nil {
		c.usage.PromptTokens += out.Usage.PromptTokens
		c.usage.CompletionTokens += out.Usage.CompletionTokens
		c.usage.TotalTokens += out.Usage.TotalTokens
	}
	c.mu.Unlock()
	return out, err
}
func (c *modelCounter) Stream(ctx context.Context, input model.ModelInput) (model.Stream, error) {
	stream, err := c.base.Stream(ctx, input)
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return stream, err
}
func (c *modelCounter) snapshot() (int, model.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.usage
}
