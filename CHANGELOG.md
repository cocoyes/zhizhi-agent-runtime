# Changelog

## Unreleased

## 1.0.5 - 2026-09-05

- Added deterministic semantic compatibility checks for replanned capability
  replacements, backed by explicit semantic groups and fallback allowlists.
- Preserved successful step results across replans and prevented ambiguous
  writes from being retried, replaced, or reported as completed.
- Made execution evidence distinguish plan step IDs, requested and executed
  capabilities, actual tool IDs, receipts, attempts, and fallback counts.
- Added unknown-safe conditional execution and schema validation for condition
  source paths and value types.

## 1.0.4 - 2026-09-05

- Completed the P0 execution contract: multimodal messages, named/required tool
  choice, JSON Schema response formats, per-run tools/options, model/tool/
  planner/replanner/agent middleware, bounded agentic steps, unified lifecycle
  stream events, accurate cumulative statistics, and injectable unique run IDs.
- Added versioned suspend/checkpoint/resume for simple and complex runs, with
  catalog-drift checks, completed-step replay, receipt preservation, and stale
  checkpoint invalidation.
- Added optional streaming tools with aggregation for `Run` and tool-delta
  delivery for `Stream`.
- Added lazy/eager MCP lifecycle management, optional/required isolation,
  single-flight connection, refresh generations, reconnect backoff, and bounded
  close.
- Added explicit model capability negotiation and concurrency-safe tool
  registry duplicate detection.

## 1.0.0 - 2026-09-01

- Public stateless Agent API with chat, streaming, planning, replanning, tools, and MCP.
- Capability-driven routing and dependency-safe parallel execution.
- Schema validation, retries, fallback, deadlines, budgets, guards, confirmation gates, and action receipts.
- JSONL observability, replay traces, evaluation primitives, and MCP trust-boundary controls.
