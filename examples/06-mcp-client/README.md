# MCP capability provider example

这个示例与 `10`、`11` 一样调用真实的 OpenAI-compatible LLM。运行时先只把
`amap` 和 `baike-mcp-server` 的名称、能力描述交给模型；模型选择一个或多个 MCP
后，才连接被选中的服务并执行 `ListTools`。未选中的 MCP 不建立连接，也不加载工具。

配置环境变量：

```powershell
$env:LLM_BASE_URL = "..."
$env:LLM_API_KEY = "..."
$env:LLM_MODEL = "..."
$env:AMAP_MCP_KEY = "..."
$env:BAIKE_MCP_TOKEN = "..."
```

运行默认的多 MCP 请求：

```powershell
go run ./examples/06-mcp-client
```

也可以只触发高德：

```powershell
go run ./examples/06-mcp-client "帮我查询一下今天深圳的天气"
```

`AMAP_MCP_URL` 可用于直接覆盖完整的高德 MCP 地址。百度 token 只在选择百科 MCP
时需要。示例把两个服务声明为只读 MCP；生产环境应按服务的实际副作用配置
`DefaultToolPolicy` 或逐工具 `ToolPolicies`。
