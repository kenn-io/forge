# Claude Session Context Generation Design

## Goal

Send Middleman's generated workspace identity through Claude's `SessionStart`
hook without reading `CLAUDE.local.md` or treating any generated instruction
file as a data source.

## Daemon API Boundary

The installed hook command receives the configured Middleman config path. For
every supported Claude and Codex lifecycle event, the CLI discovers the running
daemon through runtime metadata, authenticates with its local API token, and
forwards the original hook JSON together with the agent and Middleman runtime
session identity.

The CLI is transport only. It does not write agent-activity reports, open the
Middleman database, inspect workspace files, or decide how hook events map to
workspace state. If the daemon is unavailable, the CLI exits successfully with
no output so agent hooks remain fail-open.

The daemon owns both effects of the event stream. It records Working, Idle,
Input, Approval, and session-end transitions through the existing
agent-activity store. On a Claude `SessionStart`, it also resolves the persisted
workspace whose worktree matches the hook `cwd` and uses the same workspace
summary conversion and rendering pipeline as agent-launch context generation:
`BuildAgentContext` followed by `RenderAgentContext`.

The generated ownership marker is an instruction-file concern and is omitted
from Claude's `additionalContext` value. All remaining content, including the
untrusted-source-text boundary, is identical to the normal generated workspace
context.

`CLAUDE.local.md` is never opened or inspected by the hook. Its presence,
absence, contents, type, or ownership therefore cannot influence session-start
context.

## Shared Generation Boundary

Workspace context lookup and rendering live behind a workspace-package
function used by the daemon API handler. The existing launch path continues to
use the same `BuildAgentContext` and `RenderAgentContext` functions when
composing agent instruction files. No second context format or field mapping is
introduced.

The worktree lookup compares normalized paths against persisted workspace
summaries and returns no context when the hook is not running in a known
Middleman workspace.

## Error Handling

Agent activity remains independent from context generation: the daemon records
the lifecycle payload before attempting the Claude-specific lookup. Invalid
hook payloads, unsupported events, missing workspaces, daemon discovery or
transport failures, and generation failures produce no hook output and do not
fail the agent session.

The hook continues to emit context only for Claude `SessionStart`; Codex and
other lifecycle events remain state-only.

## Installation

Hook installation records the absolute Middleman config path explicitly in the
generated command. Reinstalling hooks refreshes commands after a config-path
change. Data-directory, listener, base-path, and API-auth changes are resolved
from config and daemon runtime metadata rather than copied into hook commands.

## Testing

Focused tests will establish that:

- Claude `SessionStart` emits context generated from a persisted workspace even
  when `CLAUDE.local.md` does not exist;
- a user-authored or stale `CLAUDE.local.md` cannot affect emitted context;
- all supported lifecycle events reach the daemon and update agent activity;
- unknown worktrees and unavailable daemons fail open with no context output;
- non-Claude agents and non-start events do not emit context while still
  recording their activity transitions;
- installed hook commands carry the config path required for daemon discovery.

The affected Go package tests and repository verification provide the final
regression check.
