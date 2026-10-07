package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The fixture speaks Chat Completions, so tests cover the real model adapter
// and capability selection rather than calling MCP tools directly.
func modelFixture(t *testing.T, selected bool) string {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected model path: %s", r.URL.Path)
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		calls++
		message := map[string]any{"role": "assistant"}
		switch calls {
		case 1:
			if len(request.Tools) != 0 {
				t.Error("tools loaded before capability selection")
			}
			catalog, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(catalog), "parallel-search") {
				t.Error("missing capability catalog")
			}
			message["content"] = `{"providers":[]}`
			if selected {
				message["content"] = `{"providers":["parallel-search"]}`
			}
		case 2:
			if !selected {
				message["content"] = "Hello without web access."
				break
			}
			if len(request.Tools) != 2 {
				t.Errorf("got %d tools, want only search and fetch", len(request.Tools))
			}
			toolCalls := []any{}
			for _, spec := range request.Tools {
				name := spec.Function.Name
				var arguments string
				switch name {
				case "parallel-search_web_search":
					arguments = `{"objective":"Find official Go 1.25 release changes","search_queries":["Go 1.25 release notes"]}`
				case "parallel-search_web_fetch":
					arguments = `{"urls":["https://go.dev/doc/go1.25"],"objective":"Find the container-aware GOMAXPROCS change"}`
				default:
					t.Errorf("unexpected tool %s", name)
				}
				toolCalls = append(toolCalls, map[string]any{"id": name, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}})
			}
			message["tool_calls"] = toolCalls
		default:
			var evidence []string
			for _, msg := range request.Messages {
				if msg.Role == "tool" {
					evidence = append(evidence, msg.Content)
				}
			}
			if len(evidence) != 2 {
				t.Errorf("got %d tool results, want 2", len(evidence))
			}
			message["content"] = "Go 1.25 research from https://go.dev/doc/go1.25:\n" + strings.Join(evidence, "\n")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

type requestRecorder struct {
	base    http.RoundTripper
	mu      sync.Mutex
	methods []string
}

func (r *requestRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	// The model fixture uses the same default transport but is not an MCP request.
	if req.URL.Path == "/chat/completions" {
		return r.base.RoundTrip(req)
	}
	// Observational only: don't modify the shipped configuration or request.
	if req.Header.Get("Authorization") != "" {
		return nil, fmt.Errorf("unexpected MCP authorization")
	}
	if req.UserAgent() != "zhizhi-agent-runtime/"+zhizhi.Version {
		return nil, fmt.Errorf("unexpected MCP user-agent %q", req.UserAgent())
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &rpc) == nil {
			method := rpc.Method
			if rpc.Params.Name != "" {
				method += ":" + rpc.Params.Name
			}
			r.mu.Lock()
			r.methods = append(r.methods, method)
			r.mu.Unlock()
		}
	}
	return r.base.RoundTrip(req)
}
func recordRequests(t *testing.T) *requestRecorder {
	t.Helper()
	original := http.DefaultTransport
	recorder := &requestRecorder{base: original}
	http.DefaultTransport = recorder
	t.Cleanup(func() { http.DefaultTransport = original })
	return recorder
}
func testEnvironment(modelURL, mcpURL string) func(string) string {
	// No saved settings, dotenv loader, or inherited credentials are consulted.
	env := map[string]string{"LLM_BASE_URL": modelURL, "LLM_MODEL": "fixture", "PARALLEL_MCP_URL": mcpURL}
	return func(key string) string { return env[key] }
}
func localMCP(t *testing.T) string {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "parallel-fixture", Version: "1"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{"objective": map[string]any{"type": "string"}, "search_queries": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "urls": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}
	for _, name := range []string{"web_search", "web_fetch", "unexpected_write"} {
		sdk.AddTool(server, &sdk.Tool{Name: name, Description: name, InputSchema: schema}, func(_ context.Context, req *sdk.CallToolRequest, input map[string]any) (*sdk.CallToolResult, any, error) {
			if req.Params.Name == "unexpected_write" {
				t.Error("excluded tool called")
			}
			if input["objective"] == nil {
				t.Error("missing objective")
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "Go 1.25 makes GOMAXPROCS container-aware. Source: https://go.dev/doc/go1.25"}}}, nil, nil
		})
	}
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}
func TestResearch(t *testing.T) {
	recorder := recordRequests(t)
	var output bytes.Buffer
	err := run(context.Background(), testEnvironment(modelFixture(t, true), localMCP(t)), nil, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "GOMAXPROCS container-aware") {
		t.Fatalf("missing useful final answer: %s", output.String())
	}
	assertExecution(t, recorder)
}
func assertExecution(t *testing.T, r *requestRecorder) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, expected := range []string{"initialize", "tools/list", "tools/call:web_search", "tools/call:web_fetch"} {
		found := false
		for _, method := range r.methods {
			if method == expected {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s in %v", expected, r.methods)
		}
	}
	t.Logf("Observed anonymous MCP requests with project User-Agent: %v", r.methods)
}
func TestUnselectedCapabilityDoesNotConnect(t *testing.T) {
	recorder := recordRequests(t)
	var output bytes.Buffer
	if err := run(context.Background(), testEnvironment(modelFixture(t, false), "http://127.0.0.1:1"), []string{"Hello"}, &output); err != nil {
		t.Fatal(err)
	}
	if len(recorder.methods) != 0 {
		t.Fatalf("unselected MCP connected: %v", recorder.methods)
	}
	if !strings.Contains(output.String(), "Hello without web access") {
		t.Fatal(output.String())
	}
}
func TestConfigurationRequired(t *testing.T) {
	if err := run(context.Background(), func(string) string { return "" }, nil, io.Discard); err == nil {
		t.Fatal("missing model configuration accepted")
	}
}
func TestSelectedUnavailableServerFails(t *testing.T) {
	if err := run(context.Background(), testEnvironment(modelFixture(t, true), "http://127.0.0.1:1"), nil, io.Discard); err == nil {
		t.Fatal("selected unavailable server silently ignored")
	}
}
func TestLiveParallel(t *testing.T) {
	if os.Getenv("ZHIZHI_PARALLEL_LIVE") != "1" {
		t.Skip("set ZHIZHI_PARALLEL_LIVE=1 to call the public anonymous MCP endpoint")
	}
	recorder := recordRequests(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var output bytes.Buffer
	if err := run(ctx, testEnvironment(modelFixture(t, true), ""), nil, &output); err != nil {
		t.Fatal(err)
	}
	assertExecution(t, recorder)
	if !strings.Contains(output.String(), "GOMAXPROCS") {
		t.Fatalf("missing release evidence: %s", output.String())
	}
	t.Logf("Final answer from deterministic model fixture and live MCP evidence: %s", output.String())
}
