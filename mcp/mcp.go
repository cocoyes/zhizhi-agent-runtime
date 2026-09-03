package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/contract"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/security"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

type Transport string

const TransportStreamableHTTP Transport = "streamable-http"

type Config struct {
	ID                 string
	Transport          Transport
	Endpoint           string
	AllowTools         []string
	DenyTools          []string
	CapabilityMappings map[string][]string
	ToolPolicies       map[string]ToolPolicy
	Headers            map[string]string
	ConnectTimeout     time.Duration
}

// ToolPolicy is the application-owned side-effect declaration for an MCP
// tool. MCP metadata is untrusted, so undeclared tools remain unknown and
// require confirmation.
type ToolPolicy struct {
	SideEffect   tool.SideEffectClass
	Idempotency  tool.Idempotency
	Retryable    bool
	RiskLevel    tool.RiskLevel
	Confirmation tool.ConfirmationPolicy
}

type Provider struct {
	cfg     Config
	session *sdk.ClientSession
	tools   []tool.Tool
}
type Catalog struct {
	ProviderID  string
	Fingerprint string
	LoadedAt    time.Time
	Tools       []tool.Tool
}

func Connect(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("mcp: server id is required")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("mcp: endpoint is required")
	}
	if cfg.Transport == "" {
		cfg.Transport = TransportStreamableHTTP
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	httpClient := &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: cfg.Headers}}
	client := sdk.NewClient(&sdk.Implementation{Name: "zhizhi-agent-runtime", Version: "1.0.0"}, nil)
	transport := &sdk.StreamableClientTransport{Endpoint: cfg.Endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp %s connect: %w", cfg.ID, err)
	}
	provider := &Provider{cfg: cfg, session: session}
	if err := provider.refresh(connectCtx); err != nil {
		session.Close()
		return nil, err
	}
	return provider, nil
}

func (p *Provider) refresh(ctx context.Context) error {
	result, err := p.session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("mcp %s list tools: %w", p.cfg.ID, err)
	}
	p.tools = make([]tool.Tool, 0, len(result.Tools))
	for _, definition := range result.Tools {
		if definition == nil || !p.allowed(definition.Name) {
			continue
		}
		if security.ValidateToolText(definition.Description) != nil {
			continue
		}
		p.tools = append(p.tools, p.normalize(definition))
	}
	sort.Slice(p.tools, func(i, j int) bool { return p.tools[i].Spec().ID < p.tools[j].Spec().ID })
	return nil
}
func (p *Provider) Tools() []tool.Tool { return append([]tool.Tool(nil), p.tools...) }
func (p *Provider) Catalog() Catalog {
	return Catalog{ProviderID: p.cfg.ID, Fingerprint: p.Fingerprint(), LoadedAt: time.Now().UTC(), Tools: p.Tools()}
}
func (p *Provider) Fingerprint() string {
	h := sha256.New()
	for _, value := range p.tools {
		spec := value.Spec()
		_, _ = h.Write([]byte(spec.ID + "\x00" + spec.Description + "\x00" + string(spec.InputSchema)))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
func (p *Provider) Refresh(ctx context.Context) error { return p.refresh(ctx) }
func (p *Provider) Healthy() bool                     { return p != nil && p.session != nil }
func (p *Provider) AddToRegistry(registry *tool.Registry) {
	for _, value := range p.tools {
		registry.Add(value)
	}
}
func (p *Provider) Close() error {
	if p == nil || p.session == nil {
		return nil
	}
	return p.session.Close()
}
func (p *Provider) allowed(name string) bool {
	for _, denied := range p.cfg.DenyTools {
		if denied == name {
			return false
		}
	}
	if len(p.cfg.AllowTools) == 0 {
		return true
	}
	for _, allowed := range p.cfg.AllowTools {
		if allowed == name {
			return true
		}
	}
	return false
}

func (p *Provider) normalize(definition *sdk.Tool) tool.Tool {
	input, _ := json.Marshal(definition.InputSchema)
	output, _ := json.Marshal(definition.OutputSchema)
	capabilities := append([]string(nil), p.cfg.CapabilityMappings[definition.Name]...)
	description := definition.Description
	if description == "" {
		description = definition.Name
	}
	spec := tool.Spec{ID: p.cfg.ID + "." + definition.Name, Description: description, InputSchema: input, OutputSchema: output, Capabilities: capabilities, ProviderID: p.cfg.ID, Version: "mcp", SideEffect: tool.SideEffectUnknown, Idempotency: tool.IdempotencyUnknown, Retryable: false, RiskLevel: tool.RiskHigh, Confirmation: tool.ConfirmationOnRisk, TrustLevel: "untrusted"}
	if configured, ok := p.cfg.ToolPolicies[definition.Name]; ok {
		if configured.SideEffect != "" {
			spec.SideEffect = configured.SideEffect
		}
		if configured.Idempotency != "" {
			spec.Idempotency = configured.Idempotency
		}
		spec.Retryable = configured.Retryable
		if configured.RiskLevel != "" {
			spec.RiskLevel = configured.RiskLevel
		}
		if configured.Confirmation != "" {
			spec.Confirmation = configured.Confirmation
		}
	}
	return &remoteTool{provider: p, name: definition.Name, spec: spec, modelSpec: model.ToolSpec{Type: "function", Function: model.FunctionSpec{Name: spec.ID, Description: spec.Description, Parameters: input}, Capabilities: append([]string(nil), capabilities...), OutputSchema: output}}
}

type remoteTool struct {
	provider  *Provider
	name      string
	spec      tool.Spec
	modelSpec model.ToolSpec
}

func (t *remoteTool) Spec() tool.Spec           { return t.spec }
func (t *remoteTool) ModelSpec() model.ToolSpec { return t.modelSpec }
func (t *remoteTool) Call(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return tool.Result{}, fmt.Errorf("mcp tool %s invalid arguments: %w", t.spec.ID, err)
	}
	result, err := t.provider.session.CallTool(ctx, &sdk.CallToolParams{Name: t.name, Arguments: args})
	if err != nil {
		return tool.Result{}, fmt.Errorf("mcp tool %s call: %w", t.spec.ID, err)
	}
	if result.IsError {
		return tool.Result{}, fmt.Errorf("mcp tool %s returned error", t.spec.ID)
	}
	var content any
	if result.StructuredContent != nil {
		content = result.StructuredContent
	} else {
		var text []string
		for _, item := range result.Content {
			if value, ok := item.(*sdk.TextContent); ok {
				text = append(text, value.Text)
			}
		}
		if len(text) > 0 {
			content = strings.Join(text, "\n")
		} else {
			content = result.Content
		}
	}
	if t.spec.SideEffect == tool.SideEffectUnknown {
		now := time.Now().UTC()
		return tool.Result{Content: content, Receipt: true, Action: &tool.ActionReceipt{ToolID: t.spec.ID, Status: contract.ActionUnknown, ExecutedAt: now, Timestamp: now, Message: "MCP provider did not declare whether the side effect completed"}}, nil
	}
	if t.spec.IsWrite() {
		now := time.Now().UTC()
		return tool.Result{Content: content, Receipt: true, Action: &tool.ActionReceipt{ToolID: t.spec.ID, Status: contract.ActionSuccess, ExecutedAt: now, Timestamp: now}}, nil
	}
	return tool.Result{Content: content}, nil
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		clone.Header.Set(k, v)
	}
	return t.base.RoundTrip(clone)
}
