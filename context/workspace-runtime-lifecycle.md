# Workspace Runtime Lifecycle

Use this document for changes in workspace delete flows, runtime session
management, tmux persistence, and workspace terminal UI behavior.

## Purpose

- Keep the lifecycle of middleman-managed runtime state explicit.
- Preserve the distinction between the durable workspace, the base tmux
  terminal, and launched runtime sessions.
- Prevent review regressions around destructive ordering, stale tmux rows, and
  UI/runtime disagreement after exits.

## Runtime Model

Middleman manages three related but different things:

- The persisted workspace record and worktree.
- The base workspace `tmux` terminal, which is durable and reconnectable.
- Launched runtime sessions and the shell drawer. When tmux is available they
  are tmux-backed, recorded, and reconnectable across middleman restarts; when
  tmux is unavailable they use ptyowner.

Rules:

- The base workspace `tmux` tab is part of the durable workspace experience.
- Launched agent sessions and shell sessions are not durable after natural exit.
- The shell drawer is a singleton per workspace, but a tmux-backed shell should
  survive middleman server restarts until the shell exits or the workspace is
  deleted.

## Natural Exit Rules

Natural process exit should collapse stale runtime state quickly.

- When a launched runtime session exits naturally, remove it from backend
  runtime state and from the workspace UI.
- If the exited session was active, return the UI to Home rather than leaving a
  dead terminal tab selected.
- If the session was tmux-backed, forget the persisted runtime tmux row once the
  backing tmux session is gone.
- When the shell drawer process exits, close or collapse the drawer, forget any
  persisted runtime tmux row once the backing tmux session is gone, and require
  a fresh launch on reopen.

The base workspace `tmux` tab is the exception:

- Keep reconnect behavior for the base `tmux` tab.
- Do not auto-close that tab just because the websocket detached or the view
  remounted.

## Delete Ordering Rules

Workspace deletion is intentionally conservative.

- First decide whether deletion is allowed, including dirty-worktree checks.
- Only after a clean preflight may runtime sessions and shells be stopped.
- Only after runtime shutdown succeeds should destructive worktree and DB
  teardown continue.

This ordering prevents a rejected delete from silently killing the user's live
workspace sessions.

## Tmux Persistence Rules

Persisted tmux-backed runtime rows are only valid while the backing tmux session
still exists.

- Restore persisted runtime tmux sessions on startup only when the backing tmux
  session is still present.
- Treat "tmux session is no longer running" and equivalent dead-server cases as
  gone state to be cleaned up, not as a reason to preserve stale runtime rows.
- During explicit delete or stop flows, forgetting the persisted row is part of
  cleanup.
- During middleman shutdown, detach/restart behavior is different: do not treat
  normal server shutdown as a natural user exit that should erase recoverable
  base runtime state.

## UI Contract Rules

The workspace UI should reflect runtime truth without leaving users stranded in
stale tabs.

- Runtime lists returned by `/workspaces/{id}/runtime` are the authoritative
  backend view of live launched sessions.
- The frontend may react immediately to terminal exit events, but should then
  reconcile with a runtime refresh.
- Keyboard and pointer interactions inside workspace rows must not trigger
  unintended navigation when the user is targeting a nested control.
- Persisted "last active tab" state must be scoped per workspace.

## Shell Command Override

When tmux is unavailable, the plain shell session is launched through ptyowner
rather than as a direct child of middleman. This decouples shell ownership and
lifetime from the middleman server process. Hardened deployments (systemd
services with `SystemCallFilter=~@privileged`, `LockPersonality=`,
`MemoryDenyWriteExecute=`, etc.) can still need a `[shell] command` wrapper or
external ptyowner manager path that starts the shell outside the restricted
service unit: zsh and bash both call `setresuid(uid, uid, uid)` during startup to
drop saved-uid privileges, and that syscall is in `@privileged`.

For these deployments, set `[shell] command = [...]` to wrap the launch
in something that escapes the parent unit's filter. On systemd hosts,
`systemd-run --user` spawns a fresh transient unit with its own
(unfiltered) policy:

```toml
[shell]
command = [
  "systemd-run", "--user", "--quiet", "--collect", "--wait", "--pipe",
  "--service-type=exec",
  "--property=KillMode=process",
  "--description=middleman shell",
  "--",
  "zsh",  # absolute path or PATH-resolvable name; see below
]
```

Notes:

- `cwd` is propagated by the runtime via `cmd.Dir` — your wrapper must
  forward it to the actual shell. With `systemd-run`, that's
  `--working-directory=$PWD` (or a fixed path); without an explicit
  flag the transient unit does not inherit the launcher's working
  directory.
- The configured argv is invoked verbatim (no shell expansion). The
  first element must be an absolute path or a `PATH`-resolvable name;
  relative paths are rejected so a malicious worktree cannot drop a
  binary into itself and gain code execution.
- When unset, the runtime falls back to `$SHELL`, then `/bin/sh`. This
  is the safe default for unhardened single-user installs.

The `[tmux] command` setting follows the same wrap-it-in-systemd-run
pattern for similar reasons; the two are independent.

## Testing Expectations

Prefer full-stack coverage when the bug crosses backend lifecycle and frontend
behavior.

- Use real SQLite-backed server tests for delete ordering, tmux cleanup, and
  runtime-session API behavior.
- A server test that creates a workspace must wait for setup to reach a terminal
  state (`waitForWorkspaceReady`) before it returns. The `202 Accepted` create
  runs clone/setup in a background goroutine; if the test returns first, that
  goroutine can keep writing into the test's `t.TempDir` clone path and race
  `RemoveAll` teardown, failing intermittently with "directory not empty".
- Use tmux wrappers/fakes for missing-session and dead-server cases.
- Add frontend or Playwright coverage when the regression is visible in tab
  selection, shell drawer state, or workspace navigation.

Related intent docs:

- [`context/workspace-apis.md`](./workspace-apis.md) for workspace API scope and
  non-goals.
- [`context/ui-interaction-contracts.md`](./ui-interaction-contracts.md) for
  row/button, tab, and keyboard interaction expectations in the UI.
