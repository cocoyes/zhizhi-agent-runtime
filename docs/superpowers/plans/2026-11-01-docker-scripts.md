# Docker Skill Scripts Implementation Plan

**Goal:** Execute explicitly approved Python Skill entrypoints in disposable Docker containers through the existing Skill runner.

**Architecture analysis:** Catalog already stores name, description, directory, root, instruction fingerprint; unknown YAML fields are ignored. Selected instructions enter a bounded internal ReAct runner. Registry/tools, request contexts, middleware, total budgets, composite effects and checkpoint continuations are reusable. `skill.Executor` interprets instruction packages and is not a process executor. `skill.read_resource` reads confined text only; there is no script executor or task workspace manager.

**Integration:** Keep `skill.Executor`. Add the orthogonal `skill/script.Executor` contract and a Docker implementation in `skill/script/docker`, using the official Moby SDK. Opt-in `SkillOptions.ScriptExecutor` injects `run_skill_script` only inside selected Skill execution, constrained to that Skill and an explicit approved entrypoint manifest. Preserve existing APIs and checkpoint compatibility.

## Tasks

- [x] Script contract, approval manifests and bounded snapshot/workspace helpers. Pin instruction and approved file hashes; never trust model-provided paths, environment or Docker options.
- [x] Docker SDK adapter, environment check and image inspect/pull deduplication. Fake API unit tests must run without Docker.
- [x] Disposable execution, non-root readonly mounts, limits, bounded live logs and workspace monitoring, wait/status and cleanup on every path.
- [x] Existing Skill tool integration, task workspace lifecycle and persisted workspace references; fingerprint execution permissions during Resume.
- [x] Unit/security tests, opt-in actual Docker integration tests, runnable example and Python dependency Dockerfile.
- [x] Targeted tests/static checks and independent final review; record real Engine availability and any limits honestly.

## Decisions

- User explicitly requests implementation without further design approval. Continue inline on current branch; do not discard or amend existing work.
- Workspace disk bounds use live monitoring plus final validation on controlled writable bind directories. These are termination/rejection bounds, not host filesystem hard quotas; audited code is required. Docker enforces CPU, memory, PIDs and bounded tmpfs independently.
- Executor is application-owned and may be shared. Agent releases task workspaces on terminal completion, retains them during suspension; explicit Executor.Close cancels active work, waits for cleanup and closes its client. Retained output workspaces are removed only by explicit policy or trusted cleanup call.
- Snapshot mounts require a local Docker daemon with access to the configured workspace root. Future remote sandboxes implement the same script.Executor interface.

## Execution evidence

- Initial tests observed missing script and Docker APIs, followed by passing implementation tests. New SDK pinned to client v0.6.2 / API v1.56.1 without raising the existing Go 1.25 requirement.
- Workspace initialization initially rejected test directories whose private parent had a 0755 child; diagnosis confirmed the permission gate. Configuration now owns a private `.zhizhi-scripts` child without changing caller directory permissions.
- Independent review found one Important issue: directory/file replacement with a FIFO could block scanning/opening and prevent timeout cleanup. Fixed with Unix nonblocking/no-follow descriptor opening and actual type checks, plus a reader-held task lock. Deterministic replacement regression passes. Non-Unix hosts return unsupported_host rather than weakening the checks.
- Real Docker first unavailable and explicitly skipped. After user started Engine, actual integration passed: JSON stdout, stderr, file, exit 7, readonly Skill mount, no socket, no network, non-root, timeout, active cancellation, log overflow, real 64 MiB OOM, and absence of owned containers after execution.
- Real active cancellation exposed a start-ack race returning container_error instead of canceled; error mapping now handles cancellation during create/attach/start too. Real regression passed after fix.
- End-to-end `go run ./examples/15-docker-skills` passed, returning JSON success and a 32-byte `output/report.txt` through Agent tool evidence and trusted file retrieval.
- Existing per-package race checks and go vet passed. Final repository compatibility verification is recorded below after completion.
- Final `go test -race ./...` passed across the entire repository, including existing offline e2e tests and all examples compiling. No standalone/repeated full build was performed.
- Final `ZHIZHI_DOCKER_TEST=1 go test -race ./skill/script/docker -run '^TestDockerIntegration$' -v` passed against the user's running Engine (3.61 s test body); no owned containers remained.
- Final `go vet ./skill/script/... ./skill . ./internal/runtime/react ./internal/runtime/exec ./examples/15-docker-skills` and `git diff --check` passed. Changes remain uncommitted on `codex/docker-skill-scripts`.
