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

	"github.com/zhizhi-ai/zhizhi-agent-runtime/contract"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
)

type Config struct {
	BaseURL, APIKey, Model string
	Timeout                time.Duration
	ExtraHeaders           map[string]string
	Client                 *http.Client
	Compatibility          string
	Capabilities           *model.ModelCapabilities
	MaxBodySize            int64
}
type Client struct {
	cfg        Config
	httpClient *http.Client
}

func New(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
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
		return *c.cfg.Capabilities
	}
	return model.ModelCapabilities{ToolCalling: true, ParallelToolCalls: true, Streaming: true, JSONMode: true}
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
	Model          string           `json:"model"`
	Messages       []model.Message  `json:"messages"`
	Tools          []model.ToolSpec `json:"tools,omitempty"`
	Stream         bool             `json:"stream,omitempty"`
	ResponseFormat *responseFormat  `json:"response_format,omitempty"`
}
type responseFormat struct {
	Type string `json:"type"`
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
	request := chatRequest{Model: c.cfg.Model, Messages: input.Messages, Tools: input.Tools, Stream: stream}
	if input.JSONMode && !stream {
		request.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
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
func (c *Client) Generate(ctx context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	wireInput, toolNames := wireModelInput(input)
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
			return &model.ModelOutput{Text: text, Usage: decoded.Usage}, nil
		}
		return nil, &Error{Code: "MODEL_RESPONSE_INVALID", Class: contract.ErrorRemote, Message: fmt.Sprintf("response has no choices: %s", compactBody(body))}
	}
	ch := decoded.Choices[0]
	for i := range ch.Message.ToolCalls {
		if original, ok := toolNames[ch.Message.ToolCalls[i].Function.Name]; ok {
			ch.Message.ToolCalls[i].Function.Name = original
		}
	}
	return &model.ModelOutput{Text: ch.Message.Content, ToolCalls: ch.Message.ToolCalls, FinishReason: model.FinishReason(ch.FinishReason), Usage: decoded.Usage}, nil
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
	wireInput, toolNames := wireModelInput(input)
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
			Content   string `json:"content"`
			ToolCalls []struct {
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
				ev.FinishReason = model.FinishReason(choice.FinishReason)
				for _, tc := range choice.Delta.ToolCalls {
					d := s.calls[tc.Index]
					d.Index = tc.Index
					d.ID += tc.ID
					name := tc.Function.Name
					if original, ok := s.toolNames[name]; ok {
						name = original
					}
					d.Name += name
					d.Arguments += tc.Function.Arguments
					s.calls[tc.Index] = d
					ev.ToolCallDeltas = append(ev.ToolCallDeltas, d)
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

func wireModelInput(input model.ModelInput) (model.ModelInput, map[string]string) {
	out := input
	out.Messages = append([]model.Message(nil), input.Messages...)
	out.Tools = append([]model.ToolSpec(nil), input.Tools...)
	toolNames := make(map[string]string, len(input.Tools))
	originalNames := make(map[string]string, len(input.Tools))
	used := make(map[string]struct{}, len(input.Tools))
	for i := range out.Tools {
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
	return out, toolNames
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
