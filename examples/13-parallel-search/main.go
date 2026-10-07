package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/cocoyes/zhizhi-agent-runtime/mcp"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

const parallelEndpoint = "https://search.parallel.ai/mcp"
const defaultRequest = "Find the official Go 1.25 release notes and summarize two changes with source URLs."

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Getenv, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, args []string, out io.Writer) error {
	if getenv("LLM_BASE_URL") == "" || getenv("LLM_MODEL") == "" {
		return fmt.Errorf("set LLM_BASE_URL and LLM_MODEL (and LLM_API_KEY if your model endpoint requires it)")
	}
	endpoint := getenv("PARALLEL_MCP_URL")
	if endpoint == "" {
		endpoint = parallelEndpoint
	}
	readOnly := mcp.ToolPolicy{SideEffect: tool.SideEffectRead, Idempotency: tool.IdempotencySafe, Retryable: true, RiskLevel: tool.RiskLow, Confirmation: tool.ConfirmationNever}
	capabilities, err := mcp.NewCapabilityProvider(mcp.Config{
		ID: "parallel-search", Name: "parallel-search",
		Description: "Search the web for current facts, documentation, and research with source URLs. Fetch specific web pages when the question needs exact wording or search excerpts are insufficient.",
		Endpoint:    endpoint, Transport: mcp.TransportStreamableHTTP,
		Headers:      map[string]string{"User-Agent": "zhizhi-agent-runtime/" + zhizhi.Version},
		AllowTools:   []string{"web_search", "web_fetch"},
		ToolPolicies: map[string]mcp.ToolPolicy{"web_search": readOnly, "web_fetch": readOnly},
		Required:     true,
	})
	if err != nil {
		return err
	}
	defer capabilities.Close()
	modelClient := openaicompat.New(openaicompat.Config{
		BaseURL: getenv("LLM_BASE_URL"), APIKey: getenv("LLM_API_KEY"), Model: getenv("LLM_MODEL"),
		Thinking: getenv("LLM_THINKING"), ReasoningEffort: getenv("LLM_REASONING_EFFORT"),
	})
	agent, err := zhizhi.New(zhizhi.WithModel(modelClient), zhizhi.WithMCPCapabilityProvider(capabilities))
	if err != nil {
		return err
	}
	defer agent.Close(context.Background())
	request := defaultRequest
	if len(args) > 0 {
		request = strings.Join(args, " ")
	}
	mode := zhizhi.RunModeAgentSimple
	response, err := agent.Run(ctx, zhizhi.Request{Input: request, Mode: &mode})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, response.Text)
	return err
}
