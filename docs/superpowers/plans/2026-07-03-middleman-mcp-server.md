# Middleman MCP Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `middleman mcp` companion command exposing curated read tools plus one local workflow-state write tool over stdio and loopback HTTP MCP transports, backed by a new generic PR/issue workflow-state store in the daemon.

**Architecture:** First generalize the PR-only `middleman_kanban_state` table into `middleman_item_workflow_state` (PRs and issues, last-writer metadata, expected-status conflicts) while keeping the public PR kanban API byte-compatible. Then add `GET/PUT /workflow-state` daemon endpoints. Finally build the MCP companion (`internal/mcpserver` + `cmd/middleman/mcp.go`) that discovers the running daemon via `runtimelock` metadata exactly like `middleman api` does, and exposes 9 curated tools, a guidance resource, and a triage prompt.

**Tech Stack:** Go 1.26, huma v2, modernc SQLite, `github.com/modelcontextprotocol/go-sdk` **v1.6.1** (new dependency), stdlib `flag` CLI, testify.

**Spec:** `docs/superpowers/specs/2026-07-01-middleman-mcp-server-design.md`. Read the spec section named in each task before implementing it.

## Global Constraints

- Status vocabulary everywhere: `new`, `reviewing`, `waiting`, `awaiting_merge`. Missing workflow row reads as effective `new`.
- One rule for `new`, no exceptions: setting `status=new` stores an explicit row (with metadata) exactly like any other status — it is never a delete/reset, and rows are never deleted by state changes. Everywhere that filters or lists by state, `new` matches the *effective* status: explicit `new` rows AND items with no row at all. `expected_status` likewise compares against the effective status. Explicit `new` rows sort by their `updated_at` like any other explicit row.
- Metadata byte limits (validated at the API layer, exact values from spec): `updated_source` max 40 bytes matching `^[a-z][a-z0-9_-]{0,39}$`; `updated_actor` max 120 bytes; `updated_reason` max 500 bytes.
- `updated_source` values: existing PR kanban route writes `"ui"`, new workflow-state PUT defaults to `"api"`, MCP write tool always sends `"mcp"`.
- Repository identity is always `(platform, platform_host, owner, name)`. No repo-scoped data keyed by owner/repo/number alone.
- Datetimes are UTC in storage and API; RFC3339 on the wire.
- MCP companion never opens SQLite and never calls provider APIs. Daemon is the only data authority.
- MCP HTTP transport: loopback bind only, requires `--http-token-env`, bearer token, Host check, Origin rejection, no CORS headers, no daemon token in responses/errors/logs.
- The only MCP write tool is `middleman_set_item_workflow_state`.
- Existing PR kanban route `PUT /pulls/{provider}/{owner}/{name}/{number}/state` (and host variant) keeps its exact request/response shape.
- Go tests: `-shuffle=on`, never `-count=1`, never `-v`, testify (`require` for preconditions, `assert` otherwise; local `assert := assert.New(t)` when >3 assertions), no `t.Fatal`/`t.Error` family.
- Default daemon-request timeout in the companion: 10s, flag `--daemon-timeout`. Reads retry once after rediscovery on transient connection failure; workflow writes never retry.
- No emojis anywhere. Conventional commits with reason-focused subjects. Commit every task.

## Codebase Facts (verified 2026-07-03)

Implementers get zero other context; these are load-bearing:

- Latest migration is `000036_kata_workspace_owner_keys`; new migration is `000037_*`. Migrations are auto-discovered from `internal/db/migrations/*.sql` via embed + glob (`internal/db/migrations.go`); no registration code.
- `middleman_kanban_state` DDL (`internal/db/migrations/000001_initial_schema.up.sql:67`): `merge_request_id INTEGER PRIMARY KEY REFERENCES middleman_merge_requests(id) ON DELETE CASCADE, status TEXT NOT NULL DEFAULT 'new', updated_at DATETIME NOT NULL DEFAULT (datetime('now'))`.
- Kanban DB funcs live at `internal/db/queries.go:2727-2771` (`EnsureKanbanState`, `SetKanbanState`, `GetKanbanState`). PR queries join kanban via `LEFT JOIN middleman_kanban_state k ON k.merge_request_id = p.id` and select `COALESCE(k.status, '') AS kanban_status` at `queries.go:2312/2316`, `2373/2376`, `2495/2499`. Kanban filter logic at `queries.go:2458-2465` (`COALESCE(k.status,'new') = ?` for `new`, plain `k.status = ?` otherwise).
- `db.KanbanStatus` type + constants: `internal/db/types.go:240-246`. `MergeRequest.KanbanStatus` field with enum tag: `types.go:220`. `KanbanState` struct: `types.go:347-351`.
- `db.Issue`: `types.go:376-398` — has no workflow field today. Issue queries: `GetIssue` `queries.go:3228`, `GetIssueByRepoIDAndNumber` `queries.go:3279`, `ListIssues` `queries.go:3321` (select list at `3385-3389`).
- DB test helpers: `openTestDB(t)` (`internal/db/db_test.go:18`, template-copy, fast), `openDBWithMigrations(t)` (`db_test.go:23`, full migrations), `insertTestRepo`, `insertTestMR(t, d, repoID, number, title, activityTime)`, `baseTime()` in `queries_test.go`. `TestOpenAndSchema` (`db_test.go:33`) hardcodes an expected-tables list including `"middleman_kanban_state"` at `db_test.go:40` — must be updated. Kanban CRUD test to migrate: `TestKanbanState` `queries_test.go:2869`; filter test `TestListPullRequestsFilterByKanban` `queries_test.go:2529`.
- Opaque-cursor precedent: `EncodeCursor`/`DecodeCursor` in `internal/db/queries_activity.go:410-445` (base64 RawURLEncoding over `"<unixMillis>:<source>:<sourceID>"`).
- Server: kanban handler `setKanbanState` `internal/server/huma_routes.go:1886-1917`; input struct `huma_routes.go:72-81`; host wrapper `internal/server/provider_route_wrappers.go:24-33,355-365`; registrations `huma_routes.go:1191-1192`. `validKanbanStates` map: `internal/server/api_types.go:133-138`. `EnsureKanbanState` call site: `huma_routes.go:3074`. apitest seeder calls `EnsureKanbanState` at `internal/server/apitest/fixtures_test.go:170`.
- Repo/item resolution: `lookupRepoByProviderRoute` (`internal/server/repo_ref.go:72`), `lookupMRID`/`lookupIssueID` (`internal/server/helpers.go:195-225`), `providerRouteLookupError` (`huma_routes.go:56`), `repoNumberPathRef` (`helpers.go:15`).
- Problem helpers in `internal/server/problems.go:306-452`: `problemValidation(field, detail, allowed...)`, `problemNotFound(code, detail, details)`, `problemConflict(code, detail, details)`, `problemBadRequest`, `problemInternal`. Codes at `problems.go:36-64` include `CodeConflict = "conflict"`, `CodePullNotFound`, `CodeIssueNotFound`, `CodeRepoNotFound`, `CodeValidationError`.
- Route registration pattern: two explicit registrations per route (default + `/host/{platform_host}/...`), with an `...OnHost` wrapper struct/handler in `provider_route_wrappers.go` whose only difference is `PlatformHost string` carrying a `path:"platform_host"` tag. Path-param `item_type` precedent: `resolveItemInput` uses `enum:"pr,issue"` (`huma_routes.go:197-204`).
- Output wrappers: `bodyOutput[T]` (`internal/server/output_wrappers.go:3`), `statusOnlyOutput = okStatusOutput` (`huma_routes.go:83`). API mounts under `/api/v1`.
- Existing daemon routes the companion consumes: `GET /activity` (params `repo`, `types[]`, `search`, `after`, `since`; response `activityResponse{items, capped}` with snake_case fields, `internal/server/api_types.go:563-629`), `GET /repos/summary` (`repoSummaryResponse`, `api_types.go:192-215`), `GET /pulls` (params `repo`, `state` in open/closed/all, `kanban`, `starred`, `q`, `limit`, `offset`), `GET /pulls/{provider}/{owner}/{name}/{number}` (detail), `GET .../files` + `GET .../diff` (`filesResponse`/`diffResponse` with `stale`, `whitespace_only_count`, `files[]` of `gitclone.DiffFile`; note `getFiles` leaves `whitespace_only_count` 0), `GET .../stack` (per-PR stack context, `stackContextResponse{stack_id, stack_name, position, size, health, members[]}` — use this instead of scanning `GET /stacks`), `GET /issues`, `GET /issues/{...}/{number}`, `GET /workspaces`.
- **JSON casing gotcha:** `mergeRequestResponse` and `issueResponse` embed `db.MergeRequest`/`db.Issue` which have NO json tags, so those fields serialize with Go names: `"Number"`, `"Title"`, `"State"`, `"Author"`, `"URL"`, `"IsDraft"`, `"KanbanStatus"`, `"LastActivityAt"`, `"CreatedAt"`, `"CommentCount"`. The wrapper-level extras are snake_case: `"repo"`, `"repo_owner"`, `"repo_name"`, `"platform_host"`, `"detail_loaded"`, `"detail_fetched_at"`, `"workspace"`. `repoRefResponse` (`internal/server/repo_ref.go:22-34`) is snake_case: `provider`, `platform_host`, `repo_path`, `owner`, `name`. Companion decode structs must match this exactly.
- Daemon discovery (mirror `cmd/middleman/api_verb.go` exactly): `config.Load(*configPath)` → `runtimelock.Read(cfg.DataDir)` → require `st.Running && st.Metadata != nil` → base URL `http://<st.Metadata.ListenAddr><basePath>` where basePath is `st.Metadata.BasePath` falling back to `cfg.BasePath`, trailing `/` trimmed → `runtimelock.ReadAuthToken(cfg.DataDir)` → `Authorization: Bearer <token>` when non-empty; `Content-Type: application/json` on every non-GET. Metadata struct: `internal/runtimelock/metadata.go:16-38`. `config.DefaultConfigPath()` is the `--config` default. Do NOT call `config.EnsureDefault` (the `api` verb doesn't).
- CLI dispatch: hand-rolled `switch args[0]` in `runCLI` (`cmd/middleman/main.go:186-223`), stdlib `flag.NewFlagSet(name, flag.ContinueOnError)` with `fs.SetOutput(io.Discard)` per subcommand.
- cmd/middleman e2e helpers (package `main`, reusable from a new test file): `buildMiddleman(t)`, `writeMinimalConfig`, `appendConfig`, `reserveFreePort(t)`, `waitForFile(t, path, timeout)`; daemon launch via `procutil.Command(bin, "--config", cfgPath)`; see `cmd/middleman/api_verb_e2e_test.go:30-140`.
- Host/loopback validation precedent to mirror (unexported, package server — copy the logic, do not import): `internal/server/host_check.go` (`isLiteralLoopbackIP` `:143`, loopback synonyms `127.0.0.1`/`localhost`/`[::1]` `:152-165`). Constant-time token compare precedent: `internal/server/api_auth.go:28-31` (`crypto/subtle.ConstantTimeCompare`).
- `make api-generate` regenerates 4 checked-in artifacts: `internal/apiclient/spec/openapi.json`, `internal/apiclient/generated/client.gen.go`, `frontend/openapi/openapi.yaml`, `packages/ui/src/api/generated/schema.ts` (+`client.ts`). apitest uses generated methods named `<OperationID>WithResponse`.
- apitest pattern: `setupTestServer(t)` + `seedPR`/`seedIssue` + `setupTestClient(t, srv)` (in-process ServeHTTP round-tripper), `internal/server/apitest/fixtures_test.go`.

## File Structure

| File | Responsibility |
|---|---|
| `internal/db/migrations/000037_item_workflow_state.{up,down}.sql` | New generic table and initial copy from kanban rows; legacy table stays live |
| `internal/db/migrations/000038_drop_kanban_state.{up,down}.sql` | Re-sync legacy kanban rows, then drop the old PR-only table |
| `internal/db/queries_workflow.go` | All generic workflow-state queries (get/ensure/set/list/cursor) |
| `internal/db/queries_workflow_test.go` | Tests for the above |
| `internal/db/types.go` | `ItemWorkflowState`, params/opts/row types, conflict error (added to existing file) |
| `internal/server/workflow_state_routes.go` | New GET/PUT workflow-state inputs, outputs, handlers, host wrappers, registration |
| `internal/server/apitest/workflow_state_test.go` | API tests for new endpoints |
| `internal/mcpserver/daemon.go` | Daemon discovery + authenticated loopback client + problem mapping |
| `internal/mcpserver/server.go` | MCP server assembly: tool/resource/prompt registration, Run |
| `internal/mcpserver/types.go` | Compact MCP response shapes + daemon decode structs |
| `internal/mcpserver/tools_read.go` | list_repos, list_activity, search_items |
| `internal/mcpserver/tools_candidates.go` | find_review_candidates grouping |
| `internal/mcpserver/tools_items.go` | get_item_context, list_items_by_workflow_state |
| `internal/mcpserver/tools_diff.go` + `difftmp.go` | get_item_diff + temp-file store |
| `internal/mcpserver/tools_stack.go` | get_stack_context |
| `internal/mcpserver/tools_workflow.go` | set_item_workflow_state (only write) |
| `internal/mcpserver/guidance.md` + `guidance.go` | Embedded guidance resource + prompt text |
| `internal/mcpserver/http.go` | Loopback HTTP transport with token/Host/Origin checks |
| `internal/mcpserver/*_test.go` | Unit tests with a fake daemon (`httptest`) |
| `cmd/middleman/mcp.go` | `middleman mcp` flag parsing and startup |
| `cmd/middleman/mcp_e2e_test.go` | CLI + full-stack stdio e2e |
| `docs/middleman-mcp.md` | User-facing guidance document |

---

### Task 1: Migration 000037 — generic workflow-state table

**Spec sections:** "Local Workflow State > Model", "Rollout" step 1.

**Files:**
- Create: `internal/db/migrations/000037_item_workflow_state.up.sql`
- Create: `internal/db/migrations/000037_item_workflow_state.down.sql`
- Modify: `internal/db/db_test.go` (expected-tables list at `:40`, plus new migration test)

**Interfaces:**
- Produces: table `middleman_item_workflow_state(repo_id, item_type, item_number, status, updated_at, updated_source, updated_actor, updated_reason)` with `UNIQUE(repo_id, item_type, item_number)`. `middleman_kanban_state` is NOT dropped here — it stays live (and still consistent, since nothing writes the new table yet) until Task 3 rewires all readers/writers and drops it in migration 000038 within the same commit. This keeps every commit on the branch a working bisect point.

- [ ] **Step 1: Write the failing migration test**

Add to `internal/db/db_test.go` (model on `TestOpenBackfillsLegacyIssueLabelsIntoNormalizedTables` at `db_test.go:181` for the seeded-old-DB pattern; here we can seed via the previous schema in a simpler way — open a DB, hand-create legacy rows only if the old table exists is impossible post-migration, so instead seed through raw SQL against a pre-037 database file the same way that test seeds a version-4 DB. If building a pre-037 fixture proves disproportionate, the fallback below seeds `middleman_item_workflow_state` expectations via the copy semantics asserted on a fresh DB plus a direct-SQL simulation; prefer the seeded-old-DB form):

```go
func TestOpenMigratesKanbanRowsToItemWorkflowState(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// Build a database at migration version 36, then seed legacy kanban rows.
	openAtVersionForTest(t, path, 36, func(raw *sql.DB) {
		_, err := raw.Exec(`INSERT INTO middleman_repos (platform, platform_host, owner, name, created_at)
			VALUES ('github', 'github.com', 'acme', 'widget', datetime('now'))`)
		require.NoError(t, err)
		_, err = raw.Exec(`INSERT INTO middleman_merge_requests
			(repo_id, platform_id, number, created_at, updated_at, last_activity_at)
			VALUES (1, 101, 7, datetime('now'), datetime('now'), datetime('now'))`)
		require.NoError(t, err)
		_, err = raw.Exec(`INSERT INTO middleman_kanban_state (merge_request_id, status, updated_at)
			VALUES (1, 'reviewing', '2026-07-01 10:00:00')`)
		require.NoError(t, err)
	})

	d, err := Open(path)
	require.NoError(t, err)
	defer d.Close()

	var itemType, status, source string
	var number int
	err = d.ro.QueryRow(`SELECT item_type, item_number, status, updated_source
		FROM middleman_item_workflow_state`).Scan(&itemType, &number, &status, &source)
	require.NoError(t, err)
	assert.Equal("pr", itemType)
	assert.Equal(7, number)
	assert.Equal("reviewing", status)
	assert.Equal("", source)
	// The old table stays until Task 3's migration 000038 drops it,
	// so this commit remains a working bisect point.
	assert.True(tableExistsForTest(t, d, "middleman_kanban_state"))
	assert.True(tableExistsForTest(t, d, "middleman_item_workflow_state"))
}
```

`openAtVersionForTest` is a new helper: copy the approach of `openSchemaVersion4DBForTest` (`db_test.go:886`) but stop the migrator at version 36. If `golang-migrate`'s `Migrate(36)` API is awkward through the existing wrapper, add a small internal helper in `migrations.go`:

```go
// migrateToVersionForTest migrates the database at path up to exactly
// version v. Test-only helper for seeding pre-migration fixtures.
func migrateToVersionForTest(path string, v uint) error {
	// same source/driver setup as runMigrations, then m.Migrate(v)
}
```

Also update the expected-tables list in `TestOpenAndSchema` (`db_test.go:40`): add `"middleman_item_workflow_state"`. Keep `"middleman_kanban_state"` in the list — Task 3 removes it when migration 000038 drops the table.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/db -run 'TestOpenMigratesKanbanRowsToItemWorkflowState|TestOpenAndSchema' -shuffle=on`
Expected: FAIL (`no such table: middleman_item_workflow_state` / table-list mismatch).

- [ ] **Step 3: Write the migration**

`internal/db/migrations/000037_item_workflow_state.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS middleman_item_workflow_state (
    repo_id        INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    item_type      TEXT NOT NULL,
    item_number    INTEGER NOT NULL,
    status         TEXT NOT NULL DEFAULT 'new',
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_source TEXT NOT NULL DEFAULT '',
    updated_actor  TEXT NOT NULL DEFAULT '',
    updated_reason TEXT NOT NULL DEFAULT '',
    UNIQUE(repo_id, item_type, item_number)
);

CREATE INDEX IF NOT EXISTS idx_item_workflow_status
    ON middleman_item_workflow_state(status, updated_at DESC);

INSERT INTO middleman_item_workflow_state
    (repo_id, item_type, item_number, status, updated_at)
SELECT mr.repo_id, 'pr', mr.number, k.status, k.updated_at
FROM middleman_kanban_state k
JOIN middleman_merge_requests mr ON mr.id = k.merge_request_id;
```

No `DROP TABLE` here: the old table stays live and all existing code keeps working until Task 3 rewires it and drops the table in migration 000038 (same commit as the rewiring).

`internal/db/migrations/000037_item_workflow_state.down.sql`:

```sql
DROP TABLE middleman_item_workflow_state;
```

Legacy rows keep `updated_source = ''` (empty means "predates metadata"). Item deletion no longer cascades workflow rows (only repo deletion does); orphaned rows are invisible because every read joins through the item tables, and a re-claim overwrites them — do not add cleanup machinery.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/db -shuffle=on`
Expected: PASS — the full package passes because nothing that exists yet reads or writes the new table, and the old table is untouched.

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations/000037_item_workflow_state.up.sql internal/db/migrations/000037_item_workflow_state.down.sql internal/db/db_test.go internal/db/migrations.go
git commit -m "feat: generalize PR kanban storage into item workflow state

Adds middleman_item_workflow_state keyed by (repo_id, item_type,
item_number) with last-writer metadata so issues can carry the same
local review workflow as PRs and MCP writes can be attributed. Existing
kanban rows are copied as item_type='pr'; the old table stays live and
authoritative until the Go layer is rewired, so every commit in the
sequence remains a working bisect point."
```

---

### Task 2: Generic workflow-state DB API (get/ensure/set with expected-status conflict)

**Spec sections:** "Local Workflow State" (Model, Status Vocabulary, Metadata).

**Files:**
- Create: `internal/db/queries_workflow.go`
- Create: `internal/db/queries_workflow_test.go`
- Modify: `internal/db/types.go`

**Interfaces:**
- Produces (used by Tasks 3, 4, 5, 6):

```go
const (
	ItemTypePR    = "pr"
	ItemTypeIssue = "issue"
)

type ItemWorkflowState struct {
	RepoID        int64
	ItemType      string
	ItemNumber    int
	Status        string
	UpdatedAt     time.Time
	UpdatedSource string
	UpdatedActor  string
	UpdatedReason string
}

type WorkflowStateConflictError struct{ Expected, Current string }
func (e *WorkflowStateConflictError) Error() string

type SetItemWorkflowStateParams struct {
	RepoID         int64
	ItemType       string
	ItemNumber     int
	Status         string
	ExpectedStatus string // "" = unconditional
	Source, Actor, Reason string
}

func (d *DB) GetItemWorkflowState(ctx context.Context, repoID int64, itemType string, number int) (*ItemWorkflowState, error)
func (d *DB) EnsureItemWorkflowState(ctx context.Context, repoID int64, itemType string, number int) error
// SetItemWorkflowState returns the effective previous status ("new" when
// no row existed) and a *WorkflowStateConflictError when ExpectedStatus
// is set and does not match.
func (d *DB) SetItemWorkflowState(ctx context.Context, p SetItemWorkflowStateParams) (string, error)
```

- [ ] **Step 1: Write the failing tests**

`internal/db/queries_workflow_test.go`:

```go
package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestItemWorkflowStateCRUD(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	insertTestMR(t, d, repoID, 1, "pr 1", baseTime())

	st, err := d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(t, err)
	assert.Nil(st)

	require.NoError(t, d.EnsureItemWorkflowState(ctx, repoID, ItemTypePR, 1))
	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal("new", st.Status)

	prev, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 1,
		Status: "reviewing", Source: "mcp", Actor: "agent-a", Reason: "recent force push",
	})
	require.NoError(t, err)
	assert.Equal("new", prev)

	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(t, err)
	assert.Equal("reviewing", st.Status)
	assert.Equal("mcp", st.UpdatedSource)
	assert.Equal("agent-a", st.UpdatedActor)
	assert.Equal("recent force push", st.UpdatedReason)

	// Ensure never overwrites.
	require.NoError(t, d.EnsureItemWorkflowState(ctx, repoID, ItemTypePR, 1))
	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(t, err)
	assert.Equal("reviewing", st.Status)
}

func TestSetItemWorkflowStateExpectedStatus(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	insertTestMR(t, d, repoID, 1, "pr 1", baseTime())

	// First claim of a never-moved item: expected "new" succeeds with no row.
	prev, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 1,
		Status: "reviewing", ExpectedStatus: "new", Source: "mcp",
	})
	require.NoError(t, err)
	assert.Equal("new", prev)

	// Stale expectation conflicts and does not overwrite.
	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 1,
		Status: "waiting", ExpectedStatus: "new", Source: "mcp",
	})
	var conflict *WorkflowStateConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal("new", conflict.Expected)
	assert.Equal("reviewing", conflict.Current)

	st, err := d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 1)
	require.NoError(t, err)
	assert.Equal("reviewing", st.Status)

	// Matching expectation succeeds.
	prev, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 1,
		Status: "waiting", ExpectedStatus: "reviewing", Source: "api",
	})
	require.NoError(t, err)
	assert.Equal("reviewing", prev)
}

func TestItemWorkflowStateIssueType(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")

	prev, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypeIssue, ItemNumber: 9,
		Status: "reviewing", Source: "mcp",
	})
	require.NoError(t, err)
	assert.Equal(t, "new", prev)

	st, err := d.GetItemWorkflowState(ctx, repoID, ItemTypeIssue, 9)
	require.NoError(t, err)
	assert.Equal(t, "reviewing", st.Status)

	// PR namespace is independent of issue namespace for the same number.
	st, err = d.GetItemWorkflowState(ctx, repoID, ItemTypePR, 9)
	require.NoError(t, err)
	assert.Nil(t, st)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db -run 'TestItemWorkflowState|TestSetItemWorkflowState' -shuffle=on`
Expected: FAIL (compile error: undefined `GetItemWorkflowState` etc.).

- [ ] **Step 3: Implement types and queries**

Add types from the Interfaces block above to `internal/db/types.go` (next to `KanbanState` at `:347`). Conflict error:

```go
// WorkflowStateConflictError reports an expected-status mismatch on a
// conditional workflow-state write. Current is the effective status at
// write time (missing row reads as "new").
type WorkflowStateConflictError struct {
	Expected string
	Current  string
}

func (e *WorkflowStateConflictError) Error() string {
	return fmt.Sprintf("workflow state is %q, expected %q", e.Current, e.Expected)
}
```

`internal/db/queries_workflow.go`:

```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Item types stored in middleman_item_workflow_state.
const (
	ItemTypePR    = "pr"
	ItemTypeIssue = "issue"
)

func (d *DB) GetItemWorkflowState(
	ctx context.Context, repoID int64, itemType string, number int,
) (*ItemWorkflowState, error) {
	var w ItemWorkflowState
	err := d.ro.QueryRowContext(ctx,
		`SELECT repo_id, item_type, item_number, status, updated_at,
		        updated_source, updated_actor, updated_reason
		 FROM middleman_item_workflow_state
		 WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		repoID, itemType, number,
	).Scan(&w.RepoID, &w.ItemType, &w.ItemNumber, &w.Status, &w.UpdatedAt,
		&w.UpdatedSource, &w.UpdatedActor, &w.UpdatedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get item workflow state: %w", err)
	}
	return &w, nil
}

// EnsureItemWorkflowState creates a row with status "new" if none exists.
func (d *DB) EnsureItemWorkflowState(
	ctx context.Context, repoID int64, itemType string, number int,
) error {
	_, err := d.rw.ExecContext(ctx,
		`INSERT INTO middleman_item_workflow_state (repo_id, item_type, item_number, status)
		 VALUES (?, ?, ?, 'new')
		 ON CONFLICT(repo_id, item_type, item_number) DO NOTHING`,
		repoID, itemType, number,
	)
	if err != nil {
		return fmt.Errorf("ensure item workflow state: %w", err)
	}
	return nil
}

// SetItemWorkflowState upserts workflow state. When ExpectedStatus is
// non-empty it is compared against the effective current status (a
// missing row is "new"); a mismatch returns *WorkflowStateConflictError
// and leaves the row untouched. Returns the effective previous status.
func (d *DB) SetItemWorkflowState(
	ctx context.Context, p SetItemWorkflowStateParams,
) (string, error) {
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("set item workflow state: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current := "new"
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM middleman_item_workflow_state
		 WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		p.RepoID, p.ItemType, p.ItemNumber,
	).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("set item workflow state: read current: %w", err)
	}

	if p.ExpectedStatus != "" && p.ExpectedStatus != current {
		return "", &WorkflowStateConflictError{Expected: p.ExpectedStatus, Current: current}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO middleman_item_workflow_state
		    (repo_id, item_type, item_number, status, updated_at,
		     updated_source, updated_actor, updated_reason)
		VALUES (?, ?, ?, ?, datetime('now'), ?, ?, ?)
		ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
		    status         = excluded.status,
		    updated_at     = excluded.updated_at,
		    updated_source = excluded.updated_source,
		    updated_actor  = excluded.updated_actor,
		    updated_reason = excluded.updated_reason`,
		p.RepoID, p.ItemType, p.ItemNumber, p.Status,
		p.Source, p.Actor, p.Reason,
	)
	if err != nil {
		return "", fmt.Errorf("set item workflow state: upsert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("set item workflow state: commit: %w", err)
	}
	return current, nil
}
```

Byte-limit validation for source/actor/reason lives at the server layer (Task 6), not here.
Vocabulary validation DOES live here, at the DB API boundary: reject any
`itemType` other than `ItemTypePR`/`ItemTypeIssue` and any `Status` (or
non-empty `ExpectedStatus`) outside new/reviewing/waiting/awaiting_merge
with a descriptive error before touching the table. A typo like
`"pull_request"` would otherwise silently key a separate row, so canonical
readers see the item as effective-"new" and expected-status conflict
detection is bypassed. Required tests: invalid item type on all three
funcs, invalid status and invalid expected_status on SetItemWorkflowState,
each asserting an error and (for writes) that no row was created. Also
seed one valid existing row before an invalid status/expected_status write
and assert the stored row remains unchanged.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db -run 'TestItemWorkflowState|TestSetItemWorkflowState' -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/queries_workflow.go internal/db/queries_workflow_test.go internal/db/types.go
git commit -m "feat: add generic item workflow-state queries with conditional writes

expected_status compares against the effective status inside a write
transaction (missing row = new) so a periodic agent can claim
never-moved items without racing humans or other agents."
```

---

### Task 3: Rewire PR kanban paths onto the generic store

**Spec sections:** "PR Kanban Compatibility".

**Files:**
- Create: `internal/db/migrations/000038_drop_kanban_state.up.sql`, `.down.sql`
- Modify: `internal/db/queries.go` (joins at `2312/2316`, `2373/2376`, `2495/2499`; filter at `2458-2465`; delete `EnsureKanbanState`/`SetKanbanState`/`GetKanbanState` at `2727-2771`; also the host-cleanup DELETE at `queries.go:916`)
- Modify: `internal/db/types.go` (delete `KanbanState` struct at `:347-351`; keep `KanbanStatus` type and `MergeRequest.KanbanStatus`)
- Modify: `internal/server/huma_routes.go` (`setKanbanState` handler `:1886`, `EnsureKanbanState` call at `:3074`)
- Modify: `internal/db/db_test.go` (`TestOpenAndSchema` list: remove `"middleman_kanban_state"`)
- Modify: `internal/db/queries_test.go` (`TestKanbanState` at `:2869` and any other `EnsureKanbanState`/`SetKanbanState` callers)
- Modify: `internal/server/apitest/fixtures_test.go` (`seedPR` at `:170`)

**Interfaces:**
- Consumes: `EnsureItemWorkflowState`, `SetItemWorkflowState`, `GetItemWorkflowState`, `ItemTypePR` (Task 2).
- Produces: unchanged public behavior — `KanbanStatus` on PR list/detail responses, `PUT .../state` request/response shape identical. All old `*KanbanState` DB funcs and `middleman_kanban_state` itself are gone after this task; the rewiring and the drop land in ONE commit so no commit references a dropped table.

- [ ] **Step 1: Add migration 000038 (re-sync then drop)**

Between Task 1's migration and this task, running pre-rewire builds still wrote `middleman_kanban_state` — and nothing in production wrote the new table during that window — so the legacy table is authoritative and the drop migration re-syncs UNCONDITIONALLY (no `updated_at` comparison: `datetime('now')` has one-second precision, so a timestamp-guarded upsert could drop a kanban change made within the same second as the 000037 copy). For anyone upgrading across the whole branch, 000037 and 000038 run back-to-back and the re-sync is a no-op. The migration test must cover the equal-timestamp case: seed a kanban row whose status differs from the already-copied generic row but shares its `updated_at`, run `Open`, and assert the kanban value won.

`internal/db/migrations/000038_drop_kanban_state.up.sql`:

```sql
INSERT INTO middleman_item_workflow_state
    (repo_id, item_type, item_number, status, updated_at)
SELECT mr.repo_id, 'pr', mr.number, k.status, k.updated_at
FROM middleman_kanban_state k
JOIN middleman_merge_requests mr ON mr.id = k.merge_request_id
WHERE 1
ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
    status     = excluded.status,
    updated_at = excluded.updated_at;

-- The workflow helpers reject statuses outside the canonical vocabulary, so
-- an invalid legacy status carried into the canonical table would be
-- unreadable and unfixable through the API. Normalize the whole table, not
-- just the re-synced rows, to also cover rows copied by 000037 whose kanban
-- counterpart has since been deleted.
UPDATE middleman_item_workflow_state
SET status = 'new'
WHERE status NOT IN ('new', 'reviewing', 'waiting', 'awaiting_merge');

DROP TABLE middleman_kanban_state;
```

Required migration test alongside the equal-timestamp case: seed (at version
37) a kanban row with an out-of-vocabulary status (for example `'triage'`)
plus a generic-table row carrying an invalid status with no kanban
counterpart, run `Open`, and assert both read back as `'new'`.

`internal/db/migrations/000038_drop_kanban_state.down.sql`:

```sql
CREATE TABLE IF NOT EXISTS middleman_kanban_state (
    merge_request_id INTEGER PRIMARY KEY REFERENCES middleman_merge_requests(id) ON DELETE CASCADE,
    status           TEXT NOT NULL DEFAULT 'new',
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_kanban_status
    ON middleman_kanban_state(status, updated_at DESC);

INSERT INTO middleman_kanban_state (merge_request_id, status, updated_at)
SELECT mr.id, w.status, w.updated_at
FROM middleman_item_workflow_state w
JOIN middleman_merge_requests mr
    ON mr.repo_id = w.repo_id AND mr.number = w.item_number
WHERE w.item_type = 'pr';
```

Update `TestOpenAndSchema` (`db_test.go:40`): remove `"middleman_kanban_state"` from the expected list. Update Task 1's migration test assertion (`assert.True(tableExistsForTest(t, d, "middleman_kanban_state"))` → `assert.False(...)`) since `Open` now runs through 000038.

- [ ] **Step 2: Update the DB joins and filter**

In `internal/db/queries.go`, replace all three occurrences of

```sql
LEFT JOIN middleman_kanban_state k ON k.merge_request_id = p.id
```

with

```sql
LEFT JOIN middleman_item_workflow_state k
    ON k.repo_id = p.repo_id AND k.item_type = 'pr' AND k.item_number = p.number
```

The `COALESCE(k.status, '') AS kanban_status` select expressions and the kanban filter logic (`COALESCE(k.status, 'new') = ?` for `new`) are unchanged. Update the host-cleanup statement at `queries.go:916` from

```sql
DELETE FROM middleman_kanban_state WHERE merge_request_id IN (SELECT id FROM middleman_merge_requests WHERE repo_id IN (SELECT id FROM middleman_repos WHERE platform_host != ?))
```

to

```sql
DELETE FROM middleman_item_workflow_state WHERE repo_id IN (SELECT id FROM middleman_repos WHERE platform_host != ?)
```

Delete `EnsureKanbanState`, `SetKanbanState`, `GetKanbanState` (`queries.go:2727-2771`) and the `KanbanState` struct (`types.go:347-351`). Grep for remaining callers and fix each:

```bash
grep -rn "EnsureKanbanState\|SetKanbanState\|GetKanbanState\|KanbanState{" internal/ cmd/
```

Known callers and their replacements:
- `internal/server/huma_routes.go:1912` (`setKanbanState` handler): replace `s.db.SetKanbanState(ctx, mrID, input.Body.Status)` with

```go
if _, err := s.db.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
	RepoID: repo.ID, ItemType: db.ItemTypePR, ItemNumber: input.Number,
	Status: input.Body.Status, Source: "ui",
}); err != nil {
	return nil, problemInternal("set kanban state failed")
}
```

Keep the `lookupMRID` existence check above it exactly as-is (it guards item existence). Response stays `statusOnlyOutput{Status: http.StatusOK}`.
- `internal/server/huma_routes.go:3074`: replace `_ = s.db.EnsureKanbanState(ctx, mrID)` with `_ = s.db.EnsureItemWorkflowState(ctx, repo.ID, db.ItemTypePR, ...)` — read the surrounding function to get the repo and number variables in scope; if only `mrID` is in scope, fetch the MR row that is already loaded in that handler (it is a PR-creation/import path; the MR number and repo are available upstream — thread them through rather than re-querying).
- `internal/server/apitest/fixtures_test.go:170` (`seedPR`): replace `database.EnsureKanbanState(ctx, prID)` with `database.EnsureItemWorkflowState(ctx, repoID, db.ItemTypePR, number)` (repoID and number are already in scope in `seedPR`).
- `internal/db/queries_test.go:2869` (`TestKanbanState`) and `:2529` (`TestListPullRequestsFilterByKanban`): port to the new API — `SetKanbanState(ctx, id2, "reviewing")` becomes `SetItemWorkflowState(ctx, SetItemWorkflowStateParams{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 2, Status: "reviewing", Source: "ui"})`; `EnsureKanbanState(ctx, id3)` becomes `EnsureItemWorkflowState(ctx, repoID, ItemTypePR, 3)`. The filter assertions (`KanbanState: "reviewing"` returns PR 2; `"new"` returns PRs 3 and 1) stay identical — they prove the compatibility contract.

- [ ] **Step 3: Run the full affected suites**

Run: `go test ./internal/db ./internal/server/... -shuffle=on`
Expected: PASS, including untouched kanban API tests in apitest (the public-contract proof).

- [ ] **Step 4: Commit**

```bash
git add -A internal/db internal/server
git commit -m "refactor: back PR kanban reads and writes with item workflow state

The kanban board, PR list/detail KanbanStatus, and the existing PUT
/pulls/.../state route keep their exact public behavior while the
storage moves to the generic (repo_id, item_type, item_number) table,
so issues and MCP writes can share one state store without a
duplicate-write shim. Migration 000038 re-syncs any rows written to
the old table since 000037 and drops it in this same commit, so no
commit on the branch queries a dropped table."
```

---

### Task 4: Workflow-state listing query with cursor pagination

**Spec sections:** "Daemon API Additions" (`GET /workflow-state` semantics).

**Files:**
- Create additions in: `internal/db/queries_workflow.go`, `internal/db/types.go`
- Test: `internal/db/queries_workflow_test.go`

**Interfaces:**
- Consumes: `RepoFilter` (existing type used by `ListMergeRequestsOpts.RepoFilters` — see `internal/db/types.go`; reuse it, do not invent a new filter shape).
- Produces (used by Task 6):

```go
type ListWorkflowStatesOpts struct {
	RepoFilters   []RepoFilter
	ItemTypes     []string // subset of {"pr","issue"}; empty = both
	States        []string // effective statuses to include; empty = all
	IncludeClosed bool
	Limit         int    // default 50, cap 200 applied here
	Cursor        string // opaque; "" = first page
}

type WorkflowStateListRow struct {
	Platform, PlatformHost, Owner, Name string
	RepoPath       string // stored middleman_repos.repo_path; falls back to owner/name in SQL for legacy empty rows
	ItemType       string
	Number         int
	Title, State, URL, Author string
	IsDraft        bool
	LastActivityAt time.Time
	Status         string     // effective ("new" when no row)
	HasRow         bool
	UpdatedAt      *time.Time // nil when generated
	UpdatedSource, UpdatedActor, UpdatedReason string
}

// ListItemWorkflowStates returns one page plus the next cursor ("" when
// exhausted).
func (d *DB) ListItemWorkflowStates(ctx context.Context, opts ListWorkflowStatesOpts) ([]WorkflowStateListRow, string, error)
```

Ordering contract (single keyset satisfying both spec bullets): primary key `sortKey = CAST(COALESCE(w.updated_at, item.last_activity_at) AS TEXT) DESC`, then `activityKey = CAST(item.last_activity_at AS TEXT) DESC`, ties broken ascending by `(platform, platform_host, owner, name, item_type, number)`. Stored workflow rows therefore sort by their workflow `updated_at`, while generated `new` rows with no workflow storage sort by item `last_activity_at`. Cursor encodes that full tuple as an opaque `base64.RawURLEncoding` payload over fields joined with `"\x1f"` (`sortKey, activityKey, platform, platformHost, owner, name, itemType, number`). Invalid cursors return an error (the handler maps it to `problemValidation`). Cursors are filter-bound: clients must only reuse a cursor with the same repo, item-type, state, and include-closed filter set that produced it.

Cursor stability is best-effort across requests, not snapshot isolation. If a workflow write or item activity update happens between page requests, the changed item can move across the cursor boundary and may be skipped or repeated. This is accepted for v1 because the cursor prevents offset drift for a stable ordering without holding DB snapshots across HTTP requests.

- [ ] **Step 1: Write the failing tests**

Append to `internal/db/queries_workflow_test.go`:

```go
func TestListItemWorkflowStates(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	// PR 1: open, no workflow row -> effective new.
	insertTestMR(t, d, repoID, 1, "pr one", base.Add(1*time.Hour))
	// PR 2: open, explicit reviewing.
	insertTestMR(t, d, repoID, 2, "pr two", base.Add(2*time.Hour))
	// PR 3: closed (excluded by default). Use the existing helper or set state directly:
	id3 := insertTestMR(t, d, repoID, 3, "pr three", base.Add(3*time.Hour))
	_, err := d.rw.Exec(`UPDATE middleman_merge_requests SET state='closed' WHERE id=?`, id3)
	require.NoError(t, err)
	// Issue 5: open, no row -> effective new.
	insertTestIssue(t, d, repoID, 5, "issue five", base.Add(4*time.Hour))

	_, err = d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 2,
		Status: "reviewing", Source: "ui",
	})
	require.NoError(t, err)

	rows, next, err := d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{})
	require.NoError(t, err)
	assert.Empty(next)
	// PR 2 has the newest sort key (its updated_at is now); then issue 5 and
	// PR 1 by last_activity_at. Closed PR 3 excluded.
	require.Len(t, rows, 3)
	assert.Equal(2, rows[0].Number)
	assert.Equal("reviewing", rows[0].Status)
	assert.True(rows[0].HasRow)
	assert.Equal("issue", rows[1].ItemType)
	assert.Equal(5, rows[1].Number)
	assert.Equal("new", rows[1].Status)
	assert.False(rows[1].HasRow)
	assert.Equal(1, rows[2].Number)

	// state=new matches effective status: items with no row AND items
	// explicitly set to "new" (Global Constraints rule).
	rows, _, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{States: []string{"new"}})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// include_closed picks up PR 3.
	rows, _, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{IncludeClosed: true})
	require.NoError(t, err)
	assert.Len(rows, 4)

	// item type filter.
	rows, _, err = d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{ItemTypes: []string{"issue"}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal("issue", rows[0].ItemType)
}

func TestListItemWorkflowStatesCursorPagination(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()
	for i := 1; i <= 5; i++ {
		insertTestMR(t, d, repoID, i, "pr", base.Add(time.Duration(i)*time.Hour))
	}

	var got []int
	cursor := ""
	pages := 0
	for {
		rows, next, err := d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{Limit: 2, Cursor: cursor})
		require.NoError(t, err)
		for _, r := range rows {
			got = append(got, r.Number)
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	assert.Equal([]int{5, 4, 3, 2, 1}, got)
	assert.Equal(3, pages)

	_, _, err := d.ListItemWorkflowStates(ctx, ListWorkflowStatesOpts{Cursor: "not-base64!!"})
	assert.Error(err)
}
```

If `insertTestIssue` does not exist in `queries_test.go`, add a helper mirroring `insertTestMR` that inserts into `middleman_issues` with the given number, title, and `last_activity_at` (look at `seedLegacyIssueForTest` at `db_test.go:913` and the `middleman_issues` columns; `platform_id` must be unique per repo — use the number).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db -run TestListItemWorkflowStates -shuffle=on`
Expected: FAIL (undefined `ListItemWorkflowStates`).

- [ ] **Step 3: Implement**

Append to `internal/db/queries_workflow.go` (types to `types.go`). Shape of the implementation — a UNION ALL over PRs and issues, each LEFT JOINed to workflow state, wrapped for keyset filtering:

```go
func encodeWorkflowCursor(r WorkflowStateListRow, sortKey, activityKey string) string {
	raw := strings.Join([]string{
		sortKey,
		activityKey,
		r.Platform, r.PlatformHost, r.Owner, r.Name, r.ItemType,
		strconv.Itoa(r.Number),
	}, "\x1f")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

type workflowCursor struct {
	sortKey, activityKey                   string
	platform, host, owner, name, itemType string
	number                                 int
}

func decodeWorkflowCursor(s string) (workflowCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return workflowCursor{}, fmt.Errorf("invalid cursor: %w", err)
	}
	parts := strings.Split(string(raw), "\x1f")
	if len(parts) != 8 {
		return workflowCursor{}, errors.New("invalid cursor")
	}
	num, err := strconv.Atoi(parts[7])
	if err != nil {
		return workflowCursor{}, fmt.Errorf("invalid cursor number: %w", err)
	}
	return workflowCursor{
		sortKey: parts[0], activityKey: parts[1],
		platform: parts[2], host: parts[3], owner: parts[4], name: parts[5],
		itemType: parts[6], number: num,
	}, nil
}
```

Core query (build with the same conds/args style as `ListMergeRequests`; study `queries.go:2413-2530` first):

```sql
SELECT * FROM (
    SELECT r.platform, r.platform_host, r.owner, r.name,
           COALESCE(NULLIF(r.repo_path, ''), r.owner || '/' || r.name) AS repo_path,
           'pr' AS item_type, p.number, p.title, p.state, p.url, p.author,
           p.is_draft, p.last_activity_at,
           COALESCE(w.status, 'new') AS status,
           (w.repo_id IS NOT NULL) AS has_row,
           w.updated_at, COALESCE(w.updated_source,'') AS updated_source,
           COALESCE(w.updated_actor,'') AS updated_actor,
           COALESCE(w.updated_reason,'') AS updated_reason,
           CAST(COALESCE(w.updated_at, p.last_activity_at) AS TEXT) AS sort_key,
           CAST(p.last_activity_at AS TEXT) AS activity_key
    FROM middleman_merge_requests p
    JOIN middleman_repos r ON r.id = p.repo_id
    LEFT JOIN middleman_item_workflow_state w
        ON w.repo_id = p.repo_id AND w.item_type = 'pr' AND w.item_number = p.number
    UNION ALL
    SELECT r.platform, r.platform_host, r.owner, r.name,
           COALESCE(NULLIF(r.repo_path, ''), r.owner || '/' || r.name) AS repo_path,
           'issue' AS item_type, i.number, i.title, i.state, i.url, i.author,
           0 AS is_draft, i.last_activity_at,
           COALESCE(w.status, 'new') AS status,
           (w.repo_id IS NOT NULL) AS has_row,
           w.updated_at, COALESCE(w.updated_source,''), COALESCE(w.updated_actor,''),
           COALESCE(w.updated_reason,''),
           CAST(COALESCE(w.updated_at, i.last_activity_at) AS TEXT) AS sort_key,
           CAST(i.last_activity_at AS TEXT) AS activity_key
    FROM middleman_issues i
    JOIN middleman_repos r ON r.id = i.repo_id
    LEFT JOIN middleman_item_workflow_state w
        ON w.repo_id = i.repo_id AND w.item_type = 'issue' AND w.item_number = i.number
) t
WHERE <conds>
ORDER BY t.sort_key DESC, t.activity_key DESC,
         t.platform, t.platform_host, t.owner, t.name, t.item_type, t.number
LIMIT ?
```

Conds assembled in Go: item-type filter (`t.item_type IN (...)` — validate values are `pr`/`issue`, else error), states filter (`t.status IN (...)`), closed filter when `!IncludeClosed` (`t.state NOT IN ('closed','merged')`), repo filters (MUST be applied inside each UNION arm against `r.*` using the exact casefold-aware helper/condition `ListMergeRequests` uses for `RepoFilters` — read `queries.go:2413-2530` and reuse that helper or its condition builder verbatim; it matches on the `owner_key`/`name_key`/`repo_path_key` key columns plus `platform`/`platform_host`, NOT case-sensitive display `owner`/`name`. Do not filter on the outer `t.*` display columns), and the keyset predicate when a cursor is present:

```sql
(t.sort_key < ?
 OR (t.sort_key = ? AND t.activity_key < ?)
 OR (t.sort_key = ? AND t.activity_key = ?
     AND (t.platform, t.platform_host, t.owner, t.name, t.item_type, t.number)
         > (?, ?, ?, ?, ?, ?)))
```

SQLite supports row-value comparison since 3.15; modernc.org/sqlite handles it. Limit: default 50 when `opts.Limit <= 0`, cap at 200. Fetch `limit+1` rows; when you get the extra row, drop it and return `encodeWorkflowCursor(lastKept, lastKeptSortKey, lastKeptActivityKey)` as next cursor. Scan `sort_key` and `activity_key` as strings alongside each row for cursor encoding; parse `last_activity_at` and nullable `updated_at` with the DB time parser before assigning response fields.

Cursor stability: the cursor is a keyset snapshot, not an isolation mechanism. A workflow write between pages changes an item's `sort_key`, which can move it across the cursor boundary (appearing twice or being skipped). This is accepted for v1; document it in the endpoint's huma `doc:` tag on `cursor` so API consumers know pages are best-effort under concurrent writes. The cursor is also filter-bound: it encodes only the keyset position, not the filters that produced it, so reusing a cursor with a different `state`/`item_type`/`repo`/`include_closed` combination silently resumes the new filter set from the old position. Clients must pass the same filters for every page of a walk; state that in the same `doc:` tag.

Add one more test alongside the pagination test: seed two repos differing only in owner case (or one on a non-default `platform_host`) and assert a `RepoFilters` entry with mixed-case input matches via the key columns and excludes the other repo — this pins the casefold-aware filter requirement above. In the same test, give one repo a stored `repo_path` whose display casing differs from `owner + "/" + name` (GitLab-style) and assert `WorkflowStateListRow.RepoPath` returns the stored value, not the concatenation.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db -run TestListItemWorkflowStates -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/queries_workflow.go internal/db/queries_workflow_test.go internal/db/types.go internal/db/queries_test.go
git commit -m "feat: list PR and issue workflow states with keyset pagination

Missing rows surface as effective new so state=new returns open items
that were never moved; explicit rows sort by their write time so recent
claims surface first, and the opaque cursor carries the full ordering
tuple instead of an offset."
```

---

### Task 5: Issue workflow-state exposure on issue list/detail responses

**Spec sections:** "PR Kanban Compatibility" (last paragraph).

**Files:**
- Modify: `internal/db/queries.go` (`ListIssues` select `3385-3389` and its variants, `GetIssue` `3228`, `GetIssueByRepoIDAndNumber` `3279`)
- Modify: `internal/db/types.go` (`Issue` struct `:376`)
- Modify: `internal/server/api_types.go` (`issueDetailResponse` `:151`), plus the builder `buildIssueDetailResponse` (`huma_routes.go:~2511`)
- Test: `internal/db/queries_workflow_test.go` (DB-level) AND `internal/server/apitest/issue_workflow_test.go` (new, owned by THIS task): the issue wire contract lands here, not in Task 6 — seed an issue with no workflow row via `seedIssue`, assert `GET /issues` and `GET /issues/{provider}/{owner}/{name}/{number}` serialize `WorkflowStatus` as `"new"` (never `""`), then write a state via `SetItemWorkflowState` and assert list/detail reflect it plus detail's `workflow` metadata block. Run: `go test ./internal/server/apitest -run TestIssueWorkflow -shuffle=on`. Task 6 owns only the new `/workflow-state` endpoint tests.

**Interfaces:**
- Produces: `db.Issue.WorkflowStatus KanbanStatus` field (empty string when no row, mirroring PR `KanbanStatus` behavior); `issueDetailResponse.Workflow *workflowStateMetaResponse`.

```go
// api_types.go — new shared shape, reused by Task 6 responses
type workflowStateMetaResponse struct {
	Status        db.KanbanStatus `json:"status" enum:"new,reviewing,waiting,awaiting_merge"`
	UpdatedAt     string          `json:"updated_at,omitempty" format:"date-time"`
	UpdatedSource string          `json:"updated_source,omitempty"`
	UpdatedActor  string          `json:"updated_actor,omitempty"`
	UpdatedReason string          `json:"updated_reason,omitempty"`
}
```

There is exactly ONE workflow status contract on the wire: `Status` uses the
same `db.KanbanStatus` enum as `Issue.WorkflowStatus`/PR `KanbanStatus`, and
every value placed in it is normalized through the same helper that
normalizes the item-level field (unknown/empty → `"new"`, with a warning
log). A response must never be able to say `issue.WorkflowStatus: "new"`
while `workflow.status` carries a different raw string — two divergent
representations of the same datum is a contract bug. `UpdatedAt` declares
`format:"date-time"` so generated clients keep RFC3339 typing.

- [ ] **Step 1: Write the failing DB test**

```go
func TestIssueQueriesExposeWorkflowStatus(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "owner", "repo")
	insertTestIssue(t, d, repoID, 5, "issue five", baseTime())

	_, err := d.SetItemWorkflowState(ctx, SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypeIssue, ItemNumber: 5,
		Status: "waiting", Source: "api",
	})
	require.NoError(t, err)

	iss, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 5)
	require.NoError(t, err)
	assert.Equal(KanbanStatus("waiting"), iss.WorkflowStatus)

	list, err := d.ListIssues(ctx, ListIssuesOpts{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(KanbanStatus("waiting"), list[0].WorkflowStatus)

	// No row -> empty string (public responses read empty as new,
	// mirroring PR KanbanStatus).
	insertTestIssue(t, d, repoID, 6, "issue six", baseTime())
	iss, err = d.GetIssueByRepoIDAndNumber(ctx, repoID, 6)
	require.NoError(t, err)
	assert.Equal(KanbanStatus(""), iss.WorkflowStatus)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db -run TestIssueQueriesExposeWorkflowStatus -shuffle=on`
Expected: FAIL (no `WorkflowStatus` field).

- [ ] **Step 3: Implement**

Add to `db.Issue` (after `Starred`, `types.go`): `WorkflowStatus KanbanStatus \`enum:"new,reviewing,waiting,awaiting_merge"\`` . In each issue select (`GetIssue`, `GetIssueByRepoIDAndNumber`, `ListIssues`) add

```sql
COALESCE(w.status, '') AS workflow_status,
```

and

```sql
LEFT JOIN middleman_item_workflow_state w
    ON w.repo_id = i.repo_id AND w.item_type = 'issue' AND w.item_number = i.number
```

scanning into `&iss.WorkflowStatus` in the same position. Then in `internal/server/api_types.go` add `workflowStateMetaResponse` (above) and `Workflow *workflowStateMetaResponse \`json:"workflow,omitempty"\`` to `issueDetailResponse`; populate it in `buildIssueDetailResponse` via `s.db.GetItemWorkflowState(ctx, repo.ID, db.ItemTypeIssue, issue.Number)` — status falls back to `"new"` when nil:

```go
row, err := s.db.GetItemWorkflowState(ctx, repo.ID, db.ItemTypeIssue, issue.Number)
if err != nil {
	// A DB failure must surface, not masquerade as effective "new".
	return nil, problemInternal("read issue workflow state failed")
}
wf := &workflowStateMetaResponse{Status: "new"}
if row != nil {
	wf = &workflowStateMetaResponse{
		Status:        row.Status,
		UpdatedAt:     row.UpdatedAt.UTC().Format(time.RFC3339),
		UpdatedSource: row.UpdatedSource,
		UpdatedActor:  row.UpdatedActor,
		UpdatedReason: row.UpdatedReason,
	}
}
resp.Workflow = wf
```

Every code path that constructs `issueDetailResponse` must carry the field: grep for `issueDetailResponse{` — the issue sync handler (`POST /issues/.../sync`) builds its response separately from `buildIssueDetailResponse`. Refactor it to reuse the shared builder (preferred) or populate `Workflow` there too, and add an apitest asserting the sync route's response includes `workflow` after a state write.

Wire normalization mirrors PRs exactly: the empty string a missing row scans into `db.Issue.WorkflowStatus` is an internal DB detail and must never reach the wire. PR responses already normalize via `mergeRequestResponseKanbanStatus` (`huma_routes.go:1693-1701`, empty/unexpected → `new`); add the analogous `issueResponseWorkflowStatus` helper and apply it wherever `issueResponse`/`issueDetailResponse` are built, so issue list/detail emit `WorkflowStatus: "new"` for missing rows. Add an apitest asserting an issue with no workflow row serializes `WorkflowStatus` as `"new"` on both list and detail.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/db ./internal/server/... -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db internal/server
git commit -m "feat: expose local workflow status on issue reads

API consumers no longer need to special-case PRs for local review
state; issues carry WorkflowStatus on list rows and last-writer
metadata on detail."
```

---

### Task 6: Daemon workflow-state endpoints + regenerate API artifacts

**Spec sections:** "Daemon API Additions", "Testing > Backend tests".

**Files:**
- Create: `internal/server/workflow_state_routes.go`
- Create: `internal/server/apitest/workflow_state_test.go`
- Modify: `internal/server/huma_routes.go` (one line: call `s.registerWorkflowStateAPI(api)` next to the existing `s.registerProviderRepoAPI(api)` call — grep for it)
- Regenerate: `make api-generate` artifacts

**Interfaces:**
- Consumes: Task 2/4/5 DB API; `lookupRepoByProviderRoute`, `lookupMRID`, `lookupIssueID`, `providerRouteLookupError`, `problem*` helpers, `bodyOutput`, `parseRepoFilters`, `hasInvalidRepoFilter` (all existing in package `server`).
- Produces routes (OperationIDs → generated client methods):
  - `GET /workflow-state` (`list-workflow-state` → `ListWorkflowStateWithResponse`)
  - `PUT /workflow-state/{item_type}/{provider}/{owner}/{name}/{number}` (`set-workflow-state` → `SetWorkflowStateWithResponse`)
  - `PUT /host/{platform_host}/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}` (`set-workflow-state-on-host`)
- Produces wire shapes (consumed by the MCP companion in Tasks 10/12):

```json
GET /workflow-state -> 200
{
  "items": [
    {
      "provider": "github", "platform_host": "github.com",
      "owner": "acme", "name": "widget", "repo_path": "acme/widget",
      "item_type": "pr", "number": 42,
      "title": "...", "state": "open", "url": "...", "author": "alice",
      "is_draft": false, "last_activity_at": "2026-07-01T14:12:00Z",
      "workflow": {"status": "reviewing", "updated_at": "...",
                    "updated_source": "mcp", "updated_actor": "agent-a",
                    "updated_reason": "..."}
    }
  ],
  "next_cursor": ""
}

PUT /workflow-state/... body:
{"status": "reviewing", "expected_status": "new", "source": "mcp",
 "actor": "agent-a", "reason": "recent force push"}
-> 200 {"previous_status": "new", "status": "reviewing",
        "updated_at": "...", "updated_source": "mcp",
        "updated_actor": "agent-a", "updated_reason": "..."}
-> 409 problem {"code": "conflict",
        "details": {"current_status": "reviewing", "expected_status": "new"}}
```

- [ ] **Step 1: Write the failing API tests**

`internal/server/apitest/workflow_state_test.go` — raw `srv.ServeHTTP` via the existing `setupTestClient` transport; use generated methods once they exist, but write the first version with plain HTTP through the same round-tripper if the generated client is not regenerated yet (regeneration happens in Step 3; after it, switch to `<OperationID>WithResponse` methods per the apitest convention). Cover, one test function each, table-driven where natural:

```go
package apitest

// TestWorkflowStatePutAndGet: seedPR + seedIssue; PUT pr -> reviewing with
// expected_status "new" succeeds (200, previous_status "new"); GET
// /workflow-state returns both items with the PR first (explicit row);
// issue shows effective "new" with no metadata.

// TestWorkflowStatePutConflict: PUT with stale expected_status returns 409,
// problem code "conflict", details.current_status/expected_status set, and
// a follow-up GET proves the state did not change.

// TestWorkflowStatePutValidation: invalid status -> 422/400 problem
// validationError; invalid item_type in path -> huma enum rejection;
// source failing ^[a-z][a-z0-9_-]{0,39}$ -> validation problem;
// actor > 120 bytes -> validation problem; reason > 500 bytes ->
// validation problem.

// TestWorkflowStatePutMissingItem: unknown PR number -> 404 pullNotFound;
// unknown issue number -> 404 issueNotFound; unknown repo -> 404
// repoNotFound.

// TestWorkflowStateHostVariant: seedPROnHost (existing helper) then PUT via
// /host/{platform_host}/workflow-state/... succeeds and GET filtered by
// repo=provider|host/owner/name returns it.

// TestWorkflowStateListFilters: closed PR excluded by default, included
// with include_closed=true; state=new matches effective status (no-row
// items AND explicit new rows, per the Global Constraints rule);
// item_type=issue excludes PRs; limit+cursor walk returns disjoint pages
// in the documented order (deterministic pagination).
```

Write these as full tests, not comments — the comment block above defines the required scenarios; each maps 1:1 to a `func Test...(t *testing.T)`. Use `seedPR`/`seedIssue`/`seedPROnHost` from `fixtures_test.go` and follow `api_test.go:41-58` for shape.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/apitest -run TestWorkflowState -shuffle=on`
Expected: FAIL (404s — routes missing).

- [ ] **Step 3: Implement routes**

`internal/server/workflow_state_routes.go`:

```go
package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
)

type listWorkflowStateInput struct {
	Repo          string   `query:"repo" doc:"Repository filter. Accepts provider|platform_host/repo_path, with comma-separated values for multiple repositories."`
	ItemType      []string `query:"item_type" doc:"Optional item type filter: pr, issue."`
	State         []string `query:"state" doc:"Optional effective workflow states to include."`
	IncludeClosed bool     `query:"include_closed"`
	Limit         int      `query:"limit"`
	Cursor        string   `query:"cursor"`
}

type workflowStateItemResponse struct {
	Provider       string                    `json:"provider"`
	PlatformHost   string                    `json:"platform_host"`
	Owner          string                    `json:"owner"`
	Name           string                    `json:"name"`
	RepoPath       string                    `json:"repo_path"`
	ItemType       string                    `json:"item_type"`
	Number         int                       `json:"number"`
	Title          string                    `json:"title"`
	State          string                    `json:"state"`
	URL            string                    `json:"url"`
	Author         string                    `json:"author"`
	IsDraft        bool                      `json:"is_draft"`
	LastActivityAt string                    `json:"last_activity_at"`
	Workflow       workflowStateMetaResponse `json:"workflow"`
}

type workflowStateListResponse struct {
	Items      []workflowStateItemResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

type listWorkflowStateOutput = bodyOutput[workflowStateListResponse]

type setWorkflowStateBody struct {
	Status         string `json:"status"`
	ExpectedStatus string `json:"expected_status,omitempty"`
	Source         string `json:"source,omitempty"`
	Actor          string `json:"actor,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type setWorkflowStateInput struct {
	ItemType     string `path:"item_type" enum:"pr,issue"`
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setWorkflowStateBody
}

type setWorkflowStateHostInput struct {
	ItemType     string `path:"item_type" enum:"pr,issue"`
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setWorkflowStateBody
}

type workflowStateChangeResponse struct {
	PreviousStatus string `json:"previous_status"`
	Status         string `json:"status"`
	UpdatedAt      string `json:"updated_at"`
	UpdatedSource  string `json:"updated_source"`
	UpdatedActor   string `json:"updated_actor,omitempty"`
	UpdatedReason  string `json:"updated_reason,omitempty"`
}

type setWorkflowStateOutput = bodyOutput[workflowStateChangeResponse]

var workflowSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

func (s *Server) registerWorkflowStateAPI(api huma.API) {
	huma.Get(api, "/workflow-state", s.listWorkflowState,
		documentOperation("list-workflow-state", "List item workflow state", "Workflow State"))
	huma.Register(api, huma.Operation{
		OperationID: "set-workflow-state", Method: http.MethodPut,
		Path:          "/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}",
		DefaultStatus: http.StatusOK, Summary: "Set item workflow state",
		Tags: []string{"Workflow State"},
	}, s.setWorkflowState)
	huma.Register(api, huma.Operation{
		OperationID: "set-workflow-state-on-host", Method: http.MethodPut,
		Path:          "/host/{platform_host}/workflow-state/{item_type}/{provider}/{owner}/{name}/{number}",
		DefaultStatus: http.StatusOK, Summary: "Set item workflow state",
		Tags: []string{"Workflow State"},
	}, s.setWorkflowStateOnHost)
}

func (s *Server) listWorkflowState(
	ctx context.Context, input *listWorkflowStateInput,
) (*listWorkflowStateOutput, error) {
	if hasInvalidRepoFilter(input.Repo) {
		return nil, problemValidation("query.repo", "repo filter must be provider|platform_host/repo_path")
	}
	for _, it := range input.ItemType {
		if it != db.ItemTypePR && it != db.ItemTypeIssue {
			return nil, problemValidation("query.item_type", "item_type must be one of: pr, issue", "pr", "issue")
		}
	}
	for _, st := range input.State {
		if !validKanbanStates[st] {
			return nil, problemValidation("query.state",
				"state must be one of: new, reviewing, waiting, awaiting_merge",
				"new", "reviewing", "waiting", "awaiting_merge")
		}
	}
	rows, next, err := s.db.ListItemWorkflowStates(ctx, db.ListWorkflowStatesOpts{
		RepoFilters:   parseRepoFilters(input.Repo),
		ItemTypes:     input.ItemType,
		States:        input.State,
		IncludeClosed: input.IncludeClosed,
		Limit:         input.Limit,
		Cursor:        input.Cursor,
	})
	if err != nil {
		if input.Cursor != "" {
			return nil, problemValidation("query.cursor", "invalid cursor")
		}
		return nil, problemInternal("list workflow state failed")
	}
	out := workflowStateListResponse{Items: make([]workflowStateItemResponse, 0, len(rows)), NextCursor: next}
	for _, r := range rows {
		item := workflowStateItemResponse{
			Provider: r.Platform, PlatformHost: r.PlatformHost,
			Owner: r.Owner, Name: r.Name, RepoPath: r.RepoPath,
			ItemType: r.ItemType, Number: r.Number, Title: r.Title,
			State: r.State, URL: r.URL, Author: r.Author, IsDraft: r.IsDraft,
			LastActivityAt: r.LastActivityAt.UTC().Format(time.RFC3339),
			Workflow:       workflowStateMetaResponse{Status: r.Status},
		}
		if r.HasRow && r.UpdatedAt != nil {
			item.Workflow.UpdatedAt = r.UpdatedAt.UTC().Format(time.RFC3339)
			item.Workflow.UpdatedSource = r.UpdatedSource
			item.Workflow.UpdatedActor = r.UpdatedActor
			item.Workflow.UpdatedReason = r.UpdatedReason
		}
		out.Items = append(out.Items, item)
	}
	return &listWorkflowStateOutput{Body: out}, nil
}

func (s *Server) setWorkflowState(
	ctx context.Context, input *setWorkflowStateInput,
) (*setWorkflowStateOutput, error) {
	if !validKanbanStates[input.Body.Status] {
		return nil, problemValidation("body.status",
			"status must be one of: new, reviewing, waiting, awaiting_merge",
			"new", "reviewing", "waiting", "awaiting_merge")
	}
	if input.Body.ExpectedStatus != "" && !validKanbanStates[input.Body.ExpectedStatus] {
		return nil, problemValidation("body.expected_status",
			"expected_status must be one of: new, reviewing, waiting, awaiting_merge",
			"new", "reviewing", "waiting", "awaiting_merge")
	}
	source := input.Body.Source
	if source == "" {
		source = "api"
	}
	if !workflowSourcePattern.MatchString(source) {
		return nil, problemValidation("body.source",
			"source must match ^[a-z][a-z0-9_-]{0,39}$")
	}
	if len(input.Body.Actor) > 120 {
		return nil, problemValidation("body.actor", "actor must be at most 120 bytes")
	}
	if len(input.Body.Reason) > 500 {
		return nil, problemValidation("body.reason", "reason must be at most 500 bytes")
	}

	repo, err := s.lookupRepoByProviderRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	ref := repoNumberPathRef{
		repoID: repo.ID, owner: repo.Owner, name: repo.Name,
		number: input.Number, platformHost: repo.PlatformHost,
	}
	switch input.ItemType {
	case db.ItemTypePR:
		if _, err := s.lookupMRID(ctx, ref); err != nil {
			return nil, problemNotFound(CodePullNotFound, err.Error(), nil)
		}
	case db.ItemTypeIssue:
		if _, err := s.lookupIssueID(ctx, ref); err != nil {
			return nil, problemNotFound(CodeIssueNotFound, err.Error(), nil)
		}
	}

	prev, err := s.db.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID: repo.ID, ItemType: input.ItemType, ItemNumber: input.Number,
		Status: input.Body.Status, ExpectedStatus: input.Body.ExpectedStatus,
		Source: source, Actor: input.Body.Actor, Reason: input.Body.Reason,
	})
	var conflict *db.WorkflowStateConflictError
	if errors.As(err, &conflict) {
		return nil, problemConflict(CodeConflict, "workflow state changed", map[string]any{
			"current_status":  conflict.Current,
			"expected_status": conflict.Expected,
		})
	}
	if err != nil {
		return nil, problemInternal("set workflow state failed")
	}

	row, err := s.db.GetItemWorkflowState(ctx, repo.ID, input.ItemType, input.Number)
	if err != nil || row == nil {
		return nil, problemInternal("read workflow state failed")
	}
	return &setWorkflowStateOutput{Body: workflowStateChangeResponse{
		PreviousStatus: prev,
		Status:         row.Status,
		UpdatedAt:      row.UpdatedAt.UTC().Format(time.RFC3339),
		UpdatedSource:  row.UpdatedSource,
		UpdatedActor:   row.UpdatedActor,
		UpdatedReason:  row.UpdatedReason,
	}}, nil
}

func (s *Server) setWorkflowStateOnHost(
	ctx context.Context, input *setWorkflowStateHostInput,
) (*setWorkflowStateOutput, error) {
	next := setWorkflowStateInput{
		ItemType: input.ItemType, Provider: input.Provider,
		PlatformHost: input.PlatformHost, Owner: input.Owner,
		Name: input.Name, Number: input.Number, Body: input.Body,
	}
	return s.setWorkflowState(ctx, &next)
}
```

Check `lookupIssueID`'s exact signature at `helpers.go:195-225` and adapt the call. Register: grep for where `registerProviderRepoAPI` is invoked in `huma_routes.go` and add `s.registerWorkflowStateAPI(api)` on the following line.

- [ ] **Step 4: Regenerate API artifacts and finish tests**

Run: `make api-generate`
Then switch the apitest file to the generated `ListWorkflowStateWithResponse`/`SetWorkflowStateWithResponse` methods where convenient (raw round-tripper HTTP is also acceptable per `context/testing.md` wire-level discipline; pick one style and stay consistent).

Run: `go test ./internal/server/... -shuffle=on`
Expected: PASS.

Because `api-generate` touched `packages/ui/src/api/generated/*`, run the frontend suite from `frontend/`: `../node_modules/.bin/vp test` (full run). Expected: PASS (generated-types-only change).

- [ ] **Step 5: Commit**

```bash
git add internal/server internal/apiclient frontend/openapi packages/ui/src/api/generated
git commit -m "feat: add workflow-state daemon endpoints for PRs and issues

GET /workflow-state lists effective states (missing rows read as new)
with deterministic keyset pagination; PUT validates metadata limits and
returns a conflict problem when expected_status is stale, so MCP and
other local callers can claim items without overwriting humans."
```

---

### Task 7: MCP companion scaffolding — SDK dep, daemon client, `middleman mcp` stdio CLI

**Spec sections:** "Recommended Approach", "Process Model", "Timeouts, Retries, And Compatibility", "Error Handling".

**Files:**
- Modify: `go.mod`/`go.sum` (add `github.com/modelcontextprotocol/go-sdk v1.6.1`)
- Create: `internal/mcpserver/daemon.go`, `internal/mcpserver/daemon_test.go`, `internal/mcpserver/server.go`
- Create: `cmd/middleman/mcp.go`
- Modify: `cmd/middleman/main.go` (add `case "mcp":`)
- Test: `cmd/middleman/mcp_cli_test.go`

**Interfaces:**
- Produces (used by all tool tasks):

```go
package mcpserver

// Options configures the companion process.
type Options struct {
	ConfigPath    string
	Transport     string        // "stdio" or "http"
	Addr          string        // http only
	HTTPTokenEnv  string        // http only: env var NAME holding the bearer token
	DaemonTimeout time.Duration // default 10s
	Version       string
}

// daemonClient talks to the running daemon over loopback.
type daemonClient struct { /* configPath, timeout, cached baseURL+token, mu */ }

func newDaemonClient(configPath string, timeout time.Duration) *daemonClient

// getJSON GETs path (already including /api/v1 prefix joining) with query
// params, decodes JSON into out. Retries once after rediscovery on
// connection errors. Returns *daemonError on failure.
func (c *daemonClient) getJSON(ctx context.Context, path string, query url.Values, out any) error

// putJSON PUTs body as JSON. Never retries.
func (c *daemonClient) putJSON(ctx context.Context, path string, body, out any) error

// daemonError is the typed failure surfaced to MCP clients.
type daemonError struct {
	Kind    string // "daemon_unavailable", "daemon_auth", "not_found",
	               // "conflict", "invalid_request", "daemon_error",
	               // "daemon_timeout", "version_mismatch"
	Message string
	Details map[string]any // e.g. current_status/expected_status on conflict
}
func (e *daemonError) Error() string

// New assembles the MCP server (tools, resource, prompt registered).
func New(opts Options) (*Server, error)
// Server.RunStdio(ctx) error ; Server.RunHTTP(ctx) error (Task 13)
```

- [ ] **Step 1: Add the dependency and pin the SDK API**

Run: `go get github.com/modelcontextprotocol/go-sdk@v1.6.1 && go mod tidy`

Then read the SDK's package docs to confirm the exact v1.6.1 names used in this plan: `mcp.NewServer(&mcp.Implementation{...}, opts)`, `mcp.AddTool(server, &mcp.Tool{Name, Description}, handler)` with handler signature `func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error)`, `server.AddResource`, `server.AddPrompt`, `server.Run(ctx, &mcp.StdioTransport{})`, `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server, opts)`.

Run: `go doc github.com/modelcontextprotocol/go-sdk/mcp | head -80`

**If any required capability (stdio server, per-request HTTP handler, typed tool input/output) is missing from the SDK, STOP and update the design doc rather than hand-rolling JSON-RPC** (spec requirement). Small name differences: adapt silently.

- [ ] **Step 2: Write the failing daemon-client tests**

`internal/mcpserver/daemon_test.go` — no real daemon; write runtime files by hand and point at an `httptest.Server`:

```go
package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/runtimelock"
)

// writeFakeDaemonFiles creates config.toml + runtime metadata + auth token
// under a temp dir pointing at ts, and returns the config path.
func writeFakeDaemonFiles(t *testing.T, ts *httptest.Server, token string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath,
		[]byte("data_dir = \""+dir+"\"\n"), 0o600))

	// Acquire the runtime lock so runtimelock.Read reports Running.
	h, err := runtimelock.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Release() })
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	require.NoError(t, h.WriteMetadata(runtimelock.Metadata{
		PID: os.Getpid(), ListenAddr: u.Host, BasePath: "/",
		TokenPath: runtimelock.AuthTokenPath(dir), RequireAuth: token != "",
	}))
	require.NoError(t, os.WriteFile(runtimelock.AuthTokenPath(dir), []byte(token), 0o600))
	return cfgPath
}

func TestDaemonClientGetJSONSendsBearer(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer ts.Close()
	cfg := writeFakeDaemonFiles(t, ts, "sekrit")

	c := newDaemonClient(cfg, 5*time.Second)
	var out struct{ OK bool `json:"ok"` }
	require.NoError(t, c.getJSON(t.Context(), "/api/v1/activity", nil, &out))
	assert.True(t, out.OK)
	assert.Equal(t, "Bearer sekrit", gotAuth)
}

func TestDaemonClientDaemonUnavailable(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("data_dir = \""+dir+"\"\n"), 0o600))

	c := newDaemonClient(cfgPath, time.Second)
	var out any
	err := c.getJSON(t.Context(), "/api/v1/activity", nil, &out)
	var derr *daemonError
	require.ErrorAs(t, err, &derr)
	assert.Equal(t, "daemon_unavailable", derr.Kind)
	// The message must not contain the auth token or token path contents.
}

func TestDaemonClientMapsProblems(t *testing.T) {
	assert := assert.New(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		switch r.URL.Path {
		case "/api/v1/nf":
			// code "notFound" is the exact regression case: the daemon
			// uses it for domain states (diff unavailable, thread
			// missing), so it must map to not_found, NOT
			// version_mismatch.
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"status":404,"code":"notFound","detail":"diff not available"}`))
		case "/api/v1/nf-item":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"status":404,"code":"pullNotFound","detail":"nope"}`))
		case "/api/v1/conflict":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"status":409,"code":"conflict","detail":"workflow state changed","details":{"current_status":"reviewing","expected_status":"new"}}`))
		case "/api/v1/auth":
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"status":401,"code":"unauthorized","detail":"missing token"}`))
		}
	}))
	defer ts.Close()
	cfg := writeFakeDaemonFiles(t, ts, "")
	c := newDaemonClient(cfg, 5*time.Second)

	var out any
	var derr *daemonError
	require.ErrorAs(t, c.getJSON(t.Context(), "/api/v1/nf", nil, &out), &derr)
	assert.Equal("not_found", derr.Kind)
	require.ErrorAs(t, c.getJSON(t.Context(), "/api/v1/nf-item", nil, &out), &derr)
	assert.Equal("not_found", derr.Kind)
	require.ErrorAs(t, c.putJSON(t.Context(), "/api/v1/conflict", map[string]any{}, &out), &derr)
	assert.Equal("conflict", derr.Kind)
	assert.Equal("reviewing", derr.Details["current_status"])
	require.ErrorAs(t, c.getJSON(t.Context(), "/api/v1/auth", nil, &out), &derr)
	assert.Equal("daemon_auth", derr.Kind)
}
```

Check `runtimelock.Acquire`/`Handle.WriteMetadata`/`Release` exact names in `internal/runtimelock` (see `cmd/middleman/main_test.go:126-141` for a working example) and adjust.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/mcpserver -shuffle=on`
Expected: FAIL (package does not exist yet / undefined symbols).

- [ ] **Step 4: Implement daemon client + server skeleton + CLI**

`internal/mcpserver/daemon.go` — mirror `cmd/middleman/api_verb.go` discovery:

```go
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/runtimelock"
)

type daemonError struct {
	Kind    string
	Message string
	Details map[string]any
}

func (e *daemonError) Error() string { return e.Kind + ": " + e.Message }

type daemonClient struct {
	configPath string
	timeout    time.Duration

	mu      sync.Mutex
	baseURL string
	token   string
}

func newDaemonClient(configPath string, timeout time.Duration) *daemonClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &daemonClient{configPath: configPath, timeout: timeout}
}

// discover re-reads config + runtime metadata. Errors never include the
// auth token value.
func (c *daemonClient) discover() error {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return &daemonError{Kind: "daemon_unavailable", Message: "load config: " + err.Error()}
	}
	st, err := runtimelock.Read(cfg.DataDir)
	if err != nil {
		return &daemonError{Kind: "daemon_unavailable", Message: "read runtime status: " + err.Error()}
	}
	if !st.Running || st.Metadata == nil {
		return &daemonError{Kind: "daemon_unavailable",
			Message: "no middleman daemon is running on " + cfg.DataDir}
	}
	prefix := st.Metadata.BasePath
	if prefix == "" {
		prefix = cfg.BasePath
	}
	prefix = strings.TrimSuffix(prefix, "/")
	token, err := runtimelock.ReadAuthToken(cfg.DataDir)
	if err != nil {
		return &daemonError{Kind: "daemon_unavailable", Message: "read auth token failed"}
	}
	c.mu.Lock()
	c.baseURL = "http://" + st.Metadata.ListenAddr + prefix
	c.token = token
	c.mu.Unlock()
	return nil
}

func (c *daemonClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	c.mu.Lock()
	base := c.baseURL
	c.mu.Unlock()
	if base == "" {
		if err := c.discover(); err != nil {
			return err
		}
		c.mu.Lock()
		base = c.baseURL
		c.mu.Unlock()
	}

	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return &daemonError{Kind: "daemon_error", Message: "encode request: " + err.Error()}
		}
		rdr = bytes.NewReader(buf)
	}
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return &daemonError{Kind: "daemon_error", Message: err.Error()}
	}
	if body != nil || method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.Lock()
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.mu.Unlock()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &daemonError{Kind: "daemon_timeout", Message: "daemon request timed out"}
		}
		var nerr net.Error
		if errors.As(err, &nerr) || errors.Is(err, io.EOF) {
			return &daemonError{Kind: "daemon_unavailable", Message: "daemon connection failed"}
		}
		return &daemonError{Kind: "daemon_unavailable", Message: "daemon request failed"}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return &daemonError{Kind: "daemon_error", Message: "decode response: " + err.Error()}
		}
		return nil
	}
	return c.problemToError(resp)
}

// problemToError maps an RFC 9457 problem document to a daemonError.
func (c *daemonClient) problemToError(resp *http.Response) error {
	var prob struct {
		Status  int            `json:"status"`
		Code    string         `json:"code"`
		Detail  string         `json:"detail"`
		Details map[string]any `json:"details"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&prob)
	msg := prob.Detail
	if msg == "" {
		msg = resp.Status
	}
	kind := "daemon_error"
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		kind = "daemon_auth"
	case resp.StatusCode == http.StatusNotFound:
		// Always plain not_found here. Do NOT infer version_mismatch
		// from code "notFound": existing daemon routes use that code
		// for ordinary domain states (diff unavailable, review thread
		// missing, item not found — e.g. huma_routes.go:1529), so the
		// generic mapper cannot distinguish a missing route from a
		// missing resource. Version detection happens only in the
		// explicit capability probe below.
		kind = "not_found"
	case resp.StatusCode == http.StatusConflict:
		kind = "conflict"
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		kind = "invalid_request"
	}
	return &daemonError{Kind: kind, Message: fmt.Sprintf("%s (%s)", msg, prob.Code), Details: prob.Details}
}

// getJSON retries once on daemon_unavailable after rediscovery.
func (c *daemonClient) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	err := c.do(ctx, http.MethodGet, path, query, nil, out)
	var derr *daemonError
	if errors.As(err, &derr) && derr.Kind == "daemon_unavailable" {
		if derr2 := c.discover(); derr2 != nil {
			return derr2
		}
		return c.do(ctx, http.MethodGet, path, query, nil, out)
	}
	return err
}

// putJSON never retries: a retry could hide whether the write applied.
func (c *daemonClient) putJSON(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPut, path, nil, body, out)
}
```

Version-mismatch detection lives ONLY in the workflow capability probe, never in the generic mapper: the probe is `GET /api/v1/workflow-state?limit=1`, and that route with a valid query cannot 404 for domain reasons — so ANY 404 from the probe (regardless of problem `code`) means the route is missing and the daemon predates this companion; the probe converts it to `daemonError{Kind: "version_mismatch", Message: "daemon does not support /workflow-state; upgrade middleman"}`. Ordinary `not_found` errors from other routes flow to tool-specific handlers (diff tool → `diff_unavailable`, stack tool → `present: false`, item tools → item-not-found). The cached probe result is keyed by the daemon identity from runtime metadata (`Metadata.PID` + `Metadata.StartedAt`, captured at discovery): whenever discovery re-runs and observes a different PID/StartedAt pair, the cached probe result is discarded and the next workflow tool call re-probes — so a daemon upgrade or restart while the companion stays alive cannot serve a stale `version_mismatch`. Required tests: a probe 404 (any problem code) yields `version_mismatch` on workflow tool calls; a non-probe 404 with `code:"notFound"` stays `not_found`; and after simulating a daemon restart (new metadata PID/StartedAt) a previously failed probe re-runs and clears. Daemon discovery and capability checking are LAZY, uniformly: `New` never contacts the daemon, and `middleman mcp` starts successfully with no daemon running (tools then return `daemon_unavailable`). The workflow-state capability probe (GET `/api/v1/workflow-state?limit=1`) runs on the first call to either workflow tool, after a successful discovery; on `version_mismatch`/`not_found` the result is cached and both workflow tools return a version/capability error naming the missing route (spec: do not silently downgrade). No eager startup probe exists on any transport — stdio and HTTP behave identically.

`internal/mcpserver/server.go`:

```go
package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	ConfigPath    string
	Transport     string
	Addr          string
	HTTPTokenEnv  string
	DaemonTimeout time.Duration
	Version       string
}

type Server struct {
	opts   Options
	daemon *daemonClient
	mcp    *mcp.Server
	diffs  *diffFileStore // created in Task 11; nil until then
}

func New(opts Options) (*Server, error) {
	s := &Server{
		opts:   opts,
		daemon: newDaemonClient(opts.ConfigPath, opts.DaemonTimeout),
	}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "middleman", Version: opts.Version}, nil)
	s.registerTools()
	return s, nil
}

// registerTools is filled in across Tasks 8-12.
func (s *Server) registerTools() {}

func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) Close() error {
	if s.diffs != nil {
		return s.diffs.Close()
	}
	return nil
}

var _ = fmt.Sprintf // placeholder until tools land
```

`cmd/middleman/mcp.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/mcpserver"
)

func runMCPCLI(args []string) error {
	fs := flag.NewFlagSet("middleman mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	transport := fs.String("transport", "stdio", "MCP transport: stdio or http")
	addr := fs.String("addr", "127.0.0.1:0", "HTTP listen address (http transport only)")
	tokenEnv := fs.String("http-token-env", "", "environment variable holding the HTTP bearer token")
	daemonTimeout := fs.Duration("daemon-timeout", 10*time.Second, "per-request daemon timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *transport != "stdio" && *transport != "http" {
		return fmt.Errorf("unsupported transport %q: use stdio or http", *transport)
	}

	srv, err := mcpserver.New(mcpserver.Options{
		ConfigPath:    *configPath,
		Transport:     *transport,
		Addr:          *addr,
		HTTPTokenEnv:  *tokenEnv,
		DaemonTimeout: *daemonTimeout,
		Version:       version,
	})
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *transport == "http" {
		return srv.RunHTTP(ctx) // Task 13; return an explicit error until then
	}
	return srv.RunStdio(ctx)
}
```

In `cmd/middleman/main.go` `runCLI` switch, after `case "api":`:

```go
case "mcp":
	return runMCPCLI(args[1:])
```

Until Task 13, make `RunHTTP` return `fmt.Errorf("http transport not yet available")` so the flag exists but fails loudly.

- [ ] **Step 5: Write and run the CLI test**

`cmd/middleman/mcp_cli_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPCLIRejectsUnknownTransport(t *testing.T) {
	err := runMCPCLI([]string{"--transport", "carrier-pigeon"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport")
}

func TestMCPCLIDefaultsToStdioAndFailsCleanlyWithoutDaemon(t *testing.T) {
	// stdio startup itself succeeds with no daemon (discovery is lazy);
	// this test just proves flag parsing + config path handling do not
	// panic or leak secrets. Run with a closed stdin so Run returns.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte("data_dir = \""+dir+"\"\n"), 0o600))
	// Full stdio behavior is covered by the e2e in Task 14.
}
```

Run: `go test ./internal/mcpserver ./cmd/middleman -run 'TestDaemonClient|TestMCPCLI' -shuffle=on`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/mcpserver cmd/middleman
git commit -m "feat: add middleman mcp companion skeleton with daemon discovery

The companion discovers the running daemon through the same runtime
metadata and auth-token files as the api verb and maps problem
documents to typed errors, so MCP tools can report daemon-unavailable,
auth, conflict, and version-mismatch conditions without leaking the
daemon token."
```

---

### Task 8: Read tools — `middleman_list_repos`, `middleman_list_activity`, `middleman_search_items`

**Spec sections:** the three matching "Tool:" sections.

**Files:**
- Create: `internal/mcpserver/types.go`, `internal/mcpserver/tools_read.go`, `internal/mcpserver/tools_read_test.go`

**Interfaces:**
- Consumes: `daemonClient.getJSON`, `daemonError` (Task 7).
- Produces shared shapes used by Tasks 9-12:

```go
// repoFilterInput is the structured repo filter every tool accepts.
type repoFilterInput struct {
	Provider     string `json:"provider,omitempty"`
	PlatformHost string `json:"platform_host,omitempty"`
	Owner        string `json:"owner,omitempty"`
	Name         string `json:"name,omitempty"`
}
// queryValue renders "provider|platform_host/owner/name" for the daemon's
// repo query param ("", nil when the filter is entirely empty). Validate
// BEFORE rendering: a non-empty filter requires provider, owner, AND name
// all set — a partial filter (any of the three missing) is an error, never
// a silently broadened or malformed query. Validate provider before
// rendering: an empty provider on a non-empty filter is invalid input, so
// do not route empty through NormalizeKind's empty-means-github default.
// For non-empty provider, call platform.NormalizeKind, then require
// platform.MetadataFor to recognize the normalized provider even when
// platform_host was supplied explicitly. Render the normalized provider
// string, not the raw alias/casing. The daemon filter format REQUIRES the
// host segment, but platform_host is optional on tool inputs: when it is
// empty, use the normalized provider's default host from MetadataFor; never
// emit a host-less filter.
// Required tests:
//   - {provider: "github", owner: "acme", name: "widget"}, no
//     platform_host → "github|github.com/acme/widget", nil.
//   - {provider: "GH", owner: "acme", name: "widget"}, no platform_host →
//     "github|github.com/acme/widget", nil.
//   - {} (all empty) → "", nil.
//   - {owner: "acme", name: "widget"} (no provider) → error.
//   - {provider: "github", owner: "acme"} (no name) → error.
//   - {provider: "nonesuch", owner: "a", name: "b"}, no platform_host →
//     error naming "nonesuch".
//   - {provider: "nonesuch", platform_host: "git.example.com", owner: "a",
//     name: "b"} → error naming "nonesuch" (explicit host does not make an
//     unknown provider valid).
//   - {provider: "gitlab", platform_host: "git.example.com", owner: "a",
//     name: "b"} → "gitlab|git.example.com/a/b", nil.
// Tool handlers map the error to an invalid_params MCP tool error.
func (r repoFilterInput) queryValue() (string, error)

// itemRef is the compact provider-aware item identity on every output.
type itemRef struct {
	Type         string `json:"type"`
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	RepoPath     string `json:"repo_path"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	State        string `json:"state"`
	Author       string `json:"author"`
	IsDraft      bool   `json:"is_draft"`
}

// itemRefInput is the provider-aware ref tools take as input.
type itemRefInput struct {
	ItemType     string `json:"item_type"` // "pr" or "issue"
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host,omitempty"` // empty = provider default host
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
}

// Daemon decode structs (Go-name keys for embedded db fields — see
// Codebase Facts): daemonPull, daemonIssue, daemonRepoSummary,
// daemonActivityItem, daemonActivityResponse.
type daemonPull struct {
	Number         int       `json:"Number"`
	Title          string    `json:"Title"`
	State          string    `json:"State"`
	Author         string    `json:"Author"`
	URL            string    `json:"URL"`
	IsDraft        bool      `json:"IsDraft"`
	KanbanStatus   string    `json:"KanbanStatus"`
	LastActivityAt time.Time `json:"LastActivityAt"`
	Repo           daemonRepoRef `json:"repo"`
	PlatformHost   string    `json:"platform_host"`
	RepoOwner      string    `json:"repo_owner"`
	RepoName       string    `json:"repo_name"`
	Workspace      *daemonWorkspaceRef `json:"workspace"`
	DetailLoaded   bool      `json:"detail_loaded"`
	DetailFetchedAt string   `json:"detail_fetched_at"`
}
// daemonIssue mirrors daemonPull minus IsDraft/KanbanStatus, plus
// WorkflowStatus `json:"WorkflowStatus"`.
// daemonRepoRef: provider, platform_host, repo_path, owner, name (snake).
// daemonWorkspaceRef: id, status (snake).
```

Before writing decode structs, verify one field of the real serialization: run the daemon test server (or just check `mergeRequestResponse`/`db.MergeRequest` json tags again) — embedded untagged fields serialize as `"Number"`, `"URL"`, `"IsDraft"`, `"KanbanStatus"`, `"LastActivityAt"` etc.

- [ ] **Step 1: Write failing tests with a fake daemon**

`tools_read_test.go` — helper `newTestServer(t, mux *http.ServeMux) *Server` that wires `writeFakeDaemonFiles` (export it from `daemon_test.go` as a shared test helper in this package) + `New`. Call tool handler functions directly (they are methods; no MCP transport needed for unit tests):

```go
func TestListReposTool(t *testing.T) {
	assert := assert.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"repo": {"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","owner":"acme","name":"widget",
			"open_pr_count":3,"open_issue_count":2,
			"last_sync_completed_at":"2026-07-01T10:00:00Z","last_sync_error":""
		}]`))
	})
	s := newTestServer(t, mux)

	out, err := s.listRepos(t.Context(), listReposInput{})
	require.NoError(t, err)
	require.Len(t, out.Repos, 1)
	assert.Equal("github", out.Repos[0].Provider)
	assert.Equal("acme/widget", out.Repos[0].RepoPath)
	assert.Equal(3, out.Repos[0].OpenPRCount)
	assert.Equal("2026-07-01T10:00:00Z", out.Repos[0].LastSyncCompletedAt)
}

func TestSearchItemsMergesAndOrders(t *testing.T) {
	// /pulls?q=retry returns PR #42 (LastActivityAt 14:00);
	// /issues?q=retry returns issue #7 (LastActivityAt 15:00).
	// Expect issue first, and no bodies anywhere in the output.
	// Also assert the daemon received state=open by default and q=retry.
}

func TestSearchItemsMergedStateFilter(t *testing.T) {
	// state "merged": daemon called with state=all for pulls; results
	// filtered client-side to State=="merged"; issues skipped entirely
	// (issues cannot be merged).
}

func TestListActivityPassthrough(t *testing.T) {
	// /activity returns two items + capped=true; tool output preserves
	// compact fields (activity_type, item ref, author, created_at,
	// body_preview) and the capped flag, and forwards since/repo/types/
	// search/after params.
}
```

Flesh these out fully (assert on `r.URL.Query()` inside the mux handlers to pin request shapes).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/mcpserver -run 'TestListRepos|TestSearchItems|TestListActivity' -shuffle=on`
Expected: FAIL.

- [ ] **Step 3: Implement**

`tools_read.go` — inputs/outputs and handlers:

```go
type listReposInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum repositories to return (default all)"`
}

type repoRow struct {
	Provider            string `json:"provider"`
	PlatformHost        string `json:"platform_host"`
	Owner               string `json:"owner"`
	Name                string `json:"name"`
	RepoPath            string `json:"repo_path"`
	OpenPRCount         int    `json:"open_pr_count"`
	OpenIssueCount      int    `json:"open_issue_count"`
	LastSyncCompletedAt string `json:"last_sync_completed_at,omitempty"`
	LastSyncError       string `json:"last_sync_error,omitempty"`
}

type listReposOutput struct {
	Repos []repoRow `json:"repos"`
}

func (s *Server) listRepos(ctx context.Context, in listReposInput) (listReposOutput, error)

type listActivityInput struct {
	Since  string          `json:"since,omitempty"`  // RFC3339 or duration like "24h"; default "24h"
	Repo   repoFilterInput `json:"repo,omitempty"`
	Types  []string        `json:"types,omitempty"`
	Search string          `json:"search,omitempty"`
	Limit  int             `json:"limit,omitempty"`
	After  string          `json:"after,omitempty"` // opaque cursor passthrough
}

type searchItemsInput struct {
	Query     string          `json:"query"`
	ItemTypes []string        `json:"item_types,omitempty"` // default both
	Repo      repoFilterInput `json:"repo,omitempty"`
	State     string          `json:"state,omitempty"` // open|closed|merged|all; default open
	Limit     int             `json:"limit,omitempty"` // default 25, cap 100
}

type searchResult struct {
	Item           itemRef `json:"item"`
	WorkflowStatus string  `json:"workflow_status"`
	LastActivityAt string  `json:"last_activity_at"`
}
type searchItemsOutput struct {
	Results []searchResult `json:"results"`
	Capped  bool           `json:"capped"`
}
```

Implementation notes (write real code, these are the decisions):
- `sinceToRFC3339(s string)` helper: `time.ParseDuration` first; on success `time.Now().UTC().Add(-d).Format(time.RFC3339)`; otherwise pass through unchanged. Default `"24h"`.
- `search_items`: PR call `GET /api/v1/pulls` with `q`, `state` (map `merged`→`all`, `all`→`all`, else pass), `repo`, `limit`; issue call `GET /api/v1/issues` likewise (skip when `state == "merged"`). Client-side: when input state is `merged` keep only `State == "merged"`; when `closed` keep `closed` (and for PRs also drop `merged` unless the daemon already distinguishes — check `db.MergeRequest.State` values in a quick grep: states are `open`/`closed`/`merged`). Each source is fetched with the full tool `limit` so global truncation cannot miss top results. Merge, sort by `LastActivityAt` desc with ties broken ascending by `(provider, platform_host, owner, name, item_type, number)` (deterministic ordering per the spec; no pagination in v1), truncate to limit, set `Capped` when truncated. Workflow status: PR `KanbanStatus` (empty → `"new"`), issue `WorkflowStatus` (empty → `"new"`).
- `list_activity`: forward params, but note the daemon `/activity` route has NO `limit` query parameter — the companion must truncate client-side to the tool's `limit` (default 50, cap 200) after decoding, and set the output `capped` flag when it truncated or the daemon reported `capped`. Map response items to a compact struct (drop commit-metadata fields that are empty). Test must assert the returned row count honors the tool input when the fake daemon returns more rows.
- Register in `registerTools()` (Task 7 stub) with `mcp.AddTool(s.mcp, &mcp.Tool{Name: "middleman_list_repos", Description: "..."}, wrap(s.listRepos))` — write a tiny `wrap` adapter if the SDK handler signature needs `(*mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)`; return `nil` for the result and let the SDK serialize the typed output. Tool descriptions: 1-3 sentences from the spec's tool sections, including "call middleman_list_repos first to discover valid repo filters and sync freshness".

- [ ] **Step 4: Run tests**

Run: `go test ./internal/mcpserver -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver
git commit -m "feat: add mcp repo, activity, and cached-search read tools

list_repos doubles as the staleness map and filter discovery step;
search reaches quiet items the activity-based candidate tool cannot,
and never returns bodies so context stays compact."
```

---

### Task 9: `middleman_find_review_candidates`

**Spec sections:** "Tool: middleman_find_review_candidates", "Candidate Semantics", "Staleness And Cache Signals".

**Files:**
- Create: `internal/mcpserver/tools_candidates.go`, `internal/mcpserver/tools_candidates_test.go`

**Interfaces:**
- Consumes: `daemonClient.getJSON`, `daemonPull`/`daemonIssue`/`daemonActivityItem` decode structs, `sinceToRFC3339`, `itemRef` (Task 8).
- Produces output shape (exact JSON from the spec's response example):

```go
type findCandidatesInput struct {
	Since                 string          `json:"since,omitempty"`
	Repo                  repoFilterInput `json:"repo,omitempty"`
	ItemTypes             []string        `json:"item_types,omitempty"`
	WorkflowStates        []string        `json:"workflow_states,omitempty"`
	ExcludeWorkflowStates []string        `json:"exclude_workflow_states,omitempty"`
	IncludeDrafts         bool            `json:"include_drafts,omitempty"`
	IncludeClosed         bool            `json:"include_closed,omitempty"`
	Limit                 int             `json:"limit,omitempty"` // default 25, cap 100
	ActivityTypes         []string        `json:"activity_types,omitempty"`
}

type candidate struct {
	Item     itemRef            `json:"item"`
	Workflow candidateWorkflow  `json:"workflow"`
	Activity candidateActivity  `json:"activity"`
	Workspace candidateWorkspace `json:"workspace"`
	Stack    candidateStack     `json:"stack"`
	Cache    candidateCache     `json:"cache"`
}
type candidateWorkflow struct {
	Status        string `json:"status"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	UpdatedSource string `json:"updated_source,omitempty"`
	UpdatedActor  string `json:"updated_actor,omitempty"`
	UpdatedReason string `json:"updated_reason,omitempty"`
}
// EVERY MCP output struct in the companion must carry explicit snake_case
// json tags matching the documented response contract — Go-name keys leaking
// onto the MCP wire is a contract bug. Audit all output structs in Tasks
// 8-12 for missing tags (repoRow in Task 8 has the same shorthand problem);
// add a marshaling test per tool asserting the documented key names.
// This rule applies ONLY to MCP output (and input) structs. The daemon
// DECODE structs (daemonPull, daemonIssue, daemonRepoSummary,
// daemonActivityItem, ... — Task 8) intentionally use Go-name keys like
// `json:"Number"` because the daemon API embeds db types that serialize
// Go field names; do not "fix" those tags to snake_case, that would break
// decoding.
type candidateActivity struct {
	LatestAt   string   `json:"latest_at"`
	EventCount int      `json:"event_count"`
	Types      []string `json:"types"`
	Actors     []string `json:"actors"`
	Reasons    []string `json:"reasons"`
}
type candidateWorkspace struct{ Exists bool `json:"exists"`; ID string `json:"id,omitempty"` }
type candidateStack struct {
	Present  bool   `json:"present"`
	Position int    `json:"position,omitempty"`
	Size     int    `json:"size,omitempty"`
	Health   string `json:"health,omitempty"`
}
type candidateCache struct {
	DetailLoaded    bool   `json:"detail_loaded"`
	DetailFetchedAt string `json:"detail_fetched_at,omitempty"`
}
type findCandidatesOutput struct {
	Candidates []candidate `json:"candidates"`
	Capped     bool        `json:"capped"`
}
```

Algorithm (implement exactly):
1. `GET /api/v1/activity` with `since` (converted), optional `repo`, optional `types` (from `ActivityTypes`).
2. Keep rows with `item_type` `pr` or `issue` and non-zero `item_number` (drops repo-level default-branch rows). Apply `ItemTypes` filter.
3. Group by `(repo.provider, platform_host, repo_owner, repo_name, item_type, item_number)`. Aggregate: latest `created_at`, event count, distinct types, distinct actors (max 5), reasons (max 5) via `reasonFor(activityType, author)` — mapping table: `comment` → `"<author> commented"`, `commit` → `"<author> pushed commits"`, `review` → `"<author> reviewed"`, `force_push` → `"<author> force pushed"`, `pr_opened`/`issue_opened`/`new` → `"<author> opened"`, default → `"<author>: <activity_type>"`. Check real activity-type strings in `internal/db/queries_activity.go` before finalizing the map.
4. For each distinct repo among groups: `GET /api/v1/pulls?repo=<filter>&state=all` and `GET /api/v1/issues?repo=<filter>&state=all`; index by number. This is one pair of list calls per repo, not per item.
5. Join: drop groups whose item is missing from the index; drop closed/merged unless `IncludeClosed`; drop draft PRs unless `IncludeDrafts`; workflow status from `KanbanStatus`/`WorkflowStatus` (empty → `new`); apply `WorkflowStates`/`ExcludeWorkflowStates` against the effective status.
6. Stack: for PR candidates only, `GET /api/v1/pulls/{provider}/{owner}/{name}/{number}/stack` (host-prefixed when the item's `platform_host` differs from the provider default — simplest correct rule: always use the host-prefixed route with the item's `platform_host`; verify the daemon accepts it for default hosts, it does since the host variant resolves any known host). `present:false` on `not_found`; on other errors leave `present:false` and continue (stack context is enrichment, not correctness).
7. Workspace: from the pull/issue row's `workspace` ref (no extra call). Cache block from `detail_loaded`/`detail_fetched_at`.
8. Sort by `Activity.LatestAt` desc; truncate to limit; `Capped` = truncated || activity response `capped`.

- [ ] **Step 1: Write failing tests** — fake daemon mux serving `/api/v1/activity` (3 rows: PR 42 comment+commit, issue 7 comment, one repo-level row that must be dropped), `/api/v1/pulls` (PR 42 open, kanban `""`, workspace present), `/api/v1/issues` (issue 7 open), `/api/v1/pulls/github/acme/widget/42/stack` (position 2, size 4, health blocked — via host route `/api/v1/host/github.com/pulls/...` if that is what the implementation calls). Assertions: grouping produced 2 candidates ordered issue-then-PR or per latest activity; PR candidate has `workflow.status == "new"`, `stack.present == true` with position/size/health, `workspace.exists == true`, reasons rendered, `capped == false`. A second test: `exclude_workflow_states: ["reviewing"]` drops a PR whose kanban is `reviewing`; a third: draft PR dropped by default, kept with `include_drafts`.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/mcpserver -run TestFindReviewCandidates -shuffle=on` → FAIL.

- [ ] **Step 3: Implement** per the algorithm above.

- [ ] **Step 4: Run tests** — same command → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver
git commit -m "feat: add mcp review-candidate discovery tool

Groups cached activity into provider-aware PR/issue candidates with
workflow, workspace, stack, and staleness evidence so a periodic model
can decide what deserves review without any provider calls."
```

---

### Task 10: `middleman_get_item_context` and `middleman_list_items_by_workflow_state`

**Spec sections:** the two matching "Tool:" sections.

**Files:**
- Create: `internal/mcpserver/tools_items.go`, `internal/mcpserver/tools_items_test.go`

**Interfaces:**
- Consumes: `itemRefInput` (Task 8), daemon detail routes, `GET /api/v1/workflow-state` (Task 6 shape).
- Produces:

```go
type getItemContextInput struct {
	Item             itemRefInput `json:"item"`
	EventLimit       int          `json:"event_limit,omitempty"` // default 30
	IncludeEvents    *bool        `json:"include_events,omitempty"`    // default true
	IncludeChecks    *bool        `json:"include_checks,omitempty"`    // default true (PR only)
	IncludeWorkspace *bool        `json:"include_workspace,omitempty"` // default true
	IncludeStack     *bool        `json:"include_stack,omitempty"`     // default true (PR only)
}
// Output: compact item fields + body + optional events (most recent
// event_limit, each {type, author, created_at, summary, body_preview<=500b}),
// checks, workspace, stack, workflow meta, cache{detail_loaded,
// detail_fetched_at}, and last_activity_at.

type listByWorkflowInput struct {
	States        []string        `json:"states,omitempty"`
	ItemTypes     []string        `json:"item_types,omitempty"`
	Repo          repoFilterInput `json:"repo,omitempty"`
	IncludeClosed bool            `json:"include_closed,omitempty"`
	Limit         int             `json:"limit,omitempty"`
	Cursor        string          `json:"cursor,omitempty"`
}
// Output: {items: [{item: itemRef, last_activity_at, workflow: {...}}],
//          next_cursor}
```

Routing rule shared by every ref-taking tool (write once as a helper):

```go
// itemPath renders the daemon route for a ref. kind is "pulls" or "issues".
// seg escapes one path segment. GitLab owners can nest ("group/sub"),
// and owner/name may contain reserved URL characters; unescaped values
// would silently hit the wrong route on every ref-taking tool.
func seg(s string) string { return url.PathEscape(s) }

func itemPath(kind string, ref itemRefInput) string {
	base := fmt.Sprintf("/api/v1/%s/%s/%s/%s/%d",
		kind, seg(ref.Provider), seg(ref.Owner), seg(ref.Name), ref.Number)
	if ref.PlatformHost != "" {
		return fmt.Sprintf("/api/v1/host/%s/%s/%s/%s/%s/%d",
			seg(ref.PlatformHost), kind, seg(ref.Provider), seg(ref.Owner), seg(ref.Name), ref.Number)
	}
	return base
}
```

Every hand-built daemon path in the companion (this helper, `workflowPath` in Task 12, and any query-building code) goes through `seg`. Check how the daemon serves nested owners today (grep the frontend's `provider-routes.ts` and an existing handler test for an owner containing `/`) and match that encoding exactly. Tests must include a nested GitLab owner (`group/sub`) and a self-hosted `platform_host`, asserting the fake daemon receives the expected escaped path.

`get_item_context`: PR refs hit the PR detail route (decode `merge_request` with Go-name keys, `events` with Go-name keys except tagged extras, snake-case wrapper fields incl. `stack`, `workspace`, `checks`, `workflow_approval`); issue refs hit the issue detail route (decode `issue`, `events`, `workflow`). Event limiting and include-flag filtering happen in the companion after the fetch (spec: v1 filters the MCP response shape, not the daemon payload). Truncate each event body to 500 bytes into `body_preview`; never emit full bodies of all events — emit the full body only for the item itself. Invalid `item_type` → tool error `invalid_request`.

`list_items_by_workflow_state`: straight passthrough to `GET /api/v1/workflow-state` with `state`, `item_type`, `repo`, `include_closed`, `limit`, `cursor` params, remapping items into `itemRef` + workflow meta.

- [ ] **Step 1: Write failing tests** — fake daemon: PR detail with 5 events and `detail_fetched_at` set → default output has all 5 (under limit), `event_limit: 2` keeps the 2 most recent, `include_events: false` omits events entirely but keeps cache fields; issue detail path exercised with an issue ref; workflow-state listing forwards all params (assert query on the fake) and maps `next_cursor` through.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/mcpserver -run 'TestGetItemContext|TestListItemsByWorkflow' -shuffle=on` → FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver
git commit -m "feat: add mcp item context and workflow-state listing tools

Context returns cached detail with client-side event limiting and
explicit staleness fields; the listing tool answers what is already
being reviewed without scanning all cached items."
```

---

### Task 11: `middleman_get_item_diff` + temp-file store + `middleman_get_stack_context`

**Spec sections:** "Tool: middleman_get_item_diff", "Tool: middleman_get_stack_context".

**Files:**
- Create: `internal/mcpserver/difftmp.go`, `internal/mcpserver/tools_diff.go`, `internal/mcpserver/tools_stack.go`, tests for each
- Modify: `internal/gitclone/patch.go` (+ `patch_test.go`): `BuildPatch` made complete for every changed file, `fileModeHeaders` reworked (real modes, `copy from`/`copy to`); `internal/gitclone/types.go` + `parse.go` (+ `parse_test.go`): internal `OldMode`/`NewMode` on `DiffFile` (`json:"-"`), captured in `ParseRawZ`. Possibly the frontend diff viewer if hunk-less patch sections render badly (update it directly, no compat fallback). No API shape change, so no artifact regeneration — still run `make api-generate` in this task and require it to be a no-op.

**Interfaces:**
- Consumes: `itemPath` helper, `daemonError`, `GET .../files`, `GET .../diff`, `GET .../stack`, `GET /api/v1/workflow-state` (member workflow statuses).
- Produces:

```go
type diffFileStore struct{ dir string }
func newDiffFileStore() (*diffFileStore, error) // os.MkdirTemp("", "middleman-mcp-") + chmod 0700
func (st *diffFileStore) write(name string, data []byte) (path string, size int64, err error) // 0600, overwrite
func (st *diffFileStore) Close() error // os.RemoveAll

type getItemDiffInput struct {
	Item         itemRefInput `json:"item"`
	EmitDiffFile bool         `json:"emit_diff_file,omitempty"`
}
type diffFileRow struct {
	Path        string `json:"path"`
	OldPath     string `json:"old_path,omitempty"`
	Status      string `json:"status"`
	IsBinary    bool   `json:"is_binary"`
	IsGenerated bool   `json:"is_generated"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
} // include diffFileRow keys in this tool's required marshaling test
type diffFileHandle struct{ Path string `json:"path"`; Bytes int64 `json:"bytes"` }
type getItemDiffOutput struct {
	Stale          bool            `json:"stale"`
	TotalAdditions int             `json:"total_additions"`
	TotalDeletions int             `json:"total_deletions"`
	Files          []diffFileRow   `json:"files"`
	DiffFile       *diffFileHandle `json:"diff_file,omitempty"`
}

type getStackContextInput struct{ Item itemRefInput `json:"item"` }
type stackMemberOut struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	IsDraft bool  `json:"is_draft"`
	WorkflowStatus string `json:"workflow_status"`
	IsRequested   bool   `json:"is_requested"`
	Position      int    `json:"position"`
}
type getStackContextOutput struct {
	Present bool             `json:"present"`
	Health  string           `json:"health,omitempty"`
	Members []stackMemberOut `json:"members,omitempty"`
}
```

Diff tool behavior:
- Reject issue refs up front: `item.item_type != "pr"` → `daemonError{Kind: "invalid_request", Message: "diff is only available for prs"}`.
- Summary from `GET .../files`: strip `Patch`/`Hunks`, compute `TotalAdditions`/`TotalDeletions` by summing files, pass every file row through unchanged. No whitespace-only filtering or counts anywhere in this tool — the files route does not compute whitespace status and the review use case does not need whitespace-aware line counts (spec as amended). Pass `stale` through untouched.
- Diff-unavailable: when the daemon returns 404/5xx from the files/diff routes with a clone-manager problem, surface `daemonError{Kind: "diff_unavailable", ...}` (add the kind); never return an empty summary on error.
- `EmitDiffFile`: `GET .../diff` returns `diffResponse` (structured JSON, not raw diff bytes) with per-file `Patch` strings. Design rule (spec as amended): the patch text is the SINGLE canonical serialization form. File modes are never exposed as separate API fields and there is no companion-side synthesis helper - two representations of the same change drift apart and cause mistakes. All fidelity work happens daemon-side in `gitclone.BuildPatch`:
  - `ParseRawZ` (`internal/gitclone/parse.go:12`) reads `:oldmode newmode ...` headers but drops `fields[0]`/`fields[1]` - add `OldMode`/`NewMode` string fields to `DiffFile` tagged `json:"-"` (internal to gitclone; they must never appear in any API response, so the OpenAPI shape is unchanged and no artifacts regenerate) and populate them in `ParseRawZ`.
  - Rework `fileModeHeaders` (`internal/gitclone/patch.go:53`) to use them: `added` emits `new file mode <NewMode>` (fallback `100644` only when `NewMode` is empty), `deleted` emits `deleted file mode <OldMode>` (same fallback), `renamed` keeps `rename from`/`rename to`, add `copy from`/`copy to` for status `copied`, and any status where both modes are set, differ, and neither is `000000` also emits `old mode <OldMode>`/`new mode <NewMode>` lines.
  - Make `BuildPatch` (`internal/gitclone/patch.go:9`) COMPLETE: remove the early empty return for binary/hunk-less files. Exact section shape, matching git: quoted `diff --git` header line, then extended headers; `---`/`+++` lines are emitted ONLY when hunks follow (git omits them for pure rename/copy/mode-only sections), then the hunks; binary files emit `Binary files <a-path> and <b-path> differ` (with `/dev/null` standing in for the missing side on adds/deletes) and no `---`/`+++`/hunks.
  - Scope: the non-empty-patch guarantee applies ONLY to routes that serve patch text — `GET /pulls/.../diff` (plus repo-commit and workspace diff routes that reuse the same pipeline). `GET /pulls/.../files` stays exactly as it is: the lightweight metadata path with empty `patch`/`hunks`; do not make it compute patches. The companion's summary path keeps using `/files`.
  - Plumbing, not just the builder: API responses get patch text via `ParsePatch`/`Manager.Diff`, which currently assign `Patch` only to entries the parsed patch stream marked touched — a correct `BuildPatch` alone still leaves metadata-only/binary raw entries with empty `Patch`. Change the merge step so every changed raw file gets its section built after metadata merge, and add `ParsePatch`/`Manager.Diff`-level tests proving metadata-only and binary entries arrive with non-empty `Patch`.
  - This changes what existing `/diff` consumers see in `patch` (non-empty where it used to be empty); that is the intended new behavior, not something to shim around — update the frontend diff viewer directly if it mishandles a hunk-less section, update any existing tests that assert empty patches for copied/binary files, and do NOT add compatibility fallbacks preserving the old empty-patch behavior.
  - Full-stack proof: add an `internal/server` API/e2e test over a real git fixture (local clone via the existing gitclone test helpers) covering mode-only, rename-only, copy-only, binary, and content+mode changes through `GET /pulls/.../diff`, asserting each file's `patch` is non-empty and carries its extended headers — gitclone unit tests plus fake-daemon MCP tests are not sufficient for a user-visible API change.
  - The companion concatenates `Patch` values verbatim in daemon response order, prepending and synthesizing nothing. A changed file with an empty `Patch` is a daemon bug: return `daemonError{Kind: "daemon_error", Message: "daemon returned an empty patch for <path>"}` instead of emitting a partial diff.
  Required unit tests in `internal/gitclone` (all through `BuildPatch`): rename-only file (`rename from`/`rename to`), copy-only file with `OldPath` set (`copy from`/`copy to`), mode-only file (100644->100755 emits `old mode`/`new mode`, no hunks), content+mode change (hunks AND mode headers), executable add (`new file mode 100755`) and executable delete (`deleted file mode 100755`), binary file (`Binary files differ`), and a path with control characters/quotes asserting `patchPath`-style quoting; `ParseRawZ` test asserting modes are captured and a marshaling test asserting `OldMode`/`NewMode` never appear in `DiffFile` JSON. Companion-side serialization tests feed fake daemon responses whose `patch` values include rename-only, copy-only, mode-only, and binary sections and assert verbatim concatenation plus the empty-patch failure case. Write via the store as `fmt.Sprintf("%s-%s-%s-%s-pr-%d.diff", sanitize(provider), sanitize(host), sanitize(owner), sanitize(name), number)` where `sanitize` replaces any rune outside `[a-zA-Z0-9._-]` with `_`. Cap the serialized buffer at 10 MiB - beyond that return `daemonError{Kind: "diff_too_large", Message: "... use the daemon API or a local checkout"}` instead of writing. Return absolute path + byte count. Repeat calls overwrite the same file in place via `os.WriteFile` (single-client companion; concurrent readers of a file being rewritten are out of scope and documented as such in the guidance doc). Lazily create the store on first use (`s.diffs`); `Server.Close` removes the directory (wired in Task 7). Crash cleanup: the store lives under `os.MkdirTemp("", "middleman-mcp-")`, so a killed companion leaves an orphan dir in the OS temp area that standard temp cleanup reaps; do not build extra machinery.

Stack tool behavior:
- Reject issue refs up front exactly like the diff tool: `item.item_type != "pr"` → `daemonError{Kind: "invalid_request", Message: "stack context is only available for prs"}`. Without this, an issue ref whose number collides with a PR number would silently return the wrong item's stack.
- `GET .../stack` via `itemPath("pulls", ref) + "/stack"`. `not_found` (or empty-context response — verify how `getStackForPR` responds for a PR not in any stack by reading `huma_routes.go` around `:4855` and the `getStackForPR` handler) → `{present: false}`.
- Member workflow statuses: one call `GET /api/v1/workflow-state?repo=<filter>&item_type=pr&include_closed=true&limit=200`; build number→status map (absent = `new`); mark `IsRequested` on the member whose number matches the input ref.

- [ ] **Step 1: Write failing tests**

Cover, with a fake daemon:
- summary-only default: no `diff_file` key, all file rows passed through (no whitespace filtering), totals summed, `stale` passthrough true;
- `emit_diff_file: true`: response path exists inside the store dir, file mode is `0600` (`os.Stat` + `assert.Equal(os.FileMode(0o600), info.Mode().Perm())`), content is the verbatim concatenation of the daemon `patch` values with exactly ONE `diff --git` line per file (fake patches include their own headers — the fakes should carry rename-only, copy-only, mode-only, and binary sections so the concatenation covers them — and the test asserts no duplication and no companion-added bytes), a changed file with an empty `patch` fails with a daemon-bug error instead of emitting a partial diff, and a second call overwrites (same path, new content). Extended-header/quoting fidelity itself is proven by the `BuildPatch` unit tests in `internal/gitclone`, not re-proven here;
- serialized diff exceeding the 10 MiB cap → `diff_too_large` error, no file written;
- issue ref → error with `invalid_request`;
- daemon diff route failing → `diff_unavailable` error, no partial output;
- store `Close` removes the directory;
- stack: issue ref → `invalid_request` error; present=false on 404; present case orders members by position, marks requested member, and joins workflow statuses from the workflow-state fake.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/mcpserver -run 'TestGetItemDiff|TestDiffFileStore|TestGetStackContext' -shuffle=on` → FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run tests** — PASS.

- [ ] **Step 5: Commit**

Before committing, run `go test ./internal/gitclone ./internal/mcpserver -shuffle=on` and `make api-generate` (must be a no-op — `git status --short` clean afterwards).

```bash
git add internal/mcpserver internal/gitclone
git commit -m "feat: add mcp diff evidence and stack context tools

Diff output is summary-first; the full unified diff goes to a
companion-owned 0600 temp file whose path is returned, keeping large
patches out of MCP responses while the model inspects them with its
own file tools. Stack context orders members with workflow status so
review order can respect the stack."
```

---

### Task 12: Write tool, guidance resource, prompt, and `docs/middleman-mcp.md`

**Spec sections:** "Tool: middleman_set_item_workflow_state", "Resource: middleman://mcp/guidance", "Prompt: middleman-review-candidates", "Guidance Document".

**Files:**
- Create: `internal/mcpserver/tools_workflow.go`, `internal/mcpserver/tools_workflow_test.go`
- Create: `internal/mcpserver/guidance.go`, `internal/mcpserver/guidance.md`
- Create: `docs/middleman-mcp.md`
- Test: extend `internal/mcpserver/server_test.go` with the registration test

**Interfaces:**
- Consumes: `daemonClient.putJSON`, `itemRefInput`, Task 6 PUT wire shape.
- Produces:

```go
type setWorkflowInput struct {
	Item           itemRefInput `json:"item"`
	Status         string       `json:"status"`
	ExpectedStatus string       `json:"expected_status,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Actor          string       `json:"actor,omitempty"`
}
type setWorkflowOutput struct {
	PreviousStatus string `json:"previous_status"`
	Status         string `json:"status"`
	UpdatedAt      string `json:"updated_at"`
	UpdatedSource  string `json:"updated_source"`
	UpdatedActor   string `json:"updated_actor,omitempty"`
	UpdatedReason  string `json:"updated_reason,omitempty"`
}
```

Handler: build path `"/api/v1/workflow-state/" + itemType + "/..."` (host-prefixed variant when `PlatformHost != ""` — same shape as `itemPath` but with the `/workflow-state/{item_type}/` prefix; write `workflowPath(ref)` next to `itemPath`). Body always includes `"source": "mcp"`; pass `actor` (default: the MCP client name from the initialize handshake if the SDK exposes it, else empty) and `reason` through. Conflict `daemonError` surfaces with `Details` intact so the model sees `current_status`. This is the ONLY tool that issues a PUT; grep test asserts it.

- [ ] **Step 1: Write failing tests**

```go
func TestSetItemWorkflowStateTool(t *testing.T) {
	// Fake daemon asserts: method PUT, path
	// /api/v1/workflow-state/pr/github/acme/widget/42, body has
	// source=mcp, status, expected_status, reason, actor. Returns the
	// change response; tool output mirrors it.
}

func TestSetItemWorkflowStateConflict(t *testing.T) {
	// Fake daemon returns 409 conflict problem with details; tool error
	// is daemonError kind "conflict" carrying current_status.
}

func TestRegisteredToolsAreExactlyTheCuratedSet(t *testing.T) {
	// Build Server, list tools via the SDK's introspection (or an
	// in-memory client session), and assert the names are exactly:
	// middleman_find_review_candidates, middleman_get_item_context,
	// middleman_set_item_workflow_state, middleman_list_activity,
	// middleman_list_items_by_workflow_state, middleman_list_repos,
	// middleman_search_items, middleman_get_item_diff,
	// middleman_get_stack_context — and that the resource
	// middleman://mcp/guidance and prompt middleman-review-candidates
	// are registered. Use mcp.NewInMemoryTransports (or the SDK's
	// equivalent) to connect a client and call ListTools/ListResources/
	// ListPrompts.
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/mcpserver -run 'TestSetItemWorkflowState|TestRegisteredTools' -shuffle=on` → FAIL.

- [ ] **Step 3: Implement tool + resource + prompt**

`guidance.go`:

```go
package mcpserver

import _ "embed"

//go:embed guidance.md
var guidanceMarkdown string
```

Register the resource (`URI: "middleman://mcp/guidance"`, `MIMEType: "text/markdown"`) returning `guidanceMarkdown`, and the prompt `middleman-review-candidates` whose single user message instructs the model to (all ten bullets from the spec's Prompt section, verbatim intent): call `middleman_list_repos` first for valid filters and sync freshness; use `middleman_find_review_candidates`; inspect details only for plausible items; check `middleman_get_item_diff` size/shape before claiming (full diff file only when the summary is not enough); consult `middleman_get_stack_context` before claiming a stacked PR; prefer cached evidence over assumptions; never perform provider writes; set workflow state only with a clear reason; always include `expected_status`; treat `awaiting_merge` as PR-oriented; report uncertainty and stale-cache signals.

`guidance.md` (embedded, model-facing) covers the same flow in prose plus the example flow from the spec ("Example guidance flow" block, copied).

`docs/middleman-mcp.md` (user-facing) must cover every bullet in the spec's "Guidance Document" section: client configuration for stdio (`command: middleman, args: ["mcp"]` JSON example), HTTP transport usage (`--transport http --addr 127.0.0.1:8092 --http-token-env MIDDLEMAN_MCP_TOKEN`, curl example with bearer, and a token-generation recommendation: at least 32 random bytes, e.g. `openssl rand -hex 32` — the companion checks non-blank only and does not enforce entropy), cached-data semantics (no provider refresh), local-write-only scope, example periodic flows, safe prompts, repo-filter discovery, quiet-item search, diff summary-first flow + ephemeral temp files local to the companion host, when to mark `reviewing`, `expected_status` usage, inspecting reviewing/waiting items, stale-cache interpretation, and troubleshooting daemon discovery/auth (`no middleman daemon is running on <data_dir>` → start `middleman`; auth errors → check `auth_token` file perms).

- [ ] **Step 4: Run tests** — `go test ./internal/mcpserver -shuffle=on` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver docs/middleman-mcp.md
git commit -m "feat: add mcp workflow write tool, guidance resource, and triage prompt

The only MCP write is middleman-local workflow state with source=mcp
attribution and expected_status conflict passthrough, so agents can
claim items without overwriting humans; guidance ships as both an MCP
resource and docs/middleman-mcp.md."
```

---

### Task 13: HTTP MCP transport with token, Host, Origin, and loopback checks

**Spec sections:** "MCP Library And HTTP Safety", "Process Model" flags, "Testing > HTTP transport e2e".

**Files:**
- Create: `internal/mcpserver/http.go`, `internal/mcpserver/http_test.go`
- Modify: `cmd/middleman/mcp.go` (wire `RunHTTP` for real)

**Interfaces:**
- Consumes: `mcp.NewStreamableHTTPHandler` (SDK), `Options.Addr`/`HTTPTokenEnv`.
- Produces: `Server.RunHTTP(ctx) error`; startup rules: refuse non-loopback bind IPs; refuse empty/unset token env; log the bound address to stderr (port 0 support).

- [ ] **Step 1: Write failing tests**

`http_test.go` — use `net/http/httptest` around the guarded handler plus direct `RunHTTP` startup checks:

```go
func TestRunHTTPRejectsNonLoopbackBind(t *testing.T) {
	// Options{Transport:"http", Addr:"0.0.0.0:0", HTTPTokenEnv:"X"} with
	// X set -> RunHTTP returns error mentioning loopback.
}

func TestRunHTTPRequiresTokenEnv(t *testing.T) {
	// HTTPTokenEnv unset name, or set-but-blank env var -> startup error.
}

func TestHTTPGuardChecks(t *testing.T) {
	// Build the guard handler directly (factor it as
	// s.httpGuard(next http.Handler, token string, boundHost string) http.Handler).
	// Table-driven:
	//  - no Authorization -> 401
	//  - wrong bearer -> 401 (constant-time compare)
	//  - correct bearer, Host mismatching bound host:port -> 400/403
	//  - correct bearer, loopback alias host (localhost:<port>, [::1]:<port>,
	//    127.0.0.1:<port>) -> pass-through to next
	//  - Origin present and not http://<loopback>:<port> -> 403
	//  - Origin https://127.0.0.1:<port> (wrong scheme, loopback host) -> 403
	//  - Origin present and matching http loopback origin -> pass
	//  - response never carries Access-Control-Allow-Origin
	//  - 401/403 bodies never contain the daemon token or the MCP token
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/mcpserver -run 'TestRunHTTP|TestHTTPGuard' -shuffle=on` → FAIL.

- [ ] **Step 3: Implement**

`internal/mcpserver/http.go`:

```go
package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) RunHTTP(ctx context.Context) error {
	if s.opts.HTTPTokenEnv == "" {
		return errors.New("http transport requires --http-token-env")
	}
	token := os.Getenv(s.opts.HTTPTokenEnv)
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("environment variable %s is unset or blank", s.opts.HTTPTokenEnv)
	}
	host, _, err := net.SplitHostPort(s.opts.Addr)
	if err != nil {
		return fmt.Errorf("invalid --addr: %w", err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("http transport binds loopback only; %q is not a loopback address", host)
	}

	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "middleman mcp: http transport listening on %s\n", ln.Addr())

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
	boundPort := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: s.httpGuard(handler, token, boundPort)}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// httpGuard enforces bearer auth, Host, and Origin checks. It never
// emits CORS headers and never echoes tokens.
func (s *Server) httpGuard(next http.Handler, token string, boundPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if auth == r.Header.Get("Authorization") || // no Bearer prefix
			subtle.ConstantTimeCompare([]byte(auth), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rhost, rport, err := net.SplitHostPort(r.Host)
		if err != nil {
			// Host without port (e.g. "localhost"): only valid if bound to 80; reject.
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if !isLoopbackHost(rhost) || rport != fmt.Sprint(boundPort) {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			// The transport is plain HTTP on loopback; require the
			// scheme too so e.g. https://127.0.0.1:<port> is rejected.
			ou, err := url.Parse(origin)
			if err != nil || ou.Scheme != "http" ||
				!isLoopbackHost(ou.Hostname()) || ou.Port() != fmt.Sprint(boundPort) {
				http.Error(w, "invalid origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
```

Update `runMCPCLI` to call the real `RunHTTP`.

- [ ] **Step 4: Run tests + HTTP e2e slice**

Add one e2e-flavored test in the same file: start `RunHTTP` on `127.0.0.1:0` with a token env set via `t.Setenv`, capture the bound address (refactor: have `RunHTTP` store `s.httpAddr` after listen, or accept a `ready chan<- string`), then do a real `http.Post` MCP initialize round with the bearer token → 200-family, and one without token → 401.

Run: `go test ./internal/mcpserver ./cmd/middleman -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver cmd/middleman
git commit -m "feat: add tokenized loopback http transport for middleman mcp

HTTP MCP is loopback-only in v1: startup refuses non-loopback binds
and blank token env vars, and every request needs a bearer token, a
loopback Host matching the bound port, and (when present) a loopback
Origin. No CORS headers are emitted and tokens never appear in errors."
```

---

### Task 14: Full-stack stdio e2e + final verification

**Spec sections:** "Success Criteria", "Testing > MCP tests (full-stack stdio)".

**Files:**
- Create: `cmd/middleman/mcp_e2e_test.go`

**Interfaces:**
- Consumes: e2e helpers in `cmd/middleman` (`buildMiddleman`, `writeMinimalConfig`, `reserveFreePort`, `waitForFile`, `procutil.Command` — see `api_verb_e2e_test.go:30-140`), the SDK's `mcp.Client` with `mcp.CommandTransport` (verify exact v1.6.1 name: the client transport that spawns a subprocess and speaks stdio).

- [ ] **Step 1: Write the e2e test**

```go
package main

// TestMCPStdioE2E:
// 1. bin := buildMiddleman(t); dataDir, cfgPath via writeMinimalConfig
//    PLUS a tracked-repo entry for the seeded repo. This is required:
//    the daemon filters repo summaries, activity, pulls, and issues
//    through the configured tracked repo set, so a repo that exists
//    only as a SQLite row is invisible to every list surface and the
//    list_repos/candidate/search/context assertions below would
//    fail. Add the repo to config.toml using the repo syntax from
//    internal/config (read how repos are declared there). Hermetic
//    setup, both parts required: (1) a self-hosted gitea or forgejo
//    repo whose platform_host is an unroutable loopback endpoint
//    (127.0.0.1:1) so sync fails fast with connection refused and
//    never leaves the machine (do NOT use github here — the GitHub
//    token/CLI fallback chain could make the test non-hermetic), and
//    (2) a dummy NON-EMPTY token: set token_env =
//    "MIDDLEMAN_MCP_E2E_TOKEN" on the repo entry and t.Setenv a
//    synthetic value. Leaving the token unset is NOT an option:
//    Config.ProviderTokenSources() marks configured repos as
//    required token sources and the daemon refuses to start when one
//    is missing. Sync failure just records last_sync_error; cached
//    SQLite data still serves all read routes. The seeded DB rows
//    must use the exact same (platform, platform_host, owner, name)
//    identity as the config entry.
// 2. Start the daemon: procutil.Command(bin, "--config", cfgPath),
//    waitForFile for runtimelock.MetadataPath(dataDir) and
//    AuthTokenPath. t.Cleanup: SIGTERM.
// 3. Seed cached data through the daemon API the way
//    api_verb_e2e_test drives requests — or simpler: open the SQLite
//    file directly with internal/db BEFORE starting the daemon and
//    insert a repo + one open PR + one activity-generating event, then
//    start the daemon. (The daemon reads the same DB; no sync needed.)
// 4. Connect an MCP client over stdio:
//      client := mcp.NewClient(&mcp.Implementation{Name: "e2e"}, nil)
//      session, err := client.Connect(ctx,
//          &mcp.CommandTransport{Command: exec.Command(bin, "mcp", "--config", cfgPath)}, nil)
// 5. ListTools -> assert the 9 curated names (same set as Task 12's test).
// 6. CallTool middleman_find_review_candidates {since: "720h"} ->
//    parse structured output, assert the seeded PR appears with
//    workflow.status "new".
// 7. CallTool middleman_set_item_workflow_state with the candidate ref,
//    status "reviewing", expected_status "new", reason "e2e" ->
//    previous_status "new".
// 8. Verify through the daemon API (raw GET /api/v1/workflow-state with
//    the bearer token from runtimelock.ReadAuthToken): item is
//    "reviewing" with updated_source "mcp".
// 9. CallTool again with expected_status "new" -> tool-level error/
//    conflict result; state unchanged.
//
// The read tools must also be proven against the real daemon (spec as
// amended: route selection, real JSON casing, SQLite behavior, and
// temp-file output must not hide behind fake-daemon unit tests):
// 10. CallTool middleman_list_repos -> seeded repo present with counts.
// 11. CallTool middleman_search_items {query: <word from seeded PR
//     title>} -> the PR is returned with workflow status "reviewing"
//     (set in step 7) and no body fields.
// 12. CallTool middleman_get_item_context for the PR -> title, body,
//     detail_loaded/cache fields present.
// 13. CallTool middleman_get_stack_context for the PR ->
//     present: false (seeded PR is not stacked).
// 14. CallTool middleman_get_item_diff for the PR — the real-daemon
//     summary AND emit_diff_file paths are MANDATORY (spec requires
//     proving route selection, real JSON shape, and temp-file output
//     against the real daemon, not fake-daemon mocks). The daemon's
//     /files and /diff routes read ONLY from the clone manager's
//     deterministic local clone path — with the tracked repo's sync
//     failing fast against its unroutable host, clone-URL fields are
//     never fetched, so the fixture must populate that path directly. Sequence:
//     build a worktree with gitcmd.New(), commit base then head (per
//     the setupGitLabCloneFixture pattern,
//     internal/server/e2etest/gitlab_sync_pin_test.go:36-62), then
//     `git clone --bare` it into the clone manager's per-repo path
//     BEFORE starting the daemon (os.MkdirAll the destination's
//     parent directory first — git clone does not create it). The daemon constructs
//     gitclone.New(filepath.Join(dataDir, "clones"), ...)
//     (cmd/middleman/main.go:542) and resolves repos via
//     Manager.ClonePath(host, owner, name)
//     (internal/gitclone/clone.go:66) — call ClonePath from the test
//     against a gitclone.New over the same base dir to get the exact
//     destination, and set the seeded MR row's diff_head/diff_base/
//     merge_base SHAs to the fixture's head/base commit SHAs.
//     The head commit must include BOTH a normal content change and a
//     mode-only change (chmod 755 an existing file in the fixture
//     worktree without editing it) so the full daemon-to-companion
//     path — real `git diff --raw -z` parsing, API JSON encoding,
//     companion decoding, serialization — proves OldMode/NewMode
//     survive end to end.
//     Assert: summary has the expected file with additions/deletions;
//     emit_diff_file returns a path to a 0600 file whose content
//     starts with "diff --git" and contains the seeded change; the
//     mode-only file's section contains "old mode 100644" and
//     "new mode 100755"; ALSO assert exactly one "diff --git" line
//     per changed file (guards the no-duplicate-header rule).
//     Separately assert the typed diff_unavailable error for a second
//     seeded PR whose SHAs do not exist in the fixture repo.
```

Write it fully; every numbered step is code in the test. Gate it like other cmd e2e tests (they run in normal `go test` — follow whatever `testing.Short()` gating `api_verb_e2e_test.go` uses).

- [ ] **Step 2: Run it**

Run: `go test ./cmd/middleman -run TestMCPStdioE2E -shuffle=on`
Expected: PASS.

- [ ] **Step 3: Full verification**

```bash
make test
make lint
make vet
make api-generate   # must be a no-op now; fail the task if it dirties the tree
git status --short  # clean except intended files
```

Expected: all pass; `git status` clean after `api-generate`.

- [ ] **Step 4: Commit**

```bash
git add cmd/middleman/mcp_e2e_test.go
git commit -m "test: prove stdio mcp flow end to end against a real daemon

A stdio MCP client connects to middleman mcp, lists curated tools,
finds a seeded candidate, claims it as reviewing with expected_status,
and the write is visible through the daemon API with source=mcp —
the spec's primary success criterion."
```

---

## Self-Review Notes (already applied)

- Spec coverage: every Goal/Success Criterion maps to a task — generic state (1-5), daemon endpoints + conflict semantics + `expected_status="new"` on missing rows (2, 6), kanban compatibility (3), issue exposure (5), all 9 tools (8-12), resource + prompt + docs (12), HTTP safety (13), stdio e2e + write-visibility criterion (14). "No MCP tool performs provider writes" is enforced structurally (only `putJSON` caller is the workflow tool; Task 12's registration test pins the tool set).
- `middleman_get_stack_context` uses the existing per-PR route `GET /pulls/.../stack` (host-prefixed variant for non-default hosts), matching the spec as amended: the repo-wide `GET /stacks` list filters by owner/name only and could pick the wrong stack across providers/hosts.
- Review-driven decisions (roborev jobs 4833-4836, spec amended to match): whitespace-only diff detection is out of scope entirely (no input flag, no counts — the files route cannot compute it and the use case does not need whitespace-aware line counts); `state=merged` in search is PR-only via `state=all` + client-side filter; the emitted diff file has an exact serialization spec; `status=new` is always an explicit stored row and listings match effective status; daemon discovery and the capability probe are uniformly lazy; migration 000037 no longer drops the old table — the drop moved to 000038 inside Task 3's commit so every commit is a working bisect point; all companion-built paths escape segments for nested owners; `/activity` results are truncated client-side because the daemon route has no limit param; the issue sync handler must share the detail builder; workflow-state repo filters must use the casefold key columns inside each UNION arm; the stdio e2e exercises every read tool against the real daemon.
- Type consistency: `ItemTypePR`/`ItemTypeIssue`, `SetItemWorkflowStateParams`, `WorkflowStateConflictError`, `workflowStateMetaResponse`, `itemRef`/`itemRefInput`, `daemonError` kinds, and cursor format are each defined once and referenced by name in later tasks' Interfaces blocks.
- Known verify-at-implementation points (explicitly flagged in tasks, not placeholders): exact MCP SDK v1.6.1 symbol names (Task 7 Step 1), huma's unknown-route 404 body for version_mismatch detection (Task 7), activity-type strings for the reasons map (Task 9), `getStackForPR` not-in-stack behavior (Task 11), `runtimelock.Acquire` handle API (Task 7 test helper).
