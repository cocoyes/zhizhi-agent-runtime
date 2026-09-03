# zhizhi-agent-runtime 改进设计：Eino 能力替换与生产化

> 状态：设计草案  
> 日期：2026-09-03  
> 适用项目：`github.com/cocoyes/zhizhi-agent-runtime`  

## 1. 文档目的

本文定义 `zhizhi-agent-runtime` 为替换 `github.com/cloudwego/eino` 所需补齐的通用能力、API 方向、优先级和验收标准。

本文只讨论通用 Agent Runtime 应负责的机制。目标不是复制 Eino 的全部生态，而是：

1. 覆盖模型调用、工具执行、ReAct、流式处理、结构化输出、回调和 MCP 等 Eino 核心能力；
2. 保持模型、工具、消息、流式和观测接口具备框架级扩展性；
3. 强化现有 runtime 在计划执行、副作用安全、证据和审计方面的优势；
4. 让下游应用能够在不依赖 Eino/Eino Ext 类型的情况下完成迁移。

## 2. 核心结论

`zhizhi-agent-runtime` 当前已经具备工具 Schema、普通工具循环、复杂计划、DAG 并行、条件执行、binding、replan、预算、MCP、安全策略、Evidence 和 Action Receipt 等核心能力，基础方向正确。

完全替换 Eino 前需要优先补齐以下框架机制：

1. 表达文本、图片等内容的通用多模态消息协议；
2. 完整的模型调用选项，包括具名工具强制调用和通用结构化输出；
3. `Run` 与 `Stream` 共享同一执行状态机，支持多轮工具调用和复杂任务流式事件；
4. 每次运行动态注入工具、调用选项和元数据，而不要求每请求重新创建 Agent；
5. 细粒度、可组合的 Agent/Model/Tool/Planner 回调或中间件；
6. 中断、确认、checkpoint 和安全恢复；
7. 准确的跨轮 token、调用次数、耗时和结束原因统计；
8. MCP 的懒连接、刷新、断线恢复和故障隔离；
9. 明确的并发安全、生命周期、错误分类和版本兼容契约。

模型候选选择、持久化熔断状态、应用响应协议和会话存储不属于 runtime。

## 3. 职责边界

### 3.1 runtime 必须负责

| 能力 | runtime 的职责 |
| --- | --- |
| 消息协议 | 提供 provider 无关的角色、文本、图片、音频、文件、工具调用和工具结果表达 |
| 模型抽象 | 定义 Generate、Stream、能力声明、调用选项和标准结果 |
| 结构化输出机制 | 接受 JSON Object/JSON Schema 约束并传给模型适配器；不定义调用方 Schema |
| 工具抽象 | 工具描述、输入输出 Schema、调用、流式调用、动态工具集和中间件 |
| Agent 循环 | 模型—工具—模型的多轮循环、上限、取消、错误和最终结果 |
| 计划执行 | planner、DAG、并发、条件、binding、fallback、replan 和最终合成 |
| 安全机制 | 工具风险、幂等性、副作用、确认、receipt、未知执行结果保护 |
| 中断恢复 | suspend、checkpoint、resume 和恢复后的副作用去重 |
| 流式机制 | 文本增量、推理增量、工具调用增量、步骤事件、最终结果和错误事件 |
| 可观测性 | 生命周期事件、回调/中间件、统计和敏感数据控制 |
| MCP | 协议接入、工具发现、allow/deny、连接生命周期和不可信边界 |
| 生命周期 | Agent 可长期复用、Run 请求级隔离、Close 释放资源、并发安全 |

### 3.2 runtime 不负责

- 模型候选的来源、排序、价格策略和持久化健康状态；
- 用户、账号、会话、历史消息和长期记忆的存储；
- 调用方特有的分类枚举、Prompt 和 JSON Schema；
- HTTP、SSE、WebSocket 或其他传输协议；
- 具体工具的领域逻辑和权限规则；
- 应用日志表、审计表和执行轨迹的持久化实现。

runtime 应提供 Model、ResponseFormat、Guard、Confirm、Observer、Metadata 等通用接口，由调用方注入具体实现或策略。

## 4. Agent 生命周期约定

### 4.1 推荐生命周期

```go
// 服务启动时创建一次。
agent, err := zhizhi.New(...)

// 每个请求独立 Run/Stream，可并发调用。
response, err := agent.Run(requestCtx, request, runOptions...)

// 服务退出时关闭。
err = agent.Close(shutdownCtx)
```

Agent 应为服务级长生命周期对象，不应要求调用方每次请求重新 `New`。

每请求重新创建 Agent 会导致：

- 重复构造和校验工具 Schema；
- 重复建立 MCP 连接；
- 无法有效复用 HTTP Transport、连接池和 provider session；
- 更难实现统一限流、健康检查和资源回收；
- 初始化失败会直接进入请求关键路径。

### 4.2 请求隔离

长期复用不等于保存应用会话状态。Agent 实例只保存不可变配置或并发安全的共享资源；一次运行的消息、计划、证据、工具调用和统计必须保存在独立的 run state 中。

runtime 必须保证：

- 同一个 Agent 可以被多个 goroutine 并发调用；
- 一个 Run 的取消不会影响其他 Run；
- `context.Context` 从入口原样传递到 Model、Tool、Planner、Replanner、Guard 和 Observer；
- runtime 内部不得把请求级值写入 Agent 实例字段；
- `Close` 与正在运行的请求之间有明确契约。

`Request.UserID`、`Request.SessionID` 不是执行引擎必需字段。建议将其移出核心请求，或者降级为不参与 runtime 语义的通用 Metadata。工具需要额外调用信息时，由调用方包装器从 `context.Context` 或自定义 Metadata 读取。

## 5. Eino 核心能力基线

第一阶段替换聚焦常用核心能力，而不是复制 Eino 的全部组件生态。

| Eino 使用面 | 典型用途 | runtime 当前状态 | 目标 |
| --- | --- | --- | --- |
| `schema.Message` | 上下文、图片、tool call、usage | 仅文本和 tool call | P0 补齐多模态与标准元数据 |
| `ChatModel.Generate` | 普通生成、结构化生成 | 已支持 | P0 完善调用选项 |
| `ChatModel.Stream` | 模型流式响应 | 基础支持 | P0 与 Agent 多轮执行统一 |
| `WithTools` / ToolChoice | ReAct、分类、planner | Tools 在 ModelInput 中 | P0 支持 auto/none/required/named |
| ResponseFormat/JSON Schema | 分类和结构化生成 | 只有 JSONMode | P0 提供通用 Schema 机制 |
| `tool.BaseTool` / `InvokableTool` | 本地与 MCP 工具 | 已有自有 Tool | P0 动态工具与 middleware；P1 流式工具 |
| `react.Agent` | 多轮工具循环 | Run 已有基础循环 | P0 统一 Run/Stream 并补足结果语义 |
| callbacks | OTel、usage、trace | Observer 粒度较粗 | P0 生命周期回调与统计 |
| official MCP adapter | MCP 工具发现与调用 | 已有自有实现 | P1 补连接韧性和动态发现 |
| compose graph | 当前主要由 ReAct 间接使用 | runtime 有专用 DAG | 不要求复制 Eino Compose |

Eino 的 Retriever、Embedding、Document Loader、ChatTemplate、完整通用 Compose、ADK 多 Agent 等不属于第一阶段替换的前置条件。

## 6. P0：完全替换前必须完成

### 6.1 通用多模态消息

现有 `Message.Content string` 应升级为可表达多个内容块的协议，同时保留纯文本便捷方法。

建议：

```go
type ContentType string

const (
    ContentText     ContentType = "text"
    ContentImageURL ContentType = "image_url"
    ContentAudioURL ContentType = "audio_url"
    ContentFileURL  ContentType = "file_url"
)

type ContentPart struct {
    Type     ContentType
    Text     string
    URL      string
    MIMEType string
    Detail   string
}

type Message struct {
    Role             Role
    Content          []ContentPart
    ReasoningContent string
    Name             string
    ToolCallID       string
    ToolCalls        []ToolCallRequest
    ResponseMeta     *ResponseMeta
}
```

要求：

- 提供 `TextMessage`、`UserMessage`、`AssistantMessage` 等便捷构造函数；
- OpenAI-compatible adapter 正确转换 text/image content parts；
- 历史 tool call、tool result 和 reasoning content 可无损回传；
- 不在 runtime 中加入对象存储校验、访问权限或应用特有的附件类型；
- 未支持某种内容的 adapter 必须返回明确能力错误，不能静默丢弃。

### 6.2 完整模型调用契约

`ModelInput` 应支持以下通用选项：

```go
type ToolChoice struct {
    Mode ToolChoiceMode // auto, none, required, named
    Name string
}

type ResponseFormat struct {
    Type        ResponseFormatType // text, json_object, json_schema
    Name        string
    Description string
    Schema      json.RawMessage
    Strict      bool
}

type ModelInput struct {
    Messages       []Message
    Tools          []ToolSpec
    ToolChoice     ToolChoice
    ResponseFormat *ResponseFormat
    Options        map[string]any
}
```

关键点：

- `required` 表示必须调用任意工具；`named` 表示必须调用指定工具，不能混为一谈；
- JSON Schema 的传递与校验是 runtime 能力，具体 Schema 由调用方提供；
- adapter 应声明实际支持的能力；不支持 strict schema 时返回能力错误或按调用方允许的策略降级；
- 不把数据库模型选择、熔断和供应商优先级放入 runtime；调用方可以实现组合 `model.Model`；
- provider 特有字段通过 adapter 配置或受控 Option 扩展，核心包不感知供应商名称。

暂不要求 Claude adapter。第一阶段完善 OpenAI-compatible adapter，但核心接口不得阻碍未来新增其他 adapter。

### 6.3 Run 与 Stream 使用同一状态机

当前 `Run` 和 `Stream` 是两套行为：Stream 只处理一轮工具调用，工具完成后的最终回复也不再流式输出，并且不覆盖 complex 模式。

应改成一个内部执行状态机，由两个输出适配器消费：

```text
prepare
  -> model call
  -> model deltas / complete message
  -> zero tool calls: finish
  -> tool calls: validate -> authorize -> execute
  -> append tool results
  -> next model call
  -> ... until finish/budget/error/suspend
```

`Run` 聚合全部事件后返回最终结果；`Stream` 实时返回同一批事件。两者的工具轮数、预算、错误、usage、receipt 和最终语义必须一致。

标准事件至少包括：

```go
type EventType string

const (
    EventRunStarted       EventType = "run.started"
    EventModelStarted     EventType = "model.started"
    EventTextDelta        EventType = "model.text.delta"
    EventReasoningDelta   EventType = "model.reasoning.delta"
    EventToolCallDelta    EventType = "model.tool_call.delta"
    EventToolStarted      EventType = "tool.started"
    EventToolCompleted    EventType = "tool.completed"
    EventToolFailed       EventType = "tool.failed"
    EventPlanCreated      EventType = "plan.created"
    EventStepStarted      EventType = "step.started"
    EventStepCompleted    EventType = "step.completed"
    EventRunSuspended     EventType = "run.suspended"
    EventRunCompleted     EventType = "run.completed"
    EventRunFailed        EventType = "run.failed"
)
```

调用方可以把这些事件映射为任意传输协议，但 runtime 不依赖具体协议。

### 6.4 每次运行的动态工具与 Option

Agent 长期复用时，工具集不一定对所有请求完全相同。应支持：

```go
response, err := agent.Run(ctx, request,
    zhizhi.WithRunTools(tools...),
    zhizhi.WithRunToolFilter(filter),
    zhizhi.WithRunObserver(observer),
    zhizhi.WithRunMetadata(metadata),
)
```

要求：

- 构造期工具作为默认静态工具集；
- Run 级工具只在当前运行可见；
- 支持按能力或策略筛选实际暴露给模型的工具；
- 重复 ID 必须返回错误，不能静默覆盖；
- 工具列表在 Run 开始时形成不可变快照；
- Run 级 Option 不修改 Agent 的全局配置。

这也是实现“先选择能力、再加载具体工具”的基础，具体选择规则由调用方决定。

### 6.5 Agentic Step 与确定性 Step

现有 complex runtime 偏向“一个计划步骤直接调用一个 capability/tool”。为了覆盖完整的 Agent 执行，还需要“一个目标步骤内由模型进行有限次工具选择和观察”的能力。

runtime 应支持两种通用步骤：

1. Deterministic Tool Step：输入已经明确，直接调用指定 capability，适合 DAG、binding 和并行；
2. Agentic Step：给定目标、成功标准、允许工具和工具调用预算，在步骤内部运行有限 ReAct 循环。

建议通用结构：

```go
type StepMode string

const (
    StepModeTool    StepMode = "tool"
    StepModeAgentic StepMode = "agentic"
)

type ToolCallBudget struct {
    Min               int
    Max               int
    RequiredSuccesses int
}

type Step struct {
    ID              string
    Mode            StepMode
    Goal            string
    SuccessCriteria string
    Capabilities    []string
    ToolBudget      ToolCallBudget
    // dependencies, condition, bindings, input, optional...
}
```

Agentic Step 必须使用 allowlist 后的工具快照，并受到 ModelCalls、ToolCalls、MaxIterations、deadline 和 side-effect policy 约束。

这属于通用执行能力，不包含具体应用的 planner Prompt、分类枚举或工具名称。

### 6.6 回调与中间件

当前 Observer 适合记录事件，但不足以完全替代 Eino callbacks。需要同时提供：

- Agent middleware；
- Model middleware；
- Tool middleware；
- Planner/Replanner middleware；
- 流输入和流输出生命周期；
- 构造期全局 middleware 与 Run 级 middleware；
- before/after/error 三类稳定钩子。

建议 middleware 使用 next 模式，Observer 保持只读：

```go
type ModelHandler func(context.Context, model.ModelInput) (*model.ModelOutput, error)
type ModelMiddleware func(next ModelHandler) ModelHandler

type ToolHandler func(context.Context, ToolInvocation) (tool.Result, error)
type ToolMiddleware func(next ToolHandler) ToolHandler
```

Observer 不得修改执行；Middleware 可以实现 trace、指标、限流、审计或授权包装。

runtime 不直接依赖 OpenTelemetry，但应提供独立 adapter，把 model/tool/step/run 映射为 span。

### 6.7 准确统计和结果契约

所有模型调用的 usage 必须累加，不能只保留最后一轮：

- 普通 Agent：所有 ReAct 轮次；
- complex：router（若启用）、planner、每个 Agentic Step、replanner、final composer；
- Stream：流式 usage 与工具后的最终调用；
- 失败或 suspend：返回截至当前时刻的部分统计。

统一结果应包含：

```go
type RunStats struct {
    ModelCalls       int
    ToolCalls        int
    SuccessfulTools  int
    FailedTools      int
    Retries          int
    Fallbacks        int
    PlanSteps        int
    Replans          int
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    Duration         time.Duration
}
```

所有结束路径必须有稳定的 `Outcome`、`FinishReason` 和可 `errors.Is/errors.As` 的标准错误。

### 6.8 正确性修复

P0 同时修复以下问题：

- 普通 Run 的 usage 当前被每轮结果覆盖，应改为累加；
- complex 的统计不能固定写成 `2 + replans`，应按真实调用计数；
- complex usage 不能只取 final composer；
- Registry 遇到重复工具 ID 必须失败；
- 所有忽略的 `json.Marshal` 错误都应处理；
- Evidence 必须填写一致的 `CapturedAt`、StepID、ProviderID、SchemaValid；
- Stream 的 tool call delta 必须处理非连续 index、重复片段和异常 EOF；
- RunID 生成器应可注入并保证并发唯一，不应只依赖纳秒时间；
- budget 应在真正发起调用前检查，不能发生一次超额调用后才报错；
- 模型返回 nil output 时必须转换为标准错误，不能 panic；
- 所有 goroutine 必须响应 context 取消并可通过 goleak/race 测试。

## 7. P1：生产化必须完成

### 7.1 Suspend、Checkpoint 和 Resume

当前确认不足时返回 `CONFIRMATION_REQUIRED`，但没有可恢复的执行状态。调用方若重新运行整个请求，可能重新执行已经成功的步骤。

runtime 应提供：

```go
type CheckpointStore interface {
    Save(context.Context, Checkpoint) error
    Load(context.Context, string) (*Checkpoint, error)
    Delete(context.Context, string) error
}

type SuspendReason string

type ResumeRequest struct {
    RunID      string
    Checkpoint string
    Decision   any
}
```

Checkpoint 应记录计划版本、已完成步骤、Evidence、Action Receipt、待确认调用、预算消耗和模型上下文。具体存储介质由调用方实现 Store。

恢复时必须保证：

- 已有成功 receipt 的写操作不会重复执行；
- `UNKNOWN` 状态不会自动重试；
- plan/tool catalog 发生变化时能检测 drift；
- checkpoint 有版本号和兼容校验；
- 取消、超时、进程重启后的恢复语义明确。

### 7.2 MCP 连接生命周期

MCP 属于 runtime 的通用集成能力，但初始化方式需要增强：

- 支持 eager 和 lazy 两种连接策略，生产默认建议 lazy；
- 单个 MCP 不可用不能导致整个 Agent 无法创建，除非配置为 required；
- 并发首次访问只建立一个连接；
- 请求取消可以中断正在进行的连接；
- 支持 refresh、断线重连和退避；
- Close 有独立超时，能够关闭活动连接；
- catalog refresh 生成版本和 fingerprint；
- 已编译计划执行前校验 catalog drift；
- Header、token 等敏感信息不进入 Observer；
- 保留 allowlist、denylist、不可信描述校验和默认高风险策略。

### 7.3 流式工具

为了接近 Eino 的 Tool 能力，应增加可选的流式工具接口：

```go
type StreamableTool interface {
    Tool
    Stream(context.Context, json.RawMessage) (ToolStream, error)
}
```

普通 Tool 不应被强迫实现 Stream。runtime 应能把普通结果包装成单事件流，也能聚合流式工具结果供非流式 Run 使用。

### 7.4 配置和能力协商

在执行前校验：

- 模型是否支持工具调用；
- 模型是否支持图片等内容类型；
- 模型是否支持 streaming；
- 模型是否支持 JSON Schema/JSON Object；
- 并行工具调用是否受支持；
- 当前降级策略是否允许。

不允许在 adapter 中静默丢字段。所有降级必须显式、可观测，并能由调用方禁止。

## 8. P2：接近 Eino，但不阻塞当前替换

以下能力能够增强 runtime 的通用性，但不属于第一阶段替换的前置条件：

1. 通用 `Runnable[I,O]` 和可编译 Workflow；
2. Chain、Graph、Branch、共享 State 和字段映射；
3. stream fan-out、merge、copy 和 collect/transform 四种范式；
4. Graph/Workflow 暴露为 Tool；
5. 多 Agent supervisor、handoff 和 sub-agent；
6. Retriever、Embedding、Document Transformer 等标准组件接口；
7. 可视化调试和执行图导出。

建议不要在 P0 阶段复制 Eino Compose。优先把 Agent、Tool、Model、Plan 和 Stream 做稳定；出现明确的通用静态工作流需求后，再抽象 Compose。

## 9. 建议包结构

```text
zhizhi-agent-runtime/
  agent.go                 # 稳定的 Agent/Request/Response 接口
  runoption.go             # Run 级 option
  model/                   # provider 无关模型和多模态消息
  tool/                    # Tool、Schema、middleware、registry
  react/                   # 多轮 agentic loop
  plan/                    # 语义计划和校验
  planner/                 # Planner 接口及默认实现
  exec/                    # DAG 和 Agentic Step 调度
  replan/                  # Patch 与重规划
  stream/                  # 通用流、聚合、复制和关闭语义
  middleware/              # Agent/Model/Tool middleware
  observe/                 # 只读事件和统计
  checkpoint/              # suspend/resume 接口与内存实现
  mcp/                     # MCP provider 和 catalog 生命周期
  adapter/model/openaicompat/
  adapter/observe/otel/
  runtimeerr/              # 稳定错误类型和错误码
  contract/                # Evidence、Receipt、Outcome 等稳定契约
```

adapter 可以依赖第三方 SDK，但 `model`、`tool`、`plan`、`exec` 等核心包应保持 provider 和应用无关。

## 10. API 稳定性要求

项目当前虽然声明 `1.0.0`，但尚无正式 Git tag。对外提供稳定依赖前应建立：

- 正式 SemVer tag；
- public API compatibility policy；
- Deprecated 至少保留一个 minor release；
- CHANGELOG 记录 breaking change；
- 每个 public interface 和 option 有 GoDoc；
- examples 编译测试；
- `go vet`、`go test`、`go test -race`、goleak 和 lint CI；
- 支持的最低 Go 版本声明；
- adapter 与 provider 的兼容矩阵。

不建议下游生产环境长期使用指向相邻目录的 `replace`。联调期可以使用 workspace，正式集成前应发布可复现的 tag 或 pseudo-version。

## 11. 测试与验收

### 11.1 模型契约测试

- 文本、图片和混合 content parts；
- 多个 tool calls 和并行 tool calls；
- auto/none/required/named tool choice；
- JSON Object 和严格 JSON Schema；
- reasoning content 回传；
- 非 2xx、429、超时、取消、空响应和超大响应；
- SSE 分片、跨 chunk tool arguments、异常 EOF 和 usage 尾包。

### 11.2 Agent 一致性测试

对同一 scripted model/tool 场景，分别执行 Run 和 Stream，聚合后的结果必须一致：

- 无工具普通回复；
- 单轮单工具；
- 单轮并行工具；
- 多轮工具；
- 工具失败；
- 模型失败；
- budget exhausted；
- context cancelled；
- confirmation suspend/resume；
- complex plan/replan/final compose。

### 11.3 并发和生命周期测试

- 同一 Agent 100 个并发 Run 不串数据；
- Run 取消不影响相邻 Run；
- Run 级工具和 Observer 不泄漏到下一请求；
- MCP 并发懒连接只有一个 session；
- Close 后拒绝新 Run；
- Close 等待或取消在途 Run 的语义符合文档；
- `go test -race ./...`；
- goleak 无 goroutine 泄漏。

### 11.4 副作用测试

- 非幂等写操作不自动重试；
- UNKNOWN receipt 不当作成功；
- confirmation 前工具未执行；
- resume 不重复执行已有成功 receipt 的步骤；
- fallback 不跨越不兼容的副作用策略；
- 工具 output 违反 Schema 时不能进入可信 Evidence。

### 11.5 Eino 替换验收

完成替换必须同时满足：

- 下游应用能够在生产代码和测试代码中移除 `github.com/cloudwego/eino`；
- 下游 `go.mod/go.sum` 不再直接依赖 Eino/Eino Ext；
- 文本、多模态、结构化输出、普通工具循环、复杂任务和 MCP 全部通过契约测试；
- runtime 不要求下游采用特定的 HTTP、SSE、数据库或模型路由实现；
- 关键链路具备可导出的 trace 和准确 token 统计；
- 写工具没有重复执行回归；
- 对比测试中的成功率、延迟和资源消耗达到发布阈值。

## 12. 实施顺序

### Milestone 1：模型与消息契约

- 多模态 Message；
- ToolChoice named；
- ResponseFormat JSON Schema；
- 标准 usage/finish reason/error；
- OpenAI-compatible adapter 契约测试。

完成标志：调用方能够实现 runtime `model.Model`，多模态和结构化调用不再需要 Eino Schema。

### Milestone 2：统一 Agent 执行

- Run/Stream 共用状态机；
- 多轮及并行 tool call；
- Run 级 tools/options；
- middleware/callback；
- 准确统计和 P0 正确性修复。

完成标志：普通生成和 ReAct 工具链可以替换 Eino。

### Milestone 3：复杂任务执行

- Deterministic Step + Agentic Step；
- 工具调用预算；
- plan/replan/final compose 全量统计；
- complex 流式事件；
- OTel adapter。

完成标志：Plan-Execute-Replan 与步骤内 ReAct 可以由 runtime 独立完成。

### Milestone 4：生产韧性

- suspend/checkpoint/resume；
- MCP lazy/reconnect/refresh；
- race/goleak/故障注入；
- tag、兼容策略和迁移指南。

完成标志：runtime 达到候选发布质量，下游可以灰度切换并删除 Eino 依赖。

## 13. 明确不做

本轮替换不要求：

- 在 runtime 中访问应用数据库；
- 在 runtime 中实现模型候选来源、排序或熔断落库；
- 支持 Claude；
- 内置特定应用的分类、响应、记忆或会话模型；
- 感知 HTTP、SSE、WebSocket 或特定服务框架；
- 复制 Eino 全部 Retriever/Embedding/Document 生态；
- 在 P0 阶段实现通用可视化 Graph Builder；
- 为兼容下游而让核心包依赖 Eino 类型。

## 14. 参考

- Eino 官方仓库：https://github.com/cloudwego/eino
- Eino 官方文档：https://www.cloudwego.io/docs/eino/
- Eino Examples：https://github.com/cloudwego/eino-examples

参考 Eino 时重点借鉴接口完整性、流式语义、回调切面、中断恢复和组件组合能力；不需要复制其全部 API，也不应牺牲 `zhizhi-agent-runtime` 已经具备的副作用安全、Evidence、Receipt、DAG 和可审计性。
