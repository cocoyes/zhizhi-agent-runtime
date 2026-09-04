package zhizhi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"
	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type multiMCPModel struct {
	t     *testing.T
	calls int
}

func (m *multiMCPModel) ID() string { return "multi-mcp" }
func (m *multiMCPModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{}
}
func (m *multiMCPModel) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	m.calls++
	switch m.calls {
	case 1:
		if len(input.Tools) != 0 {
			m.t.Fatalf("selection call received tools: %+v", input.Tools)
		}
		return &model.ModelOutput{Text: `{"providers":["amap","baike"]}`}, nil
	case 2:
		if len(input.Tools) != 2 {
			m.t.Fatalf("agent call received %d tools, want 2", len(input.Tools))
		}
		return &model.ModelOutput{ToolCalls: []model.ToolCallRequest{
			mcpToolCall("weather-call", "amap.weather"),
			mcpToolCall("baike-call", "baike.lookup"),
		}}, nil
	default:
		return &model.ModelOutput{Text: "weather and biography combined"}, nil
	}
}
func (m *multiMCPModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func mcpToolCall(id, name string) model.ToolCallRequest {
	call := model.ToolCallRequest{ID: id, Type: "function"}
	call.Function.Name = name
	call.Function.Arguments = `{}`
	return call
}

func TestAgentCapabilityProviderExecutesMultipleSelectedMCPs(t *testing.T) {
	amap := capabilityFixture(t, "weather")
	baike := capabilityFixture(t, "lookup")
	readOnly := &runtimemcp.ToolPolicy{SideEffect: tool.SideEffectRead, Idempotency: tool.IdempotencySafe, Retryable: true, RiskLevel: tool.RiskLow, Confirmation: tool.ConfirmationNever}
	capabilities, err := runtimemcp.NewCapabilityProvider(
		runtimemcp.Config{ID: "amap", Description: "maps and weather", Endpoint: amap, DefaultToolPolicy: readOnly},
		runtimemcp.Config{ID: "baike", Description: "encyclopedia", Endpoint: baike, DefaultToolPolicy: readOnly},
	)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := New(WithModel(&multiMCPModel{t: t}), WithMCPCapabilityProvider(capabilities))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	mode := RunModeAgentSimple
	response, err := agent.Run(context.Background(), Request{Input: "biography and weather", Mode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text == "" || len(response.Evidence) != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func capabilityFixture(t *testing.T, toolName string) string {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: toolName, Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: toolName, Description: toolName, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: toolName}}}, nil, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}
