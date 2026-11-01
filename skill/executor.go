package skill

import (
	"context"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

// Executor executes an already selected, version-checked instruction package.
// The host implementation owns permissions, budgets and durable continuation;
// loading a Skill by itself does not grant execution authority.
type Executor interface {
	Execute(context.Context, Loaded, string) (tool.Result, error)
}
