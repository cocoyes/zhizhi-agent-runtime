package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

type Config struct {
	BaseURL, APIKey, Model string
	Timeout                time.Duration
	ExtraHeaders           map[string]string
	Client                 *http.Client
	Compatibility          string
	// Thinking controls provider-specific hybrid reasoning switches used by
	// DeepSeek and Doubao ("enabled", "disabled", or "auto"). An empty value
	// defaults to ThinkingDisabled.
	Thinking string
	// ReasoningEffort is sent as the top-level reasoning_effort field. An empty
	// value defaults to "high".
	ReasoningEffort string
	Capabilities    *model.ModelCapabilities
	MaxBodySize     int64
}

const (
	ThinkingEnabled  = "enabled"
	ThinkingDisabled = "disabled"
	ThinkingAuto     = "auto"
)

type Client struct {
	cfg        Config
	httpClient *http.Client
}

func New(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Thinking == "" {
		cfg.Thinking = ThinkingDisabled
	}
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = "high"
	}
	c := cfg.Client
	if c == nil {
		c = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, httpClient: c}
}
func (c *Client) ID() string { return c.cfg.Model }
func (c *Client) Capabilities() model.ModelCapabilities {
	if c.cfg.Capabilities != nil {
		value := *c.cfg.Capabilities
		value.Declared = true
		return value
	}
	return model.ModelCapabilities{Declared: true, ToolCalling: true, ParallelToolCalls: true, Streaming: true, JSONMode: true, JSONSchema: true, Images: true}
}

type Error struct {
	StatusCode int
	Code       string
	Class      contract.ErrorClass
	Message    string
	Cause      error
}

func (e *Error) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("model %s (%d): %s", e.Code, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("model %s: %s", e.Code, e.Message)
}
func (e *Error) Unwrap() error { return e.Cause }
func statusError(status int, message string) *Error {
	class, code := contract.ErrorRemote, "MODEL_HTTP_FAILED"
	switch status {
	case 401:
		class, code = contract.ErrorUnauthorized, "MODEL_UNAUTHORIZED"
	case 403:
		class, code = contract.ErrorForbidden, "MODEL_FORBIDDEN"
	case 408:
		class, code = contract.ErrorTimeout, "MODEL_TIMEOUT"
	case 429:
		class, code = contract.ErrorRateLimit, "MODEL_RATE_LIMIT"
	case 500, 502, 503, 504:
		class, code = contract.ErrorUnavailable, "MODEL_UNAVAILABLE"
	}
	return &Error{StatusCode: status, Code: code, Class: class, Message: message}
}

type chatRequest struct {
	Model           string           `json:"model"`
	Messages        []wireMessage    `json:"messages"`
	Tools           []model.ToolSpec `json:"tools,omitempty"`
	Stream          bool             `json:"stream,omitempty"`
	ResponseFormat  *responseFormat  `json:"response_format,omitempty"`
	Thinking        *thinkingConfig  `json:"thinking,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	ToolChoice      any              `json:"tool_choice,omitempty"`
}
type wireMessage struct {
	Role             model.Role              `json:"role"`
	Content          any                     `json:"content,omitempty"`
	ReasoningContent string                  `json:"reasoning_content,omitempty"`
	Name             string                  `json:"name,omitempty"`
	ToolCallID       string                  `json:"tool_call_id,omitempty"`
	ToolCalls        []model.ToolCallRequest `json:"tool_calls,omitempty"`
}
type thinkingConfig struct {
	Type string `json:"type"`
}
type responseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *jsonSchemaFormat `json:"json_schema,omitempty"`
}
type jsonSchemaFormat struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	Strict      bool            `json:"strict,omitempty"`
}
type chatResponse struct {
	Choices []struct {
		Message      model.Message `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage      model.Usage `json:"usage"`
	OutputText string      `json:"output_text"`
	Output     []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (c *Client) endpoint() string {
	return strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
}
func (c *Client) request(ctx context.Context, input model.ModelInput, stream bool) (*http.Response, error) {
	messages, err := wireMessages(input.Messages)
	if err != nil {
		return nil, err
	}
	request := chatRequest{Model: c.cfg.Model, Messages: messages, Tools: input.Tools, Stream: stream, ReasoningEffort: c.cfg.ReasoningEffort, ToolChoice: wireToolChoice(input.ToolChoice)}
	if c.cfg.Thinking != "" {
		switch c.cfg.Thinking {
		case ThinkingEnabled, ThinkingDisabled, ThinkingAuto:
			request.Thinking = &thinkingConfig{Type: c.cfg.Thinking}
		default:
			return nil, fmt.Errorf("model: invalid thinking mode %q", c.cfg.Thinking)
		}
	}
	if input.ResponseFormat != nil {
		switch input.ResponseFormat.Type {
		case model.ResponseFormatText, "":
		case model.ResponseFormatJSONObject:
			request.ResponseFormat = &responseFormat{Type: string(model.ResponseFormatJSONObject)}
		case model.ResponseFormatJSONSchema:
			request.ResponseFormat = &responseFormat{Type: string(model.ResponseFormatJSONSchema), JSONSchema: &jsonSchemaFormat{Name: input.ResponseFormat.Name, Description: input.ResponseFormat.Description, Schema: input.ResponseFormat.Schema, Strict: input.ResponseFormat.Strict}}
		default:
			return nil, fmt.Errorf("model: unsupported response format %q", input.ResponseFormat.Type)
		}
	} else if input.JSONMode && !stream {
		request.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(input.Options) > 0 {
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			return nil, err
		}
		for key, value := range input.Options {
			if _, reserved := object[key]; reserved {
				return nil, fmt.Errorf("model option %q conflicts with a core request field", key)
			}
			object[key] = value
		}
		body, err = json.Marshal(object)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for k, v := range c.cfg.ExtraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, normalizeError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		limit := c.cfg.MaxBodySize
		if limit <= 0 {
			limit = 1 << 20
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
		err := statusError(resp.StatusCode, strings.TrimSpace(string(b)))
		return nil, err
	}
	return resp, nil
}

func wireToolChoice(choice model.ToolChoice) any {
	switch choice.Mode {
	case "":
		return nil
	case model.ToolChoiceModeAuto, model.ToolChoiceModeNone, model.ToolChoiceModeRequired:
		return string(choice.Mode)
	case model.ToolChoiceModeNamed:
		return map[string]any{"type": "function", "function": map[string]string{"name": choice.Name}}
	default:
		return string(choice.Mode)
	}
}

func wireMessages(messages []model.Message) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(messages))
	for _, message := range messages {
		wire := wireMessage{Role: message.Role, ReasoningContent: message.ReasoningContent, Name: message.Name, ToolCallID: message.ToolCallID, ToolCalls: message.ToolCalls}
		if len(message.Parts) == 0 {
			wire.Content = message.Content
			out = append(out, wire)
			continue
		}
		if message.Content != "" {
			return nil, fmt.Errorf("model message: content and parts cannot both be set")
		}
		parts := make([]any, 0, len(message.Parts))
		for _, part := range message.Parts {
			switch part.Type {
			case model.ContentText:
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			case model.ContentImageURL:
				image := map[string]any{"url": part.URL}
				if part.Detail != "" {
					image["detail"] = part.Detail
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": image})
			default:
				return nil, fmt.Errorf("openai-compatible adapter: content type %q is not supported", part.Type)
			}
		}
		wire.Content = parts
		out = append(out, wire)
	}
	return out, nil
}
func (c *Client) Generate(ctx context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	wireInput, toolNames, err := wireModelInput(input)
	if err != nil {
		return nil, err
	}
	resp, err := c.request(ctx, wireInput, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limit := c.cfg.MaxBodySize
	if limit <= 0 {
		limit = 8 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, &Error{Code: "MODEL_RESPONSE_INVALID", Class: contract.ErrorRemote, Message: "failed to read response body", Cause: err}
	}
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, &Error{Code: "MODEL_RESPONSE_INVALID", Class: contract.ErrorRemote, Message: "invalid JSON response", Cause: err}
	}
	if len(decoded.Choices) == 0 {
		text := decoded.OutputText
		if text == "" {
			var parts []string
			for _, item := range decoded.Output {
				for _, content := range item.Content {
					if content.Text != "" {
						parts = append(parts, content.Text)
					}
				}
			}
			text = strings.Join(parts, "")
		}
		if text != "" {
			out := &model.ModelOutput{Text: text, Usage: decoded.Usage}
			if err := model.ValidateOutput(input, out); err != nil {
				return nil, err
			}
			return out, nil
		}
		return nil, &Error{Code: "MODEL_RESPONSE_INVALID", Class: contract.ErrorRemote, Message: fmt.Sprintf("response has no choices: %s", compactBody(body))}
	}
	ch := decoded.Choices[0]
	for i := range ch.Message.ToolCalls {
		if original, ok := toolNames[ch.Message.ToolCalls[i].Function.Name]; ok {
			ch.Message.ToolCalls[i].Function.Name = original
		}
	}
	out := &model.ModelOutput{Text: ch.Message.Content, ReasoningContent: ch.Message.ReasoningContent, ToolCalls: ch.Message.ToolCalls, FinishReason: model.FinishReason(ch.FinishReason), Usage: decoded.Usage}
	if len(out.ToolCalls) == 0 {
		if err := model.ValidateOutput(input, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func compactBody(body []byte) string {
	const max = 2048
	text := strings.TrimSpace(string(body))
	if len(text) > max {
		return text[:max] + "..."
	}
	return text
}
func (c *Client) Stream(ctx context.Context, input model.ModelInput) (model.Stream, error) {
	wireInput, toolNames, err := wireModelInput(input)
	if err != nil {
		return nil, err
	}
	resp, err := c.request(ctx, wireInput, true)
	if err != nil {
		return nil, err
	}
	limit := c.cfg.MaxBodySize
	if limit <= 0 {
		limit = 16 << 20
	}
	return &stream{resp: resp, reader: bufio.NewReader(io.LimitReader(resp.Body, limit)), calls: map[int]model.ToolCallDelta{}, toolNames: toolNames}, nil
}

type stream struct {
	resp      *http.Response
	reader    *bufio.Reader
	calls     map[int]model.ToolCallDelta
	toolNames map[string]string
	done      bool
}
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *model.Usage `json:"usage,omitempty"`
}

func (s *stream) Recv(ctx context.Context) (model.StreamEvent, error) {
	if s.done {
		return model.StreamEvent{}, io.EOF
	}
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return model.StreamEvent{}, err
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				s.done = true
				return model.StreamEvent{}, io.EOF
			}
			var ch streamChunk
			if e := json.Unmarshal([]byte(data), &ch); e != nil {
				return model.StreamEvent{}, fmt.Errorf("decode stream chunk: %w", e)
			}
			ev := model.StreamEvent{Usage: ch.Usage}
			if len(ch.Choices) > 0 {
				choice := ch.Choices[0]
				ev.TextDelta = choice.Delta.Content
				ev.ReasoningDelta = choice.Delta.ReasoningContent
				ev.FinishReason = model.FinishReason(choice.FinishReason)
				for _, tc := range choice.Delta.ToolCalls {
					accumulated := s.calls[tc.Index]
					accumulated.Index = tc.Index
					accumulated.ID += tc.ID
					accumulated.Name += tc.Function.Name
					accumulated.Arguments += tc.Function.Arguments
					s.calls[tc.Index] = accumulated
					name := accumulated.Name
					if original, ok := s.toolNames[name]; ok {
						name = original
					}
					ev.ToolCallDeltas = append(ev.ToolCallDeltas, model.ToolCallDelta{Index: tc.Index, ID: accumulated.ID, Name: name, Arguments: tc.Function.Arguments})
				}
			}
			return ev, nil
		}
		if errors.Is(err, io.EOF) {
			s.done = true
			return model.StreamEvent{}, io.EOF
		}
	}
}
func (s *stream) Close() error { s.done = true; return s.resp.Body.Close() }

func wireModelInput(input model.ModelInput) (model.ModelInput, map[string]string, error) {
	out := input
	out.Messages = append([]model.Message(nil), input.Messages...)
	out.Tools = append([]model.ToolSpec(nil), input.Tools...)
	toolNames := make(map[string]string, len(input.Tools))
	originalNames := make(map[string]string, len(input.Tools))
	used := make(map[string]struct{}, len(input.Tools))
	for i := range out.Tools {
		parameters, err := normalizeFunctionParameters(out.Tools[i].Function.Name, out.Tools[i].Function.Parameters)
		if err != nil {
			return model.ModelInput{}, nil, err
		}
		out.Tools[i].Function.Parameters = parameters
		original := out.Tools[i].Function.Name
		wire := wireToolName(original)
		if _, exists := used[wire]; exists {
			for suffix := 2; ; suffix++ {
				candidate := fmt.Sprintf("%s_%d", wire, suffix)
				if _, exists := used[candidate]; !exists {
					wire = candidate
					break
				}
			}
		}
		used[wire] = struct{}{}
		out.Tools[i].Function.Name = wire
		toolNames[wire] = original
		originalNames[original] = wire
	}
	for i := range out.Messages {
		if len(input.Messages[i].ToolCalls) > 0 {
			out.Messages[i].ToolCalls = append([]model.ToolCallRequest(nil), input.Messages[i].ToolCalls...)
			for j := range out.Messages[i].ToolCalls {
				name := out.Messages[i].ToolCalls[j].Function.Name
				if wire, ok := originalNames[name]; ok {
					out.Messages[i].ToolCalls[j].Function.Name = wire
				}
			}
		}
	}
	if out.ToolChoice.Mode == model.ToolChoiceModeNamed {
		if wire, ok := originalNames[out.ToolChoice.Name]; ok {
			out.ToolChoice.Name = wire
		}
	}
	return out, toolNames, nil
}

func normalizeFunctionParameters(name string, raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("model tool %s: parameters must be a JSON object: %w", name, err)
	}
	if refRaw, ok := root["$ref"]; ok {
		var ref string
		if err := json.Unmarshal(refRaw, &ref); err != nil || !strings.HasPrefix(ref, "#/$defs/") {
			return nil, fmt.Errorf("model tool %s: unsupported root schema reference %q", name, ref)
		}
		var defs map[string]json.RawMessage
		if err := json.Unmarshal(root["$defs"], &defs); err != nil {
			return nil, fmt.Errorf("model tool %s: root schema reference has no usable $defs", name)
		}
		definition, ok := defs[strings.TrimPrefix(ref, "#/$defs/")]
		if !ok {
			return nil, fmt.Errorf("model tool %s: unresolved root schema reference %q", name, ref)
		}
		var expanded map[string]json.RawMessage
		if err := json.Unmarshal(definition, &expanded); err != nil {
			return nil, fmt.Errorf("model tool %s: referenced root schema is not an object", name)
		}
		for key, value := range root {
			if key != "$ref" {
				expanded[key] = value
			}
		}
		root = expanded
	}
	var schemaType string
	if typeRaw, ok := root["type"]; ok {
		if err := json.Unmarshal(typeRaw, &schemaType); err != nil {
			return nil, fmt.Errorf("model tool %s: parameters type must be \"object\"", name)
		}
	} else if _, hasProperties := root["properties"]; hasProperties || len(root) == 0 {
		schemaType = "object"
		root["type"] = json.RawMessage(`"object"`)
	}
	if schemaType != "object" {
		return nil, fmt.Errorf("model tool %s: parameters root type must be \"object\", got %q", name, schemaType)
	}
	if _, ok := root["properties"]; !ok {
		root["properties"] = json.RawMessage(`{}`)
	}
	normalized, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("model tool %s: encode normalized parameters: %w", name, err)
	}
	return normalized, nil
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
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}
func normalizeError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}
