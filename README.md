# zhizhi-agent-runtime

<p align="center">
  <img src="zhizhi-logo.png" alt="Zhizhi agent runtime" width="720">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/zhizhi-ai/zhizhi-agent-runtime.svg)](https://pkg.go.dev/github.com/zhizhi-ai/zhizhi-agent-runtime)
[![Go Report Card](https://goreportcard.com/badge/github.com/zhizhi-ai/zhizhi-agent-runtime)](https://goreportcard.com/report/github.com/zhizhi-ai/zhizhi-agent-runtime)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

[中文文档](README.zh-CN.md)

Production-grade Go runtime for agents that plan, act, recover, and remain auditable.

Zhizhi is a model-agnostic execution layer for real applications. It turns ordinary Go functions into validated tools, builds dependency-safe plans, handles conditional branches and bounded replanning, and makes side effects explicit.

## What You Get

- Typed tools with generated JSON schemas and runtime input/output validation
- Chat, streaming, capability routing, and model-planned complex tasks
- Dependency graphs, parallel batches, conditions, and evidence bindings
- Bounded replanning after required-step failures
- Retries, fallback, deadlines, budgets, and concurrency limits
- Guards, confirmation gates, action receipts, and side-effect safety
- MCP integration with allow-lists and trust-boundary controls
- JSONL traces, evidence, warnings, and execution statistics

The lifecycle is explicit:

```text
request -> route -> plan -> execute -> observe evidence -> replan -> answer
```

## Requirements

- Go 1.25 or newer
- An OpenAI-compatible Chat Completions endpoint

The built-in adapter calls `{LLM_BASE_URL}/chat/completions`. It accepts standard
Chat Completions responses and Responses-style text responses (`output_text` or
`output[].content[].text`). Internal tool IDs may contain dots; they are normalized
only on the provider wire format.

## Install

```bash
go get github.com/zhizhi-ai/zhizhi-agent-runtime
```

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    zhizhi "github.com/zhizhi-ai/zhizhi-agent-runtime"
    "github.com/zhizhi-ai/zhizhi-agent-runtime/adapter/model/openaicompat"
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
        Input: "Summarize the latest support request",
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

The endpoint can return a standard Chat Completions response:

```json
{"choices":[{"message":{"role":"assistant","content":"..."}}]}
```

Responses-style text is also accepted:

```json
{"output_text":"..."}
```

For local setup, copy `.env.example` to your shell environment and set a real
provider endpoint, API key, and model. The runtime never fabricates model
responses: planning, tool selection, replanning, and final composition all use
the configured model.

## Typed Tools

Tools are normal typed Go functions. Schemas are reflected automatically and arguments/results are validated at runtime.

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

Tool descriptions are part of the model interface. Describe what a tool does and when it should be used. Keep IDs stable and specific; the runtime resolves the selected capability to the registered implementation.

## Complex Agent Tasks

For multi-step work, ask the runtime to use complex mode. The built-in model planner chooses tools and builds the dependency graph.

```go
ctx := context.Background()
mode := zhizhi.RunModeAgentComplex
response, err := agent.Run(ctx, zhizhi.Request{
    Input: "Plan tonight's activities in Shenzhen based on the weather",
    Mode: &mode,
})
```

Here `context.Background()` can be replaced with a request-scoped context carrying
your deadline and cancellation policy.

A planner can express result-based branches:

```json
{
  "version": 1,
  "steps": [
    {"id":"weather", "capability":"weather.current", "input":{"city":"Shenzhen"}},
    {"id":"outdoor", "capability":"activity.outdoor", "depends_on":["weather"], "condition":{"source_step":"weather","source_path":"outdoor_suitable","equals":true}, "bindings":[{"source_step":"weather","source_path":"temperature","target_path":"temperature"}]},
    {"id":"indoor", "capability":"activity.indoor", "depends_on":["weather"], "condition":{"source_step":"weather","source_path":"outdoor_suitable","equals":false}},
    {"id":"compose", "capability":"itinerary.compose", "depends_on":["outdoor","indoor"]}
  ]
}
```

The condition source must be a dependency. A non-matching step is skipped and never calls its tool. Bindings pass evidence into the next typed input. If a required step fails, one bounded replan can replace that step while preserving successful work.

The complete workflow is runnable and tested:

```bash
go run ./examples/10-full-agent
go test ./examples/10-full-agent
```

## Side-Effect Safety

Declare risk and authorization requirements for writes:

```go
sendEmail := tool.Func(
    "mail.send", "Send an email to a recipient", sendEmailImpl,
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

Without confirmation, the side effect is not executed. The response contains an `ActionReceipt` with `CONFIRMATION_REQUIRED`. Unknown external outcomes are represented explicitly and must not be treated as success.

## Reliability Controls

```go
zhizhi.WithBudget(zhizhi.Budget{
    MaxSteps: 12, MaxBatches: 8, MaxModelCalls: 6,
    MaxToolCalls: 20, MaxReplans: 1, MaxParallelSteps: 4,
    HardTimeout: 30 * time.Second,
})
```

The default policy retries only transient, safe, idempotent failures. Writes are never silently repeated.

## MCP

```go
import runtimemcp "github.com/zhizhi-ai/zhizhi-agent-runtime/mcp"

agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithMCP(runtimemcp.Config{
        ID: "internal-tools", Transport: runtimemcp.TransportStreamableHTTP,
        Endpoint: "https://tools.example.com/mcp",
        AllowTools: []string{"customer.lookup", "ticket.search"},
        DenyTools: []string{"admin.delete"},
    }),
)
```

MCP tools are treated as an untrusted boundary. See [SECURITY.md](SECURITY.md).

## Observability

```go
import (
    "os"
    "github.com/zhizhi-ai/zhizhi-agent-runtime/observe"
)

observer := observe.NewJSONL(os.Stdout)
agent, err := zhizhi.New(zhizhi.WithModel(model), zhizhi.WithObserver(observer))
```

Responses expose `Evidence`, `Actions`, `Warnings`, and statistics for model calls, tool calls, plan steps, batches, and replans.

## Examples

| Example | Purpose | Command |
| --- | --- | --- |
| `00-chat` | Model-backed chat | `go run ./examples/00-chat` |
| `01-stream` | Streaming text and tool events | `go run ./examples/01-stream` |
| `02-function-tool` | Typed tool schemas | `go run ./examples/02-function-tool` |
| `03-retry` | Transient retry policy | `go run ./examples/03-retry` |
| `04-fallback` | Capability fallback | `go run ./examples/04-fallback` |
| `05-local-mcp-server` | Local MCP server | `go run ./examples/05-local-mcp-server` |
| `06-mcp-client` | MCP discovery and allow-lists | `go run ./examples/06-mcp-client` |
| `07-complex-plan` | Model-planned execution | `go run ./examples/07-complex-plan` |
| `08-observability` | JSONL traces | `go run ./examples/08-observability` |
| `09-config-agent` | YAML configuration | `go run ./examples/09-config-agent` |
| `10-full-agent` | Conditions, bindings, replan, composition | `go run ./examples/10-full-agent` |

## Development

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

`make verify` runs the standard local checks. Real-provider tests are opt-in; the default suite is offline and deterministic.

## Project Status

Current release: `v1.0.0`. The public API is intentionally small, and changes affecting tool execution, side effects, plan semantics, or provider compatibility are covered by focused tests.

## Security and License

Report vulnerabilities privately with the affected version, a minimal reproduction, and impact. See [SECURITY.md](SECURITY.md). See [LICENSE](LICENSE) for licensing terms.
