# Comment Deletion Design

**Date:** 2026-07-10
**Goal:** Let maintainers delete provider-authorized pull request and issue comments from the activity timeline.

## Context

Middleman already creates and edits provider comments through the provider-neutral comment mutation capability. The shared activity timeline exposes edit, direct-link, and copy actions, but it has no deletion action.

Deletion must remove the provider comment rather than hide a local event. The provider remains authoritative for whether the authenticated user may delete a given comment; middleman must surface a rejected deletion without removing the local timeline entry.

## Requirements

1. Pull request and issue timeline comments support provider-backed deletion across every supported provider whose comment mutation capability is enabled.
2. A trash action appears in the existing ordinary-comment action group and uses a distinct `delete_comment` operation-availability verdict.
3. Selecting Delete opens an in-app confirmation dialog. No provider request is made until the user confirms.
4. The dialog identifies the selected comment with its author and a short, plain-text excerpt, states that deletion cannot be undone, and offers Cancel and Delete actions.
5. While deletion is pending, the dialog remains open, its actions cannot be submitted twice, and mutation actions for that comment are disabled.
6. After success, middleman refreshes the current detail timeline and the deleted comment disappears.
7. After failure, the comment remains visible, the dialog remains available for retry or cancellation, and the provider-derived error is shown without replacing stable API error handling with prose matching.
8. PR and issue routes retain the full provider and host identity and use the shared frontend provider-route helpers.
9. Review-draft comments and published inline review comments are outside this feature; their lifecycle and provider APIs differ from ordinary PR/issue timeline comments.

## Provider And API Design

Extend `platform.CommentMutator` with separate provider-neutral operations for deleting a merge-request comment and deleting an issue comment. Each implementation calls its provider's native deletion API and returns an error only; a successful delete has no replacement event to normalize.

Add paired default-host and host-prefixed routes:

```text
DELETE /api/v1/pulls/{provider}/{owner}/{name}/{number}/comments/{comment_id}
DELETE /api/v1/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/comments/{comment_id}
DELETE /api/v1/issues/{provider}/{owner}/{name}/{number}/comments/{comment_id}
DELETE /api/v1/host/{platform_host}/issues/{provider}/{owner}/{name}/{number}/comments/{comment_id}
```

Handlers must:

- require `comment_mutation` at the repository and provider-interface boundaries;
- resolve the repository and parent item with full provider identity;
- prove the comment ID belongs to the requested PR or issue using the persisted event before calling the provider;
- call the provider deletion operation; and
- remove the persisted comment event only after provider success.

Return `204 No Content` on success. Use the existing stable problem envelopes for unsupported capability, missing repository/item/comment, provider rejection, rate limits, and internal persistence failure. Regenerate the OpenAPI document and Go/TypeScript clients after adding the operations.

Provider not-found is not proof of absence: write credentials can conceal authorization failures as 404, especially when read and write credentials differ. Inside the item write lock and before the first provider call, persist a deletion-attempt receipt keyed by repository, item type/number, and comment ID. Remove a newly created receipt only for an explicit provider-neutral rejection code (authorization, validation, not-found, rate, stale-state, or conflict); retain it after success, unclassified provider errors, 5xx responses, or transport/cancellation errors. Providers mark a mutation outcome uncertain when no authoritative response exists or a response cannot prove whether the mutation was applied; for GitLab this includes 5xx, 408, 425, and non-standard 499 responses, while other explicit 4xx rejections are definitive. The uncertainty marker takes precedence over any generic platform error code wrapped around the transport failure. A retry with a retained receipt may reconcile typed not-found only through the provider-neutral `CommentReader`, which performs a dedicated unconditional read-credential comment refresh; unrelated detail, review, CI, timeline, commit, and ETag outcomes do not participate. Receipts are valid for 30 days (enforced on lookup) so a lost `204` is idempotent, then are pruned opportunistically. Expiry ends this special retry window: a later DELETE is treated as a new attempt, while ordinary authoritative synchronization remains responsible for removing provider-absent comments. This receipt records operation identity, not a hidden-comment tombstone.

Removing a persisted event decrements `comment_count` transactionally and never below zero. `last_activity_at` remains provider-authored metadata and is reconciled by the next authoritative sync rather than guessed from the remaining local event subset.

Implementation order is: preserve provider outcome certainty; add atomic comment-and-count replacement; expose comment-only reads; integrate receipt recovery under the item lock; then prove the complete provider-to-HTTP-to-SQLite path with provider-specific and provider-neutral tests.

## UI Design

Add a trash icon button beside Edit in every ordinary comment action-group layout, including threaded and compact renderings. Its accessible name and tooltip are `Delete comment`.

Use the shared in-app confirmation-dialog treatment rather than `window.confirm`. The dialog title is `Delete comment?`, the destructive action is `Delete`, and the pending label is `Deleting...`. The body includes the author and a bounded plain-text excerpt so markdown, HTML, or an unusually long comment cannot expand the dialog or be rendered as active content.

The timeline owns the selected event and pending/error state. `PullDetail` and `IssueDetail` provide provider-aware delete callbacks backed by their existing detail stores. The stores model provider/local deletion and UI confirmation as separate phases: after DELETE returns 204, retries repeat only the authoritative detail GET until the ordinary `issue_comment` event is absent. On failure before 204, the stores preserve the current state and expose the stable API error detail for the dialog.

Middleman has no provider-neutral authenticated-user identity in timeline payloads, so it exposes deletion for ordinary provider comments and leaves ownership and permission enforcement to the provider. A rejected attempt must be non-destructive and explain the provider failure.

## Error And Concurrency Behavior

- Cancel and dialog dismissal perform no mutation.
- Confirm is single-flight for the selected comment.
- The selected comment cannot enter edit mode while its delete is pending.
- A failed deletion keeps the confirmation open and displays an inline error. If DELETE already returned 204 but confirmation refresh failed, retry performs only the safe detail refresh.
- A successful deletion closes the dialog only after the refreshed timeline no longer contains the event.
- A same-item generation change starts a new authoritative refresh; navigation or component teardown may discard local dialog state, but stale results and errors must not overwrite the newly selected detail.
- Provider deletion/local reconciliation and detail synchronization serialize on the full provider-scoped item identity, preventing a pre-delete fetch from upserting stale comments after deletion commits.

## Testing

Use test-driven changes at the smallest boundaries that establish the contract:

- Provider tests verify the correct native delete endpoint, identifier, method, write credential, and error mapping for GitHub, GitLab, Forgejo, and Gitea.
- Server HTTP tests verify PR and issue deletion, host-prefixed routing, capability gating, comment-to-parent validation, provider failure preservation (including first-attempt not-found), ambiguous-success reconciliation, lost-response retries, subsequent detail retrieval, and the `204` response.
- Store tests verify generated-client route construction, two-phase refresh retry, event-type-aware confirmation, refresh failure reporting, and same-item/navigation generation safety.
- `EventTimeline` component tests verify action eligibility, cancellation, comment identification, single-flight confirmation, success, and failure display across representative timeline layouts.
- An affected browser or full-stack test verifies the visible confirmation-and-removal workflow without duplicating backend authorization coverage.

Run API generation and review all checked-in artifacts. Run Svelte autofix on every edited Svelte component, targeted shuffled Go tests, the relevant component/store tests, the full frontend Vitest suite, and the affected browser or Playwright suite required by the final frontend changes.

## Non-Goals

- Hiding a comment only in middleman's SQLite state.
- Bypassing the provider's authenticated-user ownership and permission checks.
- Deleting review-draft comments, review-thread comments, reviews, system events, or comment-deletion timeline events.
- Adding authenticated-user identity to every timeline response solely to hide unauthorized delete buttons.
- Undo or restoration after provider deletion.
- Compatibility routes, aliases, or provider-specific frontend paths.
