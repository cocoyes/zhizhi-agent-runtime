package speech

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

// ToolSet is the function schema catalog plus its matching executor.
type ToolSet struct {
	Definitions []ToolDefinition
	Executor    ToolExecutor
}

type ToolAuthorizer func(context.Context, tool.Spec, FunctionCall) error

// RegistryToolSet adapts the runtime's typed tool registry to realtime speech.
// By default all tools which require confirmation are rejected, because a
// realtime voice turn has no implicit authorization UI.
func RegistryToolSet(registry *tool.Registry, authorize ToolAuthorizer) (ToolSet, error) {
	if registry == nil {
		return ToolSet{}, fmt.Errorf("speech: tool registry is required")
	}
	if err := registry.Validate(); err != nil {
		return ToolSet{}, err
	}
	byWireName := make(map[string]tool.Tool)
	definitions := make([]ToolDefinition, 0)
	for _, value := range registry.Tools() {
		name := wireToolName(value.Spec().ID)
		if _, exists := byWireName[name]; exists {
			return ToolSet{}, fmt.Errorf("speech: tool names collide after normalization: %q", name)
		}
		byWireName[name] = value
		modelSpec := value.ModelSpec()
		definitions = append(definitions, ToolDefinition{
			Type: "function", Name: name, Description: modelSpec.Function.Description,
			Parameters: append(json.RawMessage(nil), modelSpec.Function.Parameters...),
		})
	}
	executor := ToolExecutorFunc(func(ctx context.Context, call FunctionCall) (json.RawMessage, error) {
		value, ok := byWireName[call.Name]
		if !ok {
			return nil, fmt.Errorf("speech: tool %q is not registered", call.Name)
		}
		spec := value.Spec()
		if authorize != nil {
			if err := authorize(ctx, spec, call); err != nil {
				return nil, err
			}
		} else if spec.RequiresConfirmation() {
			return nil, fmt.Errorf("speech: tool %q requires explicit authorization", spec.ID)
		}
		result, err := tool.CallAndCollect(ctx, value, json.RawMessage(call.Arguments), nil)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result.Content)
	})
	return ToolSet{Definitions: definitions, Executor: executor}, nil
}

func wireToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
