package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/plan"
	"strings"
)

type Binding = plan.InputBinding
type Request struct {
	Input    string
	Step     any
	Tool     model.ToolSpec
	Evidence map[string]any
}
type Resolver interface {
	Resolve(context.Context, Request) (json.RawMessage, error)
}
type Deterministic struct{}

func (Deterministic) Resolve(_ context.Context, req Request) (json.RawMessage, error) {
	values := map[string]any{}
	if raw, ok := req.Step.(map[string]any); ok {
		if input, ok := raw["input"].(map[string]any); ok {
			values = input
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("resolve: no deterministic arguments")
	}
	return json.Marshal(values)
}
func ApplyBindings(input map[string]any, evidence map[string]any, bindings []Binding) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range input {
		out[k] = v
	}
	for _, binding := range bindings {
		value, ok := lookup(evidence[binding.SourceStep], binding.SourcePath)
		if !ok {
			source, err := normalize(evidence[binding.SourceStep])
			if err != nil {
				return nil, fmt.Errorf("resolve: encode evidence %s: %w", binding.SourceStep, err)
			}
			value, ok = lookup(source, binding.SourcePath)
		}
		if !ok {
			return nil, fmt.Errorf("resolve: missing evidence %s.%s", binding.SourceStep, binding.SourcePath)
		}
		setPath(out, binding.TargetPath, value)
	}
	return out, nil
}

func normalize(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func lookup(value any, path string) (any, bool) {
	for _, part := range strings.Split(strings.TrimPrefix(path, "."), ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}
func setPath(target map[string]any, path string, value any) {
	parts := strings.Split(strings.TrimPrefix(path, "."), ".")
	current := target
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
			return
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
}
