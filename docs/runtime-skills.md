# Local Skill runtime contract

`skill.Catalog` is a process-local, concurrency-safe metadata index. `LoadRoot`
accepts either a Skill directory or a parent containing multiple Skills. A Skill
begins at a regular `SKILL.md` with YAML front matter:

```yaml
---
name: document-summary
description: Read a document and summarize its conclusions.
---
Use the supplied document-reading tool. Cite only information actually read.
```

The catalog stores name, description, absolute directory, root and SHA-256.
Instruction bytes are discarded after indexing and reread only for invocation.
`Metadata()` returns a copy for application inspection; it is never automatically
inserted into a model prompt. Traversal stops at a Skill directory, so reference
documents cannot accidentally register nested Skills. Directory symlinks are not
followed; overlapping roots and duplicate names are rejected.

## Reloads and bounds

Repeat `LoadRoot(ctx, root)` to atomically replace a root, including removing
deleted entries. A failed reload leaves the old index untouched. Unchanged
reloads do not advance generation. Scans are serialized; metadata readers remain
available during scanning. `LoadReport` describes additions, updates and removals.

Default limits: 1 MiB per SKILL.md, 64 KiB front matter, 10,000 Skills per root,
32 nested directories. `CatalogOptions` can override these limits. Disk directories
must be controlled by the application deployer; path checks do not constitute an
OS sandbox against an attacker concurrently replacing filesystem objects.

## Discovery and execution

Only two meta tools appear alongside ordinary application tools:

1. `skills.search({query, limit})`: rank name/description in memory, with Latin
   tokens and CJK bigrams. Default limit is 5; the hard cap is 10. Custom ranking
   is available through `SkillOptions.Searcher`, but candidates are revalidated
   against authoritative metadata. No embedding service is required.
2. `skills.invoke({selection_id, skill_name, task})`: validate a returned candidate,
   read and hash the selected instructions, then execute in a bounded internal
   model/tool loop. A selection is not authorization for a write.

Selection grants are random, private to one run, valid for 15 minutes and capped
at 64 live grants per run. One grant may explicitly invoke a candidate repeatedly;
it is not an idempotency key. Missing, expired or changed selections produce a
structured rejection requiring another search. No complete Skill directory is
ever sent to the model. Returned search metadata excludes disk paths.

`AllowedTools` is an explicit ceiling, intersected with the request's filtered
registry. Empty means no business tools. The nested runner cannot invoke Skills
recursively. In a complex plan, authorizing `skills.invoke` delegates that
preconfigured ceiling; step meta-tool names do not silently narrow business
permissions. Use separate Agents or request filters for narrower delegation.

The host system policy remains in effect. Only the explicit task is supplied;
the entire main conversation is not copied. The child receives one additional
read-only `skill.read_resource` tool for bounded UTF-8 resources relative to the
selected directory. Absolute paths, traversal and escaping symlinks are rejected.
Resources are capped at 64 KiB per read and 1 MiB cumulatively per invocation.
Reading a script does not execute it; execution requires an application tool.

`skill.Executor` defines the loaded-package execution boundary; the Agent's
adapter owns the concrete policy and continuation implementation.

## Effects, budgets and recovery

Default child limits are eight model iterations and sixteen actual tool calls.
`SkillOptions` may change these local limits; every dispatch also consumes the
request-wide budget. Search, invoke and each business tool dispatch are counted
once each. Resuming a suspended invoke dispatches the meta tool again, while
completed internal business calls are retained rather than replayed.

The child uses the request's model and tool middleware. `Guard` and `Confirmation`
apply to individual internal tools. Invocations declare delegated effects and are
never automatically retried or given fallback after an execution error. Composite
results preserve actual nested evidence/actions, including on failure.

Every checkpoint saves grants and active invocations, including a checkpoint
caused by an ordinary tool after search. Use a durable `checkpoint.Store` for
process restarts. Resume restores the same scope, validates Skill, resource and
permission fingerprints, then claims approval once through the store. Approval
is bound to the exact pending nested call and arguments. An active persisted
continuation may resume after its selection grant expires; unused grants retain
their original expiry. Unknown write outcomes are never replayed.

The opaque `SkillState` field extends checkpoint v2 without invalidating older
checkpoints that contain no Skill state. Resume requires equivalent application
configuration and the same permitted tools. Changes to pinned instructions,
already-read resources or permissions produce drift errors before dispatch.

Runtime observer events include `skill.search.completed` and
`skill.invoke.started/completed/failed/suspended/resumed`, with invocation IDs and
fingerprints. Default events omit instructions, task bodies and disk paths.
Applications can inspect `LoadReport` for catalog reload events.
