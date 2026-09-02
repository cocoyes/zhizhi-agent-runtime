package main

import (
	"context"
	"fmt"
	"os"

	zhizhi "github.com/zhizhi-ai/zhizhi-agent-runtime"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

type Args struct {
	City string `json:"city" jsonschema:"required"`
}
type Result struct {
	City        string `json:"city"`
	Condition   string `json:"condition"`
	Temperature int    `json:"temperature"`
}

func main() {
	weather := tool.Func("weather.get", "Get current weather", func(context.Context, Args) (Result, error) {
		return Result{City: "Shenzhen", Condition: "Sunny", Temperature: 27}, nil
	}, tool.WithCapabilities("weather.current"))
	a, err := zhizhi.New(
		zhizhi.WithModel(openaicompat.New(openaicompat.Config{BaseURL: os.Getenv("LLM_BASE_URL"),
			APIKey: os.Getenv("LLM_API_KEY"),
			Model:  os.Getenv("LLM_MODEL")})),
		zhizhi.WithTools(weather))
	if err != nil {
		panic(err)
	}
	resp, err := a.Run(context.Background(), zhizhi.Request{Input: "深圳天气怎么样？"})
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Text)
}
