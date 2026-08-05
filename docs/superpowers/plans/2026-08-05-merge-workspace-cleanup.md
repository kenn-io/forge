# Merge Workspace Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a remembered merge-dialog checkbox that safely deletes the exact linked workspace after an immediate or deferred pull-request merge succeeds.

**Approved spec/design:** `docs/superpowers/specs/2026-08-05-merge-workspace-cleanup-design.md`

**Architecture:** The workspace API exposes its existing deletion lifecycle as an internal service and broadcasts an authoritative `workspace_deleted` event. The pull API captures the linked workspace ID when a merge is accepted, carries that immutable plan through deferred work, and reports cleanup separately from provider-confirmed merge success. The Svelte dialog persists the browser-local choice and the app consumes cleanup/deletion events for warning flashes and workspace tombstones.

**Tech Stack:** Go 1.26, Huma/OpenAPI, Svelte 5 runes, TypeScript, Vite+/Vitest, Testing Library, server-sent events.

## Global Constraints

- Show the checkbox only when the pull request currently has a workspace.
- Use local-storage key `kenn-forge:merge:delete-workspace-after-merge`, defaulting to checked when absent or unreadable.
- Apply the choice to immediate merges and **Merge after CI is complete**.
- Resolve and pin the exact workspace ID when the merge is accepted; never re-resolve after CI.
- Delete only after provider-confirmed merge success, always with `force=false`.
- Preserve dirty workspaces and report cleanup failure without changing a successful merge result.
- Show cleanup failures through a kit-ui flash with `warning` tone and explicit “workspace was not pruned” copy.
- Never run cleanup from a failed, cancelled, timed-out, or superseded deferred merge.
- Do not add a database migration or durable cleanup queue.

---

### Task 1: Share the workspace deletion lifecycle and publish deletion identity

**Files:**
- Modify: `internal/server/workspaceapi/types.go`
- Modify: `internal/server/workspaceapi/routes_handlers.go`
- Modify: `internal/server/api_test.go`

**Interfaces:**
- Produces: `workspaceapi.WorkspaceDeletedPayload`.
- Produces: `(*workspaceapi.Handler).DeleteWorkspace(ctx context.Context, id string, force bool) ([]string, error)`.

- [ ] **Step 1: Write failing lifecycle tests**

Use `setupTestServerWithWorkspacesServer`, `createReadyWorkspace`, and `srv.Hub().Subscribe`. A clean delete must remove the row and publish exact identity followed by `data_changed`:

```go
func TestDeleteWorkspacePublishesIdentityAfterSuccessfulCleanup(t *testing.T) {
    client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
    ws := createReadyWorkspace(t, t.Context(), client)
    events, _ := srv.Hub().Subscribe(t.Context(), false)

    dirty, err := srv.workspaceAPI.DeleteWorkspace(t.Context(), ws.Id, false)

    require.NoError(t, err)
    assert.Empty(t, dirty)
    stored, err := database.GetWorkspace(t.Context(), ws.Id)
    require.NoError(t, err)
    assert.Nil(t, stored)
    deleted := readEventMatching(t, events, func(event Event) bool {
        return event.Type == "workspace_deleted"
    })
    payload := deleted.Data.(workspaceapi.WorkspaceDeletedPayload)
    assert.Equal(t, ws.Id, payload.WorkspaceID)
    assert.Equal(t, "github", payload.Provider)
    assert.Equal(t, "acme/widget", payload.RepoPath)
    readEventMatching(t, events, func(event Event) bool {
        return event.Type == "data_changed"
    })
}
```

Add `TestDeleteWorkspacePreservesDirtyWorkspace` asserting non-empty dirty files, retained row, and zero events.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/server -run 'TestDeleteWorkspacePublishesIdentity|TestDeleteWorkspacePreservesDirty' -count=1
```

Expected: build failure because the exported lifecycle and event DTO do not exist.

- [ ] **Step 3: Extract the shared method**

Define in `types.go`:

```go
type WorkspaceDeletedPayload struct {
    WorkspaceID        string `json:"workspace_id"`
    Provider           string `json:"provider"`
    PlatformHost       string `json:"platform_host"`
    RepoPath           string `json:"repo_path"`
    Owner              string `json:"owner"`
    Name               string `json:"name"`
    ItemType           string `json:"item_type"`
    ItemNumber         int    `json:"item_number"`
    AssociatedPRNumber *int   `json:"associated_pr_number,omitempty"`
}
```

Move the current route's setup admission, runtime stopping, manager delete, and deletion-state cleanup into exported `DeleteWorkspace`. Load the row before deletion for event identity. Publish `workspace_deleted` and then `data_changed` only after successful deletion. The Huma route stays a thin adapter preserving existing 404, 409 dirty/ownership, and 500 mappings.

- [ ] **Step 4: Run GREEN**

```bash
go test ./internal/server -run 'TestDeleteWorkspace' -count=1
```

Expected: PASS, including existing route tests.

- [ ] **Step 5: Context-sync and commit**

Run mandatory `context-sync --commit`, stage Task 1 plus this plan, and commit with subject `refactor: share workspace deletion lifecycle`. Explain that merge cleanup must reuse dirty/runtime/invalidation safeguards.

---

### Task 2: Capture and execute cleanup on immediate and deferred merge paths

**Files:**
- Create: `internal/server/pullapi/workspace_cleanup_test.go`
- Create: `internal/server/e2etest/merge_workspace_cleanup_test.go`
- Modify: `internal/server/pullapi/handler.go`
- Modify: `internal/server/pullapi/routes.go`
- Modify: `internal/server/pullapi/deferred_merge.go`
- Modify: `internal/server/pullapi/deferred_merge_integration_test.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Consumes: `DeleteWorkspace func(context.Context, string, bool) ([]string, error)`.
- Produces: request field `delete_workspace_after_merge`.
- Produces: `WorkspaceCleanupResult` status `deleted | already_absent | not_found_at_submission | failed`.
- Produces: immutable `workspaceCleanupPlan { Requested bool; WorkspaceID string }`.

- [ ] **Step 1: Write failing cleanup tests**

Add behavior tests named `TestMergeWorkspaceCleanupRunsOnlyAfterConfirmedMerge`, `TestMergeWorkspaceCleanupPreservesDirtyWorkspaceAsWarning`, `TestMergeWorkspaceCleanupTreatsMissingPinnedWorkspaceAsAbsent`, `TestDeferredMergeCleanupUsesWorkspacePinnedAtQueueTime`, and `TestFailedAndSupersededDeferredMergesNeverRunWorkspaceCleanup`. Use the deferred-merge route fixture's real database/provider and an injected deleter spy.

The replacement test queues with `ws-old`, replaces the linked row with `ws-new` before CI passes, completes the merge, and asserts the deleter sees only `ws-old` and `force=false`.

- [ ] **Step 2: Run RED**

```bash
go test ./internal/server/pullapi -run 'Test.*Merge.*WorkspaceCleanup|TestDeferredMergeCleanup' -count=1
```

Expected: failures for absent request/result/plan/dependency types.

- [ ] **Step 3: Add request, result, plan, and dependency**

```go
type mergePRInputBody struct {
    CommitTitle string `json:"commit_title"`
    CommitMessage string `json:"commit_message"`
    Method string `json:"method"`
    ExpectedHeadSHA string `json:"expected_head_sha,omitempty"`
    DeleteWorkspaceAfterMerge bool `json:"delete_workspace_after_merge,omitempty"`
}

type WorkspaceCleanupResult struct {
    WorkspaceID string `json:"workspace_id,omitempty"`
    Status string `json:"status" enum:"deleted,already_absent,not_found_at_submission,failed"`
    Warning string `json:"warning,omitempty"`
}
```

Add this exact field to `mergePRBody` and `DeferredMergeCompletedPayload`:

```go
WorkspaceCleanup *WorkspaceCleanupResult `json:"workspace_cleanup,omitempty"`
```

Add the deleter callback to `Deps`/`Handler`, and wire `DeleteWorkspace: s.workspaceAPI.DeleteWorkspace` in `server.go`.

- [ ] **Step 4: Implement exact-ID capture and safe execution**

Implement:

```go
func (s *Handler) captureWorkspaceCleanupPlan(
    ctx context.Context, repo db.Repo, number int, requested bool,
) (workspaceCleanupPlan, error)

func (s *Handler) executeWorkspaceCleanup(
    ctx context.Context, plan workspaceCleanupPlan,
) *WorkspaceCleanupResult
```

Capture with `s.workspaces.GetByMRForProvider` after merge validation and before mutation/queue admission. Execution returns nil when unchecked, `not_found_at_submission` for requested empty ID, `already_absent` for `workspace.ErrWorkspaceNotFound`, `failed` plus warning for dirty/errors, and `deleted` on success. Always call the shared service with `force=false`. For a non-dirty error, emit structured `slog.Warn("workspace cleanup after merge failed", "workspace_id", plan.WorkspaceID, "err", err)` before returning the warning result.

- [ ] **Step 5: Thread the immutable plan through both paths**

Change the shared signature to:

```go
func (s *Handler) mergePRWithBody(
    ctx context.Context,
    provider, platformHost, owner, name string,
    number int,
    body mergePRInputBody,
    capturedCleanup *workspaceCleanupPlan,
) (mergePRBody, error)
```

`capturedCleanup == nil` means the immediate path captures after validation; the deferred worker always passes its non-nil queued plan, including the unchecked zero value, so completion cannot re-resolve. Deferred enqueue captures before `markDeferredMergeInFlight`, passes the value through `runDeferredMerge` and `completeDeferredMerge`, and checks `handle.isSuperseded()` immediately before calling `mergePRWithBody`. Do not check after the call because a successful deferred merge supersedes its own registered handle. Worker exits for CI failure, timeout, cancellation, target change, or prior supersession cannot reach cleanup. Execute only after terminal provider success and local merged-state persistence, then copy the result into the response/event.

- [ ] **Step 6: Run GREEN**

```bash
go test ./internal/server/pullapi -run 'Test.*Merge.*WorkspaceCleanup|TestDeferredMergeCleanup|TestImmediateMergeSupersedesQueuedDeferredMerge' -count=1
```

Expected: PASS with zero deleter calls on failed/superseded paths.

- [ ] **Step 7: Exercise the composed server lifecycle**

Add full HTTP + SQLite e2e tests using the real workspace manager and `workspaceAPI.DeleteWorkspace` wiring. One immediate and one deferred success must create a real PR workspace, merge through the fake provider, assert the workspace row and worktree are gone, assert `workspace_cleanup.status == "deleted"`, and observe `workspace_deleted`. A dirty-worktree case must assert the provider merge persisted, the workspace/worktree remain, the response status is `failed`, and the warning names uncommitted changes.

Run:

```bash
go test ./internal/server/e2etest -run 'TestMergeWorkspaceCleanup|TestDeferredMergeWorkspaceCleanup' -count=1
```

Expected: PASS through the real composition root, not an injected deleter spy.

- [ ] **Step 8: Context-sync and commit**

Run mandatory `context-sync --commit` and commit Task 2 with subject `feat: prune workspaces after successful merges`. Explain admission-time pinning, non-force cleanup, and merge-success independence.

---

### Task 3: Regenerate the public API contract

**Files:**
- Modify (generated): `frontend/openapi/openapi.yaml`
- Modify (generated): `internal/apiclient/generated/client.gen.go`
- Modify (generated): `packages/ui/src/api/generated/schema.ts`

**Interfaces:**
- Produces generated clients containing `delete_workspace_after_merge`, `workspace_cleanup`, and the four cleanup statuses.

- [ ] **Step 1: Regenerate and observe changed artifacts**

```bash
make api-generate
git status --short -- frontend/openapi/openapi.yaml internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts
```

Expected: the three tracked generated artifacts are modified. `internal/apiclient/spec/openapi.json` is an ignored intermediate input and is not staged.

- [ ] **Step 2: Inspect exact generated shapes**

```bash
rg -n "delete_workspace_after_merge|workspace_cleanup|WorkspaceCleanupResult" frontend/openapi/openapi.yaml internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts
```

Expected: host-prefixed and default merge operations share the contract and the enum contains exactly four values.

- [ ] **Step 3: Run contract verification**

```bash
make frontend-api-client-check huma-route-check
go test ./internal/apiclient/generated ./internal/server/pullapi -count=1
```

Expected: PASS.

- [ ] **Step 4: Context-sync and commit**

Run mandatory `context-sync --commit` and commit generated files with subject `api: expose merge workspace cleanup results`.

---

### Task 4: Add the remembered checkbox and immediate warning

**Files:**
- Create: `packages/ui/src/components/detail/mergeWorkspaceCleanupPreference.ts`
- Create: `packages/ui/src/components/detail/mergeWorkspaceCleanupPreference.test.ts`
- Modify: `packages/ui/src/components/detail/MergeModal.svelte`
- Modify: `packages/ui/src/components/detail/MergeModal.svelte.test.ts`
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`
- Modify: `packages/ui/src/components/detail/PullDetail.test.ts`

**Interfaces:**
- Produces: `readDeleteWorkspaceAfterMergePreference(storage?: Storage): boolean`.
- Produces: `writeDeleteWorkspaceAfterMergePreference(value: boolean, storage?: Storage): void`.
- Changes: `MergeModal` props `workspacePresent?: boolean` and `onmerged(cleanup?: WorkspaceCleanupResult)`.

- [ ] **Step 1: Write failing helper and component tests**

Cover absent, true, false, malformed, throwing-read, and throwing-write storage. Add modal tests for these exact behaviors: a checked option appears only for a PR with a workspace; saved preference is restored and toggles persist; immediate and deferred bodies carry the intent; failed immediate cleanup produces a warning while `onmerged` still runs.

Assert `init.body.delete_workspace_after_merge === true` and a kit-ui flash whose tone is `warning` and whose message matches `/merged.*workspace was not pruned.*uncommitted changes/i`.

- [ ] **Step 2: Run RED**

```bash
vp test run --project unit packages/ui/src/components/detail/mergeWorkspaceCleanupPreference.test.ts packages/ui/src/components/detail/MergeModal.svelte.test.ts packages/ui/src/components/detail/PullDetail.test.ts
```

Expected: failures for missing helper, checkbox, payload, and warning callback.

- [ ] **Step 3: Implement the preference helper**

Use exact key `kenn-forge:merge:delete-workspace-after-merge`. Missing/malformed/read failure returns true. Only string `"false"` returns false. Writes persist `String(value)` and catch storage exceptions without affecting submission.

- [ ] **Step 4: Implement checkbox, payload, and warning flow**

Render an accessible checkbox only for `workspacePresent`; initialize it from the helper and persist on change. Add `delete_workspace_after_merge: workspacePresent && deleteWorkspaceAfterMerge` to the common body. Pass `data?.workspace_cleanup` to `onmerged`. `PullDetail` passes `workspacePresent={workspace !== null}` and shows:

```ts
showFlash(
  `Pull request merged, but the workspace was not pruned: ${cleanup.warning ?? "workspace cleanup failed"}`,
  { tone: "warning" },
);
```

Then run the existing close/detail/pulls/activity refreshes.

- [ ] **Step 5: Run Svelte and focused verification**

```bash
vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/MergeModal.svelte --svelte-version 5
vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/PullDetail.svelte --svelte-version 5
vp test run --project unit packages/ui/src/components/detail/mergeWorkspaceCleanupPreference.test.ts packages/ui/src/components/detail/MergeModal.svelte.test.ts packages/ui/src/components/detail/PullDetail.test.ts
```

Expected: no actionable Svelte findings and all tests PASS. If `svelte-mcp` remains unavailable, record the exact limitation and rely on `svelte-check`, lint, and component tests rather than installing an undeclared tool.

- [ ] **Step 6: Context-sync and commit**

Run mandatory `context-sync --commit` and commit Task 4 with subject `feat: offer workspace pruning in merge dialog`.

---

### Task 5: Consume deletion and deferred cleanup events

**Files:**
- Modify: `packages/ui/src/stores/events.svelte.ts`
- Modify: `frontend/src/lib/stores/events.svelte.test.ts`
- Modify: `packages/ui/src/Provider.svelte`
- Modify: `frontend/src/Provider.test.ts`
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/App.test.ts`
- Modify: `frontend/src/lib/stores/workspace-host.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`

**Interfaces:**
- Produces: `WorkspaceDeletedEvent` and `EventsStoreOptions.onWorkspaceDeleted`.
- Changes Provider props: `onWarning` and `onWorkspaceDeleted`.
- Consumes: `notifyWorkspaceDeleted(workspaceId, undefined, identity)`.

- [ ] **Step 1: Write failing event and adapter tests**

Add tests for these exact behaviors: `workspace_deleted` JSON is decoded; deferred cleanup failure uses the warning callback; Provider forwards deletion and refreshes matching detail; the App adapter tombstones both owning and associated-PR identities; the workspace sidebar removes the exact event ID immediately and schedules a server refresh.

For issue #7 with `associated_pr_number: 42`, assert two tombstone publications using the same workspace ID.

- [ ] **Step 2: Run RED**

```bash
vp test run --project unit frontend/src/lib/stores/events.svelte.test.ts frontend/src/Provider.test.ts frontend/src/App.test.ts frontend/src/lib/stores/workspace-host.test.ts frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts
```

Expected: failures for missing event/callback/warning routes.

- [ ] **Step 3: Register and forward `workspace_deleted`**

Define the TypeScript DTO matching the server payload and register it with `addJSONListener`. Provider forwards the event, refreshes visible stores, and refreshes selected detail when either owning identity or associated PR matches.

Register the same `workspace_deleted` event on `WorkspaceListSidebar`'s own EventSource. Decode `workspace_id`, immediately filter that exact local row from its workspace array, and schedule `fetchWorkspaces()` to reconcile counts and remote state. Keep `workspace_status` behavior unchanged.

- [ ] **Step 4: Route deferred cleanup failure as warning**

Extend `DeferredMergeCompletedEvent` with `workspace_cleanup`. In the merged branch, call `onWarning` with `` `${event.owner}/${event.name}#${event.number} merged, but the workspace was not pruned: ${event.workspace_cleanup.warning ?? "workspace cleanup failed"}` `` when status is `failed`; otherwise keep the existing success notification. Deferred merge failure remains on `onError`.

- [ ] **Step 5: Adapt deletion to workspace-host identities**

In `App.svelte`, pass `onWarning={(msg) => showFlash(msg, { tone: "warning" })}`. For `onWorkspaceDeleted`, construct the owning identity from provider/host/repo/item fields and call `notifyWorkspaceDeleted`. When `associated_pr_number` is present, call it again with `itemType: "pull"` and that number. Existing tombstone behavior must remain idempotent for the initiating client.

- [ ] **Step 6: Run Svelte and event verification**

Run Svelte autofixer on `Provider.svelte`, `App.svelte`, and `WorkspaceListSidebar.svelte` with the same documented missing-tool fallback, then run the five focused test files from Step 2. Expected: PASS.

- [ ] **Step 7: Context-sync and commit**

Run mandatory `context-sync --commit` and commit Task 5 with subject `feat: invalidate pruned workspaces across clients`.

---

### Task 6: Integrated verification and durable context

**Files:**
- Modify if required by context-sync: `context/deferred-merge.md`
- Modify if required by context-sync: `context/workspace-apis.md`
- Modify if required by context-sync: `context/ui-interaction-contracts.md`
- Modify: `docs/superpowers/plans/2026-08-05-merge-workspace-cleanup.md` (check off completed steps)

**Interfaces:**
- Verifies request → merge/defer → safe delete → SSE → warning/tombstone.

- [ ] **Step 1: Run focused backend suites**

```bash
go test ./internal/server/workspaceapi ./internal/server/pullapi -shuffle=on
```

Expected: PASS.

- [ ] **Step 2: Run focused and full frontend unit suites**

```bash
vp test run --project unit packages/ui/src/components/detail/mergeWorkspaceCleanupPreference.test.ts packages/ui/src/components/detail/MergeModal.svelte.test.ts packages/ui/src/components/detail/PullDetail.test.ts frontend/src/lib/stores/events.svelte.test.ts frontend/src/Provider.test.ts frontend/src/App.test.ts frontend/src/lib/stores/workspace-host.test.ts
vp test run --project unit
```

Expected: PASS with pristine output.

- [ ] **Step 3: Run generation and static checks**

```bash
make api-generate
git diff --exit-code -- frontend/openapi/openapi.yaml internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts
make frontend-check-no-deps
git diff --check
```

Expected: generation stable; format, lint, types, Svelte checks, and whitespace checks PASS.

- [ ] **Step 4: Run repository short verification**

```bash
make test-short
```

Expected: PASS. Preserve and clearly separate any unrelated pre-existing failure output.

- [ ] **Step 5: Synchronize durable context**

Run mandatory `context-sync --commit`. Record only landed cross-cutting invariants supported by code: deferred cleanup pins ID at admission, automatic cleanup is non-force and cannot change merge success, and `workspace_deleted` is the cross-client tombstone signal. Obey the grep-test and per-addition budget.

- [ ] **Step 6: Commit context/checkmarks if changed**

Commit with subject `docs: record merge workspace cleanup invariants` only when context or plan checkmarks changed.

- [ ] **Step 7: Verify before completion**

Invoke `superpowers:verification-before-completion`, inspect `git status --short`, and report exact passing commands, the known `svelte-mcp` limitation if still present, and created commits. Do not claim completion from stale or partial output.
