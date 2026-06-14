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

Gitea and Forgejo expose `html_url` on issue comments and timeline comments in their Swagger-generated API schemas. Middleman should store that provider-supplied URL rather than reconstructing it.

## Requirements

1. PR and issue comment cards show a direct-link copy action only when a direct URL is available.
2. The action is visible on comment-card hover and keyboard focus, matching the existing timeline action behavior.
3. Clicking the action copies the provider browser URL for the specific comment, not the comment body.
4. The existing copy-body behavior remains available and unchanged.
5. The event API exposes the direct URL consistently for PR and issue events using the existing PascalCase event response style.
6. GitHub, Gitea, and Forgejo use provider-supplied `html_url` values when available.
7. GitLab computes the direct URL from the stored parent item URL and the note ID as `#note_<note_id>`.
8. Events without a known provider comment URL do not render the direct-link action.

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

## Provider Normalization

### GitHub

`NormalizeCommentEvent` and `NormalizeIssueCommentEvent` should copy `IssueComment.GetHTMLURL()` into `DirectURL`.

`NormalizeReviewCommentEvent` should copy `PullRequestComment.GetHTMLURL()` into `DirectURL`.

### GitLab

GitLab note normalization should compute direct URLs only for non-system comment notes:

- MR notes: `{merge_request.URL}#note_{note.ID}`
- Issue notes: `{issue.URL}#note_{note.ID}`

The normalizer currently receives `platform.RepoRef` and item number, not the persisted parent item URL. The implementation should add the parent item URL where events are normalized or add a small post-normalization enrichment step in the sync/server path that has access to the persisted MR or issue row. Prefer the smallest change that keeps GitLab URL construction out of Svelte.

If the parent URL is empty, leave `DirectURL` empty.

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

Update Gitea and Forgejo converters to copy `comment.HTMLURL` and `timeline.HTMLURL`. Then set `DirectURL` during `NormalizeIssueComments`, `NormalizeMergeRequestEvents`, `NormalizeIssueTimelineEvents`, and `NormalizeMergeRequestTimelineEvents` for comment-like events.

## UI Design

Use the current timeline action treatment in `packages/ui/src/components/detail/EventTimeline.svelte`.

For comments with `DirectURL`:

- Render a link icon button in the existing hover action group.
- Accessible label: `Copy direct link`.
- Tooltip/title before copy: `Copy direct link`.
- Tooltip/title after copy: `Copied!`.
- Copy `event.DirectURL` via the existing clipboard helper.

The action should appear beside the existing copy-body button. It should remain hidden until hover or focus-visible because this is an expert affordance, not primary timeline content.

Use a lucide icon such as `LinkIcon` or `Link2Icon`. Do not add text inside the action button.

## Error Handling

Clipboard failures should follow the current copy-body behavior. If `copyToClipboard` returns false, do not show a success state.

No server error should be introduced for missing direct URLs. Missing or unsupported URLs are represented by an empty string.

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
- Run the affected frontend component tests.
- Run targeted Go tests with `-shuffle=on`.
- Because this changes visible frontend behavior, run the affected Playwright e2e suite before pushing.

## Non-Goals

- Do not open provider links from the button. The requested behavior is copy-to-clipboard.
- Do not add direct-link actions for commits, system events, force pushes, or deleted comments.
- Do not add a compatibility route or frontend URL reconstruction layer.
- Do not delete, resolve, hide, or otherwise change provider comments.
