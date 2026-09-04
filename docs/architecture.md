# 代码层级与包边界

本项目采用 Go 的“少量公共包 + `internal` 实现包”布局。目录数量不是独立目标；真正的目标是让顶层目录等同于稳定、可解释的用户编程面，并让运行时编排细节可以在不破坏调用方的前提下演进。

## 分层

```text
应用与示例
    ↓
zhizhi（Agent 门面、配置、运行入口）
    ↓
公共能力与扩展点
    ├── model / tool / plan
    ├── observe / middleware
    ├── checkpoint / policy / route
    ├── mcp / adapter
    └── contract
    ↓
internal
    ├── runtime/exec       计划编译、依赖绑定与调度
    ├── runtime/react      有界模型—工具循环
    └── structuredoutput   结构化模型输出解码
```

依赖只能向下。`internal` 可以依赖公共能力包，公共能力包不能依赖根包 `zhizhi`；只有 `eval` 这类面向调用方的测试工具可以以根包 API 为输入。适配器通过 `model`、`tool`、`middleware`、`contract` 等窄接口接入，不反向依赖 Agent 的具体实现。

## 顶层包为什么保留

| 包 | 职责 | 保持公开的理由 |
| --- | --- | --- |
| `model` | 模型协议和流 | 自定义模型适配器的必要接口 |
| `tool` | 工具、注册表、能力解析 | 业务工具的主要编程面 |
| `plan` | 计划模型、规划与重规划 | 自定义 Planner/Replanner 的扩展点 |
| `observe` | 事件与追踪 | 可观测性接入点 |
| `middleware` | 各执行边界中间件 | 横切能力扩展点 |
| `checkpoint` | 挂起状态和持久化接口 | 外部持久化实现需要导入 |
| `policy` | 重试、回退和失败决策 | 业务安全策略需要定制 |
| `route` | 请求模式选择 | 自定义路由器需要导入 |
| `mcp` | MCP Provider 配置 | 用户直接配置和管理连接 |
| `contract` | 跨包稳定 DTO | 避免核心包之间形成循环依赖 |
| `adapter` | 第三方集成 | 隔离厂商 SDK 和可选依赖 |
| `eval` | Agent 评测辅助 | 面向库调用者，而非运行时实现 |

不要新增 `common`、`utils`、`types`、`core` 之类按技术形态聚合的包。包应该按稳定业务职责命名；无法给出单一职责时，通常意味着它应留在使用方包内。

## 何时使用 `internal`

满足下列任一条件时，默认放入 `internal`：

- 只有根运行时或仓库内适配器使用；
- 类型出现在实现流程中，但不应成为用户配置或扩展接口；
- 预计会随调度算法、模型输出兼容策略或安全规则频繁演进；
- 暴露后会迫使调用方理解 Agent 内部状态机。

反之，只有当外部调用方需要实现接口、构造值、读取稳定结果，或独立复用该能力时，才建立顶层包。不要仅为减少单文件长度而建包。

## 本次归并

| 原路径 | 新路径 | 设计依据 |
| --- | --- | --- |
| `planner`、`replan` | `plan` | 都属于计划生命周期，同一聚合边界 |
| `resolve` | `internal/runtime/exec` | 输入绑定是执行器实现细节 |
| `replay` | `observe` | 回放是观测数据的生命周期操作 |
| `capability` | `tool` | 能力解析依赖并服务于工具注册表 |
| `exec` | `internal/runtime/exec` | 调度器只由 Agent 门面驱动 |
| `react` | `internal/runtime/react` | 循环预算和状态机是运行时机制 |
| `security` | `mcp/security.go` | 当前只有 MCP 使用，不为单个辅助职责建包 |
| `runtimeerr` | 根包 `errors.go` | 结构化错误是 Agent 的公开结果契约，通过 `zhizhi.RuntimeError` 暴露 |

## 新包评审清单

新增一级目录前需同时回答：

1. 外部调用方是否必须直接导入它？
2. 它是否拥有独立且稳定的领域词汇，而不是若干辅助函数？
3. 它能否通过窄接口与相邻包交互，且不会制造循环依赖？
4. 未来修改它时，是否愿意承担公开 API 的兼容成本？

任一答案是否定的，应优先放入现有包或 `internal`。
