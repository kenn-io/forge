# Kenn Forge MCP Agent Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose Kenn Forge's cached review workflow and a daemon-authoritative MCP handoff that creates PR-, issue-, or ad-hoc-backed workspaces, launches a configured coding agent, observes its live hook session ID, and submits one initial message.

**Approved spec/design:** `docs/superpowers/specs/2026-08-07-kenn-forge-mcp-agent-handoff-design.md`, extending the preserved historical review design in `docs/superpowers/specs/2026-07-01-middleman-mcp-server-design.md`.

**Architecture:** Port the already-tested MCP review primitives from `origin/middleman/pr-633` onto the current modular Kenn Forge server, using the daemon for every stateful operation and the companion only for bounded orchestration. Add one forward migration for at-most-once initial-message receipts, project live coding sessions from normalized agent-hook reports joined to live runtime sessions, and expose no raw terminal or provider mutation surface.

**Tech Stack:** Go 1.26, Cobra, Huma, SQLite, `github.com/modelcontextprotocol/go-sdk/mcp`, existing daemon discovery/auth, workspace localruntime, and agenthook normalization.

## Global Constraints

- Use current canonical names only: `kenn-forge mcp`, `kenn_forge_*` tools, `kenn-forge://mcp/guidance`, and `kenn-forge-review-candidates`; do not add Middleman aliases.
- All item and repo identities include provider, platform host, owner/path, name, and number where applicable.
- MCP reads use cached daemon state and never force provider refreshes.
- MCP workspace creation sets `suppress_auto_assign = true`; no MCP mutation may call a provider mutator.
- The only MCP mutations are local workflow state and local workspace/agent spawn with one initial message.
- Live coding sessions require a fresh hook report, canonical worktree match, live runtime-session key, recorded runtime kind `agent`, and supported normalized agent.
- Initial messages are at most 64 KiB after CRLF normalization, reject unsafe controls, persist no message text or digest, and receive at most one delivery attempt per runtime.
- A multiline initial message requires tracked bracketed-paste mode; inactive mode fails before input.
- Spawn uses one total timeout, default five minutes and maximum fifteen minutes, and never automatically retries mutation stages or cleans up partial resources.
- Follow-up message submission, raw terminal control, stop/delete, Kata workspace creation, and fleet operations remain out of scope.
- Add exactly one migration pair in this PR; migration 000047 is the next current version and shipped migrations remain immutable.
- Use TDD for every behavior change and run `make api-generate` after final route/schema changes.
- Before every commit step in Tasks 1–8, invoke the repository `context-sync`
  skill in `--commit` mode, then invoke the mandatory commit skill. The shown
  `git add`/message blocks identify scope; they do not replace either workflow.

---

### Task 1: Restore provider-neutral workflow-state daemon routes

**Files:**
- Create: `internal/server/workflow_state_routes.go`
- Create: `internal/server/workflow_state_routes_test.go`
- Create: `internal/server/apitest/workflow_state_test.go`
- Modify: `internal/server/huma_routes.go`

**Interfaces:**
- Consumes: `db.ListItemWorkflowStates(ctx, db.ListWorkflowStatesOpts)` and `db.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams)` already present on main.
- Produces: collection and exact-item `GET /api/v1/workflow-state...` routes,
  guarded `PUT /api/v1/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}`,
  and host-prefixed variants.

- [ ] **Step 1: Port failing route-contract tests from the feature branch**

Use `origin/middleman/pr-633:internal/server/workflow_state_routes_test.go` and `internal/server/apitest/workflow_state_test.go` as behavioral references. Rename imports to `go.kenn.io/forge`, table names to `forge_*`, and retain this strict request union:

```go
type setWorkflowStateBody struct {
	Status         string `json:"status"`
	ExpectedStatus string `json:"expected_status,omitempty"`
	Force          *bool  `json:"force,omitempty"`
	Source         string `json:"source,omitempty"`
	Actor          string `json:"actor,omitempty"`
	Reason         string `json:"reason,omitempty"`
}
```

- [ ] **Step 2: Run the new tests and confirm missing-route failure**

Run: `go test ./internal/server ./internal/server/apitest -run 'WorkflowState' -shuffle=on`

Expected: FAIL with missing route/404 or undefined workflow route symbols.

- [ ] **Step 3: Port the route implementation and register it**

Adapt `origin/middleman/pr-633:internal/server/workflow_state_routes.go` to the current `Server`, `httpapi`, provider lookup, Huma registration, and response helpers. Preserve effective `new`, strict unknown-field rejection, expected-status conflict, force exclusivity, UTC RFC3339 output, opaque cursor pagination, and provider-host routing.

- [ ] **Step 4: Run focused route and DB tests**

Run: `go test ./internal/db ./internal/server ./internal/server/apitest -run 'Workflow|Kanban' -shuffle=on`

Expected: PASS.

- [ ] **Step 5: Commit the workflow API seam**

```bash
git add internal/server/workflow_state_routes.go internal/server/workflow_state_routes_test.go internal/server/apitest/workflow_state_test.go internal/server/huma_routes.go
git commit -m "feat: expose provider-neutral workflow state"
```

### Task 2: Port the MCP companion and cached review tools

**Files:**
- Create: `internal/mcpserver/{server,daemon,types,difftmp,workflow_lookup}.go`
- Create: `internal/mcpserver/tools_{read,candidates,items,diff,stack,workflow}.go`
- Create: matching `internal/mcpserver/*_test.go`
- Create: `internal/mcpserver/guidance.go`, `internal/mcpserver/guidance.md`, `internal/mcpserver/http.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: daemon runtime metadata/auth, current activity/repo/pull/issue/workspace/stack/diff routes, and Task 1 workflow routes.
- Produces: `mcpserver.New(Options)`, `RunStdio`, `RunHTTP`, `Close`, nine renamed review/workflow tools, one guidance resource, and one prompt.

- [ ] **Step 1: Add failing registration and daemon-client tests**

Port feature-branch MCP tests with current names. The exact initial surface is:

```go
[]string{
	"kenn_forge_find_review_candidates",
	"kenn_forge_get_item_context",
	"kenn_forge_get_item_diff",
	"kenn_forge_get_stack_context",
	"kenn_forge_list_activity",
	"kenn_forge_list_items_by_workflow_state",
	"kenn_forge_list_repos",
	"kenn_forge_search_items",
	"kenn_forge_set_item_workflow_state",
}
```

Assert resource `kenn-forge://mcp/guidance`, prompt `kenn-forge-review-candidates`, lazy daemon discovery, auth redaction, provider-host routes, capped payloads, strict workflow inputs, temp-file permissions/lifecycle, and HTTP loopback/token/origin policy.

- [ ] **Step 2: Verify package tests fail before implementation exists**

Run: `go test ./internal/mcpserver -shuffle=on`

Expected: FAIL because the package implementation is absent.

- [ ] **Step 3: Add the official SDK and port the feature-branch implementation**

Add `github.com/modelcontextprotocol/go-sdk v1.6.1`, then port `internal/mcpserver` from `origin/middleman/pr-633`. Replace module imports, canonical names, runtime discovery labels, temp directory prefixes, error URNs, and documentation prose. Keep files split by responsibility.

```go
func New(opts Options) (*Server, error) {
	if opts.ConfigPath == "" { opts.ConfigPath = config.DefaultConfigPath() }
	if opts.DaemonTimeout <= 0 { opts.DaemonTimeout = 10 * time.Second }
	s := &Server{opts: opts, daemon: newDaemonClient(opts.ConfigPath, opts.DaemonTimeout)}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "kenn-forge", Version: opts.Version}, nil)
	s.registerTools()
	return s, nil
}
```

- [ ] **Step 4: Run all MCP package tests**

Run: `go test ./internal/mcpserver -shuffle=on`

Expected: PASS.

- [ ] **Step 5: Commit the read/workflow MCP core**

```bash
git add go.mod go.sum internal/mcpserver
git commit -m "feat: expose cached maintainer workflows through MCP"
```

### Task 3: Register the Cobra MCP command and user guidance

**Files:**
- Create: `cmd/kenn-forge/mcp.go`, `cmd/kenn-forge/mcp_cli_test.go`, `docs/kenn-forge-mcp.md`
- Modify: `cmd/kenn-forge/cli.go`, `internal/mcpserver/guidance.md`

**Interfaces:**
- Consumes: `mcpserver.New`, `RunStdio`, `RunHTTP`, and `Close` from Task 2.
- Produces: public `kenn-forge mcp` Cobra command with stdio default and tokenized loopback HTTP.

- [ ] **Step 1: Write failing Cobra tests**

Assert `newRootCommand` contains one public `mcp` command; flags are `--config`, `--transport`, `--addr`, `--http-token-env`, and `--daemon-timeout`; unsupported transports fail validation; stdio is default; all flags affect execution.

- [ ] **Step 2: Verify CLI tests fail**

Run: `go test ./cmd/kenn-forge -run 'MCP|RootCommand' -shuffle=on`

Expected: FAIL because `mcp` is not registered.

- [ ] **Step 3: Implement the Cobra command**

```go
func newMCPCommand(run mcpRunner) *cobra.Command {
	var opts mcpserver.Options
	cmd := &cobra.Command{Use: "mcp", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd.Context(), opts)
	}}
	cmd.Flags().StringVar(&opts.Transport, "transport", "stdio", "MCP transport: stdio or http")
	return cmd
}
```

Use injected stdin/stdout for stdio tests and current Cobra conventions. Do not add a second parser or `flag.FlagSet`.

- [ ] **Step 4: Write concise workflow documentation and run tests**

Document cached reads, stdio/HTTP setup, workflow claiming, repo discovery, diff handoff, no provider writes, and the agent-spawn tools added later in this plan.

Run: `go test ./cmd/kenn-forge ./internal/mcpserver -shuffle=on`

Expected: PASS.

- [ ] **Step 5: Commit the command and guidance**

```bash
git add cmd/kenn-forge/cli.go cmd/kenn-forge/mcp.go cmd/kenn-forge/mcp_cli_test.go docs/kenn-forge-mcp.md internal/mcpserver/guidance.md
git commit -m "feat: add the Kenn Forge MCP companion command"
```

### Task 4: Persist at-most-once initial-message receipts

**Files:**
- Create: `internal/db/migrations/000047_agent_initial_message_receipts.{up,down}.sql`
- Create: `internal/db/queries_agent_message.go`, `internal/db/queries_agent_message_test.go`
- Modify: `internal/db/types.go`, `internal/db/db_test.go`

**Interfaces:**
- Produces: `AgentInitialMessageReceipt`, `ReserveAgentInitialMessage`, `MarkAgentInitialMessageDelivered`, `MarkAgentInitialMessageUncertain`, and `GetAgentInitialMessageReceipt`.
- Consumes: the workspace foreign key, transactional runtime-row validation,
  and the UTC DB clock. The runtime key is deliberately not a foreign key:
  runtime exit deletes that row while the durable receipt remains readable.

- [ ] **Step 1: Load migration and test-scope skills, then write failing tests**

Confirm 000047 is the only new migration pair. Define:

```go
type AgentInitialMessageReceipt struct {
	WorkspaceID       string
	RuntimeSessionKey string
	Agent             string
	CodingSessionID   string
	MessageBytes      int
	State             string
	ReservedAt        time.Time
	DeliveredAt       *time.Time
}
```

Tests prove one row per workspace/runtime, strict states, no message/digest
column, workspace foreign-key cleanup, runtime-row deletion retaining the
receipt, reserve conflict returning the existing receipt, delivered transition,
and startup recovery of pending rows to uncertain.

- [ ] **Step 2: Verify DB tests fail**

Run: `go test ./internal/db -run 'AgentInitialMessage|Migration' -shuffle=on`

Expected: FAIL with missing migration/table/query symbols.

- [ ] **Step 3: Implement migration and transactional DB API**

Create `forge_agent_initial_message_receipts` with a composite primary key,
workspace foreign key, strict state and byte-count checks, UTC text timestamps,
and no prompt material. Reserve in a transaction that first returns an existing
receipt, then verifies the runtime row before inserting; reject every second
reservation. Do not foreign-key the runtime row because its cleanup must not
delete lost-response recovery evidence.

- [ ] **Step 4: Run DB and migration checks**

Run: `go test ./internal/db -run 'AgentInitialMessage|Migration|Fresh' -shuffle=on`

Run: `KENN_FORGE_MIGRATION_BASE_REF=HEAD go run ./tools/migrationhistorycheck`

Expected: PASS and exactly one new migration pair.

- [ ] **Step 5: Commit receipt persistence**

```bash
git add internal/db/migrations/000047_agent_initial_message_receipts.* internal/db/queries_agent_message.go internal/db/queries_agent_message_test.go internal/db/types.go internal/db/db_test.go
git commit -m "feat: prevent duplicate initial agent prompts"
```

### Task 5: Project live coding sessions from agent hooks

**Files:**
- Modify: `internal/agentactivity/store.go`, `internal/agentactivity/store_test.go`, `internal/server/workspaceapi/agent_hook.go`, `internal/server/workspaceapi/handler.go`
- Create: `internal/server/workspaceapi/agent_sessions.go`, `internal/server/workspaceapi/agent_sessions_test.go`

**Interfaces:**
- Produces: `agentactivity.Report.Agent`, `Store.LiveReportsForWorkspace(cwd string, liveSessionKeys []string) []Report`, and `GET /workspaces/{id}/agent-sessions`.
- Consumes: normalized agent from hook route, canonical CWD matching, localruntime live sessions, and Task 4 receipt reads.

- [ ] **Step 1: Write failing report identity and live-projection tests**

Cover `(agent, session_id)` identity, same opaque ID across two agents,
agentless report removal, nested-agent exclusion, freshness expiry, wrong CWD,
dead runtime key, non-agent runtime kind, `Stop`/`Interrupt` retention as done
while the runtime lives, `SessionEnd` removal, deterministic ordering, and
receipt metadata.

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/agentactivity ./internal/server/workspaceapi -run 'AgentSession|AgentHook|LiveReport' -shuffle=on`

Expected: FAIL because reports do not retain agent identity or expose a list route.

- [ ] **Step 3: Implement normalized report storage and live join**

```go
if err := s.agentActivity.HandleEvent(string(integration), input.Body, input.RuntimeSessionKey); err != nil {
	slog.Warn("record agent hook activity", "err", err)
}
```

Key report filenames with a hash of agent plus session ID, remove agentless files during scans, and retain 30-minute freshness/fail-open behavior. Return only live `SessionInfo.Kind == localruntime.LaunchTargetAgent` matches.

- [ ] **Step 4: Run focused tests and commit**

Run: `go test ./internal/agentactivity ./internal/server/workspaceapi -run 'AgentSession|AgentHook|LiveReport' -shuffle=on`

Expected: PASS.

```bash
git add internal/agentactivity internal/server/workspaceapi/agent_hook.go internal/server/workspaceapi/agent_sessions.go internal/server/workspaceapi/agent_sessions_test.go internal/server/workspaceapi/handler.go
git commit -m "feat: expose live coding sessions for workspaces"
```

### Task 6: Add verified one-time runtime input and provider-write suppression

**Files:**
- Create: `internal/server/workspaceapi/initial_message.go`, `internal/server/workspaceapi/initial_message_test.go`
- Modify: `internal/server/workspaceapi/routes_handlers.go`, `internal/server/workspaceapi/handler.go`, `internal/workspace/localruntime/manager.go`, `internal/workspace/localruntime/manager_test.go`

**Interfaces:**
- Produces: `Manager.SubmitInitialMessage(workspaceID, sessionKey, message string) error`, initial-message POST route, and `suppress_auto_assign` request fields.
- Consumes: Task 4 receipt state and Task 5 live session join.

- [ ] **Step 1: Write failing validation, input, receipt, and auto-assign tests**

Cover required live identity match, normalized 64 KiB limit, UTF-8/NUL/control rejection, CRLF normalization, single-line text plus one CR, multiline `\x1b[200~` + text + `\x1b[201~` plus one CR, inactive-mode rejection before any write, reserve-before-write, delivered/uncertain transitions, duplicate recovery, durable receipt GET after runtime exit, and suppression for PR/issue creation while omitted preserves existing behavior.

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/workspace/localruntime ./internal/server/workspaceapi -run 'InitialMessage|SuppressAutoAssign' -shuffle=on`

Expected: FAIL with absent API/input methods and request fields.

- [ ] **Step 3: Implement the narrow input adapter and receipt orchestration**

Attach through `AttachSession`; write a single-line message and one `\r`, or
write `\x1b[200~`, normalized multiline text, `\x1b[201~`, and one `\r`.
Validate tracked bracketed-paste mode before any multiline write. Reserve
pending before attachment, mark delivered after successful write, and mark
uncertain on possibly partial failures. Return an existing receipt before
liveness validation and expose a receipt-only GET for lost-response recovery.
Guard only the optional auto-assignment branch with `SuppressAutoAssign`.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/workspace/localruntime ./internal/server/workspaceapi -run 'InitialMessage|SuppressAutoAssign|RuntimeSession' -shuffle=on`

Expected: PASS.

```bash
git add internal/server/workspaceapi/initial_message.go internal/server/workspaceapi/initial_message_test.go internal/server/workspaceapi/routes_handlers.go internal/server/workspaceapi/handler.go internal/workspace/localruntime/manager.go internal/workspace/localruntime/manager_test.go
git commit -m "feat: submit one verified initial agent prompt"
```

### Task 7: Add agent-target, live-session, and spawn MCP tools

**Files:**
- Create: `internal/mcpserver/tools_agent.go`, `internal/mcpserver/tools_agent_test.go`
- Modify: `internal/mcpserver/server.go`, `internal/mcpserver/daemon.go`, `internal/mcpserver/guidance.md`, `docs/kenn-forge-mcp.md`

**Interfaces:**
- Produces: `kenn_forge_list_agent_targets`, `kenn_forge_list_workspace_agent_sessions`, and `kenn_forge_spawn_workspace_with_agent`.
- Consumes: Tasks 5–6 APIs plus workspace create/detail/runtime routes.

- [ ] **Step 1: Write failing exact-surface and orchestration tests**

Update the exact registry to 12 tools. Prove target filtering requires both
`kind=agent` and a supported `agenthook.Profiles()` identity without exposing
argv; cover PR/issue/ad-hoc request shapes, `suppress_auto_assign: true`, branch
forwarding, validation before creation, readiness polling, always-new launch,
hook correlation, receipt-only recovery after a lost message response, five
stages, timeout cap, no mutation retry, partial errors, and no cleanup.

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/mcpserver -run 'AgentTarget|WorkspaceAgent|RegisteredTools' -shuffle=on`

Expected: FAIL because the three tools are absent.

- [ ] **Step 3: Implement schemas and bounded orchestration**

```go
type spawnWorkspaceWithAgentInput struct {
	Source         workspaceSourceInput `json:"source"`
	AgentTarget    string               `json:"agent_target"`
	InitialMessage string               `json:"initial_message"`
	Timeout        string               `json:"timeout,omitempty"`
}
```

Return stages, partial identifiers, receipt state, and `message_delivered`; retry only idempotent reads after daemon rediscovery.

- [ ] **Step 4: Update guidance, run tests, and commit**

Run: `go test ./internal/mcpserver -shuffle=on`

Expected: PASS with exactly 12 tools.

```bash
git add internal/mcpserver docs/kenn-forge-mcp.md
git commit -m "feat: hand MCP work to live coding agents"
```

### Task 8: Regenerate contracts, add full-stack proof, and finalize context

**Files:**
- Modify: `frontend/openapi/openapi.yaml`, `internal/apiclient/generated/client.gen.go`, `packages/ui/src/api/generated/schema.ts`
- Create: `cmd/kenn-forge/mcp_stdio_e2e_test.go`
- Modify: `internal/mcpserver/e2e_test.go`, `docs/superpowers/specs/2026-08-07-kenn-forge-mcp-agent-handoff-design.md`
- Modify if required by context-sync: `context/mcp-server.md`, `context/workspace-apis.md`, `context/workspace-runtime-lifecycle.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: every previous task.
- Produces: checked-in API artifacts, real-daemon stdio coverage, current-brand spec, and durable routed context.

- [ ] **Step 1: Validate the superseding design and preserve the original record**

Keep the July design byte-for-byte historical. Confirm the August superseding
design carries current command/tool/resource/prompt names and every approved
handoff requirement.

- [ ] **Step 2: Regenerate API artifacts**

Run: `make api-generate`

Expected: generated contracts contain workflow, live session, initial-message, and suppression routes with no unrelated drift.

- [ ] **Step 3: Add real-daemon stdio end-to-end coverage**

Start a hermetic daemon against temp SQLite and synthetic provider data, connect through MCP stdio, exercise cached review tools/workflow claiming, then run a fake agent that emits a normalized hook and records stdin. Prove item/ad-hoc spawn, one message, receipt, live session listing, no credentials, and temp diff cleanup.

- [ ] **Step 4: Run focused and broad verification**

```bash
go test ./internal/db ./internal/agentactivity ./internal/server/workspaceapi ./internal/server/apitest ./internal/mcpserver ./cmd/kenn-forge -shuffle=on
make api-generate
make lint
RUSTUP_TOOLCHAIN=1.95.0-aarch64-apple-darwin make test-short
```

Expected: all commands PASS; the second generation run produces no diff.

- [ ] **Step 5: Run context sync and commit final artifacts**

Use `context-sync --commit`, apply only clear MCP/workspace invariants, then the mandatory commit skill:

```bash
git add frontend/openapi/openapi.yaml internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts cmd/kenn-forge/mcp_stdio_e2e_test.go internal/mcpserver/e2e_test.go docs/kenn-forge-mcp.md docs/superpowers/specs/2026-08-07-kenn-forge-mcp-agent-handoff-design.md
git commit -m "test: prove MCP coding-agent handoff end to end"
```

If context-sync changed routed context files, add exactly the paths it reported
to the same commit; do not stage the whole `context/` directory.
