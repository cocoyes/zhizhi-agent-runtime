package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/contract"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

func TestGenerate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`)
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "secret", Model: "test"})
	out, err := c.Generate(context.Background(), ModelInputForTest("hi"))
	if err != nil || out.Text != "hello" || out.Usage.TotalTokens != 3 {
		t.Fatalf("unexpected output: %+v, %v", out, err)
	}
}

func TestGenerateAcceptsResponsesText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"hello from responses"}]}]}`)
	}))
	defer srv.Close()
	out, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), ModelInputForTest("hi"))
	if err != nil || out.Text != "hello from responses" {
		t.Fatalf("unexpected responses output: %+v, %v", out, err)
	}
}

func TestGenerateSanitizesAndRestoresToolNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Tools) != 1 || request.Tools[0].Function.Name != "activity_cached" {
			t.Fatalf("tool name was not sanitized: %+v err=%v", request, err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"activity_cached","arguments":"{}"}}]}}]}`)
	}))
	defer srv.Close()
	input := ModelInputForTest("choose")
	input.Tools = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "activity.cached", Parameters: []byte(`{}`)}}}
	out, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), input)
	if err != nil || len(out.ToolCalls) != 1 || out.ToolCalls[0].Function.Name != "activity.cached" {
		t.Fatalf("tool name was not restored: %+v err=%v", out, err)
	}
}

func TestGenerateNormalizesRootRefAndSendsThinkingOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Thinking struct {
				Type string `json:"type"`
			} `json:"thinking"`
			ReasoningEffort string           `json:"reasoning_effort"`
			Tools           []model.ToolSpec `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Thinking.Type != ThinkingEnabled || request.ReasoningEffort != "high" {
			t.Fatalf("thinking options missing from wire request: %+v", request)
		}
		var schema map[string]any
		if err := json.Unmarshal(request.Tools[0].Function.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["type"] != "object" || schema["properties"] == nil {
			t.Fatalf("root schema was not expanded into an object: %v", schema)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done","reasoning_content":"private reasoning"}}]}`)
	}))
	defer srv.Close()
	input := ModelInputForTest("choose")
	input.Tools = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{
		Name:       "activity.cached",
		Parameters: json.RawMessage(`{"$ref":"#/$defs/Input","$defs":{"Input":{"type":"object","properties":{"temperature":{"type":"integer"}}}}}`),
	}}}
	out, err := New(Config{BaseURL: srv.URL, Model: "test", Thinking: ThinkingEnabled, ReasoningEffort: "high"}).Generate(context.Background(), input)
	if err != nil || out.Text != "done" || out.ReasoningContent != "private reasoning" {
		t.Fatalf("unexpected output: %+v err=%v", out, err)
	}
}

func TestGenerateSendsThinkingDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Thinking struct {
				Type string `json:"type"`
			} `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Thinking.Type != ThinkingDisabled {
			t.Fatalf("thinking disabled missing from wire request: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer srv.Close()
	if _, err := New(Config{BaseURL: srv.URL, Model: "test", Thinking: ThinkingDisabled}).Generate(context.Background(), ModelInputForTest("choose")); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateUsesDefaultThinkingOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Thinking struct {
				Type string `json:"type"`
			} `json:"thinking"`
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Thinking.Type != ThinkingDisabled || request.ReasoningEffort != "high" {
			t.Fatalf("default thinking options missing from wire request: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer srv.Close()
	if _, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), ModelInputForTest("choose")); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateSendsForcedToolChoice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ToolChoice model.ToolChoice `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ToolChoice != model.ToolChoiceForced {
			t.Fatalf("forced tool choice missing from wire request: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer srv.Close()
	input := ModelInputForTest("choose")
	input.ToolChoice = model.ToolChoiceForced
	if _, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRejectsNonObjectToolSchemaBeforeHTTP(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	input := ModelInputForTest("choose")
	input.Tools = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "bad", Parameters: json.RawMessage(`{"type":"string"}`)}}}
	_, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), `root type must be "object"`) || called {
		t.Fatalf("expected local schema rejection, called=%v err=%v", called, err)
	}
}

func TestGenerateSanitizesToolNamesInConversationHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 2 || len(request.Messages[1].ToolCalls) != 1 || request.Messages[1].ToolCalls[0].Function.Name != "weather_get" {
			t.Fatalf("history tool name was not sanitized: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	}))
	defer srv.Close()
	call := model.ToolCallRequest{ID: "call-1", Type: "function", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "weather.get", Arguments: `{}`}}
	input := model.ModelInput{
		Messages: []model.Message{{Role: model.RoleUser, Content: "weather"}, {Role: model.RoleAssistant, ToolCalls: []model.ToolCallRequest{call}}},
		Tools:    []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "weather.get", Parameters: []byte(`{}`)}}},
	}
	out, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), input)
	if err != nil || out.Text != "done" {
		t.Fatalf("unexpected history output: %+v err=%v", out, err)
	}
}

func TestGenerateIncludesBodyWhenChoicesAreMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":{"message":"invalid model"}}`)
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), ModelInputForTest("hi"))
	if err == nil || !strings.Contains(err.Error(), "invalid model") {
		t.Fatalf("expected response body in error, got %v", err)
	}
}

func TestStreamAssemblesToolDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"need weather","tool_calls":[{"index":0,"id":"call_","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"1","function":{"arguments":"\"Shenzhen\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, Model: "test"})
	s, err := c.Stream(context.Background(), ModelInputForTest("hi"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.ReasoningDelta != "need weather" || len(first.ToolCallDeltas) != 1 || first.ToolCallDeltas[0].Name != "weather" {
		t.Fatalf("unexpected first delta: %+v", first)
	}
	second, err := s.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.ToolCallDeltas[0].ID != "call_1" || second.ToolCallDeltas[0].Arguments == "" {
		t.Fatalf("unexpected second delta: %+v", second)
	}
	_, err = s.Recv(context.Background())
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestHTTPErrorIsNormalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"slow down"}`)
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL, Model: "test"}).Generate(context.Background(), ModelInputForTest("hi"))
	modelErr, ok := err.(*Error)
	if !ok || modelErr.Class != contract.ErrorRateLimit || modelErr.Code != "MODEL_RATE_LIMIT" {
		t.Fatalf("unexpected normalized error: %T %+v", err, err)
	}
}

func TestExplicitCapabilitiesOverrideDefaults(t *testing.T) {
	c := New(Config{Model: "test", Capabilities: &model.ModelCapabilities{Streaming: false}})
	if c.Capabilities().Streaming {
		t.Fatal("explicit capabilities were ignored")
	}
}

// ModelInputForTest keeps wire tests focused on the adapter package.
func ModelInputForTest(text string) model.ModelInput {
	return model.ModelInput{Messages: []model.Message{{Role: model.RoleUser, Content: text}}}
}
