# Changelog

## Unreleased

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
