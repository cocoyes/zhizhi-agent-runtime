package plan

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

// ValidateAgainstTools checks that every deterministic step can produce a
// schema-valid invocation after its bindings are applied. It validates this
// before execution so malformed model plans consume repair attempts rather
// than runtime replan budget.
func ValidateAgainstTools(value Plan, tools []model.ToolSpec) error {
	// A nil catalog is supported for custom/offline planners that deliberately
	// operate without tool metadata. Runtime entry points always pass the real
	// registry, where full invocation validation remains mandatory.
	if len(tools) == 0 {
		return nil
	}
	steps := make(map[string]Step, len(value.Steps))
	for _, step := range value.Steps {
		steps[step.ID] = step
	}
	for _, step := range value.Steps {
		if step.Mode == StepModeAgentic {
			continue
		}
		spec, ok := findTool(tools, step.Capability)
		if !ok {
			return fmt.Errorf("plan: step %q capability %q has no tool", step.ID, step.Capability)
		}
		input := step.Input
		if input == nil {
			input = map[string]any{}
		}
		if len(step.Bindings) == 0 {
			if err := tool.ValidateValue(spec.Function.Name, spec.Function.Parameters, input); err != nil {
				return fmt.Errorf("plan: invalid input for step %q: %w; dependency values must use bindings", step.ID, err)
			}
			continue
		}
		var inputSchema map[string]any
		if err := json.Unmarshal(spec.Function.Parameters, &inputSchema); err != nil {
			return fmt.Errorf("plan: decode input schema for step %q: %w", step.ID, err)
		}
		if err := validateStaticInput(step.ID, input, inputSchema); err != nil {
			return err
		}
		for _, binding := range step.Bindings {
			sourceStep := steps[binding.SourceStep]
			sourceSpec, found := findTool(tools, sourceStep.Capability)
			if !found {
				return fmt.Errorf("plan: binding source step %q has no tool", binding.SourceStep)
			}
			sourceNode, sourceOK := schemaAt(sourceSpec.OutputSchema, binding.SourcePath)
			targetNode, targetOK := schemaAt(spec.Function.Parameters, binding.TargetPath)
			if !sourceOK {
				return fmt.Errorf("plan: step %q binding source path %q is absent from step %q output schema", step.ID, binding.SourcePath, binding.SourceStep)
			}
			if !targetOK {
				return fmt.Errorf("plan: step %q binding target path %q is absent from input schema", step.ID, binding.TargetPath)
			}
			if sourceType, _ := sourceNode["type"].(string); sourceType != "" {
				if targetType, _ := targetNode["type"].(string); targetType != "" && sourceType != targetType {
					return fmt.Errorf("plan: step %q binding %q type %s does not match target %q type %s", step.ID, binding.SourcePath, sourceType, binding.TargetPath, targetType)
				}
			}
		}
		if missing := missingRequired(inputSchema, input, step.Bindings, ""); missing != "" {
			return fmt.Errorf("plan: step %q required input %q is not provided by input or bindings", step.ID, missing)
		}
	}
	return nil
}

func findTool(tools []model.ToolSpec, capability string) (model.ToolSpec, bool) {
	for _, spec := range tools {
		if spec.Function.Name == capability {
			return spec, true
		}
		for _, candidate := range spec.Capabilities {
			if candidate == capability {
				return spec, true
			}
		}
	}
	return model.ToolSpec{}, false
}

func validateStaticInput(stepID string, input map[string]any, schema map[string]any) error {
	properties, _ := schema["properties"].(map[string]any)
	additional, restricted := schema["additionalProperties"].(bool)
	for key, value := range input {
		rawProperty, ok := properties[key]
		if !ok {
			if restricted && !additional {
				return fmt.Errorf("plan: step %q input property %q is not allowed", stepID, key)
			}
			continue
		}
		property, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		encoded, err := json.Marshal(property)
		if err != nil {
			return err
		}
		if err := tool.ValidateValue(stepID+"."+key, encoded, value); err != nil {
			return fmt.Errorf("plan: invalid static input for step %q: %w", stepID, err)
		}
	}
	return nil
}

func missingRequired(schema map[string]any, input map[string]any, bindings []InputBinding, base string) string {
	properties, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, item := range required {
		name, _ := item.(string)
		path := base + "/" + escapePointer(name)
		value, exists := input[name]
		bound := bindingCovers(bindings, path)
		if !exists && !bound {
			return path
		}
		property, _ := properties[name].(map[string]any)
		childInput, _ := value.(map[string]any)
		if !bound && property != nil {
			if missing := missingRequired(property, childInput, bindings, path); missing != "" {
				return missing
			}
		}
	}
	return ""
}

func bindingCovers(bindings []InputBinding, path string) bool {
	for _, binding := range bindings {
		target := normalizePointer(binding.TargetPath)
		if target == path || strings.HasPrefix(path, target+"/") {
			return true
		}
	}
	return false
}

func schemaAt(raw json.RawMessage, path string) (map[string]any, bool) {
	var current map[string]any
	if json.Unmarshal(raw, &current) != nil {
		return nil, false
	}
	for _, part := range strings.Split(strings.TrimPrefix(normalizePointer(path), "/"), "/") {
		properties, _ := current["properties"].(map[string]any)
		next, ok := properties[unescapePointer(part)].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func normalizePointer(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + strings.ReplaceAll(strings.TrimPrefix(path, "."), ".", "/")
}
func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
func unescapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~1", "/"), "~0", "~")
}
