package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
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
		} else {
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
		if step.Condition != nil {
			sourceStep := steps[step.Condition.SourceStep]
			sourceSpec, found := findTool(tools, sourceStep.Capability)
			if !found {
				return fmt.Errorf("plan: condition source step %q has no tool", step.Condition.SourceStep)
			}
			node, ok := schemaAt(sourceSpec.OutputSchema, step.Condition.SourcePath)
			if !ok {
				return fmt.Errorf("plan: step %q condition source path %q is absent from step %q output schema", step.ID, step.Condition.SourcePath, step.Condition.SourceStep)
			}
			conditionValue := step.Condition.Equals
			if step.Condition.NotEquals != nil {
				conditionValue = step.Condition.NotEquals
			}
			if schemaType, _ := node["type"].(string); schemaType != "" && !conditionTypeMatches(schemaType, conditionValue) {
				return fmt.Errorf("plan: step %q condition value type does not match source path %q type %s", step.ID, step.Condition.SourcePath, schemaType)
			}
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
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var current map[string]any
	if json.Unmarshal(raw, &current) != nil {
		return nil, false
	}
	for _, part := range strings.Split(strings.TrimPrefix(normalizePointer(path), "/"), "/") {
		var next map[string]any
		var ok bool
		if current["type"] == "array" {
			_, numeric := parseArrayIndex(part)
			if !numeric {
				return nil, false
			}
			next, ok = current["items"].(map[string]any)
		} else {
			properties, _ := current["properties"].(map[string]any)
			next, ok = properties[unescapePointer(part)].(map[string]any)
		}
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func parseArrayIndex(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func conditionTypeMatches(schemaType string, value any) bool {
	switch schemaType {
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number", "integer":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			return true
		}
		return false
	case "array":
		v := reflect.ValueOf(value)
		return v.IsValid() && (v.Kind() == reflect.Array || v.Kind() == reflect.Slice)
	case "object":
		v := reflect.ValueOf(value)
		return v.IsValid() && v.Kind() == reflect.Map
	case "null":
		return value == nil
	default:
		return true
	}
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
