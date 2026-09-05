package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

const defaultRequest = `帮我看看我的钥匙在哪
如果钥匙在抽屉，就提醒我晚上8点下班拿
否则就提醒我晚上10点喝水
顺便再帮我记录猫粮在客厅`

type searchInput struct {
	Query string `json:"query"`
}

type searchOutput struct {
	Result string `json:"result" jsonschema:"description=搜索得到的位置分类"`
}

type notifyInput struct {
	At      string `json:"at"`
	Message string `json:"message"`
	Channel string `json:"channel,omitempty" jsonschema:"description=投递通道，可选 cloud 或 local；默认 cloud"`
}

type notifyOutput struct {
	Status  string `json:"status"`
	Channel string `json:"channel"`
	At      string `json:"at"`
	Message string `json:"message"`
}

type recordInput struct {
	Content string `json:"content"`
}

type recordOutput struct {
	Status string `json:"status"`
}

type demoState struct {
	mu         sync.Mutex
	active     int
	maxActive  int
	calls      map[string]int
	lastNotify notifyInput
}

func newDemoState() *demoState {
	return &demoState{calls: map[string]int{}}
}

func (s *demoState) track(name string) func() {
	s.mu.Lock()
	s.active++
	s.calls[name]++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newDemoTools(state *demoState) []tool.Tool {
	search := tool.Func("search", "搜索个人物品的当前位置，输入自然语言查询并返回搜索结果。", func(ctx context.Context, in searchInput) (searchOutput, error) {
		done := state.track("search")
		defer done()
		if err := wait(ctx, 80*time.Millisecond); err != nil {
			return searchOutput{}, err
		}
		fmt.Printf("[search] %s -> 柜子\n", in.Query)
		return searchOutput{Result: "柜子"}, nil
	}, tool.WithCapabilities("search"))

	notify := tool.Func("notify", "创建定时通知；支持跨设备的 cloud 通道和无需联网的 local 通道，未指定时使用 cloud。", func(_ context.Context, in notifyInput) (notifyOutput, error) {
		done := state.track("notify")
		defer done()
		if in.Channel == "" {
			in.Channel = "cloud"
		}
		if in.Channel == "cloud" {
			return notifyOutput{}, fmt.Errorf("cloud notification service unavailable; retry with channel local")
		}
		if in.Channel != "local" {
			return notifyOutput{}, fmt.Errorf("unsupported notification channel %q", in.Channel)
		}
		state.mu.Lock()
		state.lastNotify = in
		state.mu.Unlock()
		fmt.Printf("[notify] %s %s: %s\n", in.Channel, in.At, in.Message)
		return notifyOutput{Status: "created", Channel: in.Channel, At: in.At, Message: in.Message}, nil
	}, tool.WithCapabilities("notify"), tool.WithSideEffect(tool.SideEffectWriteIdempotent), tool.WithIdempotency(tool.IdempotencyIdempotent), tool.WithRetryable(false))

	record := tool.Func("record", "记录一条需要长期保存的信息，输入完整的自然语言内容。", func(ctx context.Context, in recordInput) (recordOutput, error) {
		done := state.track("record")
		defer done()
		if err := wait(ctx, 80*time.Millisecond); err != nil {
			return recordOutput{}, err
		}
		fmt.Printf("[record] success: %s\n", in.Content)
		return recordOutput{Status: "success"}, nil
	}, tool.WithCapabilities("record"), tool.WithSideEffect(tool.SideEffectWriteIdempotent), tool.WithIdempotency(tool.IdempotencyIdempotent))

	return []tool.Tool{search, notify, record}
}

func newDemoAgent(state *demoState, observer observe.Observer) (zhizhi.Agent, error) {
	thinking := os.Getenv("LLM_THINKING")
	if thinking == "" {
		thinking = openaicompat.ThinkingDisabled
	}
	reasoningEffort := os.Getenv("LLM_REASONING_EFFORT")
	modelClient := openaicompat.New(openaicompat.Config{
		BaseURL:         os.Getenv("LLM_BASE_URL"),
		APIKey:          os.Getenv("LLM_API_KEY"),
		Model:           os.Getenv("LLM_MODEL"),
		Thinking:        thinking,
		ReasoningEffort: reasoningEffort,
	})
	options := []zhizhi.Option{
		zhizhi.WithModel(modelClient),
		zhizhi.WithTools(newDemoTools(state)...),
		zhizhi.WithBudget(zhizhi.Budget{MaxReplans: 1, MaxParallelSteps: 4}),
	}
	if observer != nil {
		options = append(options, zhizhi.WithObserver(observer))
	}
	return zhizhi.New(options...)
}

func main() {
	state := newDemoState()
	agent, err := newDemoAgent(state, observe.NewJSONL(os.Stdout, observe.WithDetails()))
	if err != nil {
		panic(err)
	}
	defer agent.Close(context.Background())

	request := defaultRequest
	if len(os.Args) > 1 {
		request = strings.Join(os.Args[1:], " ")
	}
	mode := zhizhi.RunModeAgentComplex
	response, err := agent.Run(context.Background(), zhizhi.Request{Input: request, Mode: &mode})
	if err != nil {
		panic(err)
	}

	state.mu.Lock()
	maxParallel := state.maxActive
	notification := state.lastNotify
	state.mu.Unlock()
	fmt.Println("final:", response.Text)
	fmt.Printf("replans=%d plan_steps=%d batches=%d max_parallel=%d\n", response.Replans, response.PlanSteps, response.Batches, maxParallel)
	fmt.Printf("notification: %s %s via %s\n", notification.At, notification.Message, notification.Channel)
}
