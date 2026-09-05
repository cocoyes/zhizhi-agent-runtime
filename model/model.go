package model

import (
	"context"
	"encoding/json"
	"fmt"

	jsonschema6 "github.com/santhosh-tekuri/jsonschema/v6"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentType string

const (
	ContentText     ContentType = "text"
	ContentImageURL ContentType = "image_url"
	ContentAudioURL ContentType = "audio_url"
	ContentFileURL  ContentType = "file_url"
)

type ContentPart struct {
	Type     ContentType `json:"type"`
	Text     string      `json:"text,omitempty"`
	URL      string      `json:"url,omitempty"`
	MIMEType string      `json:"mime_type,omitempty"`
	Detail   string      `json:"detail,omitempty"`
}

func ValidateOutput(input ModelInput, output *ModelOutput) error {
	if output == nil {
		return fmt.Errorf("model returned nil output")
	}
	format := input.ResponseFormat
	if format == nil && input.JSONMode {
		format = &ResponseFormat{Type: ResponseFormatJSONObject}
	}
	if format == nil || format.Type == ResponseFormatText || format.Type == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(output.Text), &value); err != nil {
		return fmt.Errorf("model output is not valid JSON: %w", err)
	}
	if format.Type != ResponseFormatJSONSchema {
		return nil
	}
	var schema any
	if err := json.Unmarshal(format.Schema, &schema); err != nil {
		return fmt.Errorf("model response schema: %w", err)
	}
	compiler := jsonschema6.NewCompiler()
	if err := compiler.AddResource("mem://response-schema.json", schema); err != nil {
		return err
	}
	compiled, err := compiler.Compile("mem://response-schema.json")
	if err != nil {
		return err
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("model output violates response schema: %w", err)
	}
	return nil
}

type Message struct {
	Role             Role              `json:"role"`
	Content          string            `json:"content,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	Name             string            `json:"name,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCallRequest `json:"tool_calls,omitempty"`
	// Parts carries multimodal content. Content remains the text convenience
	// field for source compatibility and must not be set together with Parts.
	Parts        []ContentPart `json:"content_parts,omitempty"`
	ResponseMeta *ResponseMeta `json:"response_meta,omitempty"`
}

type ResponseMeta struct {
	FinishReason FinishReason   `json:"finish_reason,omitempty"`
	Usage        Usage          `json:"usage,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func TextMessage(role Role, text string) Message { return Message{Role: role, Content: text} }
func UserMessage(parts ...ContentPart) Message {
	return Message{Role: RoleUser, Parts: append([]ContentPart(nil), parts...)}
}
func AssistantMessage(text string) Message { return TextMessage(RoleAssistant, text) }
func SystemMessage(text string) Message    { return TextMessage(RoleSystem, text) }

func TextPart(text string) ContentPart { return ContentPart{Type: ContentText, Text: text} }
func ImageURLPart(url, detail string) ContentPart {
	return ContentPart{Type: ContentImageURL, URL: url, Detail: detail}
}

type ToolCallRequest struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ToolSpec struct {
	Type         string       `json:"type"`
	Function     FunctionSpec `json:"function"`
	Capabilities []string     `json:"-"`
	// OutputSchema is runtime planning metadata and is never serialized into
	// the provider's function-tool wire object.
	OutputSchema  json.RawMessage `json:"-"`
	SemanticGroup string          `json:"-"`
	Fallbacks     []string        `json:"-"`
	SideEffect    string          `json:"-"`
	Idempotency   string          `json:"-"`
}

type PlanningToolSpec struct {
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Capabilities  []string        `json:"capabilities,omitempty"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema  json.RawMessage `json:"output_schema,omitempty"`
	SemanticGroup string          `json:"semantic_group,omitempty"`
	Fallbacks     []string        `json:"fallbacks,omitempty"`
	SideEffect    string          `json:"side_effect,omitempty"`
	Idempotency   string          `json:"idempotency,omitempty"`
}

func PlanningToolCatalog(tools []ToolSpec) []PlanningToolSpec {
	catalog := make([]PlanningToolSpec, 0, len(tools))
	for _, value := range tools {
		catalog = append(catalog, PlanningToolSpec{
			Name:          value.Function.Name,
			Description:   value.Function.Description,
			Capabilities:  append([]string(nil), value.Capabilities...),
			InputSchema:   value.Function.Parameters,
			OutputSchema:  value.OutputSchema,
			SemanticGroup: value.SemanticGroup,
			Fallbacks:     append([]string(nil), value.Fallbacks...),
			SideEffect:    value.SideEffect,
			Idempotency:   value.Idempotency,
		})
	}
	return catalog
}

type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type FinishReason string

type ToolChoiceMode string

const (
	ToolChoiceModeAuto     ToolChoiceMode = "auto"
	ToolChoiceModeNone     ToolChoiceMode = "none"
	ToolChoiceModeRequired ToolChoiceMode = "required"
	ToolChoiceModeNamed    ToolChoiceMode = "named"
)

type ToolChoice struct {
	Mode ToolChoiceMode `json:"mode"`
	Name string         `json:"name,omitempty"`
}

var (
	ToolChoiceAuto      = ToolChoice{Mode: ToolChoiceModeAuto}
	ToolChoiceForbidden = ToolChoice{Mode: ToolChoiceModeNone}
	ToolChoiceForced    = ToolChoice{Mode: ToolChoiceModeRequired}
)

func NamedTool(name string) ToolChoice { return ToolChoice{Mode: ToolChoiceModeNamed, Name: name} }

func (c ToolChoice) MarshalJSON() ([]byte, error) {
	if c.Mode == ToolChoiceModeNamed {
		return json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": c.Name}})
	}
	return json.Marshal(c.Mode)
}

func (c *ToolChoice) UnmarshalJSON(data []byte) error {
	var mode ToolChoiceMode
	if err := json.Unmarshal(data, &mode); err == nil {
		c.Mode, c.Name = mode, ""
		return nil
	}
	var named struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &named); err != nil {
		return err
	}
	if named.Type != "function" || named.Function.Name == "" {
		return fmt.Errorf("model tool choice: invalid named tool")
	}
	c.Mode, c.Name = ToolChoiceModeNamed, named.Function.Name
	return nil
}

type ResponseFormatType string

const (
	ResponseFormatText       ResponseFormatType = "text"
	ResponseFormatJSONObject ResponseFormatType = "json_object"
	ResponseFormatJSONSchema ResponseFormatType = "json_schema"
)

type ResponseFormat struct {
	Type        ResponseFormatType `json:"type"`
	Name        string             `json:"name,omitempty"`
	Description string             `json:"description,omitempty"`
	Schema      json.RawMessage    `json:"schema,omitempty"`
	Strict      bool               `json:"strict,omitempty"`
}

type ModelInput struct {
	Messages       []Message       `json:"messages"`
	Tools          []ToolSpec      `json:"tools,omitempty"`
	ToolChoice     ToolChoice      `json:"tool_choice,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Options        map[string]any  `json:"options,omitempty"`
	JSONMode       bool            `json:"json_mode,omitempty"`
}

type ModelOutput struct {
	Text             string
	ReasoningContent string
	ToolCalls        []ToolCallRequest
	FinishReason     FinishReason
	Usage            Usage
	RawMetadata      map[string]any
}

type ModelCapabilities struct {
	// Declared distinguishes a deliberately empty capability set from legacy
	// adapters which predate capability negotiation.
	Declared          bool
	ToolCalling       bool
	ParallelToolCalls bool
	Streaming         bool
	JSONMode          bool
	JSONSchema        bool
	Images            bool
	Audio             bool
	Files             bool
	MaxContextTokens  int
	MaxOutputTokens   int
}

// ValidateCapabilities rejects unsupported requests before provider I/O. A
// legacy adapter with Declared=false is treated as unknown for compatibility.
func ValidateCapabilities(capabilities ModelCapabilities, input ModelInput, streaming bool) error {
	for _, message := range input.Messages {
		if message.Content != "" && len(message.Parts) > 0 {
			return fmt.Errorf("model message: content and parts cannot both be set")
		}
		for _, part := range message.Parts {
			switch part.Type {
			case ContentText:
			case ContentImageURL:
				if !capabilities.Images {
					return fmt.Errorf("model capability: image content is not supported")
				}
			case ContentAudioURL:
				if !capabilities.Audio {
					return fmt.Errorf("model capability: audio content is not supported")
				}
			case ContentFileURL:
				if !capabilities.Files {
					return fmt.Errorf("model capability: file content is not supported")
				}
			default:
				return fmt.Errorf("model message: unsupported content type %q", part.Type)
			}
		}
	}
	if input.ToolChoice.Mode == ToolChoiceModeNamed {
		if input.ToolChoice.Name == "" {
			return fmt.Errorf("model tool choice: named tool requires a name")
		}
		found := false
		for _, value := range input.Tools {
			if value.Function.Name == input.ToolChoice.Name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("model tool choice: named tool %q is not present", input.ToolChoice.Name)
		}
	}
	if input.ResponseFormat != nil && input.ResponseFormat.Type == ResponseFormatJSONSchema {
		if input.ResponseFormat.Name == "" {
			return fmt.Errorf("model response format: JSON Schema requires a name")
		}
		var schema any
		if len(input.ResponseFormat.Schema) == 0 || json.Unmarshal(input.ResponseFormat.Schema, &schema) != nil {
			return fmt.Errorf("model response format: JSON Schema must be valid JSON")
		}
	}
	if !capabilities.Declared {
		return nil
	}
	if len(input.Tools) > 0 && !capabilities.ToolCalling {
		return fmt.Errorf("model capability: tool calling is not supported")
	}
	if input.JSONMode && !capabilities.JSONMode {
		return fmt.Errorf("model capability: JSON mode is not supported")
	}
	if input.ResponseFormat != nil {
		switch input.ResponseFormat.Type {
		case ResponseFormatText, "":
		case ResponseFormatJSONObject:
			if !capabilities.JSONMode {
				return fmt.Errorf("model capability: JSON object mode is not supported")
			}
		case ResponseFormatJSONSchema:
			if !capabilities.JSONSchema {
				return fmt.Errorf("model capability: JSON Schema is not supported")
			}
		default:
			return fmt.Errorf("model response format: unsupported type %q", input.ResponseFormat.Type)
		}
	}
	if streaming && !capabilities.Streaming {
		return fmt.Errorf("model capability: streaming is not supported")
	}
	return nil
}

type StreamEvent struct {
	TextDelta      string
	ReasoningDelta string
	ToolCallDeltas []ToolCallDelta
	FinishReason   FinishReason
	Usage          *Usage
}

type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

type Stream interface {
	Recv(context.Context) (StreamEvent, error)
	Close() error
}

type Model interface {
	ID() string
	Capabilities() ModelCapabilities
	Generate(context.Context, ModelInput) (*ModelOutput, error)
	Stream(context.Context, ModelInput) (Stream, error)
}
