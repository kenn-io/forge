# Claude Session Context Generation Design

## Goal

Send Middleman's generated workspace identity through Claude's `SessionStart`
hook without reading `CLAUDE.local.md` or treating any generated instruction
file as a data source.

## Source of Truth

The installed hook command receives the configured Middleman database path in
addition to the agent-activity state directory. On a Claude `SessionStart`, the
hook resolves the persisted workspace whose worktree matches the hook `cwd`.
It then uses the same workspace summary conversion and rendering pipeline as
agent-launch context generation: `BuildAgentContext` followed by
`RenderAgentContext`.

The generated ownership marker is an instruction-file concern and is omitted
from Claude's `additionalContext` value. All remaining content, including the
untrusted-source-text boundary, is identical to the normal generated workspace
context.

`CLAUDE.local.md` is never opened or inspected by the hook. Its presence,
absence, contents, type, or ownership therefore cannot influence session-start
context.

## Shared Generation Boundary

Workspace context lookup and rendering live behind a workspace-package
function used by the hook. The existing launch path continues to use the same
`BuildAgentContext` and `RenderAgentContext` functions when composing agent
instruction files. No second context format or field mapping is introduced.

The worktree lookup compares normalized paths against persisted workspace
summaries and returns no context when the hook is not running in a known
Middleman workspace.

## Error Handling

Agent activity remains independent from context generation: the lifecycle
payload is recorded before attempting the Claude-specific lookup. Invalid hook
payloads, unsupported events, missing workspaces, database-open failures, and
generation failures produce no hook output and do not fail the agent session.

The hook continues to emit context only for Claude `SessionStart`; Codex and
other lifecycle events remain state-only.

## Installation

Hook installation records the absolute Middleman database path explicitly in
the generated command. Reinstalling hooks refreshes commands after a configured
data-directory change, matching the existing installation model for the
agent-activity directory.

## Testing

Focused tests will establish that:

- Claude `SessionStart` emits context generated from a persisted workspace even
  when `CLAUDE.local.md` does not exist;
- a user-authored or stale `CLAUDE.local.md` cannot affect emitted context;
- unknown worktrees and unavailable databases fail open with no context output;
- non-Claude agents and non-start events do not emit context;
- installed hook commands carry the database path required for generation.

The affected Go package tests and repository verification provide the final
regression check.
