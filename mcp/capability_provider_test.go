package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
)

type selectionModel struct {
	output string
	input  model.ModelInput
}

func (m *selectionModel) ID() string { return "selection-test" }
func (m *selectionModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{}
}
func (m *selectionModel) Generate(_ context.Context, input model.ModelInput) (*model.ModelOutput, error) {
	m.input = input
	return &model.ModelOutput{Text: m.output}, nil
}
func (m *selectionModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	panic("not used")
}

func TestCapabilityProviderSelectsMultipleWithoutLoadingOthers(t *testing.T) {
	amap := startFixtureServer(t, "internal_amap_weather")
	baike := startFixtureServer(t, "internal_baike_entry")
	unused := startFixtureServer(t, "internal_unused_tool")
	provider, err := NewCapabilityProvider(
		Config{ID: "amap", Description: "maps and weather", Endpoint: amap},
		Config{ID: "baike", Name: "baike-mcp-server", Description: "encyclopedia", Endpoint: baike},
		Config{ID: "unused", Description: "unrelated", Endpoint: unused},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	selector := &selectionModel{output: `{"providers":["baike-mcp-server","amap"]}`}
	selection, err := provider.Select(context.Background(), selector, "background and weather")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Tools) != 2 || !provider.providers["amap"].Healthy() || !provider.providers["baike"].Healthy() {
		t.Fatalf("unexpected selection: names=%v tools=%d", selection.ProviderNames, len(selection.Tools))
	}
	if provider.providers["unused"].Healthy() {
		t.Fatal("unselected provider was connected")
	}
	prompt := selector.input.Messages[1].Content
	if strings.Contains(prompt, "internal_amap_weather") || strings.Contains(prompt, "internal_baike_entry") || strings.Contains(prompt, "internal_unused_tool") {
		t.Fatalf("selector prompt leaked MCP tool names: %s", prompt)
	}
}

func TestDefaultToolPolicyAppliesToDiscoveredTools(t *testing.T) {
	readOnly := &ToolPolicy{SideEffect: "read", Idempotency: "safe", Retryable: true, RiskLevel: "low", Confirmation: "never"}
	provider, err := NewCapabilityProvider(Config{ID: "safe", Description: "safe lookup", Endpoint: startFixtureServer(t, "lookup"), DefaultToolPolicy: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	selection, err := provider.Select(context.Background(), &selectionModel{output: `{"providers":["safe"]}`}, "lookup")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Tools) != 1 || selection.Tools[0].Spec().RequiresConfirmation() || !selection.Tools[0].Spec().Retryable {
		t.Fatalf("default policy was not applied: %+v", selection.Tools)
	}
}
