# Security Policy

Please report security vulnerabilities privately to the maintainers rather than opening a public issue. Include a minimal reproduction, affected versions, and the impact you observed.

The runtime treats MCP tools as untrusted by default, rejects suspicious tool descriptions, validates tool inputs and outputs against schemas, and requires explicit confirmation for high-risk or undeclared side effects. Applications should still provide a guard and confirmation callback appropriate to their authorization model.
