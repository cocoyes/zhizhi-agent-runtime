package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	zhizhi "github.com/zhizhi-ai/zhizhi-agent-runtime"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/model"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/observe"
	"github.com/zhizhi-ai/zhizhi-agent-runtime/tool"
)

type workflowModel struct {
	mu    sync.Mutex
	calls int
}

func (m *workflowModel) ID() string { return "workflow-scripted" }
func (m *workflowModel) Capabilities() model.ModelCapabilities {
	return model.ModelCapabilities{JSONMode: true}
}
func (m *workflowModel) Generate(_ context.Context, _ model.ModelInput) (*model.ModelOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	switch m.calls {
	case 1:
		return &model.ModelOutput{Text: `{"version":1,"steps":[{"id":"weather","capability":"weather.current","input":{"city":"深圳"}},{"id":"outdoor","capability":"activity.outdoor","depends_on":["weather"],"condition":{"source_step":"weather","source_path":"outdoor_suitable","equals":true},"bindings":[{"source_step":"weather","source_path":"temperature","target_path":"temperature"}]},{"id":"finish","capability":"itinerary.finish","depends_on":["outdoor"]}]}`}, nil
	case 2:
		return &model.ModelOutput{Text: `{"replace_steps":[{"id":"outdoor","capability":"activity.cached","depends_on":["weather"],"bindings":[{"source_step":"weather","source_path":"temperature","target_path":"temperature"}]}]}`}, nil
	default:
		return &model.ModelOutput{Text: "天气服务异常，已使用缓存推荐并完成行程。"}, nil
	}
}
func (m *workflowModel) Stream(context.Context, model.ModelInput) (model.Stream, error) {
	return nil, nil
}

func TestExampleWorkflowSelectsToolsAndReplans(t *testing.T) {
	var mu sync.Mutex
	called := make([]string, 0, 8)
	weather := tool.Func("weather.current", "查询指定城市的天气和户外适宜度", func(_ context.Context, in struct {
		City string `json:"city"`
	}) (struct {
		Temperature     int  `json:"temperature"`
		OutdoorSuitable bool `json:"outdoor_suitable"`
	}, error) {
		mu.Lock()
		called = append(called, "weather.current:"+in.City)
		mu.Unlock()
		return struct {
			Temperature     int  `json:"temperature"`
			OutdoorSuitable bool `json:"outdoor_suitable"`
		}{Temperature: 27, OutdoorSuitable: true}, nil
	}, tool.WithCapabilities("weather.current"))
	outdoor := tool.Func("activity.outdoor", "根据天气推荐户外活动", func(_ context.Context, in struct {
		Temperature int `json:"temperature"`
	}) (string, error) {
		mu.Lock()
		called = append(called, fmt.Sprintf("activity.outdoor:%d", in.Temperature))
		mu.Unlock()
		return "", errors.New("活动服务暂时不可用")
	}, tool.WithCapabilities("activity.outdoor"))
	cached := tool.Func("activity.cached", "使用缓存的户外活动推荐", func(_ context.Context, in struct {
		Temperature int `json:"temperature"`
	}) (string, error) {
		mu.Lock()
		called = append(called, fmt.Sprintf("activity.cached:%d", in.Temperature))
		mu.Unlock()
		return "深圳湾散步，缓存推荐", nil
	}, tool.WithCapabilities("activity.cached"))
	finish := tool.Func("itinerary.finish", "输出最终行程", func(context.Context, struct{}) (string, error) {
		mu.Lock()
		called = append(called, "itinerary.finish")
		mu.Unlock()
		return "行程已完成", nil
	}, tool.WithCapabilities("itinerary.finish"))
	irrelevant := tool.Func("finance.quote", "查询股票报价（与本需求无关）", func(context.Context, struct{}) (string, error) {
		mu.Lock()
		called = append(called, "finance.quote")
		mu.Unlock()
		return "上涨", nil
	}, tool.WithCapabilities("finance.quote"))

	m := &workflowModel{}
	stats := &observe.Stats{}
	agent, err := zhizhi.New(zhizhi.WithModel(m), zhizhi.WithTools(weather, outdoor, cached, finish, irrelevant), zhizhi.WithObserver(stats))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	mode := zhizhi.RunModeAgentComplex
	out, err := agent.Run(context.Background(), zhizhi.Request{Input: "安排深圳今晚的户外活动", Mode: &mode})
	if err != nil {
		t.Fatalf("复杂工作流执行失败：%v", err)
	}
	if out.Text == "" || out.Replans != 1 || out.PlanSteps != 3 || out.Batches != 3 || m.calls != 3 {
		t.Fatalf("工作流响应不符合预期：%+v，模型调用次数=%d", out, m.calls)
	}
	if stats.Snapshot().Replans != 1 {
		t.Fatalf("应记录一次重规划事件，实际为：%+v", stats.Snapshot())
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(called, ",")
	if joined != "weather.current:深圳,activity.outdoor:27,weather.current:深圳,activity.cached:27,itinerary.finish" {
		t.Fatalf("实际选择的工具调用不符合预期：%v", called)
	}
}
