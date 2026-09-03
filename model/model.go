package model

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role             Role              `json:"role"`
	Content          string            `json:"content,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	Name             string            `json:"name,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCallRequest `json:"tool_calls,omitempty"`
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
	OutputSchema json.RawMessage `json:"-"`
}

type PlanningToolSpec struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

func PlanningToolCatalog(tools []ToolSpec) []PlanningToolSpec {
	catalog := make([]PlanningToolSpec, 0, len(tools))
	for _, value := range tools {
		catalog = append(catalog, PlanningToolSpec{
			Name:         value.Function.Name,
			Description:  value.Function.Description,
			Capabilities: append([]string(nil), value.Capabilities...),
			InputSchema:  value.Function.Parameters,
			OutputSchema: value.OutputSchema,
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

// ToolChoice controls whether a model may or must produce a tool call.
// ToolChoiceForced follows the OpenAI-compatible "required" wire value and
// matches Eino's forced-tool structured-output pattern when one tool is bound.
type ToolChoice string

const (
	ToolChoiceAuto      ToolChoice = "auto"
	ToolChoiceForbidden ToolChoice = "none"
	ToolChoiceForced    ToolChoice = "required"
)

type ModelInput struct {
	Messages   []Message
	Tools      []ToolSpec
	ToolChoice ToolChoice
	JSONMode   bool
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
	ToolCalling       bool
	ParallelToolCalls bool
	Streaming         bool
	JSONMode          bool
	MaxContextTokens  int
	MaxOutputTokens   int
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
