# Household workflow example

这个示例通过 `LLM_BASE_URL`、`LLM_API_KEY` 和 `LLM_MODEL` 连接真实的
OpenAI-compatible 模型。Planner、Replanner 和最终答案合成都由模型完成，本地代码
只提供可控的业务工具实现。

```text
第一批（并行）
├── search    查询钥匙位置
└── record    记录“猫粮在客厅”并打印成功日志

后续批次（LLM 生成声明式条件）
├── notify    search.result equals "抽屉"，20:00 提醒拿钥匙
└── notify    search.result not_equals "抽屉"，22:00 提醒喝水
```

示例只注册 `search`、`notify`、`record` 三个函数。提醒工具本身不知道钥匙条件，
也不在描述中硬编码晚上八点或十点；两个分支及其对 `search` 结果的依赖全部由
模型根据用户请求写入计划。`notify` 默认通过 cloud 通道投递，示例后端会让首次
投递失败；真实 Replanner 随后把同一个 `notify` 步骤改为 local 通道。

模型会根据工具描述和 JSON Schema 自己生成执行图。一次典型执行如下：

```bash
go run ./examples/11-household-workflow
```

PowerShell 下运行真实模型集成测试：

```powershell
$env:ZHIZHI_RUN_LIVE_EXAMPLE = "1"
go test ./examples/11-household-workflow -count=1
```

集成测试会实际断言：只注册三个工具、根步骤发生并行重叠、云通知失败后发生
replan、另一个条件分支没有执行，以及 record 被成功调用。由于它会
调用真实模型并产生 API 用量，默认 `go test ./...` 会跳过该用例；工具元数据测试
仍会正常执行。
