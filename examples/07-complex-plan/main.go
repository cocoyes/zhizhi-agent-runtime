package main

import (
	"context"
	"fmt"
	"os"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

func main() {
	weather := tool.Func("weather.get", "Get weather", func(context.Context, struct {
		City string `json:"city"`
	}) (string, error) {
		return "sunny", nil
	}, tool.WithCapabilities("weather.current"))
	thinking := os.Getenv("LLM_THINKING")
	if thinking == "" {
		thinking = openaicompat.ThinkingDisabled
	}
	reasoningEffort := os.Getenv("LLM_REASONING_EFFORT")
	if reasoningEffort == "" {
		reasoningEffort = "high"
	}
	a, err := zhizhi.New(zhizhi.WithModel(
		openaicompat.New(openaicompat.Config{BaseURL: os.Getenv("LLM_BASE_URL"),
			APIKey:          os.Getenv("LLM_API_KEY"),
			Model:           os.Getenv("LLM_MODEL"),
			Thinking:        thinking,
			ReasoningEffort: reasoningEffort,
		})),
		zhizhi.WithTools(weather))
	if err != nil {
		panic(err)
	}

	mode := zhizhi.RunModeAgentComplex
	resp, err := a.Run(context.Background(), zhizhi.Request{Input: "What is the weather in Shenzhen?", Mode: &mode})
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s steps=%d batches=%d\n", resp.Text, resp.PlanSteps, resp.Batches)
}
