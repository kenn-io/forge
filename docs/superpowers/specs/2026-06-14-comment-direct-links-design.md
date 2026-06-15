# Comment Direct Links Design

**Date:** 2026-06-14
**Goal:** Let maintainers copy the provider browser link for a PR or issue comment directly from the timeline comment card.

## Context

Middleman already renders PR and issue timeline comments in `EventTimeline.svelte` with hover-only action buttons for edit and copy-body. The requested behavior is a second, quiet action that copies the direct provider link for the specific comment.

The feature must remain provider-neutral. Middleman supports multiple forges, so the frontend should not hand-build GitHub-like anchors. The server should expose a resolved `DirectURL` API field per event when a provider can identify a browser target for that event.

## Provider Findings

GitHub issue comments and pull request review comments include `html_url` in REST API responses. Issue comments apply to both issues and pull request timeline comments.

GitLab Notes and Discussions expose note IDs and discussion IDs but do not document a note-level browser URL field. The browser link equivalent is the parent issue or merge request URL plus a naked anchor fragment:

```text
#note_<note_id>
```

For example:

```text
https://gitlab.example.com/group/project/-/merge_requests/12#note_345678
```

Gitea and Forgejo expose `html_url` on issue comments in their Swagger-generated API schemas. Gitea also has the existing timeline transport used by middleman; Forgejo currently only needs the comment DTO path unless a Forgejo timeline transport is added separately. Middleman should store provider-supplied URLs rather than reconstructing them.

## Requirements

1. PR and issue comment cards show a direct-link copy action only when a direct URL is available.
2. The action is visible on comment-card hover and keyboard focus, matching the existing timeline action behavior.
3. Clicking the action copies the provider browser URL for the specific comment, not the comment body.
4. The existing copy-body behavior remains available and unchanged.
5. The event API exposes the direct URL consistently for PR and issue events using the existing PascalCase event response style.
6. GitHub, Gitea, and Forgejo use provider-supplied `html_url` values when available.
7. GitLab computes the direct URL from the stored parent item URL and the note ID as `#note_<note_id>`.
8. Events without a known provider comment URL do not render the direct-link action.
9. Existing rows are not backfilled by the migration. Direct links for already-synced comments appear after the next provider sync or mutation/detail refresh that sees the comment URL.
10. Provider-supplied direct URLs are treated as provider data and copied as text only. The UI must not navigate to them from this action.

## Data Model

Add `direct_url` to both event tables:

```sql
ALTER TABLE middleman_mr_events ADD COLUMN direct_url TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_issue_events ADD COLUMN direct_url TEXT NOT NULL DEFAULT '';
```

Add matching fields:

- `platform.MergeRequestEvent.DirectURL`
- `platform.IssueEvent.DirectURL`
- `db.MREvent.DirectURL`
- `db.IssueEvent.DirectURL`
- `mergeRequestEventResponse.DirectURL`

Issue detail currently returns `[]db.IssueEvent`, so the `json:"DirectURL"` default shape is sufficient unless the API response types are converted to explicit JSON tags in the implementation.

The upsert path should refresh `direct_url` on conflict, because provider URLs can become available after a later sync or mutation response. An empty incoming value should not clear a stored non-empty URL unless the implementation proves provider deletion or invalidation requires that behavior. This mirrors the existing pattern of preserving useful provider metadata when newer partial responses lack it.

Acceptance criteria for the API shape:

- PR/MR detail event payloads include `DirectURL` in `mergeRequestEventResponse`.
- Issue detail event payloads include `DirectURL` in the existing PascalCase issue event shape.
- Generated OpenAPI, Go API client, and TypeScript schema artifacts include the field.

## Provider Normalization

### GitHub

`NormalizeCommentEvent` and `NormalizeIssueCommentEvent` should copy `IssueComment.GetHTMLURL()` into `DirectURL`.

`NormalizeReviewCommentEvent` should copy `PullRequestComment.GetHTMLURL()` into `DirectURL`.

### GitLab

GitLab note normalization should compute direct URLs only for non-system comment notes:

- MR notes: `{merge_request.URL}#note_{note.ID}`
- Issue notes: `{issue.URL}#note_{note.ID}`

The canonical contract is that GitLab note normalizers accept a `parentURL` argument. Every caller that returns or stores GitLab non-system note events must pass the persisted or normalized MR/issue browser URL for the parent item. If the caller does not have that URL, it must pass an empty string and the normalizer leaves `DirectURL` empty. Do not add a separate post-normalization enrichment path unless this contract proves insufficient.

If the parent URL is empty, leave `DirectURL` empty.

For mutation responses that create or edit comments, the immediate response may not include a browser URL. In that case, keep `DirectURL` empty and rely on the next detail refresh or sync that normalizes provider notes with a parent URL. The UI must continue to omit the direct-link action until the field is present.

### Gitea And Forgejo

Extend gitealike DTOs with `HTMLURL`:

```go
type CommentDTO struct {
    ID      int64
    HTMLURL string
    User    UserDTO
    Body    string
    Created time.Time
    Updated time.Time
}

type TimelineEventDTO struct {
    ID      int64
    HTMLURL string
    // existing fields...
}
```

Update Gitea and Forgejo comment converters to copy `comment.HTMLURL`. Update Gitea timeline converters to copy `timeline.HTMLURL`. Then set `DirectURL` during `NormalizeIssueComments`, `NormalizeMergeRequestEvents`, `NormalizeIssueTimelineEvents`, and `NormalizeMergeRequestTimelineEvents` for comment-like events when those DTO fields exist. Forgejo timeline direct URLs remain unsupported unless a Forgejo timeline transport is explicitly introduced.

## UI Design

Use the current timeline action treatment in `packages/ui/src/components/detail/EventTimeline.svelte`.

For comments with `DirectURL`:

- Render a link icon button in the existing hover action group.
- Accessible label: `Copy direct link`.
- Tooltip/title before copy: `Copy direct link`.
- Tooltip/title after copy: `Copied!`.
- Copy `event.DirectURL` via the existing clipboard helper.
- Track copied state separately from the existing body-copy button, keyed by both event ID and action, so copying the direct link does not mark the body-copy action as copied.

The action should appear beside the existing copy-body button. It should remain hidden until hover or focus-visible because this is an expert affordance, not primary timeline content.

Use a lucide icon such as `LinkIcon` or `Link2Icon`. Do not add text inside the action button.

## Error Handling

Clipboard failures should follow the current copy-body behavior. If `copyToClipboard` returns false, do not show a success state.

No server error should be introduced for missing direct URLs. Missing or unsupported URLs are represented by an empty string.

When copying, the frontend treats the server-provided `DirectURL` only as clipboard text and never navigates to it. Provider normalization should not invent URLs for providers or event types that do not expose a known browser target.

## Implementation Order

1. Add the event schema migration, DB fields, query persistence, and conflict behavior.
2. Thread `DirectURL` through provider-neutral platform types and persistence helpers.
3. Add provider normalization for GitHub, GitLab, Gitea, and the currently available Forgejo comment path.
4. Confirm provider mutation responses either set `DirectURL` through the same normalizer contract or intentionally return it empty until a refresh can normalize provider note data.
5. Map `DirectURL` through server detail responses and regenerate API artifacts.
6. Add the Svelte timeline action using generated types and action-specific copied state.
7. Add backend, server e2e, frontend component, and affected browser e2e coverage.

## Testing

Backend tests:

- GitHub normalizer tests prove issue comments and review comments set `DirectURL`.
- GitLab normalizer or enrichment tests prove `#note_<id>` direct URLs for MR and issue notes when parent URLs are present, and empty URLs when parent URLs are absent.
- Gitea and Forgejo converter/normalizer tests prove `html_url` is carried into `DirectURL`.
- DB query tests prove `direct_url` is inserted, updated when non-empty, preserved when a later partial event has no URL, and returned by list queries.
- Server e2e tests prove PR and issue detail payloads include the direct URL for seeded comment events.

Frontend tests:

- `EventTimeline` renders no direct-link copy action when `DirectURL` is empty.
- `EventTimeline` renders `Copy direct link` when `DirectURL` is present.
- Clicking `Copy direct link` writes the URL to the clipboard helper and does not copy the comment body.
- Existing `Copy comment` behavior still copies the body.

Verification:

- Run Svelte autofixer for `EventTimeline.svelte` after edits.
- Run `make api-generate` after server/API type changes.
- Run the affected frontend component tests.
- Run targeted Go tests with `-shuffle=on`.
- Because this changes visible frontend behavior, run the affected Playwright e2e suite before pushing.

## Non-Goals

- Do not open provider links from the button. The requested behavior is copy-to-clipboard.
- Do not add direct-link actions for commits, system events, force pushes, or deleted comments.
- Do not add a compatibility route or frontend URL reconstruction layer.
- Do not delete, resolve, hide, or otherwise change provider comments.
