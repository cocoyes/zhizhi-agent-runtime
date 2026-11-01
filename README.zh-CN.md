# zhizhi-agent-runtime

<p align="center">
  <img src="zhizhi-logo.png" alt="Zhizhi agent runtime" width="720">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/cocoyes/zhizhi-agent-runtime.svg)](https://pkg.go.dev/github.com/cocoyes/zhizhi-agent-runtime)
[![Go Report Card](https://goreportcard.com/badge/github.com/cocoyes/zhizhi-agent-runtime)](https://goreportcard.com/report/github.com/cocoyes/zhizhi-agent-runtime)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

[English](README.md) · [知枝官网](https://deepyou.top) · [API 文档](https://pkg.go.dev/github.com/cocoyes/zhizhi-agent-runtime) · [更新日志](CHANGELOG.md)

**知枝（Zhizhi）是一个用 Go 构建 AI Agent 的运行框架。** 接入模型、把业务函数注册为工具，Agent 就可以根据用户需求调用工具完成任务。对于多步骤任务，框架提供规划、并行执行、条件分支和有限次数的失败恢复。

你可以把它嵌入 Go 服务、智能助手或自动化应用：业务侧提供模型和工具，知枝负责工具调度、预算控制、审批暂停与恢复，以及执行证据记录。

## 可以用来做什么

- **智能助手**：对话、流式输出、多模态输入和类型化函数调用。
- **任务自动化**：ReAct 工具循环，以及带依赖关系和失败恢复的 Plan-and-Execute 工作流。
- **外部能力接入**：连接 MCP 服务，按需发现和加载本地 Skill。
- **文档处理**：在一次性 Docker 容器中运行经过授权的 Skill Python 脚本。
- **可审计的业务流程**：权限检查、操作确认、断点恢复、执行追踪和动作回执。
- **语音应用**：独立的实时语音 API 和豆包双工语音适配器。

## 安装

需要 **Go 1.25+** 和模型服务。内置适配器支持 OpenAI 兼容的 Chat Completions 接口，也可以实现 `model.Model` 接入其他模型。工具调用场景需要模型支持 Tool Calling。Docker 为可选依赖，仅执行 Skill 脚本时需要。

```bash
go get github.com/cocoyes/zhizhi-agent-runtime@v1.1.1
```

## 快速开始

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
    "github.com/cocoyes/zhizhi-agent-runtime/adapter/model/openaicompat"
)

func main() {
    model := openaicompat.New(openaicompat.Config{
        BaseURL: os.Getenv("LLM_BASE_URL"),
        APIKey:  os.Getenv("LLM_API_KEY"),
        Model:   os.Getenv("LLM_MODEL"),
    })
    agent, err := zhizhi.New(zhizhi.WithModel(model))
    if err != nil { log.Fatal(err) }
    defer agent.Close(context.Background())

    response, err := agent.Run(context.Background(), zhizhi.Request{
        Input: "用三句话介绍 AI Agent 能做什么",
    })
    if err != nil { log.Fatal(err) }
    fmt.Println(response.Text)
}
```

```bash
export LLM_BASE_URL="https://your-provider.example/v1"
export LLM_API_KEY="your-api-key"
export LLM_MODEL="your-model"
go run ./examples/00-chat
```

在自己的 Go module 中将上述代码保存为 `main.go`，设置环境变量后执行 `go run .`。如果使用仓库示例，请先克隆仓库，在仓库根目录运行 `go run ./examples/00-chat`。`LLM_BASE_URL` 填 API 基础地址，例如 `https://your-provider.example/v1`，适配器会追加 `/chat/completions`。

下文片段基于快速开始，假设 `model` 已配置、`ctx` 为当前请求的 Context。请补充标注的包导入，在使用 Agent 前处理初始化错误，并在结束时关闭 Agent 和流。

## 把 Go 函数注册为工具

普通 Go 函数通过 `tool.Func` 转为工具，框架自动生成 JSON Schema，并在运行时校验参数和结果：

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
    "Get the current weather for a city",
    func(ctx context.Context, in WeatherInput) (WeatherOutput, error) {
        return WeatherOutput{City: in.City, Condition: "sunny", Temperature: 27}, nil
    },
    tool.WithCapabilities("weather.current"),
)
agent, err := zhizhi.New(zhizhi.WithModel(model), zhizhi.WithTools(weather))
```

导入 `github.com/cocoyes/zhizhi-agent-runtime/tool`，再向 Agent 询问城市天气即可。上面的天气结果是演示数据，实际使用时将函数体替换成业务 API 调用。工具描述应说明用途和适用场景，工具 ID 应保持稳定。参见[完整工具示例](examples/02-function-tool/main.go)。

## 流式输出

```go
stream, err := agent.Stream(ctx, zhizhi.Request{Input: "给我讲个小故事"})
if err != nil { return err }
defer stream.Close()
for {
    event, err := stream.Recv(ctx)
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    fmt.Print(event.TextDelta)
}
```

接收循环需要导入 `errors` 和 `io`。流中也包含工具和执行生命周期事件，参见[流式示例](examples/01-stream/main.go)。

## 执行多步骤任务

```go
ctx := context.Background()
mode := zhizhi.RunModeAgentComplex
response, err := agent.Run(ctx, zhizhi.Request{
    Input: "根据深圳天气安排今晚的活动",
    Mode: &mode,
})
```

先注册完成任务所需的工具，再选择复杂模式。框架会生成依赖图、传递前置步骤结果、并行运行独立步骤，并在必需步骤失败时按预算重规划。参见[完整 Agent 示例](examples/10-full-agent)和[家庭事务工作流](examples/11-household-workflow)。

## 接入 MCP 服务

```go
// import runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"
agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithMCP(runtimemcp.Config{
        ID: "internal-tools",
        Transport: runtimemcp.TransportStreamableHTTP,
        Endpoint: "https://tools.example.com/mcp",
        AllowTools: []string{"customer.lookup", "ticket.search"},
    }),
)
```

默认懒连接，通过 `AllowTools` 限制可用工具。连接多个服务时，可组合 `mcp.NewCapabilityProvider` 与 `zhizhi.WithMCPCapabilityProvider`，先让模型选择服务，再加载该服务的工具列表。参见 [MCP 客户端示例](examples/06-mcp-client/README.md)。

## 按需加载本地 Skill

Skill 是包含 `SKILL.md` 的目录。文件头部提供名称、描述，正文说明如何完成任务：

```text
skills/
└── greeting/
    └── SKILL.md
```

```markdown
---
name: greeting
description: Compose a short greeting for a named person.
---
Ask for the person's name if it is missing, then write a friendly greeting.
```

```go
// import "github.com/cocoyes/zhizhi-agent-runtime/skill"
catalog := skill.NewCatalog(skill.CatalogOptions{})
if _, err := catalog.LoadRoot(ctx, "./skills"); err != nil {
    return err
}
agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{}),
)
```

`LoadRoot` 将名称、描述和目录索引到内存。重复调用会原子更新该根目录，扫描失败时保留原有索引。模型通过 `skills.search` 查找候选，通过 `skills.invoke` 选定后才加载正文，不会在每次请求中暴露全部 Skill。

如果 Skill 需要业务工具，使用 `WithTools` 注册，并在 `SkillOptions.AllowedTools` 中列出允许的工具 ID；空列表不授予业务工具。参见 [Skill 使用指南](docs/runtime-skills.md)和[离线示例](examples/14-skills)。

### 使用 Docker 执行经过授权的 Python 脚本

安装 Docker Engine，加载 Skill 目录，再显式注册允许执行的入口。下面假设目录中已有 `test-skill`，并包含 `scripts/hello.py`：

```go
// import "github.com/cocoyes/zhizhi-agent-runtime/skill/script"
// import dockerscript "github.com/cocoyes/zhizhi-agent-runtime/skill/script/docker"
executor, err := dockerscript.New(ctx, dockerscript.Config{
    Enabled: true,
    Catalog: catalog,
    Image: "python:3.12-slim",
    AutoPull: true,
    Registrations: []script.Registration{{
        SkillID: "test-skill",
        Scripts: []script.EntryPoint{{Path: "scripts/hello.py"}},
    }},
})
if err != nil { return err }
defer executor.Close(context.Background())

agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{
        ScriptExecutor: executor,
    }),
)
```

只有选定 Skill 的内部执行器会获得 `run_skill_script`。每次调用使用独立临时容器，执行后销毁容器、复用镜像；任务输出文件可通过执行器读取。宿主机无需安装 Python，也无需手动创建容器。开启 `AutoPull` 后，首次运行会按需下载镜像。

默认使用非 root 用户、只读 Skill 挂载、禁用网络，并限制 CPU、内存、执行时间和输出。该能力面向经过审核的 Skill。Docker API 权限很高，需要保护持有该权限的服务进程。参见[配置与安全边界](docs/docker-skill-scripts.md)、[可运行示例](examples/15-docker-skills/README.md)和[依赖镜像 Dockerfile](examples/15-docker-skills/Dockerfile)。

## 控制执行并查看结果

通过 `context.Context` 传递请求超时和取消信号，通过 `WithBudget` 设置执行上限：

```go
zhizhi.WithBudget(zhizhi.Budget{
    MaxSteps: 12, MaxBatches: 8, MaxModelCalls: 6,
    MaxToolCalls: 20, MaxReplans: 1, MaxParallelSteps: 4,
    HardTimeout: 30 * time.Second,
})
```

有副作用的工具应声明 `tool.WithSideEffect`、`tool.WithIdempotency` 和 `tool.WithConfirmation`。使用 `WithGuard` 检查权限，使用 `WithConfirmation` 接入审批决策。执行暂停后，可通过 checkpoint 恢复：

```go
agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithTools(sendEmail),
    zhizhi.WithCheckpointStore(durableStore),
)

if err != nil { return err }
defer agent.Close(context.Background())

pending, err := agent.Run(ctx, request)
if err != nil { return err }
if pending.Suspended {
    completed, err := agent.Resume(ctx, zhizhi.ResumeRequest{
        RunID: pending.RunID, Checkpoint: pending.Checkpoint, Decision: true,
    }) // Pass the same run options here if the original request used them.
    if err != nil { return err }
    fmt.Println(completed.Text)
}
```

上例假设已注册 `sendEmail` 工具，并准备好 checkpoint store 和请求变量。恢复时需要提供相同的请求级工具；跨进程恢复应使用持久化 checkpoint store。

响应中包含 `Text`、`Evidence`、`Actions`、`Warnings` 和执行统计。可添加 `zhizhi.WithObserver(observe.NewJSONL(os.Stdout))` 记录事件，或使用 `adapter/otel` 接入 OpenTelemetry。详细 JSONL 需要显式开启，可能包含业务数据。

## 示例目录

多数模型示例读取 `LLM_BASE_URL`、`LLM_API_KEY` 和 `LLM_MODEL`，参见 [.env.example](.env.example)。示例 14 可离线运行；示例 15 使用确定性模型和真实 Docker Engine。

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
| `11-household-workflow` | 真实模型驱动的并行、条件绑定与故障重规划 | `go run ./examples/11-household-workflow` |
| `12-doubao-realtime` | 实时双工语音 | [README](examples/12-doubao-realtime/README.md) |
| `13-parallel-search` | 通过 Parallel Search MCP 进行无需密钥的可选网页研究 | `go run ./examples/13-parallel-search` |
| `14-skills` | 离线 Skill 发现、按需加载和受限执行 | `go run ./examples/14-skills` |
| `15-docker-skills` | 在临时 Docker 容器内执行已批准的 Python 脚本 | `go run ./examples/15-docker-skills` |

## 进一步阅读

- [架构与扩展点](docs/architecture.md)
- [Skill 加载和执行契约](docs/runtime-skills.md)
- [Docker 脚本配置与安全](docs/docker-skill-scripts.md)
- [实时语音接入](docs/doubao-realtime-speech.md)
- [运行时兼容性与迁移](docs/runtime-stability.md)
- [版本更新记录](CHANGELOG.md)

## 安全与许可

发现安全问题请按照 [SECURITY.md](SECURITY.md) 私下报告。本项目采用 [MIT License](LICENSE)。
