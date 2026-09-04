# zhizhi-agent-runtime

<p align="center">
  <img src="zhizhi-logo.png" alt="Zhizhi agent runtime" width="720">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/cocoyes/zhizhi-agent-runtime.svg)](https://pkg.go.dev/github.com/cocoyes/zhizhi-agent-runtime)
[![Go Report Card](https://goreportcard.com/badge/github.com/cocoyes/zhizhi-agent-runtime)](https://goreportcard.com/report/github.com/cocoyes/zhizhi-agent-runtime)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

[中文文档](README.zh-CN.md)

[Architecture and package boundaries](docs/architecture.md)

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

Hybrid reasoning models such as DeepSeek and Doubao can be configured explicitly:

```go
model := openaicompat.New(openaicompat.Config{
    BaseURL:         os.Getenv("LLM_BASE_URL"),
    APIKey:          os.Getenv("LLM_API_KEY"),
    Model:           os.Getenv("LLM_MODEL"),
    Thinking:        openaicompat.ThinkingEnabled, // or ThinkingDisabled / ThinkingAuto
    ReasoningEffort: "high",
})
```

The adapter sends `thinking: {"type":"..."}` and the top-level `reasoning_effort` field. When thinking mode is combined with tool calls, returned `reasoning_content` is automatically preserved in same-turn continuation requests. If these fields are omitted, the adapter defaults to `ThinkingDisabled` and reasoning effort `high`. Supported values still depend on the provider and model version. Example 10 also reads `LLM_THINKING` and `LLM_REASONING_EFFORT`.

Enable detailed JSONL when diagnosing plans or tool arguments:

```go
zhizhi.WithObserver(observe.NewJSONL(os.Stdout, observe.WithDetails()))
```

Detailed mode records planner/replanner model requests, raw responses and repair attempts, actual per-step inputs/outputs/errors, the final patch, and final-composer requests/responses. It may contain user or business data, so it is disabled by default; example 10 enables it explicitly.

The runtime no longer hand-parses structured model output: `jsonrepair-go` extracts and repairs non-standard JSON, `mapstructure/v2` handles common weak scalar drift, and `go-openapi/jsonpointer` resolves RFC 6901 evidence paths. Repaired syntax must still pass plan, JSON Schema, and patch-application validation before execution.

The planner and replanner follow Eino's structured-output pattern on their primary path: derive JSON Schema from the Go result type, bind Plan or Patch as the only synthetic output tool, force `tool_choice`, and consume only that tool's arguments. Plain JSON text remains a compatibility path for models that do not advertise tool calling; domain tools are catalog metadata and cannot execute during planning.

## Install

```bash
go get github.com/cocoyes/zhizhi-agent-runtime
```

## Quickstart

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

## P0 execution contract

Messages support ordered text and image parts through `model.UserMessage`,
`model.TextPart`, and `model.ImageURLPart`. Model inputs support `auto`, `none`,
`required`, and named tool choice plus `text`, `json_object`, and strict
`json_schema` response formats. Adapters declare their capabilities and reject
unsupported content instead of silently dropping it.

Each run can add or filter an immutable tool snapshot and attach scoped options:

```go
response, err := agent.Run(ctx, request,
    zhizhi.WithRunTools(requestTools...),
    zhizhi.WithRunToolFilter(allowForRequest),
    zhizhi.WithRunObserver(requestObserver),
    zhizhi.WithRunMetadata(metadata),
    zhizhi.WithRunResponseFormat(format),
)
```

Global and run-scoped middleware are available for agent, model, model stream,
tool, tool stream, planner, and replanner boundaries. `Stream` emits stable
run/model/tool/plan/step lifecycle events across every tool round and complex
mode. Complex plans may use deterministic tool steps or bounded agentic steps
with an explicit capability allowlist and tool-call budget. Final and suspended
responses contain cumulative model/tool usage and execution statistics.
Optional OpenTelemetry integration lives in `adapter/otel`; its instrumentation
provides middleware for each execution boundary plus an event observer, keeping
the core runtime independent of OTel APIs.

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

### Suspend and resume

Every agent has a process-local checkpoint store by default. Inject a durable
store in production and resume the exact suspended state after approval:

```go
agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithTools(sendEmail),
    zhizhi.WithCheckpointStore(durableStore),
)

pending, err := agent.Run(ctx, request)
if pending.Suspended {
    completed, err := agent.Resume(ctx, zhizhi.ResumeRequest{
        RunID: pending.RunID, Checkpoint: pending.Checkpoint, Decision: true,
    }, runOptions...) // provide the same request-scoped tools, if any
    _ = completed
    _ = err
}
```

Checkpoints are versioned and bound to the tool-catalog fingerprint. Completed
steps and successful write receipts are replayed without executing their tools;
`UNKNOWN` receipts are never retried. A resumed checkpoint is invalidated even
when execution suspends again, preventing stale approval tokens from replaying a
write.

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
import runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"

agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithMCP(runtimemcp.Config{
        ID: "internal-tools", Transport: runtimemcp.TransportStreamableHTTP,
        Endpoint: "https://tools.example.com/mcp",
        ConnectionStrategy: runtimemcp.ConnectionLazy,
        Required: false,
		MaxConnectAttempts: 3,
		ReconnectBackoff: 200 * time.Millisecond,
        AllowTools: []string{"customer.lookup", "ticket.search"},
        DenyTools: []string{"admin.delete"},
    }),
)
```

Lazy connection is the default. Concurrent first use is single-flight; optional
servers are isolated from agent creation, while `Required` servers fail the run
if unavailable. Providers support catalog refresh, fingerprint/version drift,
bounded reconnect backoff, cancellation, and context-bounded close. MCP tools
are treated as an untrusted boundary. See [SECURITY.md](SECURITY.md).

## Observability

```go
import (
    "os"
    "github.com/cocoyes/zhizhi-agent-runtime/observe"
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
| `11-household-workflow` | Real-model parallel steps, conditional bindings, and failure replanning | `go run ./examples/11-household-workflow` |

## Development

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

`make verify` runs the standard local checks. Real-provider tests are opt-in; the default suite is offline and deterministic.


## Security and License

Report vulnerabilities privately with the affected version, a minimal reproduction, and impact. See [SECURITY.md](SECURITY.md). See [LICENSE](LICENSE) for licensing terms.
