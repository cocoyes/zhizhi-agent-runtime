---
name: test-skill
description: Execute the audited Python hello script and create a small report file.
---
For this workflow, call run_skill_script with skill_id "test-skill", script_path
"scripts/hello.py", and no arguments. This script is an application-approved
entrypoint. It writes /output/report.txt and prints a JSON result. Report its
actual stdout and returned file reference; do not claim execution without a tool
result. Do not install dependencies or execute shell commands.
