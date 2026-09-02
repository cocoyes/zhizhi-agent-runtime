# Contributing

Thanks for contributing to zhizhi-agent-runtime.

## Development

- Go 1.25 or newer is required.
- Keep public APIs documented and backwards compatible within a major version.
- Run `gofmt`, `go vet ./...`, `go test ./...`, and `go build ./...` before opening a pull request.
- Add focused tests for behavior changes, especially around retries, side effects, cancellation, and MCP trust boundaries.

## Pull requests

Describe the user-visible behavior, security impact, and any compatibility considerations. Avoid committing credentials, provider traces containing personal data, or generated build output.
