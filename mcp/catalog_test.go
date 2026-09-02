package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
	"testing"
)

func TestCatalogFingerprintChangesWithTools(t *testing.T) {
	p := &Provider{cfg: Config{ID: "p"}}
	p.tools = []tool.Tool{p.normalize(&sdk.Tool{Name: "a", Description: "one", InputSchema: map[string]any{"type": "object"}})}
	first := p.Fingerprint()
	p.tools = append(p.tools, p.normalize(&sdk.Tool{Name: "b", Description: "two", InputSchema: map[string]any{"type": "object"}}))
	if first == p.Fingerprint() {
		t.Fatal("fingerprint did not change")
	}
}
