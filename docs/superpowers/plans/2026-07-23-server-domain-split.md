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
- Domain packages never import the root `internal/server` package. Shared wire types live below the root, while cross-domain behavior is injected through narrow callbacks or interfaces.
- The root starts and stops domain-owned background workers in construction order and reverse shutdown order. Domain cleanup must be idempotent and honor the root shutdown context.
- Mutable configuration is persisted by root-owned transactional callbacks. Domain handlers receive committed snapshots and must not retain the root config pointer or mutex.
- After every carve-out, run `make api-generate` and require no generated-artifact diff unless an API change is explicitly intended.
- Route metadata and stable-problem-code guards scan extracted packages recursively; each moved handler must replace generic Huma errors with `httpapi` problems where needed.
- Handler-focused tests move with the domain. A small root suite remains only for composition, middleware, CSRF/loopback policy, and full `ServeHTTP` wire behavior. Shared public fixtures are added to `servertest` at the first point two extracted packages need them.

## Success Criteria

- `GOMAXPROCS=24 go test ./internal/server/... -parallel=8 -shuffle=on` completes without Git, tmux, PTY, or process-resource failures.
- Server-subtree wall time is below 180 seconds on the profiling host, at least 46% lower than the 337.8-second capped baseline.
- No single ordinary domain package exceeds 60 seconds; the resource-bounded workspace package may use up to 120 seconds.
- The final timing report records each package elapsed time and the root package contains only composition/middleware tests.

## Execution Order

Tasks are numbered by domain inventory, but dependency order is: 1, 2, 4, 5, 7, 3, 6, then the provider/admin subdivisions in Task 8. Workspace moves before Kata and Fleet because those domains consume workspace DTOs and lifecycle services.

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

- [x] Export the existing problem-envelope implementation without changing its JSON shape or status mapping.
- [x] Replace root-package helper calls with `httpapi` calls directly; do not leave forwarding wrappers.
- [x] Move problem-envelope tests into `httpapi` and retain the root wire-level problem tests.
- [x] Run `go test ./internal/server/httpapi ./internal/server -run 'TestProblem|TestCodeForStatus|TestMapPlatformError|TestProviderCallProblem' -shuffle=on`.
- [x] Commit as `refactor(server): isolate the shared HTTP contract`.

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

- [x] Move docs route types, handlers, publish locking, problem mapping, and daemon-binding validation into `docsapi`.
- [x] Register the handler from the root server and update config reload through committed folder snapshots.
- [x] Keep loopback/CSRF path classification in the root middleware because it is cross-domain policy.
- [x] Keep mutable registry and publish-lock internals private; package-local tests cover rollback and lock behavior.
- [x] Run focused Docs/config tests and verify generated OpenAPI artifacts are unchanged.
- [x] Commit as `refactor(server): carve out the docs API` with the transactional ownership follow-up.

### Task 3: Kata API

**Dependency:** Execute after Task 7. Kata receives workspace DTOs and operations from `workspaceapi`; it must not call root `Server` methods or duplicate workspace wire types.

**Files:**
- Create: `internal/server/kataapi/handler.go`
- Move Kata route, proxy, task-detail, and workspace-adapter implementation files from `internal/server` into `internal/server/kataapi`
- Move corresponding tests into `internal/server/kataapi`
- Modify: `internal/server/server.go`, `internal/server/config_reload.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: daemon catalog selection, workspace manager access, config reads, HTTP transport, and root event callbacks through `kataapi.Deps`.
- Produces: `(*kataapi.Handler).Register(huma.API)`, `ApplyConfig`, and shutdown cleanup.

- [ ] Replace `Server` receiver methods with a concrete Kata handler after the Workspace boundary exists.
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

- [x] Preserve `/messages` application naming and `/msgvault` backend API naming.
- [x] Preserve safe HTML/image behavior and stable upstream error envelopes.
- [x] Replace shared config pointer/mutex ownership with a root-owned transactional save callback and package-local reload regression coverage.
- [ ] Move handler and remote-image policy tests into `messagesapi`; keep only middleware/composition wire checks in the root.
- [x] Run focused Messages tests and verify generated OpenAPI artifacts are unchanged.
- [x] Commit as `refactor(server): carve out the messages API` with the transactional ownership follow-up.

### Task 5: Repository Browser API

**Files:**
- Create: `internal/server/repobrowserapi/handler.go`
- Move repository browser and refresh implementation/tests into `internal/server/repobrowserapi`
- Modify: `internal/server/server.go`, `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: clone manager, database, provider registry/auth resolver, background context, and refresh scheduling.
- Produces: `(*repobrowserapi.Handler).Register(huma.API)`, startup seeding, and shutdown behavior.

- [x] Preserve provider/host-aware clone identity and pinned revision validation.
- [x] Move shared repository capability/ref wire types below the root so the extracted handler preserves OpenAPI schema identities without a dependency cycle.
- [x] Add a package-local weighted semaphore of two for Git-heavy tests.
- [x] Run the full package at 24 CPUs and verify generated OpenAPI artifacts are unchanged.
- [ ] Commit as `refactor(server): carve out the repository browser API`.

### Task 6: Fleet API

**Dependency:** Execute after Task 7. Fleet consumes workspace/project snapshots and runtime services through exported `workspaceapi` contracts rather than root receivers.

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
- [ ] Define the shared workspace DTO/service boundary consumed later by Kata and Fleet before moving either dependent domain.
- [ ] Add a package-level weighted semaphore, initially eight, around tests that create clones/worktrees or run substantial Git subprocesses.
- [ ] Keep the PTY semaphore at one.
- [ ] Preserve generated-client wire behavior and all workspace event ordering.
- [ ] Run `GOMAXPROCS=24 go test ./internal/server/workspaceapi ./internal/server/workspacetest -parallel=8 -shuffle=on`.
- [ ] Commit as `refactor(server): carve out the workspace API`.

### Task 8: Provider and Admin APIs

**Files:**
- Create: `internal/server/pullapi/handler.go`
- Create: `internal/server/issueapi/handler.go`
- Create: `internal/server/activityapi/handler.go`
- Create: `internal/server/repoapi/handler.go`
- Create: `internal/server/syncapi/handler.go`
- Create: `internal/server/adminapi/handler.go`
- Move pull/review/merge handlers and tests into `pullapi`
- Move issue handlers and tests into `issueapi`
- Move notifications/activity handlers and tests into `activityapi`
- Move repository metadata/labels/releases handlers and tests into `repoapi`
- Move explicit sync handlers and tests into `syncapi`
- Move settings/config/repo-import/tooling/host-runtime/archive handlers and tests into `adminapi`
- Modify: `internal/server/server.go`, `internal/server/huma_routes.go`, `internal/server/api_test.go`

**Interfaces:**
- Provider-domain `Deps` consume only the database, platform registry, syncer, clone manager, event broadcaster, and capability checks each route group needs.
- `adminapi.Deps` consumes config persistence/reload, archive controller, tooling runner, token sources, and host runtime.
- Every package produces a concrete `Register(huma.API)` method.

- [ ] Preserve provider/host-aware route wrappers and capability errors.
- [ ] Leave only composition, middleware, health, auth, SPA, OpenAPI assembly, and startup/shutdown tests in `internal/server`.
- [ ] Run `GOMAXPROCS=24 go test ./internal/server/... -parallel=8 -shuffle=on`.
- [ ] Compare package timings with the recorded 500.8-second unrestricted and 337.8-second capped baselines.
- [ ] Commit each provider route group and `adminapi` separately, one domain per commit.

### Task 9: Final Verification

**Files:**
- Modify only files required by failures discovered during verification, committing fixes with their owning domain.

- [ ] Run `GOMAXPROCS=24 go test ./internal/server/... -parallel=8 -shuffle=on -json` and record package elapsed times.
- [ ] Run `make test-short`.
- [ ] Run `make lint`.
- [ ] Confirm `git status --short` is clean and each carve-out is a separate conventional commit.
