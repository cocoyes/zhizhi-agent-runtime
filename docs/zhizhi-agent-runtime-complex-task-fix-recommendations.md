# zhizhi-agent-runtime 复杂任务执行问题与修改建议

## 1. 文档目的

本文描述 `zhizhi-agent-runtime` 在复杂任务的计划、执行、失败重规划和最终结果合成链路中存在的问题，并给出建议的调整方案。

本文只讨论 runtime 自身的行为和内部设计，不涉及任何具体业务项目的接入实现。

## 2. 问题概述

复杂任务通常会被拆分为多个计划步骤。每个步骤至少包含：

- `step_id`：计划步骤的标识，用于表达依赖关系和引用执行结果。
- `capability`：步骤需要的能力，用于匹配真正可执行的工具。
- `tool_id`：runtime 最终选中并实际调用的工具。

当前实现没有始终严格区分这三个概念。在工具执行失败并触发重规划后，可能出现以下异常：

1. 重规划器把失败步骤替换为业务语义完全不同的 capability。
2. 替换 capability 时保留原有 `step_id`，导致步骤名称与实际操作不一致。
3. 最终结果合成阶段只展示 `step_id` 和结果，没有提供真实 `tool_id` 与 capability。
4. 最终模型因此可能把步骤 ID 当作工具名，并对工具实际完成的操作作出错误解释。
5. 重规划后的执行没有继承之前已经成功的步骤，可能重复执行读取操作甚至非幂等写操作。
6. 工具调用失败时，错误结果没有完整保留工具 ID、调用次数和失败信息，导致 trace 与统计失真。

这些问题叠加后，外部表现通常是：

- 日志中仿佛出现了一个没有注册过的工具。
- 某个写操作步骤返回了搜索、查询等无关结果。
- runtime 声称执行了某项操作，但实际工具没有执行该操作。
- 已完成步骤在每次重规划后被重复执行。
- trace 显示失败步骤的 `attempts` 为 0，或者没有实际工具名称。

## 3. 核心问题分析

### 3.1 重规划缺少 capability 语义兼容性约束

当前重规划主要依赖模型根据失败原因生成 plan patch。补丁通过校验的主要条件是：

- capability 能匹配到已注册工具。
- 工具输入符合 JSON Schema。
- 步骤依赖关系有效。

这只能证明新计划“结构上可执行”，不能证明替换后的工具仍然能够完成原步骤的业务目标。

例如，一个“创建提醒”步骤失败后，如果重规划器把 capability 改成“互联网搜索”，只要搜索工具存在且输入合法，当前结构校验就可能接受该补丁。但搜索并不是创建提醒的替代能力。

#### 修改建议

不要仅通过 prompt 要求模型选择“compatible fallback”，必须增加确定性的代码校验。

建议为工具能力增加显式语义元数据：

```go
type CapabilitySpec struct {
    Name          string
    SemanticGroup string
    Effects       []Effect
    Fallbacks     []string
}
```

替换规则建议为：

1. 默认只允许步骤继续使用原 capability。
2. 只有原 capability 明确声明的 fallback capability 才能用于替换。
3. 替代能力必须属于同一 `SemanticGroup`。
4. 替代能力的副作用等级不得弱化原目标，也不得从写操作变成只读操作后仍宣称目标完成。
5. 不允许仅凭名称相似度或模型判断建立替代关系。

重规划补丁应用前应增加类似校验：

```go
func ValidateCapabilityReplacement(before, after Step, catalog Catalog) error {
    if before.Capability == after.Capability {
        return nil
    }
    if !catalog.CanFallback(before.Capability, after.Capability) {
        return fmt.Errorf(
            "step %q cannot replace capability %q with incompatible capability %q",
            before.ID,
            before.Capability,
            after.Capability,
        )
    }
    return nil
}
```

### 3.2 `step_id` 与实际工具身份混淆

`step_id` 是计划图中的内部节点标识，可以使用 `query_keys`、`create_reminder` 等便于模型理解的名称，但它不是工具调用事实。

如果重规划后 capability 或工具发生变化，而 `step_id` 保持不变，任何只展示 `step_id` 的日志或结果都可能误导后续模型和开发人员。

#### 修改建议

所有执行结果必须同时携带：

```json
{
  "step_id": "create_reminder",
  "capability": "time_event.create",
  "tool_id": "calendar_create_event",
  "status": "success",
  "attempts": 1,
  "fallbacks": 0,
  "receipt": true,
  "content": {}
}
```

建议明确以下约束：

- `step_id` 只用于计划依赖和结果引用。
- `capability` 表示计划要求。
- `tool_id` 表示实际调用事实。
- 日志、Evidence、Warning 和最终合成输入不得用 `step_id` 代替 `tool_id`。
- 对用户描述执行事实时，必须以 `tool_id`、receipt 和结构化结果为准。

### 3.3 重规划后丢失已完成步骤

复杂任务发生失败后，runtime 会重新编译计划并重新创建 scheduler。如果新的 scheduler 没有继承此前成功步骤的结果，就会从头运行修改后的计划。

这会带来严重风险：

- 重复创建提醒、任务、订单等非幂等资源。
- 重复发送消息或通知。
- 重复调用收费的第三方接口。
- 前一次执行产生的真实结果没有进入最终 Evidence。
- 条件步骤可能基于另一轮查询结果执行，造成执行快照不一致。

#### 修改建议

runtime 应在整个 complex run 生命周期内维护统一的执行状态：

```go
type ExecutionState struct {
    PlanVersion int
    Completed   map[string]StepResult
    Failed      map[string]StepResult
    Invalidated map[string]struct{}
}
```

重规划后：

1. 保留未被替换且输入、依赖均未变化的成功步骤。
2. 只使失败步骤、被替换步骤以及受其影响的下游步骤失效。
3. 将保留的结果传入下一轮 scheduler 的 `Completed` 集合。
4. 对写操作，以 receipt 或明确的 action 状态判断是否已经执行。
5. 如果写操作结果处于 unknown/ambiguous 状态，禁止自动重试或换工具再次执行。

建议使用步骤指纹判断结果是否可复用：

```text
fingerprint = hash(capability + normalized_input + bindings + dependencies)
```

只有步骤 ID 和指纹都未变化时，旧结果才可以直接复用。

### 3.4 最终合成输入缺少执行事实

最终合成阶段如果只向模型提供：

```text
create_reminder: { ...result... }
```

模型很容易把 `create_reminder` 理解为实际工具名，也无法判断结果来自读取工具还是写入工具。

此外，仅要求“没有 receipt 时不得声称成功”并不足够。模型仍可能把无关工具的成功结果解释成原目标已经部分完成。

#### 修改建议

最终合成应接收结构化 Evidence，而不是由 `step_id` 拼接的文本：

```json
{
  "goal": "...",
  "outcome": "degraded",
  "evidence": [
    {
      "step_id": "create_reminder",
      "requested_capability": "time_event.create",
      "executed_capability": "web.search",
      "tool_id": "web_search",
      "status": "success",
      "receipt": false,
      "result": {}
    }
  ],
  "warnings": []
}
```

最终合成前还应增加确定性事实判定：

- 写操作只有 `status=success` 且存在有效 receipt 时才计为完成。
- 实际 capability 与请求 capability 不兼容时，该步骤必须计为未完成。
- 查询无结果应表达为 unknown/not_found，不得自动推导为某个相反事实。
- skipped 步骤不得进入成功 Evidence。
- 最终 outcome 不能只根据最后一轮 scheduler 是否报错决定。

### 3.5 工具失败结果丢失调用元数据

工具执行策略在所有候选工具失败后，如果只返回空结果和最后一个 error，会丢失：

- 最后实际调用的工具 ID。
- 已经执行的次数。
- fallback 次数。
- 每个候选工具的失败原因。

这会导致 trace 中出现 `attempts=0`、`tool_id` 为空等错误数据，也会影响调用预算统计。

#### 修改建议

无论成功还是失败，策略执行器都应返回完整结果：

```go
return Result{
    ToolID:    lastToolID,
    Attempts:  totalAttempts,
    Fallbacks: fallbackCount,
}, lastErr
```

如果存在多个候选工具，建议增加调用明细：

```go
type AttemptRecord struct {
    ToolID   string
    Attempt  int
    Started  time.Time
    Duration time.Duration
    Error    string
}
```

工具预算必须根据真实调用次数扣减，不能因为最终失败而忽略已经发生的调用。

### 3.6 条件执行缺少 unknown 语义

查询类工具返回空列表时，只能确认“没有查到记录”，不能证明待查询事实为 false。

二值条件模型容易把：

```text
location != "drawer"
```

错误地应用到“没有 location 值”的情况，从而把 unknown 当成 not-equals。

#### 修改建议

条件判断结果应为三值：

```go
type MatchResult string

const (
    MatchTrue    MatchResult = "true"
    MatchFalse   MatchResult = "false"
    MatchUnknown MatchResult = "unknown"
)
```

建议规则：

- source step 没有结果：`unknown`。
- source path 不存在：`unknown`。
- 查询结果为空：由工具输出显式提供 `found=false`，不要推断 location。
- `not_equals` 只在 source value 确实存在时进行比较。
- 条件为 unknown 时默认跳过分支，并产生结构化 warning；如计划声明了 unknown 分支，则执行该分支。

更适合物品查询的输出结构示例：

```json
{
  "found": false,
  "matches": [],
  "total": 0
}
```

计划应针对 `found` 建立分支，而不是直接引用可能不存在的 `matches[0].location`。

### 3.7 输出 Schema 与条件路径校验不完整

计划校验不仅需要检查 bindings，还应验证 condition 使用的 `source_path` 确实存在于来源工具的 Output Schema 中。

否则模型可以生成结构合法、运行时却永远无法判断的条件。

#### 修改建议

在计划和重规划校验中增加：

1. `condition.source_step` 必须存在并位于依赖链中。
2. `condition.source_path` 必须存在于来源工具 Output Schema。
3. `equals/not_equals` 的值类型必须与输出字段类型一致。
4. 数组元素路径必须有明确的 JSON Pointer 和越界语义。
5. Output Schema 为空时，不允许其他步骤绑定或判断其内部字段。

## 4. 推荐的重规划策略

建议将“失败后处理”分成确定性策略和模型重规划两层。

### 4.1 确定性失败策略

先根据工具元数据判断：

1. 是否允许重试。
2. 是否是幂等操作。
3. 是否存在显式 fallback。
4. 是否允许跳过。
5. 是否需要终止并返回 degraded/failed。

只有需要调整计划依赖或新增补偿步骤时，才调用模型重规划器。

### 4.2 模型重规划边界

模型可以决定：

- 删除已经无法执行的可选步骤。
- 调整下游依赖。
- 使用 runtime 明确列出的 fallback capability。
- 为缺失信息增加读取或澄清步骤。

模型不应自行决定：

- 任意选择另一个已注册工具替代失败工具。
- 重复执行已经成功的非幂等写操作。
- 把只读结果当作写操作 receipt。
- 把 unknown 数据解释为 false。

### 4.3 推荐的补丁校验顺序

```text
解码补丁
  -> 校验步骤与依赖结构
  -> 校验 capability 是否存在
  -> 校验 capability 替换兼容性
  -> 校验副作用与幂等约束
  -> 校验输入和 binding Schema
  -> 校验 condition 输出路径
  -> 计算需要失效的旧步骤
  -> 应用补丁
```

## 5. Trace 与可观测性建议

每次步骤执行至少记录：

```json
{
  "run_id": "...",
  "plan_version": 2,
  "step_id": "...",
  "requested_capability": "...",
  "resolved_tool_id": "...",
  "attempt": 1,
  "fallback_index": 0,
  "status": "failed",
  "effect": "write_non_idempotent",
  "receipt": false,
  "error_code": "...",
  "error_message": "...",
  "duration_ms": 12
}
```

重规划事件还应记录：

- 失败步骤 ID 和真实工具 ID。
- 旧 capability 与新 capability。
- 替换被允许的规则或 fallback 声明。
- 保留、失效和新增的步骤列表。
- 是否涉及已执行写操作。

当重规划尝试不兼容替换时，应产生明确错误：

```text
REPLAN_INCOMPATIBLE_CAPABILITY_REPLACEMENT
```

而不是进入下一轮执行。

## 6. 测试建议

至少增加以下测试。

### 6.1 不兼容 capability 替换

- 原步骤：写入类提醒创建 capability。
- 替换步骤：只读搜索 capability。
- 预期：补丁校验失败，搜索工具不执行。

### 6.2 显式兼容 fallback

- 两个工具声明同一 SemanticGroup，并互为允许 fallback。
- 第一个工具发生可回退错误。
- 预期：允许替换，最终 Evidence 记录真实 fallback 工具。

### 6.3 重规划保留成功步骤

- 第一批包含一个成功写步骤和一个失败步骤。
- 失败后重规划。
- 预期：成功写步骤只执行一次，下一轮从 Completed 状态复用。

### 6.4 写操作结果未知

- 工具在服务端可能已写入后返回连接中断。
- 预期：标记为 ambiguous，不自动重试、不自动调用替代写工具。

### 6.5 最终合成工具身份

- 步骤 ID 与工具 ID 不同。
- 预期：最终合成输入同时包含二者，输出不得把步骤 ID 描述成工具名。

### 6.6 查询空结果

- 查询返回 `found=false`。
- 预期：不执行依赖 `location != expected` 的分支；执行显式 unknown/not-found 分支或返回无法判断。

### 6.7 失败调用统计

- 工具实际调用一次后失败。
- 预期：`attempts=1`、`tool_id` 不为空，工具预算减少一次。

### 6.8 重规划多轮执行

- 连续产生多个 plan version。
- 预期：每一轮只执行新增或失效步骤，最终 Evidence 包含整个 run 的有效执行事实，而不只是最后一轮结果。

## 7. 建议的修复优先级

### P0：正确性与副作用安全

1. 重规划时保留已完成步骤，禁止重复执行已成功的非幂等写操作。
2. 增加 capability 替换兼容性硬校验。
3. ambiguous 写操作禁止自动重试或替换执行。

### P1：事实一致性

1. 最终 Evidence 同时包含 step ID、capability、tool ID、status 和 receipt。
2. 最终合成不得仅使用 step ID 标识执行操作。
3. 工具失败时保留真实调用元数据和调用次数。

### P2：计划表达能力

1. 引入三值条件语义。
2. 完善 Output Schema 和 condition path 校验。
3. 支持显式 unknown/not-found 分支。

### P3：可观测性

1. 增加 plan patch 的差异记录。
2. 记录步骤复用和失效原因。
3. 增加不兼容重规划、重复副作用拦截等专用错误码和指标。

## 8. 验收标准

修复完成后，应满足以下条件：

- 未注册的工具不能被执行，也不能在最终回答中被描述为已调用工具。
- 步骤 ID 永远不会被当作真实工具 ID 使用。
- 失败写步骤不能被只读能力替换后视为完成。
- 重规划不会重复执行已成功且仍然有效的步骤。
- 工具失败后 trace 仍能准确显示工具 ID 和实际调用次数。
- 查询无结果不会被解释为相反事实成立。
- 最终回答中的每个完成态陈述都能对应到兼容工具的成功结果和有效 receipt。
- 多轮重规划后的统计、Evidence、Warnings 和最终 outcome 与整个 run 的真实执行历史一致。
