package zhizhi

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
	runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"
	"gopkg.in/yaml.v3"
)

type FileConfig struct {
	Model   FileModelConfig   `yaml:"model"`
	Agent   FileAgentConfig   `yaml:"agent"`
	Runtime FileRuntimeConfig `yaml:"runtime"`
	MCP     FileMCPConfig     `yaml:"mcp"`
}
type FileModelConfig struct {
	Type            string `yaml:"type"`
	BaseURL         string `yaml:"base_url"`
	APIKey          string `yaml:"api_key"`
	Model           string `yaml:"model"`
	Thinking        string `yaml:"thinking"`
	ReasoningEffort string `yaml:"reasoning_effort"`
}
type FileAgentConfig struct {
	SystemPrompt string `yaml:"system_prompt"`
}
type FileRuntimeConfig struct {
	Profile          string        `yaml:"profile"`
	MaxSteps         int           `yaml:"max_steps"`
	MaxBatches       int           `yaml:"max_batches"`
	MaxModelCalls    int           `yaml:"max_model_calls"`
	MaxToolCalls     int           `yaml:"max_tool_calls"`
	MaxReplans       int           `yaml:"max_replans"`
	MaxParallelSteps int           `yaml:"max_parallel_steps"`
	TargetLatency    time.Duration `yaml:"target_latency"`
	OptionalCutoff   time.Duration `yaml:"optional_cutoff"`
	HardTimeout      time.Duration `yaml:"hard_timeout"`
}
type FileMCPConfig struct {
	Servers []FileMCPServer `yaml:"servers"`
}
type FileMCPServer struct {
	ID           string                           `yaml:"id"`
	Transport    string                           `yaml:"transport"`
	Endpoint     string                           `yaml:"endpoint"`
	AllowTools   []string                         `yaml:"allow_tools"`
	DenyTools    []string                         `yaml:"deny_tools"`
	ToolPolicies map[string]runtimemcp.ToolPolicy `yaml:"tool_policies"`
	Headers      map[string]string                `yaml:"headers"`
}

func Load(path string) (Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = []byte(os.ExpandEnv(string(data)))
	var file FileConfig
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if file.Model.BaseURL == "" || file.Model.Model == "" {
		return nil, fmt.Errorf("config: model.base_url and model.model are required")
	}
	options := []Option{WithModel(openaicompat.New(openaicompat.Config{BaseURL: file.Model.BaseURL, APIKey: file.Model.APIKey, Model: file.Model.Model, Thinking: file.Model.Thinking, ReasoningEffort: file.Model.ReasoningEffort})), WithSystemPrompt(file.Agent.SystemPrompt), WithBudget(Budget{MaxSteps: file.Runtime.MaxSteps, MaxBatches: file.Runtime.MaxBatches, MaxModelCalls: file.Runtime.MaxModelCalls, MaxToolCalls: file.Runtime.MaxToolCalls, MaxReplans: file.Runtime.MaxReplans, MaxParallelSteps: file.Runtime.MaxParallelSteps, TargetLatency: file.Runtime.TargetLatency, OptionalCutoff: file.Runtime.OptionalCutoff, HardTimeout: file.Runtime.HardTimeout})}
	for _, server := range file.MCP.Servers {
		transport := runtimemcp.Transport(server.Transport)
		if transport == "" {
			transport = runtimemcp.TransportStreamableHTTP
		}
		if transport != runtimemcp.TransportStreamableHTTP {
			return nil, fmt.Errorf("config: unsupported MCP transport %q", transport)
		}
		options = append(options, WithMCP(runtimemcp.Config{ID: server.ID, Transport: transport, Endpoint: server.Endpoint, AllowTools: server.AllowTools, DenyTools: server.DenyTools, ToolPolicies: server.ToolPolicies, Headers: server.Headers}))
	}
	return New(options...)
}

func (c FileConfig) Validate() error {
	if strings.TrimSpace(c.Model.BaseURL) == "" {
		return fmt.Errorf("model.base_url is required")
	}
	return nil
}
