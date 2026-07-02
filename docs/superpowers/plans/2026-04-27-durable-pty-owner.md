# Durable PTY Owner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let middleman create and reopen durable workspaces on hosts where tmux is unavailable by launching a lightweight middleman-owned PTY process.

**Architecture:** Keep tmux as the preferred terminal backend. Add a `ptyowner` backend that middleman launches as a detached helper process from the same executable; the helper owns the shell PTY, exposes a local attach/control socket, and survives middleman server restarts until the workspace is deleted. Workspace setup, cleanup, and terminal WebSocket handling move behind a small backend interface so tmux and pty-owner share the same lifecycle.

**Tech Stack:** Go, SQLite, existing Huma API, existing browser terminal WebSocket protocol, `github.com/creack/pty/v2` on Unix-like hosts, a Windows PTY implementation behind build tags during the Windows task.

---

## Current Context

- `internal/workspace.Manager.Setup` currently treats `tmux new-session` as required before a workspace can become `ready`.
- `internal/terminal.Handler` always calls `EnsureTmux` and then runs `tmux attach-session` under a local PTY for each browser WebSocket.
- `internal/workspace/localruntime.Manager` can run direct PTYs, but those sessions are in-process and intentionally stop on middleman shutdown unless they were wrapped in tmux.
- Workspace responses and sidebar activity are tmux-named today: `tmux_session`, `tmux_working`, `tmux_activity_source`, and related fields.
- `middleman` already has a simple subcommand shape in `cmd/middleman/main.go`, so the owner can be a hidden subcommand without adding another installed binary.

## Approach

### Option 1: Make direct `localruntime` shell sessions durable

This would persist runtime sessions and restart them after middleman restarts.

Pros:
- Reuses a lot of existing PTY session code.

Cons:
- Restarting is not durable; it loses process state.
- It does not solve the main workspace terminal route, which currently expects the base tmux session.

Rejected.

### Option 2: Add a pty-owner helper process and backend abstraction

Middleman starts a detached helper process per base workspace terminal when tmux is unavailable. The helper owns the PTY and serves a local socket protocol for attach, input, resize, recent output replay, status, and stop.

Pros:
- Matches tmux's key durability property: middleman can restart while the workspace shell keeps running.
- Keeps tmux behavior unchanged where tmux exists.
- Gives Windows a backend path that does not depend on tmux.

Cons:
- Adds an internal local protocol and helper process lifecycle to test.
- Requires a Windows-specific PTY driver.

Chosen.

### Option 3: Embed a full terminal multiplexer library

This would build panes/sessions/windows inside middleman.

Pros:
- Could eventually replace tmux-specific assumptions.

Cons:
- Much larger than needed for a single durable shell per workspace.
- Higher risk to the existing terminal behavior.

Rejected.

## File Structure

- Create `internal/ptyowner/protocol.go`: socket request/response and frame types shared by server and helper.
- Create `internal/ptyowner/client.go`: helper discovery, ping, start, attach, resize, stop, and status client.
- Create `internal/ptyowner/owner.go`: owner process runtime loop, PTY lifecycle, subscriber fanout, output replay, and shutdown.
- Create `internal/ptyowner/paths.go`: session directory/socket path helpers rooted under middleman's data/worktree state.
- Create `internal/ptyowner/driver_unix.go`: PTY start/resize implementation using `github.com/creack/pty/v2`.
- Create `internal/ptyowner/driver_windows.go`: Windows PTY start/resize implementation, introduced with a proven ConPTY-backed dependency or a small `x/sys/windows` wrapper.
- Create `internal/workspace/terminal_backend.go`: `TerminalBackend` interface and tmux/pty-owner backend selection.
- Modify `internal/workspace/manager.go`: replace direct tmux setup/cleanup/ensure calls with backend calls while preserving tmux methods for the tmux backend.
- Modify `internal/terminal/handler.go`: attach to the selected backend instead of always attaching tmux.
- Modify `internal/server/server.go`: construct terminal backends, pass pty-owner paths/options, and skip tmux orphan cleanup when tmux is unavailable.
- Modify `internal/server/huma_routes.go` and `internal/server/api_types.go`: preserve current response fields but compute activity from the selected backend when possible.
- Modify `cmd/middleman/main.go`: add hidden `pty-owner` subcommand.
- Add tests in `internal/ptyowner/*_test.go`, `internal/workspace/manager_test.go`, `internal/terminal/handler_test.go`, and `internal/server/api_test.go`.

## Task 1: Add the PTY Owner Protocol

**Files:**
- Create: `internal/ptyowner/protocol.go`
- Create: `internal/ptyowner/paths.go`
- Test: `internal/ptyowner/protocol_test.go`

- [ ] **Step 1: Write protocol and path tests**

Add tests that assert:
- session names reject path separators and empty values
- socket/state paths are stable under a provided root
- JSON control frames round-trip for `attach`, `resize`, `stop`, `status`, and `input`
- binary output frames preserve arbitrary bytes

Run: `go test ./internal/ptyowner -run 'TestProtocol|TestSessionPaths' -shuffle=on`

Expected: FAIL because `internal/ptyowner` does not exist.

- [ ] **Step 2: Implement protocol structs**

Define:

```go
type Request struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Data []byte `json:"data,omitempty"`
}

type Response struct {
	Type     string `json:"type"`
	OK       bool   `json:"ok,omitempty"`
	Error    string `json:"error,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
}
```

Use newline-delimited JSON for control messages and length-prefixed binary frames for PTY output so the owner can multiplex replay, live output, and exit notification over one local connection.

- [ ] **Step 3: Implement path helpers**

Use a root like `<data-dir>/pty-owner/<session>/` with:
- `control.sock` on Unix
- `control.pipe` metadata naming `\\.\pipe\middleman-<session>` on Windows
- `owner.json` containing session, cwd, pid, created_at, and backend version

Reject session names that are empty or contain `/`, `\`, NUL, or `..`.

- [ ] **Step 4: Verify**

Run: `go test ./internal/ptyowner -run 'TestProtocol|TestSessionPaths' -shuffle=on`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ptyowner
git commit -m "feat: define durable pty owner protocol"
```

Commit body:

```text
The pty owner needs a small local protocol before workspace setup can
delegate terminal ownership away from tmux. This adds the shared frame
types and stable per-session paths used by the owner and middleman.
```

## Task 2: Implement the Owner Runtime on Unix

**Files:**
- Create: `internal/ptyowner/owner.go`
- Create: `internal/ptyowner/driver_unix.go`
- Test: `internal/ptyowner/owner_test.go`

- [ ] **Step 1: Write owner lifecycle tests**

Cover:
- owner starts a shell command in a PTY and emits output to an attached client
- a detached client can reconnect and receive recent output replay
- resize control calls the driver resize path
- stop terminates the child process and removes the socket/state files
- owner exits when the child exits and reports the exit code

Run: `go test ./internal/ptyowner -run TestOwner -shuffle=on`

Expected: FAIL because owner runtime is missing.

- [ ] **Step 2: Implement the owner**

Implement `RunOwner(ctx, Options) error` with:
- command resolution using the same executable safety rules as `localruntime.resolveExecutable`
- sanitized environment using the existing `localruntime` environment helper or a small exported wrapper
- PTY output fanout to attached clients
- a bounded replay buffer matching `localruntime.maxSessionOutputReplay` size
- graceful stop with context timeout and process kill fallback

- [ ] **Step 3: Add Unix PTY driver**

Under `//go:build !windows`, wrap:
- `pty.StartWithSize`
- `pty.Setsize`
- process wait/close

Keep the owner package API platform-neutral so the Windows driver can land separately.

- [ ] **Step 4: Verify**

Run: `go test ./internal/ptyowner -run TestOwner -shuffle=on`

Expected: PASS on Unix-like hosts.

- [ ] **Step 5: Commit**

```bash
git add internal/ptyowner internal/workspace/localruntime
git commit -m "feat: run durable pty owner sessions"
```

Commit body:

```text
The fallback backend needs a process outside the middleman server to
own the workspace terminal. This adds the owner runtime, PTY driver,
output replay, resize, and stop behavior for Unix-like hosts.
```

## Task 3: Add the Hidden `middleman pty-owner` Subcommand

**Files:**
- Modify: `cmd/middleman/main.go`
- Test: `cmd/middleman/main_test.go`

- [ ] **Step 1: Write CLI tests**

Cover:
- `runCLI([]string{"pty-owner"})` requires a session, cwd, and root
- `version` and `config read` behavior remain unchanged
- invalid owner args return an error without starting the normal server

Run: `go test ./cmd/middleman -run 'TestRunCLIPtyOwner|TestRunCLIVersion|TestRunCLIConfig' -shuffle=on`

Expected: FAIL for the new pty-owner tests.

- [ ] **Step 2: Implement subcommand parsing**

Add a hidden `pty-owner` branch before normal server startup:

```go
case "pty-owner":
    return runPtyOwnerCLI(args[1:], stdout)
```

Flags:
- `-session`
- `-cwd`
- `-root`
- `-shell` optional repeated/JSON command flag, defaulting to the same login shell behavior as tmux setup

Do not require GitHub token or config loading for this subcommand.

- [ ] **Step 3: Verify**

Run: `go test ./cmd/middleman -run 'TestRunCLIPtyOwner|TestRunCLIVersion|TestRunCLIConfig' -shuffle=on`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/middleman internal/ptyowner
git commit -m "feat: expose pty owner helper command"
```

Commit body:

```text
Middleman launches the durable PTY owner from its own executable so
installations do not need a second binary. The helper command bypasses
normal server startup and owns only one workspace terminal session.
```

## Task 4: Introduce Workspace Terminal Backends

**Files:**
- Create: `internal/workspace/terminal_backend.go`
- Modify: `internal/workspace/manager.go`
- Test: `internal/workspace/manager_test.go`

- [ ] **Step 1: Write backend selection tests**

Cover:
- tmux available selects tmux backend
- tmux unavailable selects pty-owner backend
- configured tmux command that exists still selects tmux
- setup failure in the selected backend marks the workspace `error`
- retry cleanup calls the selected backend cleanup

Run: `go test ./internal/workspace -run 'TestManager.*TerminalBackend|TestManager.*Retry' -shuffle=on`

Expected: FAIL because no backend abstraction exists.

- [ ] **Step 2: Add the interface**

Define:

```go
type TerminalBackend interface {
    Kind() string
    Start(ctx context.Context, session string, cwd string) error
    Ensure(ctx context.Context, session string, cwd string) error
    Stop(ctx context.Context, session string) error
    Snapshot(ctx context.Context, session string) (TmuxPaneSnapshot, error)
    IsAbsent(error) bool
}
```

Name can stay tmux-flavored in public response structs for compatibility, but internal code should refer to terminal sessions.

- [ ] **Step 3: Move tmux calls behind `tmuxBackend`**

Keep existing tmux methods in `manager.go` or move them into `terminal_backend.go` with no behavior change:
- `newTmuxSession`
- `EnsureTmux`
- `killTmuxSession`
- `TmuxPaneSnapshot`
- `ReapOrphanTmuxSessions`

- [ ] **Step 4: Add `ptyOwnerBackend`**

Use `ptyowner.Client` to:
- start the helper if no live owner responds
- ensure existing owner on attach
- stop owner on delete/retry
- return output/title snapshot from owner status when available

- [ ] **Step 5: Wire setup and cleanup**

Change `Setup` from hardcoded `m.newTmuxSession` to `m.terminal.Start`.
Change cleanup from `cleanupTmuxSession` to `cleanupTerminalSession`, preserving stored runtime tmux cleanup for existing tmux-backed agent sessions.

- [ ] **Step 6: Verify**

Run: `go test ./internal/workspace -run 'TestManager.*TerminalBackend|TestManagerEnsureTmux|TestManagerReapOrphanTmuxSessions' -shuffle=on`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/workspace internal/ptyowner
git commit -m "feat: choose durable workspace terminal backends"
```

Commit body:

```text
Workspace setup should no longer fail solely because tmux is missing.
This introduces a terminal backend boundary and falls back to the
pty-owner helper when tmux cannot be resolved.
```

## Task 5: Attach Browser Terminals Through the Selected Backend

**Files:**
- Modify: `internal/terminal/handler.go`
- Modify: `internal/terminal/bridge.go`
- Test: `internal/terminal/handler_test.go`

- [ ] **Step 1: Write terminal handler tests**

Cover:
- tmux backend still shells out to `tmux attach-session`
- pty-owner backend attaches to the owner socket
- binary input reaches the owner
- resize control reaches the owner
- reconnect receives replayed output
- only one active base terminal attachment is still enforced per workspace

Run: `go test ./internal/terminal -run TestHandler -shuffle=on`

Expected: FAIL for pty-owner attach behavior.

- [ ] **Step 2: Add an attachment interface**

Define an internal terminal attachment interface mirroring `localruntime.Attachment`:

```go
type Attachment interface {
    Output() <-chan []byte
    Done() <-chan struct{}
    Write([]byte) error
    Resize(cols, rows int) error
    Close()
    ExitCode() int
}
```

Use it for the pty-owner path. Keep the tmux path as a local PTY running `tmux attach-session` if that backend is selected.

- [ ] **Step 3: Update handler flow**

After workspace readiness checks and slot claim:
- call backend `Ensure`
- if backend is tmux, preserve existing `tmux attach-session` flow
- if backend is pty-owner, bridge WebSocket to the owner attachment

- [ ] **Step 4: Verify**

Run: `go test ./internal/terminal -run TestHandler -shuffle=on`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/terminal internal/workspace internal/ptyowner
git commit -m "feat: attach workspace terminals without tmux"
```

Commit body:

```text
The browser terminal now attaches through the workspace terminal backend.
Tmux attach behavior remains unchanged, while pty-owner workspaces bridge
directly to the durable owner process.
```

## Task 6: Wire Server Startup and API Behavior

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/huma_routes.go`
- Modify: `internal/server/api_types.go`
- Test: `internal/server/api_test.go`

- [ ] **Step 1: Write full-stack API tests**

Add e2e tests with real SQLite that simulate tmux absence:
- creating a workspace reaches `ready` instead of `error`
- `GET /workspaces/{id}` returns the existing response shape
- WebSocket `/workspaces/{id}/terminal` can read/write through the pty-owner backend
- server shutdown and recreation can reattach to the still-running owner
- delete stops the owner and removes state files

Run: `go test ./internal/server -run 'TestWorkspace.*PtyOwner|TestWorkspaceTerminal.*PtyOwner' -shuffle=on`

Expected: FAIL because server construction does not yet pass owner options.

- [ ] **Step 2: Add server options**

Extend `server.ServerOptions` with an internal pty-owner root or derive it from `WorktreeDir`/config data dir:

```go
PtyOwnerDir string
```

In production, use `<cfg.DataDir>/pty-owner`.

- [ ] **Step 3: Select backend during `newServer`**

Resolve tmux availability once with the existing `ResolveLaunchTargets`/`lookPath` behavior. If the tmux target is unavailable, configure workspace manager and terminal handler with the pty-owner backend.

Do not call `ReapOrphanTmuxSessions` when tmux is unavailable.

- [ ] **Step 4: Preserve response compatibility**

Keep JSON fields as-is for now:
- `tmux_session` remains the stable session identifier
- `tmux_working` can report true when either tmux or pty-owner shows activity
- `tmux_activity_source` may gain a value such as `pty_owner` only if frontend tests are updated; otherwise map pty-owner recent output to `output`

- [ ] **Step 5: Verify**

Run: `go test ./internal/server -run 'TestWorkspace.*PtyOwner|TestWorkspaceTerminal.*PtyOwner|TestWorkspaceRuntimeTargets' -shuffle=on`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server internal/workspace internal/terminal internal/ptyowner
git commit -m "feat: keep workspaces ready when tmux is unavailable"
```

Commit body:

```text
Workspace creation now falls back to a middleman-owned PTY process when
tmux is missing. The API keeps the existing workspace response contract
while allowing terminal reattachment after middleman restarts.
```

## Task 7: Add Windows Driver Support

**Files:**
- Create: `internal/ptyowner/driver_windows.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Test: `internal/ptyowner/owner_windows_test.go`

- [ ] **Step 1: Choose and document the Windows PTY dependency**

Use the smallest maintained ConPTY-backed Go package that supports:
- start process attached to pseudoconsole
- read/write
- resize
- wait/close

If a new dependency is needed, add a short comment in `driver_windows.go` explaining why `github.com/creack/pty/v2` is not used for Windows.

- [ ] **Step 2: Implement Windows driver behind build tags**

Expose the same internal driver methods used by `owner.go`.

- [ ] **Step 3: Add compile coverage**

Run:

```bash
GOOS=windows GOARCH=amd64 go test ./internal/ptyowner -run TestSessionPaths -shuffle=on
GOOS=windows GOARCH=amd64 go test ./cmd/middleman -run TestRunCLIPtyOwner -shuffle=on
```

Expected: PASS or a documented skip for tests that require a live ConPTY host.

- [ ] **Step 4: Commit**

```bash
git add internal/ptyowner go.mod go.sum
git commit -m "feat: support pty owner on windows"
```

Commit body:

```text
Windows hosts do not have tmux, so the fallback backend needs a native
PTY driver. This adds the ConPTY implementation behind build tags while
keeping the owner protocol shared across platforms.
```

## Task 8: Update Runtime Target Semantics

**Files:**
- Modify: `internal/workspace/localruntime/targets.go`
- Modify: `internal/workspace/localruntime/manager.go`
- Test: `internal/workspace/localruntime/targets_test.go`
- Test: `internal/workspace/localruntime/manager_test.go`

- [ ] **Step 1: Write tests for no-tmux hosts**

Cover:
- tmux target is unavailable when tmux cannot be found
- agent launches do not attempt tmux wrapping when tmux is unavailable
- plain shell still works
- pty-owner fallback does not make `tmux` appear available as a launch target

Run: `go test ./internal/workspace/localruntime -run 'TestResolveLaunchTargets|TestLaunchCommand' -shuffle=on`

Expected: existing tests pass, new no-tmux fallback tests may fail until behavior is explicit.

- [ ] **Step 2: Keep agent wrapping strictly tmux-based**

Do not wrap agent runtime sessions in pty-owner in this feature. The user request is durable base workspaces when no tmux exists; extending per-agent runtime durability can follow later.

- [ ] **Step 3: Verify**

Run: `go test ./internal/workspace/localruntime -run 'TestResolveLaunchTargets|TestLaunchCommand' -shuffle=on`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/workspace/localruntime
git commit -m "fix: keep runtime launch targets honest without tmux"
```

Commit body:

```text
The pty-owner backend makes base workspaces durable, but it is not a
tmux replacement for agent launch targets. Runtime target reporting now
keeps that boundary explicit on hosts without tmux.
```

## Task 9: Final Verification

**Files:**
- All changed files

- [ ] **Step 1: Regenerate API artifacts if response types changed**

Run: `make api-generate`

Expected: no diff unless OpenAPI response types changed intentionally.

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./internal/ptyowner ./internal/workspace ./internal/terminal ./internal/server ./internal/workspace/localruntime -shuffle=on
```

Expected: PASS.

- [ ] **Step 3: Run full Go tests**

Run: `make test`

Expected: PASS.

- [ ] **Step 4: Run frontend tests only if JSON response semantics changed**

Run: `bun test` from `frontend/` only if frontend code or generated API types change.

Expected: PASS.

- [ ] **Step 5: Commit any verification-only fixes**

Use a conventional commit whose subject names the user-visible reason for the fix.

## Self-Review

- Spec coverage: The plan covers durable no-tmux workspaces, owner process launch, reconnect after middleman restart, cleanup, Windows support, and compatibility with existing tmux behavior.
- Placeholder scan: No TODO/TBD placeholders are present.
- Type consistency: `ptyowner` owns the local protocol; `workspace.TerminalBackend` owns lifecycle selection; `terminal.Handler` owns browser attach bridging.
- Scope check: Per-agent runtime durability through pty-owner is intentionally excluded. Existing tmux-backed agent durability remains unchanged.
