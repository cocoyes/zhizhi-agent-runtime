package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	zhizhi "github.com/zhizhi-ai/zhizhi-agent-runtime"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/observe"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

type weatherInput struct {
	City string `json:"city"`
}
type weatherOutput struct {
	City            string `json:"city"`
	Condition       string `json:"condition"`
	Temperature     int    `json:"temperature"`
	OutdoorSuitable bool   `json:"outdoor_suitable"`
}
type activityInput struct {
	Temperature int `json:"temperature"`
}
type activityOutput struct {
	Activities []string `json:"activities"`
	Reason     string   `json:"reason"`
}
type itineraryOutput struct {
	Status string `json:"status"`
	Plan   string `json:"plan"`
}

func main() {
	weather := tool.Func("weather.current", "A：查询指定城市的当前天气，并判断是否适合户外活动", func(_ context.Context, in weatherInput) (weatherOutput, error) {
		return weatherOutput{City: in.City, Condition: "晴朗", Temperature: 27, OutdoorSuitable: true}, nil
	}, tool.WithCapabilities("weather.current"))
	outdoor := tool.Func("activity.outdoor", "B：根据天气推荐适合户外的活动", func(_ context.Context, in activityInput) (activityOutput, error) {
		return activityOutput{}, fmt.Errorf("户外活动服务暂时不可用（气温 %d 度）", in.Temperature)
	}, tool.WithCapabilities("activity.outdoor"))
	cached := tool.Func("activity.cached", "B：户外服务不可用时使用缓存的活动推荐", func(_ context.Context, in activityInput) (activityOutput, error) {
		return activityOutput{Activities: []string{"莲花山公园散步", "深圳湾看日落"}, Reason: fmt.Sprintf("使用缓存推荐，气温 %d 度", in.Temperature)}, nil
	}, tool.WithCapabilities("activity.cached"))
	indoor := tool.Func("activity.indoor", "C：天气不适合户外时推荐室内活动", func(_ context.Context, in activityInput) (activityOutput, error) {
		return activityOutput{Activities: []string{"博物馆参观", "室内餐厅用餐"}, Reason: fmt.Sprintf("气温 %d 度，改为室内活动", in.Temperature)}, nil
	}, tool.WithCapabilities("activity.indoor"))
	compose := tool.Func("itinerary.compose", "D：根据活动推荐整理出完整行程", func(context.Context, struct{}) (itineraryOutput, error) {
		return itineraryOutput{Status: "已整理", Plan: "下午活动 -> 晚餐 -> 晚间散步"}, nil
	}, tool.WithCapabilities("itinerary.compose"))
	finish := tool.Func("itinerary.finish", "H：输出最终行程结果", func(context.Context, struct{}) (itineraryOutput, error) {
		return itineraryOutput{Status: "已完成", Plan: "行程已准备好"}, nil
	}, tool.WithCapabilities("itinerary.finish"))

	thinking := os.Getenv("LLM_THINKING")
	if thinking == "" {
		thinking = openaicompat.ThinkingDisabled
	}
	reasoningEffort := os.Getenv("LLM_REASONING_EFFORT")
	if reasoningEffort == "" {
		reasoningEffort = "high"
	}
	modelClient := openaicompat.New(openaicompat.Config{
		BaseURL:         os.Getenv("LLM_BASE_URL"),
		APIKey:          os.Getenv("LLM_API_KEY"),
		Model:           os.Getenv("LLM_MODEL"),
		Thinking:        thinking,
		ReasoningEffort: reasoningEffort,
	})
	agent, err := zhizhi.New(
		zhizhi.WithModel(modelClient),
		zhizhi.WithTools(weather, outdoor, cached, indoor, compose, finish),
		zhizhi.WithObserver(observe.NewJSONL(os.Stdout, observe.WithDetails())),
	)

	if err != nil {
		panic(err)
	}
	defer agent.Close(context.Background())
	mode := zhizhi.RunModeAgentComplex
	request := "安排深圳今晚的活动"
	if len(os.Args) > 1 {
		request = strings.Join(os.Args[1:], " ")
	}
	resp, err := agent.Run(context.Background(), zhizhi.Request{Input: request, Mode: &mode})
	if err != nil {
		panic(err)
	}
	fmt.Println("final:", resp.Text)
	fmt.Printf("plan_steps=%d batches=%d tool_calls=%d warnings=%d\n", resp.PlanSteps, resp.Batches, resp.ToolCalls, len(resp.Warnings))
	for _, evidence := range resp.Evidence {
		fmt.Printf("executed=%s result=%v\n", evidence.ToolID, evidence.Content)
	}
}
