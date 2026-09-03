// Package structuredoutput adapts LLM-produced JSON to typed Go values using
// maintained third-party repair and decoding libraries.
package structuredoutput

import (
	"encoding/json"
	"fmt"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/go-viper/mapstructure/v2"
	"github.com/invopop/jsonschema"
	jsonrepair "github.com/silaswei-io/jsonrepair-go"
)

// SchemaFor derives the JSON Schema supplied to a structured-output tool.
// This mirrors Eino's GoStruct2ParamsOneOf approach: the Go result type is the
// source of truth and nested definitions are inlined for provider portability.
func SchemaFor[T any]() ([]byte, error) {
	schema := (&jsonschema.Reflector{DoNotReference: true}).Reflect(new(T))
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode structured output schema: %w", err)
	}
	return encoded, nil
}

// ToolFor creates the single synthetic tool used to carry a typed model
// result. It is output-only metadata and is never registered for execution.
func ToolFor[T any](name, description string) (model.ToolSpec, error) {
	schema, err := SchemaFor[T]()
	if err != nil {
		return model.ToolSpec{}, err
	}
	return model.ToolSpec{Type: "function", Function: model.FunctionSpec{Name: name, Description: description, Parameters: schema}}, nil
}

// Candidate reads the arguments of the expected synthetic output tool. Text
// fallback is only accepted for models that do not advertise tool calling.
func Candidate(output *model.ModelOutput, toolName string, requireTool bool) (string, error) {
	if output == nil {
		return "", fmt.Errorf("structured output model returned nil")
	}
	for _, call := range output.ToolCalls {
		if call.Function.Name == toolName {
			return call.Function.Arguments, nil
		}
	}
	if requireTool {
		if len(output.ToolCalls) > 0 {
			return "", fmt.Errorf("structured output model called unexpected tool %q", output.ToolCalls[0].Function.Name)
		}
		return "", fmt.Errorf("structured output model did not call required tool %q", toolName)
	}
	if output.Text == "" {
		return "", fmt.Errorf("structured output model returned no content")
	}
	return output.Text, nil
}

// Decode extracts and repairs JSON from text. It first lets jsonrepair perform
// target-shape normalization, then falls back to weak typed decoding.
func Decode(text string, result any) error {
	directErr := jsonrepair.UnmarshalJSONFromText(text, result)
	if directErr == nil {
		return nil
	}
	return decodeMapStructure(text, result, false, directErr)
}

// DecodeStrict rejects unknown fields while retaining JSON repair and weak
// scalar conversion. Use it at boundaries where accepting a wrapper as a
// zero-value object could turn malformed output into a successful no-op.
func DecodeStrict(text string, result any) error {
	return decodeMapStructure(text, result, true, nil)
}

func decodeMapStructure(text string, result any, errorUnused bool, directErr error) error {
	var generic any
	if err := jsonrepair.UnmarshalJSONFromText(text, &generic); err != nil {
		return fmt.Errorf("repair JSON: %w", err)
	}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           result,
		TagName:          "json",
		WeaklyTypedInput: true,
		ZeroFields:       true,
		ErrorUnused:      errorUnused,
	})
	if err != nil {
		return fmt.Errorf("configure typed decoder: %w", err)
	}
	if err := decoder.Decode(generic); err != nil {
		if directErr != nil {
			return fmt.Errorf("decode repaired JSON: %w (direct decode: %v)", err, directErr)
		}
		return fmt.Errorf("decode repaired JSON: %w", err)
	}
	return nil
}
