# Server Domain Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the monolithic `internal/server` implementation and test binary with independently testable HTTP-domain packages while keeping one resource-bounded workspace lane.

**Architecture:** `internal/server` remains the composition root and owns process-wide middleware, startup, shutdown, SPA serving, and OpenAPI assembly. Each HTTP domain exposes a concrete `Handler` constructed with a narrow `Deps` struct and a `Register(huma.API)` method; the root constructs and registers these handlers explicitly. Workspace and repository-browser tests retain bounded subprocess concurrency so a 24-CPU suite does not overload Git, tmux, or PTY resources.

**Tech Stack:** Go 1.26, Huma v2, SQLite, `testify`, generated API client.

## Global Constraints

- Preserve every existing HTTP path, operation ID, response schema, problem code, CSRF rule, and provider-aware repository identity.
- Do not add compatibility wrappers, aliases, legacy registrations, or dual code paths.
- Keep one domain carve-out per commit.
- Run direct Go tests with `-shuffle=on`, without `-count=1` or `-v`.
- Keep PTY concurrency at one and bound Git/worktree-heavy workspace tests independently of `GOMAXPROCS`.
- Use the generated API client for wire-level integration tests where it already covers the route.

---

### Task 1: Shared HTTP Contract

**Files:**
- Create: `internal/server/httpapi/problems.go`
- Create: `internal/server/httpapi/outputs.go`
- Create: `internal/server/httpapi/operations.go`
- Move tests: `internal/server/problems_test.go` to `internal/server/httpapi/problems_test.go`
- Modify: all `internal/server/*.go` callers of problem/output/operation helpers

**Interfaces:**
- Produces: exported `ProblemError`, `ProblemCode`, problem constructors, generic output wrappers, and `DocumentOperation`.
- Consumes: Huma, platform errors, database repository identity, and token redaction.

- [ ] Export the existing problem-envelope implementation without changing its JSON shape or status mapping.
- [ ] Replace root-package helper calls with `httpapi` calls directly; do not leave forwarding wrappers.
- [ ] Move problem-envelope tests into `httpapi` and retain the root wire-level problem tests.
- [ ] Run `go test ./internal/server/httpapi ./internal/server -run 'TestProblem|TestCodeForStatus|TestMapPlatformError|TestProviderCallProblem' -shuffle=on`.
- [ ] Commit as `refactor(server): isolate the shared HTTP contract`.

### Task 2: Docs API

**Files:**
- Create: `internal/server/docsapi/handler.go`
- Create: `internal/server/docsapi/routes.go`
- Create: `internal/server/docsapi/problems.go`
- Move: `internal/server/docs_routes_test.go` to `internal/server/docsapi/routes_test.go`
- Move: `internal/server/docs_git_routes_test.go` to `internal/server/docsapi/git_routes_test.go`
- Move: `internal/server/docs_daemon_bindings_test.go` to `internal/server/docsapi/daemon_bindings_test.go`
- Modify: `internal/server/server.go`, `internal/server/config_reload.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: `docsapi.Deps{Config, ConfigPath, ConfigMu, Registry}`.
- Produces: `(*docsapi.Handler).Register(huma.API)`, `ReplaceFolders`, and registry inspection methods needed by configuration reload tests.

- [ ] Move docs route types, handlers, publish locking, problem mapping, and daemon-binding validation into `docsapi`.
- [ ] Register the handler from the root server and update config reload through `ReplaceFolders`.
- [ ] Keep loopback/CSRF path classification in the root middleware because it is cross-domain policy.
- [ ] Run `go test ./internal/server/docsapi ./internal/server -run 'TestDocs|TestConfigReload.*Doc' -shuffle=on`.
- [ ] Commit as `refactor(server): carve out the docs API`.

### Task 3: Kata API

**Files:**
- Create: `internal/server/kataapi/handler.go`
- Move Kata route, proxy, task-detail, and workspace-adapter implementation files from `internal/server` into `internal/server/kataapi`
- Move corresponding tests into `internal/server/kataapi`
- Modify: `internal/server/server.go`, `internal/server/config_reload.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: daemon catalog selection, workspace manager access, config reads, HTTP transport, and root event callbacks through `kataapi.Deps`.
- Produces: `(*kataapi.Handler).Register(huma.API)`, `ApplyConfig`, and shutdown cleanup.

- [ ] Replace `Server` receiver methods with a concrete Kata handler.
- [ ] Preserve daemon selection headers, proxy caching, redirect behavior, and workspace mappings.
- [ ] Run `go test ./internal/server/kataapi ./internal/server/e2etest -run 'TestKata' -shuffle=on`.
- [ ] Commit as `refactor(server): carve out the Kata API`.

### Task 4: Messages API

**Files:**
- Create: `internal/server/messagesapi/handler.go`
- Move msgvault routes, saved searches, and remote-image handling into `internal/server/messagesapi`
- Move corresponding tests into `internal/server/messagesapi`
- Modify: `internal/server/server.go`, `internal/server/config_reload.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: config, base path, msgvault client transport, and remote-image dependencies.
- Produces: `(*messagesapi.Handler).Register(huma.API)` and `ApplyConfig`.

- [ ] Preserve `/messages` application naming and `/msgvault` backend API naming.
- [ ] Preserve safe HTML/image behavior and stable upstream error envelopes.
- [ ] Run `go test ./internal/server/messagesapi ./internal/server -run 'TestMsgvault|TestMessages' -shuffle=on`.
- [ ] Commit as `refactor(server): carve out the messages API`.

### Task 5: Repository Browser API

**Files:**
- Create: `internal/server/repobrowserapi/handler.go`
- Move repository browser and refresh implementation/tests into `internal/server/repobrowserapi`
- Modify: `internal/server/server.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: clone manager, database, provider registry/auth resolver, background context, and refresh scheduling.
- Produces: `(*repobrowserapi.Handler).Register(huma.API)`, startup seeding, and shutdown behavior.

- [ ] Preserve provider/host-aware clone identity and pinned revision validation.
- [ ] Add a package-local weighted semaphore of two for Git-heavy tests.
- [ ] Run `go test ./internal/server/repobrowserapi -shuffle=on`.
- [ ] Commit as `refactor(server): carve out the repository browser API`.

### Task 6: Fleet API

**Files:**
- Create: `internal/server/fleetapi/handler.go`
- Move fleet proxy, SSH, hub, worktree discovery/stats/links, tmux monitoring, and adapter files/tests into `internal/server/fleetapi`
- Modify: `internal/server/server.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: local workspace/project snapshots, config, provider auth resolution, event source, and background lifecycle.
- Produces: `(*fleetapi.Handler).Register(huma.API)`, snapshot methods, and `Shutdown`.

- [ ] Preserve local/remote host routing, SSH relay headers, websocket behavior, and provider-aware refs.
- [ ] Run `go test ./internal/server/fleetapi ./internal/server/apitest ./internal/server/e2etest -run 'TestFleet|TestSSH' -shuffle=on`.
- [ ] Commit as `refactor(server): carve out the fleet API`.

### Task 7: Workspace and Projects API

**Files:**
- Create: `internal/server/workspaceapi/handler.go`
- Move workspace/project handlers, runtime terminal, diff cache, enrichment, pushed-head observation, branch actions, tmux activity, and related tests into `internal/server/workspaceapi`
- Move the workspace section of `internal/server/api_test.go` into `internal/server/workspacetest`
- Create or extend: `internal/server/servertest` for reusable public wire fixtures
- Modify: `internal/server/server.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: workspace manager, database, clone manager, provider registry, syncer callbacks, event broadcaster, runtime manager, and shutdown context.
- Produces: `(*workspaceapi.Handler).Register(huma.API)`, workspace response enrichment, background observer lifecycle, and shutdown.

- [ ] Keep workspace and projects in one package so the full suite launches one Git/worktree-heavy test binary.
- [ ] Add a package-level weighted semaphore, initially eight, around tests that create clones/worktrees or run substantial Git subprocesses.
- [ ] Keep the PTY semaphore at one.
- [ ] Preserve generated-client wire behavior and all workspace event ordering.
- [ ] Run `GOMAXPROCS=24 go test ./internal/server/workspaceapi ./internal/server/workspacetest -parallel=8 -shuffle=on`.
- [ ] Commit as `refactor(server): carve out the workspace API`.

### Task 8: Provider and Admin APIs

**Files:**
- Create: `internal/server/providerapi/handler.go`
- Create: `internal/server/adminapi/handler.go`
- Move pull/issue/activity/review/merge/sync/release handlers and tests into `providerapi`
- Move settings/config/repo-import/tooling/host-runtime/archive handlers and tests into `adminapi`
- Modify: `internal/server/server.go`, `internal/server/huma_routes.go`, `internal/server/api_test.go`

**Interfaces:**
- `providerapi.Deps` consumes database, platform registry, syncer, clone manager, event broadcaster, and capability checks.
- `adminapi.Deps` consumes config persistence/reload, archive controller, tooling runner, token sources, and host runtime.
- Both produce concrete `Register(huma.API)` methods.

- [ ] Preserve provider/host-aware route wrappers and capability errors.
- [ ] Leave only composition, middleware, health, auth, SPA, OpenAPI assembly, and startup/shutdown tests in `internal/server`.
- [ ] Run `GOMAXPROCS=24 go test ./internal/server/... -parallel=8 -shuffle=on`.
- [ ] Compare package timings with the recorded 500.8-second unrestricted and 337.8-second capped baselines.
- [ ] Commit `providerapi` and `adminapi` as separate commits, one domain per commit.

### Task 9: Final Verification

**Files:**
- Modify only files required by failures discovered during verification, committing fixes with their owning domain.

- [ ] Run `GOMAXPROCS=24 go test ./internal/server/... -parallel=8 -shuffle=on -json` and record package elapsed times.
- [ ] Run `make test-short`.
- [ ] Run `make lint`.
- [ ] Confirm `git status --short` is clean and each carve-out is a separate conventional commit.
