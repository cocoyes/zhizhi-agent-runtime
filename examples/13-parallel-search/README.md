# Parallel Search MCP example

Run web research through the runtime's lazy MCP capability selection. This separate
example connects to [Parallel Search MCP](https://docs.parallel.ai/integrations/mcp/search-mcp)
over Streamable HTTP. The model first sees the server's capability description;
only a selected server connects and loads tools. It can then call `web_search`
and `web_fetch` and use their results to answer with sources.

Parallel's anonymous MCP tier is free and needs no API key. It uses Fast search
with service-managed limits, intended for exploration and light use. Your model
endpoint still needs its own configuration and may charge for inference.

From the repository root, configure an OpenAI-compatible endpoint:

```bash
export LLM_BASE_URL="https://your-provider.example/v1"
export LLM_API_KEY="your-model-provider-key"
export LLM_MODEL="your-model"
go run ./examples/13-parallel-search
```

Or supply your own research question:

```bash
go run ./examples/13-parallel-search "Read https://go.dev/doc/go1.25 and explain the container-aware GOMAXPROCS change with a source URL."
```

`LLM_API_KEY` is optional for model endpoints that don't require authentication.
`LLM_THINKING` and `LLM_REASONING_EFFORT` pass through to the model adapter.
`PARALLEL_MCP_URL` optionally overrides `https://search.parallel.ai/mcp`, for
example when testing against a local MCP fixture. The example sends a
`zhizhi-agent-runtime/<version>` User-Agent and never reads or sends Parallel
credentials. It doesn't load dotenv files or saved settings.

Only `web_search` and `web_fetch` are allowed, with explicit read-only, safe,
retryable tool policies. Other server tools aren't exposed. A selected server's
connection failure fails the run rather than silently dropping research. The run
has a two-minute deadline and supports Ctrl+C cancellation. Remote content is
untrusted; avoid sensitive queries and apply your application's own trust policy.
The example prints the final answer without enabling detailed traces.

Running this example is opt-in; it doesn't change runtime defaults or the existing
maps and encyclopedia example.

## Validation

```bash
go test ./examples/13-parallel-search -v
ZHIZHI_PARALLEL_LIVE=1 go test ./examples/13-parallel-search -run TestLiveParallel -count=1 -v
```

The default tests use local MCP and Chat Completions fixtures to check capability
selection, lazy discovery, tool filtering, search/fetch execution, and answer
composition through the same `run` function as the executable. The opt-in test
uses the real anonymous Parallel endpoint with a deterministic local model
fixture. It makes public web requests and is subject to service limits; it isn't
a measurement of model answer quality or application latency.
