# zhizhi-agent-runtime

<p align="center">
  <img src="zhizhi-logo.png" alt="Zhizhi agent runtime" width="720">
</p>

[![Go Reference](https://pkg.go.dev/badge/github.com/cocoyes/zhizhi-agent-runtime.svg)](https://pkg.go.dev/github.com/cocoyes/zhizhi-agent-runtime)
[![Go Report Card](https://goreportcard.com/badge/github.com/cocoyes/zhizhi-agent-runtime)](https://goreportcard.com/report/github.com/cocoyes/zhizhi-agent-runtime)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

[中文文档](README.zh-CN.md) · [Zhizhi Official Website](https://deepyou.top) · [API Reference](https://pkg.go.dev/github.com/cocoyes/zhizhi-agent-runtime) · [Changelog](CHANGELOG.md)

Zhizhi (知枝) is a Go runtime for building AI Agents. Connect a model, register your business functions as tools, and let the Agent call them to complete tasks. For multi-step work, the runtime supports planning, parallel execution, conditional branches, and bounded recovery.

Use it inside a Go service, assistant, or automation application. You supply the model and business integrations; Zhizhi manages tool execution, budgets, approval checkpoints, and execution evidence.

## What you can build

- **Conversational assistants:** chat, streaming, multimodal inputs, and typed function calling.
- **Task automation:** ReAct tool loops and Plan-and-Execute workflows with dependencies and recovery.
- **Connected agents:** integrate MCP servers and discover local Skills on demand.
- **Document workflows:** run approved Skill Python scripts in disposable Docker containers.
- **Auditable services:** apply guards and confirmations, resume suspended work, and inspect traces and action receipts.
- **Voice applications:** use the independent realtime speech API and Doubao adapter.

## Install

Requires **Go 1.25+** and a model endpoint. The built-in adapter supports OpenAI-compatible Chat Completions; you can also implement the `model.Model` interface for another provider. Tool workflows require a model that supports tool calling. Docker is optional and needed only for Skill script execution.

```bash
go get github.com/cocoyes/zhizhi-agent-runtime@v1.1.1
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
        Input: "Explain what an AI Agent can do in three sentences",
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

Save the Go code as `main.go` in your own module and run `go run .` with those environment variables. To use the repository examples, clone this repository and run the example command from its root. Set `LLM_BASE_URL` to the API base URL, such as `https://your-provider.example/v1`; the adapter appends `/chat/completions`.

The following snippets extend the quickstart and assume `model` is configured and `ctx` is a request context. Add the indicated package imports and handle initialization errors before using an Agent. Always close Agents and streams when finished.

## Register a Go function as a tool

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

The weather data above is illustrative. Replace the function body with your own API or service call. Import `github.com/cocoyes/zhizhi-agent-runtime/tool`, then ask the Agent about a city's weather. See [the complete tool example](examples/02-function-tool/main.go).

## Stream a response

```go
stream, err := agent.Stream(ctx, zhizhi.Request{Input: "Tell me a short story"})
if err != nil { return err }
defer stream.Close()
for {
    event, err := stream.Recv(ctx)
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    fmt.Print(event.TextDelta)
}
```

Import `errors` and `io` for the receive loop. The stream also exposes tool and execution lifecycle events; see [the streaming example](examples/01-stream/main.go).

## Run a multi-step task

```go
ctx := context.Background()
mode := zhizhi.RunModeAgentComplex
response, err := agent.Run(ctx, zhizhi.Request{
    Input: "Plan tonight's activities in Shenzhen based on the weather",
    Mode: &mode,
})
```

Register the tools needed for the task first. Complex mode builds a dependency graph, passes results between steps, runs independent steps in parallel, and can replan within the configured budget when a required step fails. See [the complete Agent example](examples/10-full-agent) and [the household workflow](examples/11-household-workflow).

## Connect an MCP server

```go
// import runtimemcp "github.com/cocoyes/zhizhi-agent-runtime/mcp"
agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithMCP(runtimemcp.Config{
        ID: "internal-tools",
        Transport: runtimemcp.TransportStreamableHTTP,
        Endpoint: "https://tools.example.com/mcp",
        AllowTools: []string{"customer.lookup", "ticket.search"},
    }),
)
```

MCP connections are lazy by default. Use allowlists to control which remote tools are available. For multiple servers, `mcp.NewCapabilityProvider` with `zhizhi.WithMCPCapabilityProvider` lets the model select a server before its tool list is loaded. See [the MCP client example](examples/06-mcp-client/README.md).

## Load local Skills on demand

A Skill is a directory containing a `SKILL.md` with `name` and `description` frontmatter and usage instructions:

```text
skills/
└── greeting/
    └── SKILL.md
```

```markdown
---
name: greeting
description: Compose a short greeting for a named person.
---
Ask for the person's name if it is missing, then write a friendly greeting.
```

```go
// import "github.com/cocoyes/zhizhi-agent-runtime/skill"
catalog := skill.NewCatalog(skill.CatalogOptions{})
if _, err := catalog.LoadRoot(ctx, "./skills"); err != nil {
    return err
}
agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{}),
)
```

`LoadRoot` indexes names, descriptions, and directories in memory. Calling it again atomically refreshes that root; a failed scan preserves the previous index. The model sees `skills.search` and `skills.invoke`, discovers candidates, and loads instructions only after selection. The full Skill library is not included in every request.

To let a Skill call business tools, register them with `WithTools` and list their IDs in `SkillOptions.AllowedTools`. An empty list grants no business tools. See [the Skill guide](docs/runtime-skills.md) and [the offline example](examples/14-skills).

### Execute approved Python scripts with Docker

Install Docker Engine, load your Skill catalog, and explicitly approve its script entrypoints. The example below assumes the catalog includes `test-skill` with `scripts/hello.py`:

```go
// import "github.com/cocoyes/zhizhi-agent-runtime/skill/script"
// import dockerscript "github.com/cocoyes/zhizhi-agent-runtime/skill/script/docker"
executor, err := dockerscript.New(ctx, dockerscript.Config{
    Enabled: true,
    Catalog: catalog,
    Image: "python:3.12-slim",
    AutoPull: true,
    Registrations: []script.Registration{{
        SkillID: "test-skill",
        Scripts: []script.EntryPoint{{Path: "scripts/hello.py"}},
    }},
})
if err != nil { return err }
defer executor.Close(context.Background())

agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithSkillProvider(catalog, zhizhi.SkillOptions{
        ScriptExecutor: executor,
    }),
)
```

Only the selected Skill receives `run_skill_script`. Each call uses a separate temporary container; images are reused, containers are removed, and task outputs can be read through the executor. You do not need host Python or a manually created container. With `AutoPull: true`, the first run downloads a missing image.

Defaults include non-root execution, read-only Skill files, disabled networking, and CPU, memory, time, and output limits. This feature is intended for audited Skills. Docker API access is highly privileged; protect the process using it. See [configuration and security boundaries](docs/docker-skill-scripts.md), [the runnable example](examples/15-docker-skills/README.md), and [the dependency image Dockerfile](examples/15-docker-skills/Dockerfile).

## Control execution and inspect results

Pass a deadline or cancellation through `context.Context`, and configure per-Agent limits with `WithBudget`:

```go
zhizhi.WithBudget(zhizhi.Budget{
    MaxSteps: 12, MaxBatches: 8, MaxModelCalls: 6,
    MaxToolCalls: 20, MaxReplans: 1, MaxParallelSteps: 4,
    HardTimeout: 30 * time.Second,
})
```

Declare side effects with `tool.WithSideEffect`, `tool.WithIdempotency`, and `tool.WithConfirmation`. Use `WithGuard` for authorization and `WithConfirmation` for the approval decision. When execution suspends, resume its checkpoint after approval:

```go
agent, err := zhizhi.New(
    zhizhi.WithModel(model),
    zhizhi.WithTools(sendEmail),
    zhizhi.WithCheckpointStore(durableStore),
)

if err != nil { return err }
defer agent.Close(context.Background())

pending, err := agent.Run(ctx, request)
if err != nil { return err }
if pending.Suspended {
    completed, err := agent.Resume(ctx, zhizhi.ResumeRequest{
        RunID: pending.RunID, Checkpoint: pending.Checkpoint, Decision: true,
    }) // Pass the same run options here if the original request used them.
    if err != nil { return err }
    fmt.Println(completed.Text)
}
```

The snippet assumes a registered `sendEmail` tool, a checkpoint store, and your request variables. Provide the same request-scoped tools when resuming. Use a durable checkpoint store for recovery across process restarts.

Responses include `Text`, `Evidence`, `Actions`, `Warnings`, and execution statistics. Add `zhizhi.WithObserver(observe.NewJSONL(os.Stdout))` to record events, or use `adapter/otel` for OpenTelemetry integration. Detailed JSONL is opt-in and may include business data.

## Examples

Most model-backed examples read `LLM_BASE_URL`, `LLM_API_KEY`, and `LLM_MODEL`; see [.env.example](.env.example). Example 14 runs offline, while example 15 uses a deterministic model and a real Docker Engine.

| Example | Purpose | Command |
| --- | --- | --- |
| `00-chat` | Model-backed chat | `go run ./examples/00-chat` |
| `01-stream` | Streaming text and tool events | `go run ./examples/01-stream` |
| `02-function-tool` | Typed tool schemas | `go run ./examples/02-function-tool` |
| `03-retry` | Transient retry policy | `go run ./examples/03-retry` |
| `04-fallback` | Capability fallback | `go run ./examples/04-fallback` |
| `05-local-mcp-server` | Local MCP server | `go run ./examples/05-local-mcp-server` |
| `06-mcp-client` | Real-model, multi-server MCP capability selection | `go run ./examples/06-mcp-client` |
| `07-complex-plan` | Model-planned execution | `go run ./examples/07-complex-plan` |
| `08-observability` | JSONL traces | `go run ./examples/08-observability` |
| `09-config-agent` | YAML configuration | `go run ./examples/09-config-agent` |
| `10-full-agent` | Conditions, bindings, replan, composition | `go run ./examples/10-full-agent` |
| `11-household-workflow` | Real-model parallel steps, conditional bindings, and failure replanning | `go run ./examples/11-household-workflow` |
| `12-doubao-realtime` | Realtime duplex speech | [README](examples/12-doubao-realtime/README.md) |
| `13-parallel-search` | Opt-in keyless web research via Parallel Search MCP | `go run ./examples/13-parallel-search` |
| `14-skills` | Offline Skill discovery and bounded execution | `go run ./examples/14-skills` |
| `15-docker-skills` | Approved Python scripts in disposable Docker containers | `go run ./examples/15-docker-skills` |

## Further reading

- [Architecture and extension points](docs/architecture.md)
- [Skill loading and execution](docs/runtime-skills.md)
- [Docker script configuration and security](docs/docker-skill-scripts.md)
- [Realtime speech](docs/doubao-realtime-speech.md)
- [Runtime compatibility and migration](docs/runtime-stability.md)
- [Release history](CHANGELOG.md)

## Security and license

Report vulnerabilities privately following [SECURITY.md](SECURITY.md). This project is released under the [MIT License](LICENSE).
