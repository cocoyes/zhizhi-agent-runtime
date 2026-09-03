# zhizhi-agent-runtime

<p align="center">
  <img src="zhizhi-logo.png" alt="Zhizhi Agent Runtime Logo" width="720">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/cocoyes/zhizhi-agent-runtime.svg)](https://pkg.go.dev/github.com/cocoyes/zhizhi-agent-runtime)
[![Go Report Card](https://goreportcard.com/badge/github.com/cocoyes/zhizhi-agent-runtime)](https://goreportcard.com/report/github.com/cocoyes/zhizhi-agent-runtime)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

[English README](README.md)

面向生产环境的 Go Agent Runtime，帮助 Agent 完成规划、工具调用、失败恢复和全过程审计。

Zhizhi 把普通 Go 函数注册成经过校验的工具，把复杂需求编排成依赖安全的执行计划，并对条件分支、重规划和副作用进行显式控制。

## 核心能力

- 类型安全的工具函数，自动生成 JSON Schema 并校验输入/输出
- Chat、Streaming、能力路由和模型驱动的复杂任务规划
- 依赖图、并行批次、条件分支和跨步骤证据绑定
- 必需步骤失败后的有限次重规划
- 重试、Fallback、超时、预算和并发限制
- Guard、确认门禁、Action Receipt 和副作用保护
- MCP 接入、工具 Allowlist 和信任边界控制
- JSONL 链路追踪、Evidence、Warnings 和运行统计

Agent 生命周期清晰可见：

```text
请求 -> 路由 -> 规划 -> 执行 -> 记录证据 -> 重规划 -> 最终回复
```

## 环境要求

- Go 1.25 或更高版本
- OpenAI 兼容的 Chat Completions 接口

内置适配器请求 `{LLM_BASE_URL}/chat/completions`，支持标准 Chat Completions 响应，也支持 Responses 风格的 `output_text` 或 `output[].content[].text` 文本响应。内部工具 ID 可以使用点号，适配器只在发送给模型时转换为合法的 wire 名称。

DeepSeek、豆包等混合推理模型可通过适配器显式控制思考模式：

```go
model := openaicompat.New(openaicompat.Config{
    BaseURL:         os.Getenv("LLM_BASE_URL"),
    APIKey:          os.Getenv("LLM_API_KEY"),
    Model:           os.Getenv("LLM_MODEL"),
    Thinking:        openaicompat.ThinkingEnabled, // 或 ThinkingDisabled / ThinkingAuto
    ReasoningEffort: "high",
})
```

适配器会发送 `thinking: {"type":"..."}` 和顶层 `reasoning_effort`。思考模式结合工具调用时，返回的 `reasoning_content` 会在同一轮后续请求中自动回传。不传这两个字段时，适配器默认使用 `ThinkingDisabled` 和 `high` 推理强度。具体枚举范围仍取决于 provider 和模型版本。示例 10 也支持环境变量 `LLM_THINKING` 和 `LLM_REASONING_EFFORT`。

排查规划或工具参数问题时，可启用详细 JSONL：

```go
zhizhi.WithObserver(observe.NewJSONL(os.Stdout, observe.WithDetails()))
```

详细模式会记录 planner/replanner 的模型请求、原始响应和修复重试、每个步骤的实际输入/输出/错误、最终 patch，以及最终合成请求与响应。它可能包含用户或业务数据，因此默认关闭；示例 10 已显式开启。

模型结构化输出不再由运行时手写拆解：非标准 JSON 的提取和修复使用 `jsonrepair-go`，常见弱类型漂移使用 `mapstructure/v2`，跨步骤路径按 RFC 6901 交给 `go-openapi/jsonpointer`。语法修复后仍须通过计划、JSON Schema 和 patch 应用校验，不会因“能反序列化”就继续执行。

Planner 和 Replanner 的主路径参考 Eino 的结构化输出设计：从 Go 类型生成 JSON Schema，将 Plan 或 Patch 作为唯一的 synthetic output tool，并使用强制 `tool_choice`，只消费该工具的 arguments。普通 JSON 文本仅作为不支持工具调用模型的兼容路径；业务工具只作为规划目录，不会在规划阶段被调用。

## 安装与最小示例

```bash
go get github.com/cocoyes/zhizhi-agent-runtime
```

```go
model := openaicompat.New(openaicompat.Config{
    BaseURL: os.Getenv("LLM_BASE_URL"),
    APIKey: os.Getenv("LLM_API_KEY"),
    Model: os.Getenv("LLM_MODEL"),
})
agent, err := zhizhi.New(zhizhi.WithModel(model))
if err != nil { log.Fatal(err) }
defer agent.Close(context.Background())
response, err := agent.Run(context.Background(), zhizhi.Request{Input: "总结最新的客服请求"})
if err != nil { log.Fatal(err) }
fmt.Println(response.Text)
```

```bash
export LLM_BASE_URL="https://your-provider.example/v1"
export LLM_API_KEY="your-api-key"
export LLM_MODEL="your-model"
go run ./examples/00-chat
```

接口可以返回标准 Chat Completions 响应：

```json
{"choices":[{"message":{"role":"assistant","content":"..."}}]}
```

也支持 Responses 风格的文本响应：

```json
{"output_text":"..."}
```

本地运行时请参考 `.env.example` 设置真实的 provider 地址、API Key 和模型。运行时不会伪造模型结果：规划、工具选择、重规划和最终回复都会调用你配置的真实模型。

## 注册工具

工具就是普通的类型化 Go 函数，运行时自动生成并校验 Schema：

```go
type WeatherInput struct {
    City string `json:"city" jsonschema:"required"`
}
type WeatherOutput struct {
    City string `json:"city"`
    Condition string `json:"condition"`
    Temperature int `json:"temperature"`
}

weather := tool.Func(
    "weather.current",
    "查询指定城市的当前天气",
    func(ctx context.Context, in WeatherInput) (WeatherOutput, error) {
        return WeatherOutput{City: in.City, Condition: "晴朗", Temperature: 27}, nil
    },
    tool.WithCapabilities("weather.current"),
)
agent, err := zhizhi.New(zhizhi.WithModel(model), zhizhi.WithTools(weather))
```

工具描述会提供给模型。描述清楚工具用途和适用场景，保持工具 ID 稳定且具体；模型负责选择，运行时负责解析并执行已注册实现。

## 复杂任务与条件分支

多步骤任务使用 `RunModeAgentComplex`。内置 planner 会根据用户目标和工具目录选择工具并建立依赖图，无需在业务代码中手写 planner：

```go
ctx := context.Background()
mode := zhizhi.RunModeAgentComplex
response, err := agent.Run(ctx, zhizhi.Request{
    Input: "根据天气安排今晚深圳的活动",
    Mode: &mode,
})
```

示例中的 `ctx` 应替换为请求级上下文，并按业务设置超时和取消策略。

计划可以根据前置证据选择分支：

```json
{
  "version": 1,
  "steps": [
    {"id":"weather", "capability":"weather.current", "input":{"city":"深圳"}},
    {"id":"outdoor", "capability":"activity.outdoor", "depends_on":["weather"], "condition":{"source_step":"weather","source_path":"outdoor_suitable","equals":true}, "bindings":[{"source_step":"weather","source_path":"temperature","target_path":"temperature"}]},
    {"id":"indoor", "capability":"activity.indoor", "depends_on":["weather"], "condition":{"source_step":"weather","source_path":"outdoor_suitable","equals":false}},
    {"id":"compose", "capability":"itinerary.compose", "depends_on":["outdoor","indoor"]}
  ]
}
```

条件来源必须是当前步骤的依赖。条件不满足时步骤会被跳过，不会调用工具。`bindings` 将前一步证据注入后续工具的类型化输入。必需步骤失败时，运行时默认允许一次有限重规划，在保留成功步骤的同时替换失败步骤。

完整流程：

```bash
go run ./examples/10-full-agent
```

## 副作用保护

为写操作声明副作用、幂等性、风险等级和确认策略：

```go
sendEmail := tool.Func(
    "mail.send", "向指定收件人发送邮件", sendEmailImpl,
    tool.WithCapabilities("mail.send"),
    tool.WithSideEffect(tool.SideEffectWriteNonIdempotent),
    tool.WithIdempotency(tool.IdempotencyNonIdempotent),
    tool.WithRisk(tool.RiskHigh),
    tool.WithConfirmation(tool.ConfirmationAlways),
)
agent, err := zhizhi.New(
    zhizhi.WithModel(model), zhizhi.WithTools(sendEmail),
    zhizhi.WithGuard(authorize),
    zhizhi.WithConfirmation(askUser),
)
```

没有确认时，副作用工具不会执行，响应会返回 `CONFIRMATION_REQUIRED` 的 `ActionReceipt`。外部系统结果不明确时会显式标记，不能当作成功处理。

## 可靠性控制

```go
zhizhi.WithBudget(zhizhi.Budget{
    MaxSteps: 12, MaxBatches: 8, MaxModelCalls: 6,
    MaxToolCalls: 20, MaxReplans: 1, MaxParallelSteps: 4,
    HardTimeout: 30 * time.Second,
})
```

默认策略只重试瞬时、可安全重复且幂等的失败；写操作不会被静默重复执行。

## MCP 与可观测性

```go
import (
    "os"
    runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"
    "github.com/cocoyes/zhizhi-agent-runtime/observe"
)

agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithMCP(runtimemcp.Config{
        ID: "internal-tools", Transport: runtimemcp.TransportStreamableHTTP,
        Endpoint: "https://tools.example.com/mcp",
        AllowTools: []string{"customer.lookup", "ticket.search"},
        DenyTools: []string{"admin.delete"},
    }),
)
observer := observe.NewJSONL(os.Stdout)
```

MCP 工具属于不可信边界，详见 [SECURITY.md](SECURITY.md)。响应提供 `Evidence`、`Actions`、`Warnings` 和完整运行统计。

## 示例目录

| 示例 | 内容 | 命令 |
| --- | --- | --- |
| `00-chat` | 基础模型对话 | `go run ./examples/00-chat` |
| `01-stream` | 流式文本和工具事件 | `go run ./examples/01-stream` |
| `02-function-tool` | 类型化工具 Schema | `go run ./examples/02-function-tool` |
| `03-retry` | 瞬时错误重试 | `go run ./examples/03-retry` |
| `04-fallback` | 能力级 Fallback | `go run ./examples/04-fallback` |
| `05-local-mcp-server` | 本地 MCP 服务 | `go run ./examples/05-local-mcp-server` |
| `06-mcp-client` | MCP 发现与 Allowlist | `go run ./examples/06-mcp-client` |
| `07-complex-plan` | 模型规划执行 | `go run ./examples/07-complex-plan` |
| `08-observability` | JSONL 追踪 | `go run ./examples/08-observability` |
| `09-config-agent` | YAML 配置 | `go run ./examples/09-config-agent` |
| `10-full-agent` | 条件、绑定、replan 和组合 | `go run ./examples/10-full-agent` |

## 开发与验证

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

`make verify` 会执行标准本地检查。真实 provider 测试需要显式开启，默认测试套件离线且确定性可复现。

## 项目状态、安全与许可

当前版本：`v1.0.0`。涉及工具执行、副作用、计划语义和 provider 兼容性的变更都应配套专项测试。请私下报告安全漏洞，并附上受影响版本、最小复现和影响说明。详见 [SECURITY.md](SECURITY.md) 和 [LICENSE](LICENSE)。
