package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
	"github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
	"github.com/cocoyes/zhizhi-agent-runtime/mcp"
	"github.com/cocoyes/zhizhi-agent-runtime/observe"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

const defaultRequest = "周杰伦的背景资料是啥，我今天心情不错，打算出门，能帮我看看深圳的天气吗"

const amapDescription = "高德地图位置与本地生活能力。适用于需要真实地理位置或本地信息的请求，包括附近美食/餐厅/咖啡店/景点/娱乐/商场/生活设施推荐，地点与商家搜索、地址解析、当前位置识别、天气查询、距离计算、路线规划和导航等。当用户询问‘附近有什么’‘哪里好吃/好玩’‘某地点在哪里’‘天气怎么样’‘离我多远’‘怎么去’等问题时使用。选择该能力后，先获取内部工具列表，再根据任务选择具体工具并支持多步骤连续调用。"

const baikeDescription = "百度百科提供百科官方内容检索服务，帮助用户快速、全面地获取权威的百科信息。支持百科词条义项查询、词条详情查询以及秒懂百科视频获取。适用于 AI 问答、内容创作、教育教学、知识检索等场景，为模型提供可信的知识支撑。根据用户输入词条名或者词条 ID 查询相关内容。"

func main() {
	modelClient := openaicompat.New(openaicompat.Config{
		BaseURL:         os.Getenv("LLM_BASE_URL"),
		APIKey:          os.Getenv("LLM_API_KEY"),
		Model:           os.Getenv("LLM_MODEL"),
		Thinking:        envOr("LLM_THINKING", openaicompat.ThinkingDisabled),
		ReasoningEffort: envOr("LLM_REASONING_EFFORT", "high"),
	})
	amapEndpoint := os.Getenv("AMAP_MCP_URL")
	if amapEndpoint == "" {
		if key := os.Getenv("AMAP_MCP_KEY"); key != "" {
			amapEndpoint = "https://mcp.amap.com/mcp?key=" + url.QueryEscape(key)
		}
	}
	if amapEndpoint == "" {
		panic("set AMAP_MCP_URL or AMAP_MCP_KEY")
	}
	readOnly := &mcp.ToolPolicy{SideEffect: tool.SideEffectRead, Idempotency: tool.IdempotencySafe, Retryable: true, RiskLevel: tool.RiskLow, Confirmation: tool.ConfirmationNever}
	capabilities, err := mcp.NewCapabilityProvider(
		mcp.Config{ID: "amap", Name: "amap", Description: amapDescription, Endpoint: amapEndpoint, DefaultToolPolicy: readOnly, Required: true},
		mcp.Config{ID: "baike-mcp-server", Name: "baike-mcp-server", Description: baikeDescription, Endpoint: "https://qianfan.baidubce.com/v2/mcp/baike", Headers: bearerHeader(os.Getenv("BAIKE_MCP_TOKEN")), DefaultToolPolicy: readOnly, Required: true},
	)
	if err != nil {
		panic(err)
	}
	agent, err := zhizhi.New(
		zhizhi.WithModel(modelClient),
		zhizhi.WithMCPCapabilityProvider(capabilities),
		zhizhi.WithObserver(observe.NewJSONL(os.Stdout, observe.WithDetails())),
	)
	if err != nil {
		panic(err)
	}
	defer agent.Close(context.Background())

	request := defaultRequest
	if len(os.Args) > 1 {
		request = strings.Join(os.Args[1:], " ")
	}
	mode := zhizhi.RunModeAgentSimple
	response, err := agent.Run(context.Background(), zhizhi.Request{Input: request, Mode: &mode})
	if err != nil {
		panic(err)
	}
	fmt.Println("final:", response.Text)
	for _, evidence := range response.Evidence {
		fmt.Printf("provider=%s tool=%s result=%v\n", evidence.ProviderID, evidence.ToolID, evidence.Content)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func bearerHeader(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + token}
}
