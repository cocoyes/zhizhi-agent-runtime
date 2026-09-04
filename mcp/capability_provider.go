package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cocoyes/zhizhi-agent-runtime/model"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

// Capability describes one MCP server without exposing or loading its tools.
type Capability struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Selection is the result of capability routing. Tools contains tools from
// only the selected MCP servers.
type Selection struct {
	ProviderNames []string
	Tools         []tool.Tool
	Usage         model.Usage
}

// CapabilityProvider routes a request against a compact MCP capability
// catalog before connecting to any server and listing its tools.
type CapabilityProvider struct {
	providers map[string]*Provider
	byName    map[string]string
	configs   map[string]Config
	catalog   []Capability
}

// NewCapabilityProvider creates a lazy MCP capability catalog. It performs no
// network I/O; selected servers are connected by Select.
func NewCapabilityProvider(configs ...Config) (*CapabilityProvider, error) {
	result := &CapabilityProvider{
		providers: make(map[string]*Provider, len(configs)),
		byName:    make(map[string]string, len(configs)),
		configs:   make(map[string]Config, len(configs)),
		catalog:   make([]Capability, 0, len(configs)),
	}
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.Description) == "" {
			result.Close()
			return nil, fmt.Errorf("mcp %s: capability description is required", cfg.ID)
		}
		if cfg.Name == "" {
			cfg.Name = cfg.ID
		}
		if _, exists := result.providers[cfg.ID]; exists {
			result.Close()
			return nil, fmt.Errorf("mcp: duplicate server id %q", cfg.ID)
		}
		if _, exists := result.byName[cfg.Name]; exists {
			result.Close()
			return nil, fmt.Errorf("mcp: duplicate server name %q", cfg.Name)
		}
		provider, err := NewProvider(cfg)
		if err != nil {
			result.Close()
			return nil, err
		}
		result.providers[cfg.ID] = provider
		result.byName[cfg.Name] = cfg.ID
		result.configs[cfg.ID] = cfg
		result.catalog = append(result.catalog, Capability{Name: cfg.Name, Description: cfg.Description})
	}
	sort.Slice(result.catalog, func(i, j int) bool { return result.catalog[i].Name < result.catalog[j].Name })
	return result, nil
}

// Capabilities returns the deterministic compact catalog exposed during MCP
// selection. The returned slice is an immutable copy.
func (p *CapabilityProvider) Capabilities() []Capability {
	if p == nil {
		return nil
	}
	return append([]Capability(nil), p.catalog...)
}

// Select asks the model which MCP servers are relevant, then connects and
// lists tools only for those servers. A request may select multiple servers.
func (p *CapabilityProvider) Select(ctx context.Context, selector model.Model, request string) (Selection, error) {
	if p == nil {
		return Selection{}, fmt.Errorf("mcp capability provider is nil")
	}
	if selector == nil {
		return Selection{}, fmt.Errorf("mcp capability provider: selector model is required")
	}
	encoded, err := json.Marshal(p.catalog)
	if err != nil {
		return Selection{}, fmt.Errorf("mcp capability provider: encode catalog: %w", err)
	}
	input := model.ModelInput{
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: "Select every MCP capability needed to answer the request. Select none when no capability applies. Return only JSON in the form {\"providers\":[\"name\"]}. Provider names must come from the supplied catalog. A single request may require multiple providers."},
			{Role: model.RoleUser, Content: request + "\nMCP capability catalog (selection only; tools are not loaded yet):\n" + string(encoded)},
		},
		JSONMode: true,
	}
	out, err := selector.Generate(ctx, input)
	if err != nil {
		return Selection{}, fmt.Errorf("mcp capability provider: select: %w", err)
	}
	if out == nil {
		return Selection{}, fmt.Errorf("mcp capability provider: selector returned nil output")
	}
	var envelope struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal([]byte(cleanJSONObject(out.Text)), &envelope); err != nil {
		return Selection{}, fmt.Errorf("mcp capability provider: invalid selection: %w", err)
	}
	seen := make(map[string]struct{}, len(envelope.Providers))
	selection := Selection{Usage: out.Usage}
	for _, name := range envelope.Providers {
		id, ok := p.byName[name]
		if !ok {
			// Accept IDs too so configurations with a display name remain robust.
			_, ok = p.providers[name]
			id = name
		}
		if !ok {
			return Selection{}, fmt.Errorf("mcp capability provider: model selected unknown provider %q", name)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		provider := p.providers[id]
		if err := provider.Ensure(ctx); err != nil {
			if p.configs[id].Required {
				return Selection{}, err
			}
			continue
		}
		selection.ProviderNames = append(selection.ProviderNames, p.configs[id].Name)
		selection.Tools = append(selection.Tools, provider.Tools()...)
	}
	return selection, nil
}

func cleanJSONObject(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		value = strings.TrimPrefix(value, "```json")
		value = strings.TrimPrefix(value, "```")
		value = strings.TrimSuffix(strings.TrimSpace(value), "```")
	}
	return strings.TrimSpace(value)
}

// Close closes every MCP session opened by this capability provider.
func (p *CapabilityProvider) Close() error {
	if p == nil {
		return nil
	}
	var first error
	for _, provider := range p.providers {
		if err := provider.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
