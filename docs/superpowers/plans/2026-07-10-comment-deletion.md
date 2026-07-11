# Comment Deletion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let maintainers delete ordinary PR and issue timeline comments after an explicit in-app confirmation.

**Architecture:** Extend the existing provider-neutral `CommentMutator`, expose paired provider-aware Huma DELETE routes, remove the persisted event only after upstream success, and regenerate the API clients. Thread delete callbacks through the existing detail stores into `EventTimeline`, where a kit-ui confirmation modal owns pending and error state.

**Tech Stack:** Go 1.26, Huma, SQLite, provider SDKs, Svelte 5, TypeScript, kit-ui, Vitest/Vite+.

## Global Constraints

- Support ordinary PR and issue comments for GitHub, GitLab, Forgejo, and Gitea.
- Keep repository identity as `(platform, platform_host, owner, name)` and use provider route helpers.
- Require explicit in-app confirmation; do not use `window.confirm`.
- Let the provider remain authoritative for ownership and permission.
- Do not add compatibility routes, deletion tombstones, or review-comment deletion.
- Follow RED-GREEN-REFACTOR and the repository's test-scope discipline.

---

### Task 1: Provider-Neutral Delete Contract And Native Transports

**Files:**
- Modify: `internal/platform/client.go`
- Modify: `internal/github/client.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/client_test.go`
- Modify: `internal/platform/gitlab/mutation.go`
- Modify: `internal/platform/gitlab/mutation_test.go`
- Modify: `internal/platform/gitealike/types.go`
- Modify: `internal/platform/gitealike/provider.go`
- Modify: `internal/platform/gitealike/provider_test.go`
- Modify: `internal/platform/gitea/mutation.go`
- Modify: `internal/platform/gitea/client_test.go`
- Modify: `internal/platform/forgejo/mutation.go`
- Modify: `internal/platform/forgejo/client_test.go`
- Modify: GitHub `github.Client` mocks identified by compiler failures

**Interfaces:**
- Produces: `DeleteMergeRequestComment(context.Context, platform.RepoRef, int, int64) error`
- Produces: `DeleteIssueComment(context.Context, platform.RepoRef, int, int64) error`
- Produces: `gitealike.MutationTransport.DeleteIssueComment(context.Context, platform.RepoRef, int64) error`

- [ ] **Step 1: Write failing provider tests**

Add table-driven assertions that deletion sends the native request and preserves mapped failures. The neutral interface is:

```go
type CommentMutator interface {
    // existing create/edit methods
    DeleteMergeRequestComment(ctx context.Context, ref RepoRef, number int, commentID int64) error
    DeleteIssueComment(ctx context.Context, ref RepoRef, number int, commentID int64) error
}
```

GitHub expects `DELETE /repos/acme/widget/issues/comments/44`; GitLab expects the MR or issue note endpoint with the parent IID; Gitea and Forgejo expect their issue-comment delete endpoint.

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./internal/github ./internal/platform/gitlab ./internal/platform/gitealike ./internal/platform/gitea ./internal/platform/forgejo -run 'Delete.*Comment' -shuffle=on
```

Expected: compile failures or missing-method failures for the delete contract.

- [ ] **Step 3: Implement minimal native deletion**

Use the installed SDK methods and existing error/rate mapping:

```go
func (c *liveClient) DeleteIssueComment(ctx context.Context, owner, repo string, commentID int64) error {
    resp, err := c.writeGH().Issues.DeleteComment(ctx, owner, repo, commentID)
    c.trackWriteRate(resp)
    if err != nil {
        return fmt.Errorf("deleting comment %d on %s/%s: %w", commentID, owner, repo, err)
    }
    return nil
}
```

GitLab calls `Notes.DeleteMergeRequestNote` or `Notes.DeleteIssueNote`; gitealike calls the shared transport deletion and maps its error. Add required no-op method implementations to existing test mocks only where the compiler requires the expanded interface.

- [ ] **Step 4: Run tests and verify GREEN**

Run the command from Step 2. Expected: PASS.

- [ ] **Step 5: Commit provider support**

```bash
git add internal/platform internal/github
git commit -m "feat: delete comments through every provider"
```

---

### Task 2: Persistence And Provider-Aware DELETE Routes

**Files:**
- Modify: `internal/db/queries.go`
- Modify: `internal/db/queries_activity_test.go`
- Add: `internal/db/migrations/000039_comment_deletion_receipts.{up,down}.sql`
- Modify: `internal/server/huma_routes.go`
- Modify: `internal/server/provider_route_wrappers.go`
- Modify: `internal/server/api_test.go`
- Modify: `internal/github/sync.go`
- Modify: `internal/github/sync_test.go`
- Regenerate: `frontend/openapi/openapi.yaml`
- Regenerate: `packages/ui/src/api/generated/schema.ts`
- Regenerate: `internal/apiclient/generated/client.gen.go`

**Interfaces:**
- Consumes: Task 1 comment delete methods.
- Produces: `DB.DeleteMRCommentEvent(ctx, mrID, platformID) error`
- Produces: `DB.DeleteIssueCommentEvent(ctx, issueID, platformID) error`
- Produces: four DELETE routes returning HTTP 204.

- [ ] **Step 1: Write failing DB and HTTP tests**

The DB tests seed two comments and prove only the requested parent-scoped row is deleted. HTTP tests exercise default-host and host-prefixed paths, upstream failure preservation, and missing/non-comment IDs. Use the generated API client where it exposes the new operation after generation; before generation, use `srv.ServeHTTP` for RED.

```go
rr := doAPIRequest(t, srv, http.MethodDelete,
    "/api/v1/pulls/github/acme/widget/7/comments/44", nil)
assert.Equal(t, http.StatusNoContent, rr.Code)
assert.False(t, commentEventExists(t, database, mrID, 44))
```

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./internal/db ./internal/server -run 'Delete.*Comment' -shuffle=on
```

Expected: missing DB methods/routes and 404 responses.

- [ ] **Step 3: Implement scoped persistence deletion and handlers**

Delete the parent-scoped ordinary comment row idempotently after the provider confirms successful deletion:

```sql
DELETE FROM middleman_mr_events
WHERE merge_request_id = ? AND platform_id = ? AND event_type = 'issue_comment'
```

Register paired operations with `DefaultStatus: http.StatusNoContent` and add a typed `delete_comment` repository operation derived from comment mutation, write credentials, and REST rate state. Read/create the bounded deletion-attempt receipt inside the item lock before the provider call. Remove a receipt created by this request only after a typed, definitive provider rejection; retain it across success or ambiguous transport failure. A retry may reconcile typed not-found only after a dedicated unconditional comment refresh confirms absence, and a retained receipt makes a lost `204` retry idempotent. Serialize provider deletion/local writes with item detail synchronization on full provider identity so concurrent deletes coalesce and stale pre-delete fetches cannot restore the comment.

- [ ] **Step 4: Generate clients and review the contract**

```bash
make api-generate
git diff --check
git diff -- frontend/openapi/openapi.yaml packages/ui/src/api/generated/schema.ts internal/apiclient/generated/client.gen.go
```

Expected: paired PR/issue DELETE operations and no unrelated schema churn.

- [ ] **Step 5: Run tests and verify GREEN**

Run the command from Step 2. Expected: PASS.

- [ ] **Step 6: Commit API support**

```bash
git add internal/db internal/github/sync.go internal/github/sync_test.go internal/server internal/apiclient/generated/client.gen.go frontend/openapi/openapi.yaml packages/ui/src/api/generated/schema.ts
git commit -m "feat: expose provider-aware comment deletion"
```

---

### Task 3: Detail Store Delete Operations

**Files:**
- Modify: `packages/ui/src/stores/detail.svelte.ts`
- Modify: `frontend/src/lib/stores/detail-comment.svelte.test.ts`
- Modify: `packages/ui/src/stores/issues.svelte.ts`
- Modify: `frontend/src/lib/stores/issues-comment.svelte.test.ts`

**Interfaces:**
- Consumes: Task 2 generated DELETE paths.
- Produces: `detail.deleteComment(owner, name, number, commentID): Promise<boolean>`
- Produces: `issues.deleteIssueComment(owner, name, number, commentID): Promise<boolean>`

- [ ] **Step 1: Write failing store tests**

Assert route parameters include provider/host identity, success refreshes detail, and API failure returns false without refreshing while retaining the API error. Also cover DELETE success followed by refresh failure and retry, same-item success and failure generation changes, navigation during a failed DELETE, and another event type sharing the numeric platform ID.

```ts
await expect(store.deleteComment("acme", "widget", 7, 44)).resolves.toBe(true);
expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("/comments/44"), expect.objectContaining({ method: "DELETE" }));
```

- [ ] **Step 2: Run tests and verify RED**

```bash
cd frontend && ../node_modules/.bin/vp test src/lib/stores/detail-comment.svelte.test.ts src/lib/stores/issues-comment.svelte.test.ts
```

Expected: delete store methods are missing.

- [ ] **Step 3: Implement minimal store methods**

Capture the selected identity and generation before DELETE. Record a pending confirmation only after DELETE returns 204. Retries for that comment skip DELETE and repeat only the authoritative detail refresh. A same-item generation change must start a new refresh; navigation to another item skips assignment. Report success only when the refreshed timeline omits an `issue_comment` with the target platform ID.

```ts
async function deleteComment(owner: string, name: string, number: number, commentID: number): Promise<boolean> {
  const ref = currentDetailRef(owner, name, number);
  const deletionKey = commentDeletionKey(ref, commentID);
  // Run DELETE only when this key is not already awaiting confirmation.
  if (!isDetailShowingRef(ref)) return true;
  const refreshGen = ++syncGeneration;
  const refreshed = await refreshDetail(owner, name, number, refreshGen, ref);
  return refreshed.ok && !detail?.events.some(
    (event) => event.EventType === "issue_comment" && event.PlatformID === commentID,
  );
}
```

Mirror this for issues with `detailError` and `refreshIssueDetail`.

- [ ] **Step 4: Run tests and verify GREEN**

Run the command from Step 2. Expected: PASS.

- [ ] **Step 5: Commit stores**

```bash
git add packages/ui/src/stores frontend/src/lib/stores/detail-comment.svelte.test.ts frontend/src/lib/stores/issues-comment.svelte.test.ts
git commit -m "feat: refresh details after deleting comments"
```

---

### Task 4: Confirmed Timeline Deletion UI

**Files:**
- Modify: `packages/ui/src/components/detail/EventTimeline.svelte`
- Modify: `packages/ui/src/components/detail/EventTimeline.test.ts`
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`
- Modify: `packages/ui/src/components/detail/PullDetail.test.ts`
- Modify: `packages/ui/src/components/detail/IssueDetail.svelte`
- Modify: `packages/ui/src/components/detail/IssueDetail.test.ts`
- Modify: `frontend/tests/e2e/comment-editing.spec.ts`

**Interfaces:**
- Consumes: Task 3 store methods.
- Produces: `EventTimeline.onDeleteComment?: (event) => Promise<string | null>`.

- [ ] **Step 1: Write failing component tests**

Cover hidden action without callback, confirmation without immediate mutation, cancel, author/excerpt rendering, single-flight pending state, success close, and failure error:

```ts
const onDeleteComment = vi.fn().mockResolvedValue(null);
render(EventTimeline, { props: { events: [comment], onDeleteComment } });
await fireEvent.click(screen.getByRole("button", { name: "Delete comment" }));
expect(onDeleteComment).not.toHaveBeenCalled();
await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
await waitFor(() => expect(onDeleteComment).toHaveBeenCalledWith(comment));
```

- [ ] **Step 2: Run tests and verify RED**

```bash
./node_modules/.bin/vp test packages/ui/src/components/detail/EventTimeline.test.ts packages/ui/src/components/detail/PullDetail.test.ts packages/ui/src/components/detail/IssueDetail.test.ts
```

Expected: Delete comment action/dialog are absent.

- [ ] **Step 3: Implement the confirmation modal and callbacks**

Import `Trash2Icon`, `Modal`, and `Button`. Add reactive selected/pending/error state, a plain-text bounded excerpt helper, modal-stack registration, and an ordinary-comment `canDeleteComment` predicate. Catch rejected callbacks as inline errors and render the trash action in each ordinary comment action layout and a kit-ui modal:

```svelte
{#if deleteTarget}
  <Modal title="Delete comment?" onclose={cancelDelete}>
    <p>Delete {deleteTarget.Author || "Unknown"}'s comment?</p>
    <blockquote>{commentExcerpt(deleteTarget.Body)}</blockquote>
    <p>This cannot be undone.</p>
    {#if deleteError}<p class="delete-error">{deleteError}</p>{/if}
    {#snippet footer()}
      <Button onclick={cancelDelete} disabled={deletingId !== null}>Cancel</Button>
      <Button tone="danger" onclick={() => void confirmDelete()} disabled={deletingId !== null}>
        {deletingId !== null ? "Deleting..." : "Delete"}
      </Button>
    {/snippet}
  </Modal>
{/if}
```

Wire `PullDetail.editTimelineComment`-adjacent and `IssueDetail.editTimelineComment`-adjacent delete callbacks to their stores. Pass `onDeleteComment` under the comment capability, stale-detail check, and distinct `delete_comment` operation gate.

- [ ] **Step 4: Run Svelte autofix and targeted tests**

```bash
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer packages/ui/src/components/detail/EventTimeline.svelte
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer packages/ui/src/components/detail/PullDetail.svelte
./node_modules/.bin/vp exec svelte-mcp svelte-autofixer packages/ui/src/components/detail/IssueDetail.svelte
./node_modules/.bin/vp test packages/ui/src/components/detail/EventTimeline.test.ts packages/ui/src/components/detail/PullDetail.test.ts packages/ui/src/components/detail/IssueDetail.test.ts
```

Expected: autofixer reports no actionable issues; tests PASS.

- [ ] **Step 5: Run full affected verification**

```bash
go test ./internal/platform/... ./internal/github ./internal/db ./internal/server -shuffle=on
./node_modules/.bin/vp test
```

Run the affected Playwright mock lane. Expected: PASS with no new warnings.

```bash
cd frontend && ../node_modules/.bin/playwright test tests/e2e/comment-editing.spec.ts
```

- [ ] **Step 6: Commit UI**

```bash
git add packages/ui/src frontend/tests/e2e/comment-editing.spec.ts
git commit -m "feat: confirm deletion of timeline comments"
```

---

### Task 5: Final Contract And Context Verification

**Files:**
- Review: all files changed by Tasks 1-4
- Modify: context docs only if implementation introduces a durable invariant not already covered

**Interfaces:**
- Consumes: completed feature.
- Produces: clean worktree and verified final commit set.

- [ ] **Step 1: Verify generated artifacts and worktree**

```bash
make api-generate
git diff --check
git status --short
```

Expected: API generation creates no new diff; only intentional files are present.

- [ ] **Step 2: Run repository-required final checks**

```bash
make test-short
make vet
./node_modules/.bin/vp test
```

Expected: PASS.

- [ ] **Step 3: Run context-sync decision gate**

```bash
scripts/context-sync --check
```

If no topic context changed, mark that the provider-neutral comment mutation invariant already covers the implementation and the feature design/spec owns feature-specific details.

- [ ] **Step 4: Commit any final generated or context correction**

Create a new commit only if Step 1 or Step 3 produced an intentional diff. Never amend and never bypass hooks.
