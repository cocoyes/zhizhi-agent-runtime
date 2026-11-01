# 按需发现与执行 Skills

状态：方案 B 与详细设计已获用户批准；实现与验证记录见对应执行计划。

## 目标与边界

为 zhizhi-agent-runtime 增加本地 Skill 能力。应用传入磁盘目录，框架扫描其中的 SKILL.md，将 name、description、绝对目录及版本信息保存在内存。支持多目录和重复加载。主 LLM 默认只接收两个固定 function tools：skills.search 与 skills.invoke。全部 Skill 清单和正文均不进入默认 prompt。

search 返回有限候选，LLM 选择后 invoke 才加载正文并执行。这里的“确定”是 LLM 选择；人工授权仍由宿主现有 Guard、Confirmation 决定，搜索不构成副作用授权。

第一版不引入向量数据库、文件监听、任意 shell 执行器或 Skill 生成的输入 schema。用户提供的 Skill 目录属于受控部署输入；框架不声称在攻击者可并发替换文件的目录中提供操作系统级隔离。

## 已选方案及替代方案

已选择 B：固定元工具 + 服务端检索 + 内部执行器。它保持模型工具目录稳定，支持运行中发现能力，适合现有 Tool 接口与 planner。

A：运行前选择并注入 Skill 正文。实现较小，但缺少模型显式调用过程，运行中发现不足。

C：搜索后将每个 Skill 动态注册成工具。表现直观，但要求修改 simple、complex、stream、resume 的工具目录生命周期，且普通 SKILL.md 没有函数 schema。第一版不采用。

## 现有架构及依赖方向

- mcp.CapabilityProvider 已实现按需选择 provider，但 Select 会向模型发送完整 provider 元数据清单，Skill 不复制这一行为。
- tool.Tool 提供 Spec、ModelSpec、Call；Registry.ModelSpecs 生成模型函数目录。
- 每次运行有独立 registry、modelCounter 和 budget.Ledger，新增能力必须继承它们。
- internal/runtime/react.Runner 可承载 Skill 内部执行，并产生证据、动作和 continuation。
- checkpoint.Store 支持持久化与单次授权认领；不能用进程内 map 替代恢复状态。

新增公开 skill 包负责目录、解析、检索与协议数据。根包负责把其接入 Agent 和内部 ReAct runner。skill 包不导入根包，也不依赖 internal/runtime/react。根包通过适配器实现 Executor，避免循环依赖。

## 公开 API 草案

```go
catalog := skill.NewCatalog(skill.CatalogOptions{})
report, err := catalog.LoadRoot(ctx, "/srv/skills")
// 对相同规范化根目录再次调用：重新扫描并原子替换该根的快照。
report, err = catalog.LoadRoot(ctx, "/srv/skills")

agent, err := zhizhi.New(
    zhizhi.WithModel(llm),
    zhizhi.WithTools(applicationTools...),
    zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{
        AllowedTools: []string{"documents.read", "documents.write"},
    }),
)
```

名称与签名在实现计划中确认，但上述行为构成契约。AllowedTools 是应用拥有的显式权限上限，空集合表示无底层业务工具；不能隐式继承全部工具。实际子工具集合取 AllowedTools、运行时过滤后工具集合和策略的交集。不得授予 skills.search、skills.invoke 或间接 Skill 执行入口。

建议公开 Catalog、Metadata、LoadReport、Searcher、Executor 及稳定 sentinel errors。只有实际有第二实现需求的可替换部分保留接口，不为每个内部结构建立接口。

## 目录加载与内存模型

Metadata 保存 name、description、Dir、SkillFile、Root、SKILL.md SHA-256。Catalog 保存 root 快照和全局 generation。Metadata 返回值为副本，不暴露内部 map。

LoadRoot 接受一个 Skill 目录或包含多个 Skill 的根目录。规范化为绝对路径并解析根目录 symlink；递归扫描普通文件 SKILL.md，不跟随目录 symlink。遇到一个 Skill 根后不再把其 references、scripts、assets 内的 SKILL.md 注册为其他 Skill。

通过现有 yaml.v3 读取文件开头的 YAML front matter，要求 name、description 为非空字符串；支持引号和多行 description，允许未知字段。UTF-8、文件大小、front matter 大小、Skill 数量、扫描深度均有可配置上限。默认建议正文 1 MiB、front matter 64 KiB、单 root 10,000 Skills、深度 32。

初始化阶段按文件流解析 metadata 并计算正文 fingerprint，随后丢弃正文；不会把完整正文留在内存或交给模型。只有 invoke 才将正文装入执行上下文。

先在锁外构建完整候选快照；发布时短暂加锁，重新检查跨 root 同名冲突后整体交换。同 root 并发 LoadRoot 串行化，读操作不持有扫描锁。相同内容重复加载是 no-op。更新、删除和新增以该 root 的完整快照为准；任何扫描、解析、权限、冲突错误均保留旧快照。报告 added、updated、removed、unchanged 及 generation。

重叠 root 默认拒绝，避免同一 Skill 被重复归属。不同 root 的同名 Skill 默认报错，不静默覆盖。name 为稳定逻辑标识，不作为文件路径或模型函数名拼接使用。

## search 协议

```json
{"query":"读取并整理 PDF 内容","limit":5}
```

响应只包含 selection_id、候选 name、description、fingerprint 和截断提示。绝对目录保存在服务端，不在搜索结果中泄露。

默认 limit=5，硬上限 10。空白查询拒绝；无结果返回空候选且不签发 selection_id。Searcher 使用规范化 name 和 description 的确定性文本排名：精确名称优先，名称匹配其次，描述匹配再次；同分按 name 排序。必须兼顾中文子串和拉丁词项，不使用仅依赖空格的英文分词。第一版以中英文代表性查询验证召回，不承诺语义等价。Searcher 接口允许应用接入自有检索器；最终结果必须经过 Catalog 存在性及数量校验。

selection_id 是服务端随机不可预测标识，绑定本次执行作用域、候选名称与 fingerprint。每次运行的候选数和票据总量有上限。票据有效期默认 15 分钟；只保存本次候选，不保存全清单。有效期内允许对同一候选发起多次显式调用；票据不提供幂等保证，任何写操作按底层工具策略执行且 invoke 不自动重试。

主工具描述明确：当任务可能依赖可安装工作流时先 search，无匹配时使用已有能力；不得声称某个 Skill 已执行。search 工具为只读、安全、可重试。

## invoke 协议和执行

```json
{"selection_id":"...","skill_name":"pdf","task":"提取指定报告的结论"}
```

invoke 校验作用域、有效期、候选成员资格和当前文件 fingerprint；任一不符返回稳定错误，要求重新搜索。不能仅接受任意名称或磁盘路径。打开后读取的同一份字节既用于 hash 又用于执行，避免先校验再重读正文。Catalog reload 不影响已开始执行的正文快照；尚未开始的调用必须匹配当前元数据。

Executor 接收已加载的 Skill、任务以及宿主提供的运行作用域。正文作为有边界的工作流说明进入子执行上下文，宿主 system policy 始终保留优先级。显式传入必要任务上下文，不默认复制主会话全部历史或敏感数据。

返回结构包括 status、text、skill_name、fingerprint、evidence、actions。成功只表示内部 runner 达成执行终止条件；有 unknown action 时不得标记成功。子执行失败仍保留已经产生的 evidence/actions，外层响应不得因 tool error 丢掉它们。

invoke 的 Spec 声明其为委托执行，禁止自动重试。不能使用默认 tool.Func 的 read/safe 声明掩盖内部写动作，也不能用 SideEffectUnknown 的现有拦截逻辑把已成功的所有 invoke 都归为 unknown。实现时需增加明确的复合执行语义，由实际子工具报告效果和确认要求；这是共享运行时契约，不能靠字符串命名绕过。

具体采用向后兼容的 tool.Result 可选 Composite 字段，包含全部 Evidence、Actions、Status，以及暂停时的 PendingInvocation 和版本化 Continuation。Continuation 是有类型标识的 JSON 状态；由注册的根包适配器解码，禁止序列化 Go 函数或 context。普通工具继续使用现有 Content/Action。复合调用即使同时返回 error，也必须先由公共结果处理器收集证据和动作，再决定失败或暂停；simple、stream 和 scheduler 都通过这个处理器处理。scheduler 的内部 StepResult 相应扩展为完整集合，原单 Action 字段保持兼容但不再作为复合执行的唯一事实来源。

## 引用资源与脚本

内部 runner 提供限定于当前 Skill 的资源读取能力，按相对路径读取 references、scripts 和文本模板，单文件及累计读取量有上限。路径拒绝绝对地址、.. 越界和 symlink 越界，解析后的路径必须落在当前 Skill 根中；读取记录带资源 fingerprint。第一版不自动把二进制资产塞进 prompt。

SKILL.md 引用脚本时，runner 可读取脚本，但执行必须通过应用显式授权的工具。框架不新增通用 shell 权限。外部业务文件由业务 tools 访问，不能通过 Skill 资源接口越界读取。

## 运行作用域与预算

每次 Run/Stream 建立 SkillScope，持有候选票据、执行状态和权限快照。全局 Catalog 只管理文件目录，不保存用户执行状态，避免并发 session 串用。

元工具在普通工具和按需选择的 MCP 工具完成过滤、包装后绑定，并经相同 middleware。子工具采用已经包装的工具实例，不再重复包装；共享当前 counted model、budget context、取消和绝对截止时间。元工具调用本身和实际子工具调用分别计数，均只计一次。子模型调用同时计入本地上限和 request 总账。

内部最大迭代默认 8，最大子工具调用默认 16，最大执行深度 1。以请求剩余额度为上限，不创建独立无限预算。局部参数只能收紧。复杂计划并行步骤拥有各自 invocation ID，共享请求总账。

权限交集里的运行时工具集合明确指 request 级 registry，而非只含元工具名称的 agentic step registry。planner 在步骤中授权 skills.invoke，表示允许该步骤使用应用预先配置的 Skill 委托权限；它不授予配置之外的工具。此语义需要写入 planner 的工具说明和公开文档。第一版不增加 step 级 delegated allowlist；需要更细权限的应用使用 request filter 或建立更小权限的 Agent 实例。

## 确认与恢复

当子工具要求确认，向外传播结构化 suspended 状态，而非普通 error 后从头调用 invoke。checkpoint 保存外层 pending call、invocation ID、Skill 与已读资源 fingerprint、权限工具指纹、票据候选、内部 ReAct continuation、已完成证据/动作、累计预算和原始 deadline。

每一种 checkpoint 均保存整个 SkillScope 的候选 grants 和活动 invocation，包括 search 后因普通工具暂停的场景。Resume 在任何 dispatch 前恢复 scope；普通暂停不延长候选原始 TTL。只有已经开始执行并持久化为 continuation 的 invoke 享有下文的恢复豁免。无 SkillScope 的历史 checkpoint 按空 scope 处理。

Resume 先重建本次 SkillScope 与工具绑定，再校验外层和内部目录指纹。Skill 正文、已读资源或权限发生变化时返回 drift；不得悄悄执行新版本。原票据 TTL 对新的 invoke 生效，已持久化且通过校验的 pending continuation 通过 checkpoint 授权继续，无需用户重新 search。确认精确绑定内部 pending tool ID 与参数，不能把批准 invoke 当作批准所有后续子调用。

沿用 Store.Delete 的单次认领语义，在任何恢复 dispatch 前消费授权。恢复从未执行的具体子调用继续，绝不重放之前的写。拒绝确认按现有降级终止行为返回证据。发生结果未知的写操作不得自动重试。

simple、complex agentic step、stream、resume 必须共用复合调用处理逻辑；不能在四处复制四套 Skill 状态机。现有 checkpoint opaque State 可增加版本化嵌套字段；旧无 Skill 的 v2 checkpoint 应继续兼容。若公共格式必须改版本，需提供明确兼容策略并在实现前记录。

## 可观测性和错误

沿用 observer，增加 skill.catalog.loaded、skill.search.completed、skill.invoke.started/completed/failed/suspended/resumed。关联 run ID、invocation ID、skill name、generation、fingerprint、耗时和数量。默认不记录正文、任务全文、资源内容或绝对路径；详细诊断遵守现有 opt-in details 行为。

对调用者暴露可 errors.Is 匹配的 not found、invalid metadata、duplicate name、selection expired、selection mismatch、catalog drift、resource denied。错误包含必要定位信息，但不把服务端路径默认发给模型。模型可以修复的搜索/选择错误应返回结构化 Tool Result，基础设施和预算错误沿既有错误链终止。

## 验证与交付

仅增加风险相关的定向测试，不运行重复全量 build。关键验证：

1. root 首次/重复/删除加载、冲突与失败保持旧快照；并发读与 reload。
2. 中英文检索、Top-K 上限、默认模型输入没有完整 Skill 清单或正文。
3. invoke 只加载选中 Skill；跨 scope 票据拒绝、版本漂移拒绝。
4. 资源路径与 symlink 越界拒绝、大小限制和取消。
5. 权限交集、禁止递归、预算只计一次、内部错误保留动作证据。
6. simple/complex/stream 下暂停恢复不重复写；并发 Resume 只 dispatch 一次。

提供不依赖真实网络的 fake-model 示例和中英文 README。公共 API 文档解释加载与执行区别、生命周期、权限和重载语义。新增导出类型有 GoDoc。首批实现按目录与检索、执行集成、恢复与文档分段完成，每段定向验证。

## 实现者可决定的细节

在保持上述契约前提下，文件拆分、内部锁结构和排名分值可由实现者决定。建议文件为 skill/catalog.go、loader.go、search.go、provider.go、errors.go，以及根包 skill.go 与内部复合执行处理模块。不在本次顺便重构 MCP 或整个 agent.go。
