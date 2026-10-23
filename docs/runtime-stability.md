# 规划稳定性修复与迁移（2026-10-23）

本次修复统一首次规划和 patch 的步骤契约，并将调用预算移到实际调用之前。没有修改业务工具或调用方 module 缓存；没有发布版本或变更标签。

## 配置与零值

内置 planner/replanner 默认最多 repair 一次（此前两次）。首次恢复默认关闭，显式开启：

```go
repairs := 1
a, err := zhizhi.New(
    zhizhi.WithModel(m),
    zhizhi.WithTools(tools...),
    zhizhi.WithBudget(zhizhi.Budget{
        MaxModelCalls: 8, MaxToolCalls: 4,
        HardTimeout: 60 * time.Second,
    }),
    zhizhi.WithPlannerRecovery(zhizhi.PlannerRecoveryPolicy{
        MaxRepairAttempts: &repairs,
        InitialFallback: zhizhi.InitialFallbackBoundedAgentic,
        MaxFallbackTools: 2,
    }),
)
```

`MaxRepairAttempts == nil` 表示默认一次，指向 0 表示禁止 repair，指向 2 可显式保留旧重试次数。负数配置拒绝。`InitialFallback` 的空值或 `disabled` 表示关闭。`bounded_agentic` 要求 `MaxFallbackTools > 0`，该值限制实际调用次数，与目录长度无关。请求级 `MaxModelCalls`、`MaxToolCalls` 沿用 0 为不设上限的语义；建议宿主显式设置有限值。步骤 `tool_budget.max` 必须为正数。

YAML 对应 `runtime.planner_recovery`：

```yaml
runtime:
  max_model_calls: 8
  max_tool_calls: 4
  hard_timeout: 60s
  planner_recovery:
    max_repair_attempts: 1
    initial_fallback: bounded_agentic
    max_fallback_tools: 2
    disable_output_tool: false
```

默认通过 synthetic function tool 发送共享的步骤联合 Schema。这不是提供方 strict-schema 支持承诺。若服务不接受联合 tool schema，设 `disable_output_tool: true`，使用同一协议的 Schema 提示及 JSON mode（仅模型声明支持时启用）。两种模式均在本地严格解码并验证同一 Schema、步骤语义、目录和输入。提供方 HTTP/能力错误直接返回，不自动换协议或恢复。自定义 `ModelPlanner`、`ModelReplanner` 可直接设置 `MaxRepairAttempts` 和 `DisableOutputTool`。

## 输出契约

新模型输出必须明确写 `mode`。tool 步骤必需 `id`、`capability`，可用静态 input、bindings、依赖与 condition。agentic 步骤必需非空 `goal`、`success_criteria`、非空且可解析的 `capabilities` 和有限 `tool_budget`，禁止 capability 和 bindings 混入。`0 <= min <= max`、`0 <= required_successes <= max`；两者间不另设大小约束。未知字段和弱类型转换拒绝。

```json
{
  "version": 1,
  "steps": [{
    "id": "lookup-chain",
    "mode": "agentic",
    "goal": "依据真实工具观察处理请求",
    "success_criteria": "如缺少必要信息则询问，不猜测查询结果",
    "capabilities": ["lookup"],
    "tool_budget": {"min": 0, "max": 2, "required_successes": 0}
  }]
}
```

自定义 Go planner 和历史内存对象仍可通过 `Plan.Normalize` 将空 mode 的 tool 步骤明确归一化；不会从标题或 ID 补全 agentic goal。历史零值冗余字段可规范化，新模型输出采用严格 Schema。`remove_steps` 仅用字符串数组；历史对象数组需宿主在版本化迁移边界转换。patch 保留计划 ID、Goal，Version 加一。默认禁止模式切换，agentic 替换不得扩大原步骤工具集合；tool 替换仍需目录声明的 fallback、语义组和副作用兼容性。

Schema-less 工具输出不能成为确定性 binding 或 condition 的字段来源。此类运行时观察串联应使用受限 agentic 步骤。

## 恢复和权限

仅首次规划候选校验耗尽、尚无任何领域工具 dispatch、请求有效、上下文有效、仍有模型预算且配置开启时恢复。代码构造一个步骤，不再向模型请求第二份执行图。恢复目录来自宿主已经筛选的当前请求目录，使用该目录全部工具时不会称为最小集合。guard、确认与实际参数 Schema 每次调用继续检查。工具局部上限取配置与请求剩余量较小值。无工具或工具余额为零时进入明确禁止工具的一次回答/澄清。

提供方、取消、超时、预算错误不触发首次恢复；replan 和 Resume 也不会触发首次恢复。恢复路径执行失败不会重新生成图来刷新局部预算。恢复结果保守使用现有 `degraded` 状态及 `INITIAL_PLAN_RECOVERED` 警告；恢复发生本身不表示用户目标已完成。

已成功写入不会因 patch 改动参数、依赖或能力而重放；已有写入后也不能通过新增节点 ID 发起新的写入。写结果未知，以及步骤先成功写入后在其他操作失败时，停止图并基于真实事实生成降级结果。声明 `SideEffectUnknown` 的工具即使传输成功，若没有可信 Action 回执，仍保守记录 unknown 并停止后续自动执行。agentic 审批 continuation 保存既有回执、消息、待执行原始调用和局部消费量，审批仅匹配确切 tool ID 与参数。

## 硬预算和 checkpoint

每个 runtime 发起的 Generate/Stream 在调用前原子预占一次模型配额，包括目录选择、规划、repair、agentic、replan、最终合成及 middleware 重试。Stream chunk 不另计费。每次实际领域工具 dispatch（失败、retry、fallback 也算）在输入校验后预占一次工具配额；并行步骤共用账本。middleware 转发不重复收费，内部再次实际调用则另计。

宿主模型内部不透明网络重试、failover，以及自定义 planner/工具自行绕开 runtime 模型或 dispatch 边界的外部请求，不由此账本统计。宿主应把这些请求接入受限公共边界；不能据此声称限制了所有提供方网络请求。

checkpoint 升至 version 2，持久化消费量、原始限额和最早绝对截止时间；交互等待计入 HardTimeout。Resume 使用原始限额和当前宿主限额中更严格者，不重新获得时间或额度。version 1 明确拒绝，因为旧复杂 checkpoint 可能缺少真实消费；不得默认补零。升级前应处理或终止旧待审批任务。

`Store.Delete` 现在是单次审批消费边界：必须原子删除，记录不存在时返回 `checkpoint.ErrNotFound`。durable store 必须满足此约定，防止多实例同时恢复同一审批。审批在 dispatch 前消费；失败后旧 checkpoint 不可重用。需要再次审批时生成新的 checkpoint。业务服务仍负责幂等约束和未知写状态查询。

## 错误和安全事件

声明 `var failure *plan.StructuredOutputError`，通过 `errors.As(err, &failure)` 可取得 Phase、Kind、RFC 6901 Path、Attempts、RepairAttempts、Recoverable 和 Cause。Phase 是 `initial_plan` 或 `replan`；Kind 为 candidate_missing、decode、schema、semantic、capability_reference。Attempts 包含首次生成，RepairAttempts 只计修复。Recoverable 仅表示输出错误类别可能恢复。repair 的模型错误保留原始原因及上一校验错误，Recoverable=false。

`RuntimeError` 保留 Unwrap；取消、超时、预算、模型调用和结构输出类别独立。`errors.Is(err, zhizhi.ErrBudgetExceeded)` 继续有效。结构输出 Error() 不携带 candidate、参数或用户正文；底层 Cause 只供受控诊断使用，不应直接写到公共日志。

默认 observer Data 新增固定字段：

| 事件 | 字段 |
| --- | --- |
| planner.candidate.invalid / replan.candidate.invalid | attempt、kind、path、model_id、latency_ms |
| planner.repair.started / replan.repair.started | repair_attempt、attempt、kind、path、model_id |
| planner.repair.completed / replan.repair.completed | repair_attempt、validation_succeeded、latency_ms、kind、path、model_id |
| planner.recovery.started / completed | strategy、reason_code、allowed_tool_count、remaining_model_calls、remaining_tool_calls；completed 另有 outcome |
| budget.exhausted | dimension、limit、used、operation |

`model.completed` 仍只表示模型响应完成。raw repair response 位于 `repair.output` 的 Details；`repair.completed` 表示校验完成。普通 error 事件字段采用固定错误码，不复制底层业务错误正文。JSONL 仅在显式 `observe.WithDetails()` 时输出敏感 payload，宿主需限制访问和留存；指标标签使用 kind/phase/strategy，避免用户内容或步骤 ID。

调用方升级后应删除自建 `InitialPlanRecovery` 注册、错误字符串匹配和重复恢复。先灰度启用，可单独关闭 initial fallback；Schema、预算和脱敏修复仍然生效。

本地脚本回归不能证明所有兼容服务接受联合 Schema。发布前需用实际提供方小样本验证支持性、首轮合法率、repair/恢复率及 P95 延迟；本次没有据此声明真实提供方已验收。
