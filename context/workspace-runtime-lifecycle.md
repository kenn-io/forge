# Workspace Runtime Lifecycle

Use this document for changes in workspace delete flows, runtime session
management, tmux persistence, and workspace terminal UI behavior.

## Purpose

- Keep the lifecycle of kenn-forge-managed runtime state explicit.
- Preserve the distinction between the durable workspace, the base tmux
  terminal, and launched runtime sessions.
- Prevent review regressions around destructive ordering, stale tmux rows, and
  UI/runtime disagreement after exits.

## Runtime Model

Kenn Forge manages three related but different things:

- The persisted workspace record and worktree.
- The base workspace `tmux` terminal, which is durable and reconnectable.
- Launched runtime sessions and the shell drawer. When tmux is available they
  are tmux-backed, recorded, and reconnectable across kenn-forge restarts; when
  tmux is unavailable they use ptyowner.

Rules:

- The base workspace `tmux` tab is part of the durable workspace experience.
- Launched agent sessions and shell sessions are not durable after natural exit.
- The shell drawer is a singleton per workspace, but a tmux-backed shell should
  survive kenn-forge server restarts until the shell exits or the workspace is
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
- PTY-owner process exit may precede final output; keep subscribers alive for a
  bounded drain or loaded runners lose the last repaint
  (`internal/workspace/localruntime/manager.go::watchPtyOwner`).

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

## Server Shutdown Ordering

Workspace and Fleet own independent idempotent, context-bounded lifecycles;
Fleet starts after Workspace and shuts down its workers and SSH transport before
Workspace stops (`internal/server/fleetapi/handler.go::Handler.Shutdown`). Root closes
Pull admission and cancels its workers before HTTP drain, then waits for Pull before
Fleet in the post-drain dependency stage (`internal/server/server.go::Server.Shutdown`,
`internal/server/pullapi/handler.go::Handler.Stop`).
If any stage times out, shutdown must not advance; a later call resumes at the
blocked stage (`internal/server/workspace_dependency_shutdown.go::workspaceDependencyShutdown`).

## Tmux Persistence Rules

Persisted tmux-backed runtime rows are only valid while the backing tmux session
still exists.

- Restore persisted runtime tmux sessions on startup only when the backing tmux
  session is still present.
- Treat "tmux session is no longer running" and equivalent dead-server cases as
  gone state to be cleaned up, not as a reason to preserve stale runtime rows.
- During explicit delete or stop flows, forgetting the persisted row is part of
  cleanup.
- During kenn-forge shutdown, detach/restart behavior is different: do not treat
  normal server shutdown as a natural user exit that should erase recoverable
  base runtime state.
- New tmux sessions use the `forge-` prefix; persisted `middleman-` session
  names remain valid and must not be rewritten (`internal/workspace/`).
- Every tmux client attach must force UTF-8; service launchers may omit locale
  variables, causing tmux to replace non-ASCII output before WebSocket transport
  (`internal/workspace/localruntime/tmux_launcher.go::tmuxAttachSessionCommand`).
- Local-runtime reconnects restore browser-generated cursor-key, mouse, focus,
  and paste DEC modes from session-wide PTY state, not bounded screen replay
  (`internal/workspace/localruntime/manager.go::session.subscribe`).
- Mode transitions precede one session-wide UTF-8-aware VT tail even in the
  alternate screen; retain split-rune introducers and decoded C1 controls/ST
  (`internal/workspace/localruntime/terminal_sequence_tail.go::trailingIncompleteTerminalDataLen`).
- Mode replay must mirror xterm.js 6 effective semantics: DEC private save/restore
  and ignored 1005/1015 encodings cannot re-enable modes or displace 1006/1016
  (`internal/workspace/localruntime/terminal_input_modes.go::terminalInputModeState.observe`).
- Recognize C1 CSI only as decoded U+009B; valid non-C1 UTF-8 invalidates pending
  controls, while invalid scalars, BOM, and standalone continuation bytes are
  decoder-discarded without reaching the VT parser
  (`internal/workspace/localruntime/terminal_input_modes.go::terminalInputModeState.observe`).
- Send reconnect cancellation to xterm as bytes, not text; only binary input
  clears its streaming UTF-8 decoder before replay
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::cancelPendingTerminalSequence`).
- Direct local-runtime sessions request a subscriber-only replay boundary and
  withhold resize/refresh controls until xterm parses it; the backend queues the
  boundary only after the retained VT/UTF-8 tail reaches ground. Fresh legacy
  and Fleet attachments still receive dimensions on every connection
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::connect`,
  `internal/workspace/localruntime/manager.go::session.subscribeInternal`).
- In xterm.js 6, DECSTR resets cursor keys, focus reporting, and bracketed paste,
  but leaves the mouse service's protocol and encoding unchanged
  (`internal/workspace/localruntime/terminal_input_modes.go::terminalInputModeState.softReset`).

## UI Contract Rules

The workspace UI should reflect runtime truth without leaving users stranded in
stale tabs.

- Runtime lists returned by `/workspaces/{id}/runtime` are the authoritative
  backend view of live launched sessions.
- Workspace terminals use xterm.js exclusively; there is no renderer setting
  or alternate renderer path (`frontend/src/lib/components/terminal/TerminalPane.svelte`).
- Treat terminal processes as native-terminal-equivalent, but accept bounded, write-only OSC 52 writes only after one
  recent one-shot trusted DOM gesture; terminal data callbacks are not input provenance, and browser denial falls back
  through CSRF-protected loopback (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::handleTerminalKeyDown`).
- Keep OSC 52 validation synchronous and ahead of gesture consumption; replacing it with `@xterm/addon-clipboard`
  would require custom prefilters, nonblocking write separation, and handler-order coupling to preserve current
  rejection, parser-progress, and read-denial guarantees.
- Terminal clipboard authority is revoked on external pointerdown/focus transfer, pane inactivity/parking, disablement,
  or window focus/visibility loss; pointerdown capture closes the race before a competing browser copy starts, and
  revocation also stops in-flight browser-to-loopback fallback chains before their next stage
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::handleDocumentPointerDown`).
- Terminal pointerdown only prepares clipboard access; selection intent requires both a four-CSS-pixel dead zone and
  movement of roughly one rendered terminal cell, so physical focus-click jitter expires without reaching browser or
  loopback clipboard fallbacks at readable terminal geometry without suppressing deliberate drags.
  Confirmed captured drags retain authority only through internal or destinationless focus movement while the pane stays
  active; their watchdog also releases browser capture, so a missing release cannot shield later focus loss
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::hasPointerSelectionIntent`).
- Host clipboard writes require a local browser; trusted loopback proxies must report exactly one client IP assigned
  to the host, because the proxy's loopback `RemoteAddr` alone does not establish browser locality
  (`internal/server/terminal_clipboard_access.go::isLocalTerminalClipboardRequest`).
- During active tmux SGR drags outside xterm bounds, add only clamped edge wheel, drag, and release reports; forward
  all other mouse reports unchanged, and never retain unsent drag state across a WebSocket boundary
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::connect`).
- macOS loopback clipboard fallback must run `pbcopy` with `LC_ALL=en_US.UTF-8`; service launchers may omit
  a UTF-8 locale and make `pbcopy` reinterpret unchanged UTF-8 input
  (`internal/systemclipboard/systemclipboard.go::nativeWriter.WriteText`).
- Windows loopback clipboard fallback must send UTF-16LE to `clip.exe`; UTF-8 stdin is code-page-dependent and corrupts
  non-ASCII text (`internal/systemclipboard/systemclipboard.go::encodeUTF16LE`).
- The frontend may react immediately to terminal exit events, but should then
  reconcile with a runtime refresh.
- Only the active terminal pane may publish cell geometry; font-metric changes
  must refit and publish columns/rows through the refresh control because unchanged
  container pixels do not trigger resize observation (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::refreshVisibleTerminal`).
- Keyboard and pointer interactions inside workspace rows must not trigger
  unintended navigation when the user is targeting a nested control.
- Persisted "last active tab" state must be scoped per workspace.

## Shell Command Override

When tmux is unavailable, the plain shell session is launched through ptyowner
rather than as a direct child of kenn-forge. This decouples shell ownership and
lifetime from the kenn-forge server process. Hardened deployments (systemd
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
  "--description=kenn-forge shell",
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

## Switch-Timing Instrumentation

The frontend emits one-shot `workspace-switch:<phase>` User Timing measures per
workspace switch (route selection through terminal first paint), recorded via
`frontend/src/lib/instrumentation/workspaceSwitchTiming.ts`. The phase names
are stable API for before/after performance comparisons — do not rename them,
and record new phases through that module so superseded-switch and duplicate
guards keep applying. `make profile-workspace-switch` captures a reproducible
profile; see `frontend/tests/profiling/README.md`. Each measure's `detail.traceId`
joins it to the same request's server-side OTel trace, whose export is opt-in
via `OTEL_TRACES_EXPORTER`.

- Every frontend HTTP path, including hand-written runtime requests, must use
  the shared traced fetch boundary so `traceparent` and `baggage` are not lost
  when code bypasses the generated client (`frontend/src/lib/api/runtime.ts::tracedFetch`).
- Base-path routing must preserve the inner Huma route pattern for outer OTel
  middleware; otherwise prefixed API spans collapse to the base-path pattern
  (`internal/server/otel_middleware.go::stripPrefixPreservingPattern`).
- A workspace-switch trace ends at terminal first paint or after 30 seconds;
  cancellation and supersession must clear the matching fallback timer
  (`frontend/src/lib/instrumentation/workspaceSwitchTiming.ts::endSwitchTrace`).
- Automatic HTTP tracing excludes only exact long-lived stream routes/modes;
  short endpoints such as telemetry event capture remain traced
  (`internal/server/otel_middleware.go::otelTraceable`).
- Fleet proxy and SSH terminal WebSockets need their own bounded attach span,
  ending after setup and before the long-lived bridge
  (`internal/server/fleetapi/fleet_proxy.go::startFleetAttachSpan`).

## Testing Expectations

Prefer full-stack coverage when the bug crosses backend lifecycle and frontend
behavior.

- Use real SQLite-backed server tests for delete ordering, tmux cleanup, and
  runtime-session API behavior.
- Workspace/Projects handler and Git-heavy wire tests belong to
  `internal/server/workspaceapi` or `internal/server/workspacetest`; Git and
  worktree cases in the public wire lane must acquire its weighted semaphore.
- Root-retained Git tests must cross a root composition boundary and acquire
  the root Git semaphore before expensive setup; `t.Parallel` alone is never
  a Git-work concurrency bound (`internal/server/api_test.go::acquireRootWorkspaceGitSlot`).
- A server test that creates a workspace must wait for setup to reach a terminal
  state (`waitForWorkspaceReady`) before it returns. The `202 Accepted` create
  runs clone/setup in a background goroutine; if the test returns first, that
  goroutine can keep writing into the test's `t.TempDir` clone path and race
  `RemoveAll` teardown, failing intermittently with "directory not empty".
- Kata API fixtures must use their package-private tmux server and force-delete
  created workspaces; shutdown preserves durable base sessions, so temp-dir
  cleanup alone leaks (`internal/server/kataapi/testmain_test.go::TestMain`).
- Use tmux wrappers/fakes for missing-session and dead-server cases.
- Add frontend or Playwright coverage when the regression is visible in tab
  selection, shell drawer state, or workspace navigation.

Related intent docs:

- [`context/workspace-apis.md`](./workspace-apis.md) for workspace API scope and
  non-goals.
- [`context/ui-interaction-contracts.md`](./ui-interaction-contracts.md) for
  row/button, tab, and keyboard interaction expectations in the UI.
