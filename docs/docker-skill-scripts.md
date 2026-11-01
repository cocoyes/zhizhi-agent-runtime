# Docker Skill 脚本执行

## 现有架构与接入位置

| 问题 | 仓库现状及本次复用方式 |
| --- | --- |
| Skill 注册、加载、管理 | `skill.Catalog.LoadRoot` 原子扫描及重载；本次继续使用它，不建立第二个 Skill 注册表。脚本注册表仅表达应用授予的执行权限。 |
| SKILL.md 解析字段 | YAML front matter 的 `name`、`description`；其他字段不授予脚本权限。元数据还保存 SHA-256。 |
| 路径保存 | `skill.Metadata` 的 `Dir`、`SkillFile`、`Root` 为绝对路径，JSON 默认不暴露它们。 |
| LLM 获得指引 | `skills.search` 返回少量候选，`skills.invoke` 校验选择票据后读正文并创建内部 ReAct runner。 |
| 既有文件或命令工具 | 有受限的 `skill.read_resource`，此前没有进程执行或通用 Workspace；本次不新增 Shell。 |
| Registry 和工具调用 | 复用 `tool.Tool`、`Registry`、JSON Schema、请求过滤器、middleware、Guard、Confirmation 和调用预算。 |
| ReAct 与 Plan | `skills.invoke` 本身可作为普通计划步骤或 agentic 步骤内工具，脚本工具在选定 Skill 的内部 runner 中执行。 |
| Context、超时、错误 | 继承请求取消和绝对截止时间，叠加执行器超时；沿用结构化结果、动作回执和复合工具事实。 |
| Workspace | 新增脚本任务 Workspace；同一 run 内跨 Skill 调用复用，跨 run 默认分离，引用保存到原 checkpoint。 |
| Executor | 原 `skill.Executor` 是“文档解释/工作流执行”接口，保持不变。新增 `skill/script.Executor` 表达独立的脚本进程边界，可扩展远程沙箱。 |

模块依赖为：Agent → `skill/script` 接口；应用 → `skill/script/docker` 实现 →
官方 `github.com/moby/moby/client v0.6.2`。未设置 `SkillOptions.ScriptExecutor`
时不访问 Docker、不创建 Workspace、不暴露脚本工具。SDK 是 Go 模块依赖，
宿主机没有 Python 运行依赖。

本次文件清单：

```text
新增 skill/script/script.go                         接口、结果、错误码、批准清单
新增 skill/script/script_test.go                    注册与路径验证
新增 skill/script/docker/config.go                  默认配置、镜像去重、关闭管理
新增 skill/script/docker/engine.go                  官方 SDK 的窄适配边界
新增 skill/script/docker/execute.go                 容器执行与有界输出流
新增 skill/script/docker/files.go                   快照、Workspace、输出检验与读取
新增 skill/script/docker/open*.go                   非阻塞安全文件打开及平台边界
新增 skill/script/docker/*_test.go                  单元、安全与真实 Docker 测试
新增 skill_script.go / skill_script_test.go          Skill 工具绑定与恢复集成
修改 skill.go / skill/catalog.go                    可选执行器、作用域和二进制资源读取
修改 agent.go / internal/runtime/react/react.go     生命周期与失败事实保留
修改 go.mod / go.sum                               官方 SDK 依赖
新增 examples/15-docker-skills/                     完整示例、Skill、脚本与 Dockerfile
修改 README.md / README.zh-CN.md                    使用入口
新增 docs/docker-skill-scripts.md                   架构、API、配置、安全及扩展说明
新增 docs/superpowers/plans/2026-11-01-docker-scripts.md  实施与验证记录
```

## 初始化

```go
import (
    "context"
    zhizhi "github.com/cocoyes/zhizhi-agent-runtime"
    "github.com/cocoyes/zhizhi-agent-runtime/skill"
    "github.com/cocoyes/zhizhi-agent-runtime/skill/script"
    dockerscript "github.com/cocoyes/zhizhi-agent-runtime/skill/script/docker"
)

ctx := context.Background()
catalog := skill.NewCatalog(skill.CatalogOptions{})
if _, err := catalog.LoadRoot(ctx, "/srv/audited-skills"); err != nil {
    return err
}
executor, err := dockerscript.New(ctx, dockerscript.Config{
    Enabled: true,
    Catalog: catalog,
    AutoPull: true,
    Image: "python:3.12-slim", // 生产环境建议批准固定版本或 digest
    WorkspaceRoot: "/srv/agent-artifacts",
    Registrations: []script.Registration{{
        SkillID: "pdf-generator",
        Scripts: []script.EntryPoint{{Path: "scripts/generate.py"}},
        Files: []string{"templates/report.json"},
    }},
})
if err != nil { return err }
defer executor.Close(ctx)

agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{
        ScriptExecutor: executor,
    }),
)
if err != nil { return err }
defer agent.Close(ctx)
```

`New` 必须显式 `Enabled:true`，只做配置、文件批准和 Docker Ping，不安装或修改
Daemon，不在初始化时强制拉镜像。`AutoPull` 默认 false；示例明确开启。调用方
负责关闭共享 Executor；`Agent.Close` 不会关闭可能被其他 Agent 使用的客户端。

## 信任与批准

`Registration` 来自可信 Go 配置，不来自 SKILL.md 或模型。它固定正文与每个
显式列出的脚本/资源文件的 SHA-256，只复制这些文件到执行快照。允许每个 Skill
通过 `Registration.Image` 使用不同的预批准 Python 镜像；`Registration.Env` 也是
可信配置，不复制宿主进程环境。可通过 `Registration.Fingerprint` 对接外部审核
记录，要求它匹配已审核的 Skill 正文。

脚本文件存在、Skill 被搜索到、或者正文要求执行，均不等于授予脚本权限。
重载不会自动批准新脚本或新内容；更新注册与 Executor 才能更新信任。
Docker 第一版只接受 `.py` 入口。脚本依赖的额外 Python 模块/资产必须显式列在
`Files` 中，或预先放在批准的镜像里。每个快照最多 256 个注册路径、32 MiB。
不自动执行 requirements.txt，不运行 pip install，不执行模型生成的命令。

基础参数限制为 32 个参数、每项 4096 字节、合计 16 KiB，并拒绝 NUL。
业务约束用 `EntryPoint.ValidateArgs`；设置校验器时必须提供非空 `Version`，
改变校验逻辑时也应改变 Version，让暂停恢复检测到策略漂移。例如只允许无参入口：

```go
script.EntryPoint{
    Path: "scripts/hello.py", Version: "no-args-v1",
    ValidateArgs: func(args []string) error {
        if len(args) != 0 { return script.ErrPermission }
        return nil
    },
}
```

## 工具与执行结果

`run_skill_script` 仅注入到已经选定 Skill 的内部工具集合，并受请求过滤器、
middleware 和 Guard 控制。计划中的授权入口仍是 `skills.invoke`，不把脚本工具
变为未选定 Skill 也能调用的全局命令工具。

模型参数示例：

```json
{
  "skill_id": "pdf-generator",
  "script_path": "scripts/generate.py",
  "args": ["--input", "/workspace/input/data.json", "--output", "/output/report.pdf"]
}
```

Skill ID 必须等于当前选定的 Skill，并在执行器注册表内。脚本路径必须为规范
相对路径，拒绝绝对路径、`..`、反斜线、符号链接及未授权入口。模型参数不包括
镜像、Env、Workspace、Docker 网络或宿主挂载路径。工具不进行 Shell 拼接，
实际命令固定为 `python3 -B -u /skill/<approved-path> <args...>`。

结果类型 `script.Result` 提供 `success`、`code`、`exit_code`、`stdout`、`stderr`、
`duration_ms`、`workspace_id` 和 `files`。文件只返回 `output/report.pdf` 等逻辑
路径与大小，不返回真实服务器目录。非零退出码是已知脚本失败，返回结构化数据
供模型解释；取消、基础设施失败或超限返回带部分结果的错误。错误代码包括：

| code | 含义 |
| --- | --- |
| `permission_denied` / `invalid_arguments` | 未批准入口、路径/参数或所有权不合法 |
| `script_not_found` / `approval_drift` | 已批准文件缺失或指纹变化 |
| `docker_unavailable` | Docker 连接或镜像检查基础设施失败 |
| `image_missing` / `image_pull_failed` | 禁止拉取时本地无镜像，或拉取失败 |
| `script_failed` | 进程以非零状态退出，含缺少 Python 依赖 |
| `timeout` / `canceled` | 超时或 Context 取消 |
| `resource_limit` | 已确认的 OOM 等资源终止 |
| `log_limit` / `workspace_limit` / `output_limit` | 日志、文件数量或大小超限 |
| `container_error` / `cleanup_failed` | 容器生命周期或最终回收失败 |
| `unsupported_host` | 宿主缺少本实现要求的 Unix 文件安全原语 |

后端错误的 Error 文本经过清理，不直接把 Docker 错误里的宿主路径或凭据返回
模型；可信调用方仍可通过 `errors.Is/As` 检查错误链。

## Executor 接口

`script.Executor` 包含 `Fingerprint`、`CreateWorkspace`、`ValidateWorkspace`、
`Execute`、`ReleaseWorkspace`、`OpenFile` 和 `Close`。`script.Request` 由可信宿主
构造，包含 Skill ID/正文指纹、入口、参数、所有者绑定的 Workspace 和可收紧
的 Timeout。它不依赖 ReAct 或特定计划调度器。

现有 Agent 在首次实际脚本调用时创建 Workspace；同一任务的后续调用复用它，
不同任务有不可预测的不同 ID。Workspace 引用写入原 checkpoint，Resume 检查
所有者、持久化标记和执行器策略指纹。内部已完成动作继续由原 continuation
保留，脚本工具声明非幂等写、不可自动重试。

默认批准的本地脚本不额外要求人工确认；需要时使用
`SkillOptions.ScriptConfirmation: tool.ConfirmationAlways`，沿用现有精确参数
绑定的确认恢复协议。终态 run 释放 Workspace，暂停 run 保留它。

## 镜像、容器与限制

每次执行：检查 Skill/入口与文件指纹 → 获取并发槽位与任务锁 → 复制批准文件
到只读快照 → 检查镜像 → 必要时拉取 → 创建唯一命名容器 → **启动前 attach
输出流** → 启动 Python → 等待/监控 → 收集结果 → 强制删除本次容器 → 删除
只读快照。没有常驻 Python 容器，也不操作其他业务容器或删除基础镜像。

镜像检查只将明确 NotFound 视为“可拉取”；权限和连接错误分别报告，不触发
误拉取。同一 Executor 内相同镜像的并发检查/拉取合并，执行使用检查获得的
具体镜像 ID。`EnsureImage(ctx, image)` 可用于可信应用提前准备镜像。

| Config | 默认值 |
| --- | --- |
| `Image` | `python:3.12-slim` |
| `AutoPull` | false，需调用方显式开启 |
| `MemoryMB` / `CPUs` | 384 MiB / 0.5 核 |
| `MaxParallel` / `PIDsLimit` | 1 / 64 |
| `Timeout` | 60 秒，覆盖排队、镜像准备和执行 |
| `ConnectTimeout` / `CleanupTimeout` | 5 秒 / 10 秒 |
| `NetworkEnabled` | false |
| `LogMaxBytes` | stdout/stderr 合计 1 MiB |
| `WorkspaceMaxBytes` / `OutputMaxBytes` | 100 MiB / 50 MiB |
| `MaxFiles` | 128 个文件/目录条目，包含中间目录 |
| `CleanupOnRelease` | false，保留输出等待业务消费 |

这些配置均为宿主配置。`Request.Timeout` 只能收紧总超时。容器默认非 root
（宿主非零 UID/GID；宿主为 root 时使用 65532）、禁用网络、只读根文件系统、
capabilities 全部丢弃、no-new-privileges，开启 init，禁用 swap，配置 CPU/内存/
PIDs 上限。`/tmp` 是 16 MiB、noexec/nosuid/nodev 的 tmpfs。Docker 磁盘日志
驱动禁用，stdout/stderr 经实时 attach 流有界收集。

只挂载三个受控目录：批准快照 → `/skill`（只读）、任务中间目录 →
`/workspace`、任务输出目录 → `/output`。不挂项目根、宿主根、Docker Socket
或其他任务目录。模型不能修改这些选项。

## 文件保留与已知边界

`WorkspaceRoot` 是可信父目录；执行器在其中创建私有 `.zhizhi-scripts` 目录，
不给既有父目录修改权限。不指定时创建独立临时根；可信应用可用
`executor.WorkspaceRoot()` 获取位置。bind 源必须对 Docker daemon 可见，第一版
面向本地 Linux Engine 或 macOS Docker Desktop。缺少 Unix 文件安全原语的
宿主会明确返回 `unsupported_host`，而非放宽文件检查。远程沙箱应实现脚本接口，不能假定
远程 daemon 自动拥有本机目录。rootless/user namespace 部署需要核对 UID 映射
及 bind 文件权限。

跨进程恢复必须配置相同的 `WorkspaceRoot`、注册清单和资源策略；默认临时根在
每次 New 时独立生成，仅适合单进程生命周期。服务端应复用一个 Executor 来
统一控制多个 Agent 的执行并发，而不是每个请求新建客户端。

输出、中间文件总量和条目数在执行中每 50 ms 检测并在结束后复检；检测到
超限会终止并拒绝结果。符号链接、硬链接和特殊文件均拒绝，扫描和读取使用
Go `os.Root` 限定目录边界。**文件大小限制是监测/拒绝机制，不是磁盘硬配额**，
快速写入可能在检测前短暂超过阈值。需要硬存储限制的部署应使用文件系统配额
或另一个沙箱实现。Docker 也不适合直接执行任意恶意第三方 Skill。

默认保留产物。应用用 `OpenFile(ctx, script.Workspace{ID: result.WorkspaceID,
Owner: response.RunID}, file.Path)` 获取文件后可上传 OSS/S3，再调用 Docker
实现的 `DeleteWorkspace`。基础接口不耦合对象存储。设置 `CleanupOnRelease`
会在任务终止后删除数据，因此不能在响应返回后再读取已清理文件。

`Close` 取消在途任务，等待执行清理，并重试本实例尚未回收的容器；清理使用
独立 Context，避免原请求取消跳过清理。Docker 不可达时会明确报告清理失败，
不会声称已经删除。进程被强杀或 daemon 永久不可达时仍可能遗留带
`io.zhizhi.skill-script` 标签的孤儿容器，需要运维处理；本实现不越权清理其他
实例或业务容器。保留的 Workspace 需要业务显式删除或配置清理策略。

普通 Docker 限制不能替代恶意代码隔离。Docker API 进程本身拥有很高的宿主
权限，应保护 socket/TLS 凭据，并仅批准经过审核的镜像、脚本、资源和参数。

## 验证与示例

无 Docker 单元测试通过 `docker.Engine` 窄接口替换 SDK，覆盖镜像错误分类、
并发拉取、退出码、日志、输出文件、超时取消、路径/链接/所有权拒绝和清理。
Agent 测试覆盖简单/流式/复杂计划下的工具注入、确认恢复与任务释放。

```sh
go test ./skill/script/... ./skill . ./internal/runtime/react ./internal/runtime/exec
ZHIZHI_DOCKER_TEST=1 go test ./skill/script/docker -run TestDockerIntegration -v
go run ./examples/15-docker-skills
```

真实测试默认关闭；显式开启后若无法连接 Engine 会清晰跳过，不能作为真实
容器执行已通过的证据。示例无需 Python 或 LLM API key，使用确定性模型演示
完整 Tool Calling；替换 `WithModel` 即可接真实 LLM。示例 Dockerfile 演示一次
构建 reportlab/openpyxl/Pillow 镜像，而非每次调用安装依赖。

SDK API 及容器资源配置依据 [Moby Go SDK](https://pkg.go.dev/github.com/moby/moby/client@v0.6.2)
和 [Docker 资源限制文档](https://docs.docker.com/engine/containers/resource_constraints/)。
