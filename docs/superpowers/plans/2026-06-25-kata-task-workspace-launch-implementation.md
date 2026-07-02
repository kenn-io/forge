# Kata Task Workspace Launch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Create/Open workspace support to Kata task details, using exact configured repository mappings and embedding the live Kata task details pane inside Kata-backed workspaces.

**Architecture:** Kata tasks remain non-provider objects. Workspaces gain an `item_key` owner column so provider issues/PRs keep numeric keys while Kata task workspaces use a scoped key derived from Kata daemon/project/issue identity; Kata metadata is stored with the workspace for later live-detail lookup. Repository resolution is split into config-backed manual mappings and `.kata.toml` discovery under configured watched repos, and the frontend renders the same `KataIssueDetail.svelte` component from both the Kata browser and the workspace sidebar.

**Tech Stack:** Go, SQLite migrations, Huma API handlers, Svelte 5 runes, TypeScript, Vite+ (`vp`), Bun-managed dependencies.

---

## File Structure

- Create `internal/db/migrations/000036_kata_workspace_owner_keys.up.sql` and `.down.sql`: add `item_key`, `kata_metadata`, and replace the unique workspace owner index.
- Modify `internal/db/types.go` and `internal/db/queries.go`: add Kata workspace constants, metadata structs, insert/list/get support, and provider issue/PR compatibility through `item_key`.
- Modify `internal/config/config.go` and `internal/config/config_test.go`: persist and validate manual Kata project-to-repo mappings.
- Create `internal/server/kata_workspace.go` and `internal/server/kata_workspace_test.go`: resolve workspace targets and create/reuse Kata task workspaces without provider issue lookup.
- Modify `internal/server/kata_routes.go`, `internal/server/settings_handlers.go`, and API response types: register routes and expose mappings in settings.
- Modify `internal/workspace/manager.go` and tests: support issue-like Kata workspace setup from `origin/HEAD` with a generated branch and stored metadata.
- Modify `frontend/src/lib/api/settings.ts`: include Kata project mappings in local settings.
- Create `frontend/src/lib/api/kata/workspaces.ts`: typed client for target resolution and workspace creation.
- Modify `frontend/src/lib/features/kata/KataWorkspace.svelte` and `frontend/src/lib/features/kata/KataIssueDetail.svelte`: load a target for the selected task and render Create/Open workspace actions only when available.
- Create `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte`: load live Kata task data for a Kata-backed workspace and render `KataIssueDetail.svelte`.
- Modify `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte` and `packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte`: add a `kata_task` tab rendered from a Svelte snippet instead of the provider issue pane.
- Modify settings UI files to add the Kata project mapping editor in the Workspace settings group.
- Regenerate API artifacts with `make api-generate`.
- Update context docs if the new `item_key` and Kata workspace ownership behavior belongs there.

## Task 1: Persist Workspace Owner Keys

**Files:**
- Create: `internal/db/migrations/000036_kata_workspace_owner_keys.up.sql`
- Create: `internal/db/migrations/000036_kata_workspace_owner_keys.down.sql`
- Modify: `internal/db/types.go`
- Modify: `internal/db/queries.go`
- Test: `internal/db/queries_test.go`

- [ ] **Step 1: Write failing database tests**

Add tests that insert a normal provider issue workspace and a Kata workspace. The provider issue row must read back with `ItemKey == "42"` and `ItemNumber == 42`; the Kata row must read back with `ItemType == "kata_task"`, a scoped `ItemKey` derived from daemon/project/issue identity, `ItemNumber == 0`, and decoded Kata metadata.

- [ ] **Step 2: Verify red**

Run:

```bash
go test ./internal/db -run 'TestWorkspaceItemKey|TestKataWorkspaceMetadata' -shuffle=on
```

Expected: fail because `Workspace.ItemKey`, `WorkspaceItemTypeKataTask`, and metadata storage do not exist yet.

- [ ] **Step 3: Add migration and DB model support**

Add `item_key TEXT NOT NULL DEFAULT ''`, backfill it from `item_number`, add `kata_metadata TEXT NOT NULL DEFAULT ''`, and replace the old unique index with `(platform, platform_host, repo_path_key, item_type, item_key)`. Update inserts and scans so empty `ItemKey` for legacy callers becomes `strconv.Itoa(ItemNumber)`.

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./internal/db -run 'TestWorkspaceItemKey|TestKataWorkspaceMetadata' -shuffle=on
```

Expected: pass.

## Task 2: Add Manual Kata Project Mappings to Settings

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/server/settings_handlers.go`
- Modify: `frontend/src/lib/api/settings.ts`
- Modify: settings UI files under `frontend/src/lib/components/settings/`

- [ ] **Step 1: Write failing config/settings tests**

Add tests covering:
- a valid mapping from project UID to an exact configured repo survives config load/save,
- duplicate daemon/project mapping is rejected,
- a mapping to a non-configured repo is rejected,
- settings response/update includes `kata_projects`.

- [ ] **Step 2: Verify red**

Run:

```bash
go test ./internal/config ./internal/server -run 'Test.*Kata.*Mapping|TestUpdateSettings.*Kata' -shuffle=on
```

Expected: fail because config and settings types do not expose the mapping.

- [ ] **Step 3: Implement config and settings support**

Add `KataProjectRepoMapping` with `daemon_id`, `project_uid`, `provider`, `platform_host`, and `repo_path`. Normalize host and repo path, validate exact repo membership against configured watched repos, and include mappings in settings read/update/save paths.

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./internal/config ./internal/server -run 'Test.*Kata.*Mapping|TestUpdateSettings.*Kata' -shuffle=on
```

Expected: pass.

## Task 3: Resolve Kata Workspace Targets

**Files:**
- Create: `internal/server/kata_workspace.go`
- Test: `internal/server/kata_workspace_test.go`
- Modify: `internal/server/kata_routes.go`

- [ ] **Step 1: Write failing route tests**

Add tests for `POST /api/v1/kata/workspace-target`:
- manual daemon/project mapping returns the configured repo,
- manual global project mapping is used when daemon-specific mapping is absent,
- `.kata.toml` under exactly one configured repo with `worktree_base_path` resolves automatically by project UID, identity, or unambiguous project name,
- no mapping or multiple automatic matches returns `available:false`,
- existing Kata workspace returns an `existing_workspace` ref.

- [ ] **Step 2: Verify red**

Run:

```bash
go test ./internal/server -run 'TestKataWorkspaceTarget' -shuffle=on
```

Expected: fail because the route is not registered.

- [ ] **Step 3: Implement target resolver**

Implement mapping precedence: manual daemon/project, manual global project, automatic `.kata.toml` discovery under exact configured repo clone paths. Return no button state when the mapping is absent or ambiguous. Match existing workspaces by provider, host, repo, `item_type="kata_task"`, and the scoped Kata workspace item key.

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./internal/server -run 'TestKataWorkspaceTarget' -shuffle=on
```

Expected: pass.

## Task 4: Create and Reuse Kata Workspaces

**Files:**
- Modify: `internal/workspace/manager.go`
- Modify: `internal/server/kata_workspace.go`
- Test: `internal/workspace/manager_test.go`
- Test: `internal/server/kata_workspace_test.go`

- [ ] **Step 1: Write failing create/reuse tests**

Add tests for `POST /api/v1/kata/workspaces`:
- creating a workspace does not require a provider issue row,
- the workspace uses `item_type="kata_task"`, a scoped Kata workspace `item_key`, stored metadata, and a branch generated from the Kata task ID/title,
- calling create again returns the existing workspace instead of creating a duplicate.

- [ ] **Step 2: Verify red**

Run:

```bash
go test ./internal/workspace ./internal/server -run 'Test.*Kata.*Workspace' -shuffle=on
```

Expected: fail because the manager has only PR and provider-issue create paths.

- [ ] **Step 3: Implement manager and route create path**

Add `CreateKataTask` to the workspace manager. Treat Kata workspaces as issue-like for start ref, setup, and worktree add behavior, but never look up or sync a provider issue. Store the Kata metadata JSON on insert and use a deterministic branch name from `short_id` or `qualified_id` plus title slug.

- [ ] **Step 4: Verify green**

Run:

```bash
go test ./internal/workspace ./internal/server -run 'Test.*Kata.*Workspace' -shuffle=on
```

Expected: pass.

## Task 5: Add Kata Workspace Button in the Task Browser

**Files:**
- Create: `frontend/src/lib/api/kata/workspaces.ts`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataIssueDetail.svelte`
- Test: relevant Kata frontend tests under `frontend/src/lib/features/kata/`

- [ ] **Step 1: Write failing frontend tests**

Add tests that select a Kata issue and assert:
- no workspace button appears when `available:false`,
- `Create workspace` appears when a repo target exists and no workspace exists,
- `Open workspace` appears when `existing_workspace` is returned,
- target state clears while a different selected issue is loading.

- [ ] **Step 2: Verify red**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test --run src/lib/features/kata
```

Expected: fail because the API client and button props do not exist.

- [ ] **Step 3: Implement client and UI state**

Add typed target/create helpers. In `KataWorkspace.svelte`, load the target whenever selected issue or daemon changes, clear stale target before the request resolves, and call create/open handlers from the detail action row. `KataIssueDetail.svelte` receives a workspace action prop and renders one button only when the parent provides it.

- [ ] **Step 4: Verify green**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test --run src/lib/features/kata
```

Expected: pass.

## Task 6: Embed the Live Kata Detail in Workspaces

**Files:**
- Create: `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Modify: `packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte`
- Test: workspace terminal/sidebar frontend tests

- [ ] **Step 1: Write failing sidebar tests**

Add tests for a `kata_task` workspace showing a `Kata task` tab and not rendering the provider issue pane. The pane must render through `KataIssueDetail.svelte` using live daemon data addressed by stored workspace Kata metadata.

- [ ] **Step 2: Verify red**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test --run src/lib/components/terminal packages/ui/src/components/workspace
```

Expected: fail because `kata_task` is not a supported sidebar tab.

- [ ] **Step 3: Implement shared-detail pane**

Extend sidebar tab types with `kata_task`. Pass a Svelte snippet from the app-specific terminal view into the shared UI sidebar. The snippet renders `KataWorkspaceSidebarPane.svelte`, which uses the stored daemon/project/issue metadata to load live task detail and events, then renders the exact same `KataIssueDetail.svelte` component used by the main Kata browser.

- [ ] **Step 4: Run Svelte autofixer**

Run:

```bash
vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte
vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte
vp exec svelte-mcp svelte-autofixer ./packages/ui/src/components/workspace/WorkspaceRightSidebar.svelte
```

Expected: no blocking diagnostics.

- [ ] **Step 5: Verify green**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test --run src/lib/components/terminal packages/ui/src/components/workspace
```

Expected: pass.

## Task 7: Regenerate Artifacts, Context, and Final Verification

**Files:**
- Modify generated API files from `make api-generate`
- Modify context docs only if the implemented behavior adds durable project knowledge outside the feature spec

- [ ] **Step 1: Regenerate API artifacts**

Run:

```bash
make api-generate
```

Expected: generated OpenAPI/client files include Kata workspace routes and new workspace fields.

- [ ] **Step 2: Run targeted backend verification**

Run:

```bash
go test ./internal/db ./internal/config ./internal/workspace ./internal/server -shuffle=on
```

Expected: pass.

- [ ] **Step 3: Run targeted frontend verification**

Run from `frontend/`:

```bash
../node_modules/.bin/vp test --run src/lib/features/kata src/lib/components/terminal packages/ui/src/components/workspace
```

Expected: pass.

- [ ] **Step 4: Run context-sync stop gate**

Run:

```bash
scripts/context-sync --check
```

If behavior belongs in context, update the relevant context file and mark with the concrete file changed. If no context update belongs, mark with the reason.

- [ ] **Step 5: Commit**

Run:

```bash
git status --short
git add internal frontend packages docs context
git commit -m "feat: launch workspaces from Kata tasks"
```

Expected: commit succeeds through hooks.
