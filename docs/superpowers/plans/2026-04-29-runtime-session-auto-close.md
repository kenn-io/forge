# Runtime Session Auto-Close Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically close launched workspace agent and shell surfaces when their backing process exits, while cleaning persisted tmux runtime rows.

**Architecture:** The runtime manager owns process lifecycle cleanup and removes naturally exited sessions from active maps. The server receives tmux-backed exit callbacks and forgets stored tmux rows. The Svelte workspace view reacts immediately to terminal exit events by unmounting closed panes and returning to Home.

**Tech Stack:** Go, Huma, SQLite, tmux, Svelte 5, Bun, Playwright/component tests.

---

### Task 1: Runtime Manager Cleanup

**Files:**
- Modify: `internal/workspace/localruntime/types.go`
- Modify: `internal/workspace/localruntime/manager.go`
- Test: `internal/workspace/localruntime/manager_test.go`

- [ ] **Step 1: Write failing manager tests**

Add tests that launch the helper `exit` target and assert `ListSessions("ws-1")` becomes empty after natural exit. Add a shell equivalent that asserts `ShellSession("ws-1")` becomes nil after natural shell exit.

- [ ] **Step 2: Run manager tests to verify failure**

Run: `go test ./internal/workspace/localruntime -run 'TestManagerRemovesNaturallyExited(Session|Shell)' -shuffle=on`

Expected: tests fail because exited sessions remain in manager maps.

- [ ] **Step 3: Implement manager cleanup**

Add an `OnSessionExit func(SessionInfo)` option. Store it in `Manager`. Start watcher goroutines through manager methods that call `session.watch()`, remove the exact session pointer from `sessions` or `shells`, then invoke `OnSessionExit` for natural exits. Do not invoke it from shutdown detach.

- [ ] **Step 4: Run manager tests to verify pass**

Run: `go test ./internal/workspace/localruntime -run 'TestManagerRemovesNaturallyExited(Session|Shell)' -shuffle=on`

Expected: tests pass.

### Task 2: Server Tmux Row Cleanup

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/api_test.go`

- [ ] **Step 1: Write failing API tests**

Add one E2E test for non-tmux natural agent exit returning no runtime sessions. Add one tmux-backed E2E test that launches a short-lived helper target, waits for runtime sessions to empty, and asserts `ListWorkspaceTmuxSessions` returns no rows.

- [ ] **Step 2: Run focused API tests to verify failure**

Run: `go test ./internal/server -run 'TestWorkspaceRuntimeNatural.*Exit' -shuffle=on`

Expected: tests fail because runtime sessions or stored tmux rows remain.

- [ ] **Step 3: Wire server callback**

Pass `OnSessionExit` into `localruntime.NewManager`. If `info.TmuxSession` is non-empty and `s.workspaces` is configured, call `ForgetRuntimeTmuxSession` under a bounded background context and log any error.

- [ ] **Step 4: Run focused API tests to verify pass**

Run: `go test ./internal/server -run 'TestWorkspaceRuntimeNatural.*Exit' -shuffle=on`

Expected: tests pass.

### Task 3: Workspace UI Auto-Close

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Modify: `frontend/src/lib/components/terminal/ShellDrawer.svelte`
- Test: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts` or nearest existing component test file

- [ ] **Step 1: Write failing frontend tests**

Add component tests proving agent session exit removes the tab and selects Home, and shell exit calls the parent close/refresh behavior.

- [ ] **Step 2: Run frontend tests to verify failure**

Run: `bun test frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`

Expected: tests fail because agent exit only refetches runtime and shell exit does not close the drawer.

- [ ] **Step 3: Implement Svelte exit handlers**

Add `handleSessionExit(sessionKey, id)` in `WorkspaceTerminalView.svelte`. It should ignore stale workspace ids, unmount the session terminal, select Home if active, remember Home for that workspace, and refresh runtime. Add `handleShellExit(id)` that closes `shellOpen`, clears loading, and refreshes runtime.

- [ ] **Step 4: Validate Svelte code**

Run: `npx @sveltejs/mcp@0.1.22 svelte-autofixer frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte --svelte-version 5`

Expected: no required fixes.

### Task 4: Final Verification

**Files:**
- All modified files

- [ ] **Step 1: Run focused backend tests**

Run: `go test ./internal/workspace/localruntime ./internal/server -run 'TestManagerRemovesNaturallyExited|TestWorkspaceRuntimeNatural.*Exit' -shuffle=on`

Expected: pass.

- [ ] **Step 2: Run focused frontend tests**

Run: `bun test frontend/src/lib/components/terminal/TerminalPane.test.ts frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`

Expected: pass.

- [ ] **Step 3: Run broader applicable tests**

Run: `make test-short`

Expected: pass.

- [ ] **Step 4: Commit**

Commit with a conventional message explaining that exited runtime sessions now close automatically and clean up tmux state.
