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
6. After success, the deleted card disappears immediately and ordinary detail synchronization converges SQLite to provider state.
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
- leave persisted events unchanged until ordinary synchronization observes provider state.

Return `204 No Content` on success. Use the existing stable problem envelopes for unsupported capability, missing repository/item/comment, provider rejection, rate limits, and internal persistence failure. Regenerate the OpenAPI document and Go/TypeScript clients after adding the operations.

Deletion is eventually complete: a successful provider response hides the selected card locally and starts the existing detail sync. SQLite remains provider-derived, so bulk or periodic refreshes continue normally and later authoritative synchronization removes the persisted event and updates its parent count. Failed provider deletion leaves the card and stored event unchanged.

Removing a persisted event decrements `comment_count` transactionally and never below zero. Authoritative replacement collapses duplicate `dedupe_key` identities with the last normalized event winning, then derives `comment_count` from the stored rows inside the same transaction. Comment-only replacement does not rewrite `review_decision`; GitHub may advance `last_activity_at` from authoritative comment timestamps, while provider-neutral recovery leaves it unchanged.

### Implementation Stages

1. Map provider deletion responses through the normal mutation error path.
2. Add atomic PR and issue comment replacement; database tests prove rollback, duplicate-identity counting, and preservation of unrelated parent fields.
3. Add PR and issue comment-only readers for every provider and verify they do not depend on commits, reviews, or aggregate timelines.
4. Keep PR and issue handlers provider-backed without delete-specific SQLite state.
5. Wire frontend confirmation as separate DELETE and refresh phases, with store/component coverage for retry and navigation safety.
6. Prove provider-to-HTTP-to-SQLite recovery for both item types, including final events and `comment_count`, then run the full affected frontend and browser suites.

## UI Design

Add a trash icon button beside Edit in every ordinary comment action-group layout, including threaded and compact renderings. Its accessible name and tooltip are `Delete comment`.

Use the shared in-app confirmation-dialog treatment rather than `window.confirm`. The dialog title is `Delete comment?`, the destructive action is `Delete`, and the pending label is `Deleting...`. The body includes the author and a bounded plain-text excerpt so markdown, HTML, or an unusually long comment cannot expand the dialog or be rendered as active content.

The timeline owns the selected event and pending/error state. `PullDetail` and `IssueDetail` provide provider-aware delete callbacks backed by their existing detail stores. After DELETE returns 204, the store hides that comment ID immediately and starts the existing detail sync; stale responses keep the card hidden until synchronization no longer returns it. Provider failure preserves the card and exposes the stable API error detail.

Middleman has no provider-neutral authenticated-user identity in timeline payloads, so it exposes deletion for ordinary provider comments and leaves ownership and permission enforcement to the provider. A rejected attempt must be non-destructive and explain the provider failure.

## Error And Concurrency Behavior

- Cancel and dialog dismissal perform no mutation.
- Confirm is single-flight for the selected comment.
- The selected comment cannot enter edit mode while its delete is pending.
- A failed deletion keeps the confirmation open and displays an inline error.
- A successful deletion closes the dialog after the local card is hidden; synchronization completes in the background.
- A same-item generation change starts a new authoritative refresh; navigation or component teardown may discard local dialog state, but stale results and errors must not overwrite the newly selected detail.
- Temporary SQLite staleness is acceptable after provider success; the hidden card prevents visual reappearance while ordinary synchronization converges.

## Testing

Use test-driven changes at the smallest boundaries that establish the contract:

- Provider tests verify the correct native delete endpoint, identifier, method, write credential, and error mapping for GitHub, GitLab, Forgejo, and Gitea.
- Server HTTP tests verify PR and issue deletion, host-prefixed routing, capability gating, comment-to-parent validation, provider failure preservation, unchanged immediate SQLite state, and the `204` response.
- Store tests verify generated-client route construction, immediate card suppression, stale-sync filtering, provider failure reporting, and navigation safety.
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
