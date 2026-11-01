package tool

import (
	"encoding/json"
	"github.com/cocoyes/zhizhi-agent-runtime/contract"
)

// CompositeResult carries authoritative nested facts even when Call fails.
// Pending means the outer invocation must be checkpointed, not replayed anew.
type CompositeResult struct {
	Status       string                   `json:"status"`
	Evidence     []contract.Evidence      `json:"evidence,omitempty"`
	Actions      []contract.ActionReceipt `json:"actions,omitempty"`
	Pending      *PendingInvocation       `json:"pending,omitempty"`
	Continuation json.RawMessage          `json:"continuation,omitempty"`
}

// PendingInvocation binds an approval to exact nested arguments.
type PendingInvocation struct {
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
}

// CollectComposite collects nested facts once at each execution boundary.
// The caller separately records the ordinary outer Tool Result on completion.
func CollectComposite(result Result, evidence *[]contract.Evidence, actions *[]contract.ActionReceipt) bool {
	if result.Composite == nil {
		return false
	}
	*evidence = append(*evidence, result.Composite.Evidence...)
	*actions = append(*actions, result.Composite.Actions...)
	return result.Composite.Pending != nil
}
