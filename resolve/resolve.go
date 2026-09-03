package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/plan"
	"github.com/go-openapi/jsonpointer"
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
		value, ok := Lookup(evidence[binding.SourceStep], binding.SourcePath)
		if !ok {
			source, err := normalize(evidence[binding.SourceStep])
			if err != nil {
				return nil, fmt.Errorf("resolve: encode evidence %s: %w", binding.SourceStep, err)
			}
			value, ok = Lookup(source, binding.SourcePath)
		}
		if !ok {
			return nil, fmt.Errorf("resolve: missing evidence %s.%s", binding.SourceStep, binding.SourcePath)
		}
		if err := setPath(out, binding.TargetPath, value); err != nil {
			return nil, fmt.Errorf("resolve: set target %s: %w", binding.TargetPath, err)
		}
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

// Lookup resolves an RFC 6901 JSON Pointer. For model-facing convenience it
// also accepts a dotted field path and converts it to a pointer before handing
// resolution to jsonpointer.
func Lookup(value any, path string) (any, bool) {
	pointer, err := jsonpointer.New(asJSONPointer(path))
	if err != nil {
		return nil, false
	}
	result, _, err := pointer.Get(value)
	return result, err == nil
}
func setPath(target map[string]any, path string, value any) error {
	pointer, err := jsonpointer.New(asJSONPointer(path))
	if err != nil {
		return err
	}
	_, err = pointer.Set(target, value)
	return err
}

func asJSONPointer(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/") {
		return path
	}
	parts := strings.Split(strings.TrimPrefix(path, "."), ".")
	for i := range parts {
		parts[i] = jsonpointer.Escape(parts[i])
	}
	return "/" + strings.Join(parts, "/")
}
