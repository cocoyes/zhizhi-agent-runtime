# Docker Skill scripts

Run from the repository root:

```sh
go run ./examples/15-docker-skills
```

Requires a local Linux Docker Engine or Docker Desktop exposing the Engine API.
The host does **not** need Python, and no persistent Python container needs to be
created. The official SDK honors trusted `DOCKER_HOST`/TLS configuration. Bind
mount sources must be available to that daemon; Docker Desktop may require file
sharing for `SKILL_WORKSPACE_ROOT`.

The example explicitly approves `test-skill/scripts/hello.py`, loads the existing
Skill catalog, injects the Docker executor, runs the Agent, reads its stdout and
output file, then closes the Agent and executor. A deterministic model makes this
example runnable without an API key; use a real `model.Model` in `WithModel` for
LLM-driven selection and calling. All execution still goes through ordinary tool
calling and the existing inner ReAct loop.

Defaults to `python:3.12-slim`; first execution may download it. The sample opts
into `AutoPull`. To use an approved custom image:

```sh
SKILL_PYTHON_IMAGE=registry.example.com/approved-python@sha256:YOUR_DIGEST \
SKILL_WORKSPACE_ROOT=/srv/agent-artifacts \
go run ./examples/15-docker-skills
```

The included Dockerfile demonstrates building reportlab, openpyxl and Pillow into
an image once. Build it in a trusted image pipeline, pin dependency versions and
publish its digest. The runtime does not execute `requirements.txt` or `pip install`.

The Docker API process has substantial host authority. Protect its socket/TLS
credentials and only approve audited Skill files and images. This is not a sandbox
for arbitrary hostile third-party Python. Outputs are retained by default; use
`OpenFile` to upload with your business storage service, then `DeleteWorkspace`.
Set `CleanupOnRelease` only if outputs are no longer needed when the task ends.

Real-engine tests are explicit and skip clearly if Engine is unavailable:

```sh
ZHIZHI_DOCKER_TEST=1 go test ./skill/script/docker -run TestDockerIntegration -v
```
