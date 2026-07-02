# Inline PR Diff Review Comments

## Summary

Implement inline PR diff review comments as a git-spice stack. The stack must first introduce a provider-neutral local review-draft abstraction that compiles with all providers through explicit unsupported stubs. Provider implementations then land one at a time, starting with Forgejo because the repo already has a Docker Compose-backed Forgejo integration fixture.

The intended user workflow:

- Select a single diff line or contiguous same-side range.
- Add staged inline draft comments.
- Review staged comments in a draft tray.
- Publish the staged review as `comment`, `approve`, or `request_changes` when the active provider supports that action.
- See published inline review parts in the selected PR activity/conversation timeline.
- Resolve/dismiss published inline review parts where the active provider supports thread resolution.

Workspace diffs reuse the same diff renderer but run with review mode disabled. No review draft store may be active for workspace diffs.

## Git-Spice Stack

Use git-spice to split the work into dependent branches. Do not create branch names with a `codex/` prefix.

Recommended stack:

```text
main
└── inline-review-baseline
    └── inline-review-forgejo
        └── inline-review-gitlab
            └── inline-review-github
```

Commands:

```bash
gs branch create inline-review-baseline
# implement and commit baseline

gs branch create inline-review-forgejo
# implement and commit Forgejo provider

gs branch create inline-review-gitlab
# implement and commit GitLab provider

gs branch create inline-review-github
# implement and commit GitHub provider
```

Use `gs log short` before submitting. Use `gs stack submit` to submit the stack. If a lower branch changes after review, use `gs upstack restack`; do not manually rebase stacked branches.

## Stack Part 1: Baseline Common Abstraction

Branch: `inline-review-baseline`

Goal: add the common data model, API surface, UI surface, and compile-time provider interfaces without enabling provider-specific mutation support.

This branch must compile and pass tests with all providers present. It must not advertise inline review capabilities for Forgejo, Gitea, GitLab, or GitHub until their provider branches implement and test them.

### Baseline Backend Scope

Add provider-neutral interfaces in `internal/platform/client.go`:

```go
type DiffReviewDraftMutator interface {
    PublishDiffReviewDraft(ctx context.Context, ref RepoRef, number int, input PublishDiffReviewDraftInput) (*PublishedDiffReview, error)
}

type DiffReviewThreadResolver interface {
    ResolveDiffReviewThread(ctx context.Context, ref RepoRef, number int, providerThreadID string) error
    UnresolveDiffReviewThread(ctx context.Context, ref RepoRef, number int, providerThreadID string) error
}

type MergeRequestReviewThreadReader interface {
    ListMergeRequestReviewThreads(ctx context.Context, ref RepoRef, number int) ([]MergeRequestReviewThread, error)
}
```

Do not put create/edit/delete draft-comment methods on provider interfaces. Staged comments are local in v1. Providers only need to publish a complete local draft and resolve published threads.

Add provider-neutral structs in `internal/platform/types.go`:

```go
type ReviewAction string

const (
    ReviewActionComment        ReviewAction = "comment"
    ReviewActionApprove        ReviewAction = "approve"
    ReviewActionRequestChanges ReviewAction = "request_changes"
)

type DiffReviewLineRange struct {
    Path        string
    OldPath     string
    Side        string
    StartSide   string
    StartLine   *int
    Line        int
    OldLine     *int
    NewLine     *int
    LineType    string
    DiffHeadSHA string
    CommitSHA   string
}

type LocalDiffReviewDraftComment struct {
    ID        int64
    Body      string
    Range     DiffReviewLineRange
    CreatedAt time.Time
    UpdatedAt time.Time
}

type PublishDiffReviewDraftInput struct {
    Body     string
    Action   ReviewAction
    Comments []LocalDiffReviewDraftComment
}

type PublishedDiffReview struct {
    ProviderReviewID string
    SubmittedAt      time.Time
}

type MergeRequestReviewThread struct {
    ProviderThreadID  string
    ProviderReviewID  string
    ProviderCommentID string
    Body              string
    AuthorLogin       string
    Range             DiffReviewLineRange
    Resolved          bool
    CreatedAt         time.Time
    UpdatedAt         time.Time
    ResolvedAt        *time.Time
    MetadataJSON      string
}
```

Add registry/syncer accessors:

- `Registry.DiffReviewDraftMutator`
- `Registry.DiffReviewThreadResolver`
- `Registry.MergeRequestReviewThreadReader`
- matching `Syncer` accessors

Providers that do not implement an interface must return `platform.UnsupportedCapability`, following existing registry patterns.

### Baseline Capabilities

Extend `internal/platform/types.go` capabilities:

```go
ReviewDraftMutation    bool
ReviewThreadResolution bool
ReadReviewThreads      bool
NativeMultilineRanges  bool
SupportedReviewActions []ReviewAction
```

Extend `internal/server/api_types.go` provider capability response:

```go
ReviewDraftMutation    bool     `json:"review_draft_mutation"`
ReviewThreadResolution bool     `json:"review_thread_resolution"`
ReadReviewThreads      bool     `json:"read_review_threads"`
NativeMultilineRanges  bool     `json:"native_multiline_ranges"`
SupportedReviewActions []string `json:"supported_review_actions"`
```

Add boolean capability constants in `internal/server/capabilities.go`:

```go
capabilityReviewDraftMutation = "review_draft_mutation"
capabilityReviewThreadResolution = "review_thread_resolution"
capabilityReadReviewThreads = "read_review_threads"
```

Server-side publish validation must:

- require `ReviewDraftMutation`
- reject unknown actions with `400`
- reject actions not present in `SupportedReviewActions` with `400`
- treat missing `SupportedReviewActions` as no supported actions
- return `409 Conflict` when a draft comment's `diff_head_sha` does not match the current MR `DiffHeadSHA`

Baseline capabilities for all providers stay disabled until their provider branch turns them on.

### Baseline Persistence

Local draft persistence is mandatory in v1. Provider-native pending drafts are a later optimization.

Add migrations:

- `internal/db/migrations/000020_diff_review_drafts.up.sql`
- `internal/db/migrations/000020_diff_review_drafts.down.sql`

Tables:

```sql
CREATE TABLE middleman_mr_review_drafts (
    id INTEGER PRIMARY KEY,
    merge_request_id INTEGER NOT NULL REFERENCES middleman_merge_requests(id) ON DELETE CASCADE,
    body TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL DEFAULT 'comment',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(merge_request_id)
);

CREATE TABLE middleman_mr_review_draft_comments (
    id INTEGER PRIMARY KEY,
    draft_id INTEGER NOT NULL REFERENCES middleman_mr_review_drafts(id) ON DELETE CASCADE,
    body TEXT NOT NULL,
    path TEXT NOT NULL,
    old_path TEXT,
    side TEXT NOT NULL,
    start_side TEXT,
    start_line INTEGER,
    line INTEGER NOT NULL,
    old_line INTEGER,
    new_line INTEGER,
    line_type TEXT NOT NULL,
    diff_head_sha TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE middleman_mr_review_threads (
    id INTEGER PRIMARY KEY,
    merge_request_id INTEGER NOT NULL REFERENCES middleman_merge_requests(id) ON DELETE CASCADE,
    provider_thread_id TEXT NOT NULL,
    provider_review_id TEXT,
    provider_comment_id TEXT,
    path TEXT NOT NULL,
    old_path TEXT,
    side TEXT NOT NULL,
    start_side TEXT,
    start_line INTEGER,
    line INTEGER NOT NULL,
    old_line INTEGER,
    new_line INTEGER,
    line_type TEXT NOT NULL,
    diff_head_sha TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    body TEXT NOT NULL,
    author_login TEXT,
    resolved BOOLEAN NOT NULL DEFAULT false,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resolved_at TEXT,
    metadata_json TEXT,
    UNIQUE(merge_request_id, provider_thread_id)
);

CREATE INDEX idx_mr_review_draft_comments_draft_id
    ON middleman_mr_review_draft_comments(draft_id);

CREATE INDEX idx_mr_review_threads_mr_id
    ON middleman_mr_review_threads(merge_request_id);
```

Add DB query methods:

- `GetOrCreateMRReviewDraft(ctx, mrID int64) (*MRReviewDraft, error)`
- `GetMRReviewDraft(ctx, mrID int64) (*MRReviewDraft, error)`
- `ListMRReviewDraftComments(ctx, draftID int64) ([]MRReviewDraftComment, error)`
- `CreateMRReviewDraftComment(ctx, draftID int64, input MRReviewDraftCommentInput) (*MRReviewDraftComment, error)`
- `UpdateMRReviewDraftComment(ctx, draftID, commentID int64, input MRReviewDraftCommentInput) (*MRReviewDraftComment, error)`
- `DeleteMRReviewDraftComment(ctx, draftID, commentID int64) error`
- `DeleteMRReviewDraft(ctx, mrID int64) error`
- `UpsertMRReviewThreads(ctx, mrID int64, threads []MRReviewThread) error`
- `ListMRReviewThreads(ctx, mrID int64) ([]MRReviewThread, error)`
- `GetMRReviewThread(ctx, mrID, threadID int64) (*MRReviewThread, error)`
- `SetMRReviewThreadResolved(ctx, mrID, threadID int64, resolved bool, resolvedAt *time.Time) error`

All stored and emitted timestamps are UTC.

### Baseline API Routes

Preserve the existing Huma route shape. The backend API is mounted under `/api/v1`, but registered paths omit `/api/v1`.

Default-host paths:

```http
GET    /pulls/{provider}/{owner}/{name}/{number}/review-draft
POST   /pulls/{provider}/{owner}/{name}/{number}/review-draft/comments
PATCH  /pulls/{provider}/{owner}/{name}/{number}/review-draft/comments/{draft_comment_id}
DELETE /pulls/{provider}/{owner}/{name}/{number}/review-draft/comments/{draft_comment_id}
POST   /pulls/{provider}/{owner}/{name}/{number}/review-draft/publish
DELETE /pulls/{provider}/{owner}/{name}/{number}/review-draft
POST   /pulls/{provider}/{owner}/{name}/{number}/review-threads/{thread_id}/resolve
POST   /pulls/{provider}/{owner}/{name}/{number}/review-threads/{thread_id}/unresolve
```

Host-qualified variants:

```http
GET    /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft
POST   /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft/comments
PATCH  /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft/comments/{draft_comment_id}
DELETE /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft/comments/{draft_comment_id}
POST   /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft/publish
DELETE /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft
POST   /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-threads/{thread_id}/resolve
POST   /host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-threads/{thread_id}/unresolve
```

Operation IDs:

```go
get-pr-review-draft
get-pr-review-draft-on-host
create-pr-review-draft-comment
create-pr-review-draft-comment-on-host
edit-pr-review-draft-comment
edit-pr-review-draft-comment-on-host
delete-pr-review-draft-comment
delete-pr-review-draft-comment-on-host
publish-pr-review-draft
publish-pr-review-draft-on-host
discard-pr-review-draft
discard-pr-review-draft-on-host
resolve-pr-review-thread
resolve-pr-review-thread-on-host
unresolve-pr-review-thread
unresolve-pr-review-thread-on-host
```

Draft comment and thread path params are internal DB IDs. Provider IDs never appear in public response bodies unless a concrete UI need is added later.

`publish` behavior in baseline:

- load local draft and comments from DB
- validate diff staleness
- validate action against capabilities
- call `DiffReviewDraftMutator.PublishDiffReviewDraft`
- on provider unsupported, return the existing unsupported capability problem response
- on success, delete local draft rows and enqueue/sync review-thread ingestion

### Baseline API Types

Line range request/response model:

```go
type diffReviewLineRange struct {
    Path        string `json:"path"`
    OldPath     string `json:"old_path,omitempty"`
    Side        string `json:"side"` // left | right
    StartSide   string `json:"start_side,omitempty"`
    StartLine   *int   `json:"start_line,omitempty"`
    Line        int    `json:"line"`
    OldLine     *int   `json:"old_line,omitempty"`
    NewLine     *int   `json:"new_line,omitempty"`
    LineType    string `json:"line_type"` // context | add | delete
    DiffHeadSHA string `json:"diff_head_sha"`
    CommitSHA   string `json:"commit_sha"`
}
```

Draft response model:

```go
type diffReviewDraftResponse struct {
    DraftID               int64                    `json:"draft_id,omitempty"`
    Body                  string                   `json:"body"`
    Action                string                   `json:"action"`
    Comments              []diffReviewDraftComment `json:"comments"`
    SupportedActions      []string                 `json:"supported_actions"`
    NativeMultilineRanges bool                     `json:"native_multiline_ranges"`
}

type diffReviewDraftComment struct {
    ID        int64               `json:"id"`
    Body      string              `json:"body"`
    Range     diffReviewLineRange `json:"range"`
    CreatedAt string              `json:"created_at"`
    UpdatedAt string              `json:"updated_at"`
}
```

Thread response model:

```go
type diffReviewThreadResponse struct {
    ID          int64               `json:"id"`
    Body        string              `json:"body"`
    AuthorLogin string              `json:"author_login,omitempty"`
    Range       diffReviewLineRange `json:"range"`
    Resolved    bool                `json:"resolved"`
    CreatedAt   string              `json:"created_at"`
    UpdatedAt   string              `json:"updated_at"`
}
```

### Baseline Timeline Contract

Do not keep returning bare `[]db.MREvent` from PR detail once inline review metadata is needed.

Add:

```go
type mergeRequestEventResponse struct {
    db.MREvent
    ReviewThread *diffReviewThreadResponse `json:"review_thread,omitempty"`
}
```

Change `mergeRequestDetailResponse.Events` to:

```go
Events []mergeRequestEventResponse `json:"events"`
```

`buildPullDetailResponse` must:

- load `db.ListMREvents(ctx, mr.ID)`
- load review thread rows for the MR
- attach `ReviewThread` to `review_comment` events whose metadata/provider IDs match a persisted thread
- preserve existing event shape for all other timeline event types

### Baseline Review-Thread Ingestion Contract

Add a provider read path for published review threads:

- `MergeRequestReviewThreadReader.ListMergeRequestReviewThreads`
- sync/persist function that upserts `middleman_mr_review_threads`
- normalization that creates or updates matching `review_comment` `MREvent` rows

Baseline can include fake provider tests and unsupported provider stubs. Actual Forgejo/GitLab/GitHub readers are added in their provider branches.

### Baseline Diff Review Line Data

Extend diff API responses with enough metadata for reviewable line refs:

- `diff_head_sha`
- `review_commit_sha`
- `from_sha`
- `to_sha`
- `reviewable` boolean
- `review_disabled_reason` string

Inline review is enabled only for the full PR-head diff. It is disabled for single-commit and arbitrary range diff modes until provider coordinate mapping for those modes is implemented and tested.

Frontend line refs:

```ts
type DiffReviewLineRef = {
  path: string;
  oldPath?: string;
  side: "left" | "right";
  line: number;
  oldLine?: number;
  newLine?: number;
  lineType: "context" | "add" | "delete";
  diffHeadSha: string;
  commitSha: string;
};
```

Selection rules:

- same file only
- same side only
- contiguous in rendered diff order
- reject mixed left/right selections
- reject lines without provider-review line refs
- reject stale ranges on the server when request `diff_head_sha` differs from stored MR `DiffHeadSHA`

### Baseline Frontend Scope

Add `packages/ui/src/stores/diff-review-draft.svelte.ts`.

Responsibilities:

- bind state to provider, platform host, owner, name, repo path, number, and diff head SHA
- load local draft rows through the generated API client
- create/edit/delete local draft comments
- publish draft with selected review action
- discard draft
- resolve/unresolve published review threads
- abort in-flight requests on identity changes
- clear on workspace diff mode, route changes away from the PR, or PR identity changes
- expose loading, pending, and error state
- no-op when review mode is disabled

Wire the store through:

- `packages/ui/src/Provider.svelte`
- `packages/ui/src/context.ts` if a new context key is needed
- `packages/ui/src/index.ts`
- existing generated OpenAPI client patterns

Update `DiffView.svelte`:

```ts
reviewMode?: "enabled" | "disabled";
```

The layout boundary must decide review mode explicitly. `WorkspaceDiffPanel.svelte` must pass `reviewMode="disabled"`.

Create:

- `DiffInlineCommentComposer.svelte`
- `DiffReviewDraftTray.svelte`
- `DiffReviewThreadSnippet.svelte`

Modify:

- `DiffFile.svelte`
- `DiffLine.svelte`
- `EventTimeline.svelte`

Use Svelte 5 patterns:

- `$props`
- callback props instead of event dispatch
- `$derived` for computed values
- keyed each blocks

### Baseline Route Helpers

Extend `PullSuffix` in `packages/ui/src/api/provider-routes.ts` with the review suffixes.

Add missing default hosts in both route maps:

- `packages/ui/src/api/provider-routes.ts`
- `frontend/src/lib/stores/router.svelte.ts`

Defaults:

```ts
gitea: "gitea.com"
forgejo: "codeberg.org"
```

### Baseline API Generation

Run:

```bash
make api-generate
```

Verify generated artifacts:

- `frontend/openapi/openapi.yaml`
- `internal/apiclient/spec/openapi.json`
- `internal/apiclient/generated/client.gen.go`
- `packages/ui/src/api/generated/schema.ts`
- `packages/ui/src/api/generated/client.ts`

### Baseline Tests

Backend:

- migration up/down tests
- DB draft CRUD tests
- DB review-thread upsert/list/resolve tests
- route registration tests for default and host-qualified paths
- operation ID tests
- API e2e with fake providers using real SQLite:
  - create/edit/delete draft comment
  - discard draft
  - publish rejects unsupported provider
  - publish rejects unsupported action
  - stale diff head SHA returns `409`
  - resolve rejects unsupported provider
  - PR detail response can include typed `review_thread`
- generated Go client used for at least one default-host and one host-qualified route

Frontend:

- route helper suffix typing
- default-host route tests for GitHub, GitLab, Gitea, Forgejo
- diff selection accepts valid same-side ranges
- diff selection rejects mixed-side ranges
- diff selection rejects lines without review line refs
- store clears on route identity change and workspace diff mode
- workspace diff hides review controls
- PR timeline renders review-thread snippets
- run Svelte autofixer on new/changed `.svelte` files

## Stack Part 2: Forgejo Provider

Branch: `inline-review-forgejo`

Goal: implement the first real provider end to end, including integration tests against the existing Forgejo Docker Compose fixture.

Forgejo is first because the repo already has:

- `scripts/e2e/forgejo/docker-compose.yml`
- `scripts/e2e/forgejo/bootstrap.sh`
- `scripts/e2e/forgejo/README.md`
- `internal/server/gitealike_container_e2e_test.go`
- opt-in test gate `MIDDLEMAN_FORGEJO_CONTAINER_TESTS=1`

### Forgejo Backend Scope

Implement provider support in the gitealike/Forgejo path:

- add SDK adapter methods in `internal/platform/forgejo`
- add shared mapping helpers in `internal/platform/gitealike` where useful
- enable Forgejo capabilities only after tests pass:
  - `ReviewDraftMutation`
  - `ReviewThreadResolution` if SDK/API supports it
  - `ReadReviewThreads`
  - `SupportedReviewActions`
  - `NativeMultilineRanges` only if proven

If Forgejo does not support native multiline ranges in the pinned SDK/API, split a multiline local draft into one provider comment per selected line during publish.

Forgejo publish must:

- receive local draft comments from `PublishDiffReviewDraftInput`
- create one provider review containing all comments where possible
- submit using the requested supported action
- return provider review metadata
- trigger review-thread ingestion after publish

Forgejo resolve must:

- accept the persisted provider thread/comment ID from DB metadata
- call the Forgejo resolve/unresolve API if supported
- update DB resolved state after provider success

### Forgejo Container Tests

Extend the Forgejo fixture/bootstrap so the seeded PR has enough changed lines for:

- single-line inline review comment
- multiline same-side range
- published review-thread ingestion
- resolve/dismiss

Add or extend opt-in tests:

```bash
MIDDLEMAN_FORGEJO_CONTAINER_TESTS=1 go test ./internal/server -run TestForgejoContainer -shuffle=on
```

Required Forgejo test scenarios:

- create local draft comment through middleman API
- publish local draft to real Forgejo
- verify Forgejo API shows the review/comment
- sync/read review threads back into SQLite
- PR detail returns `review_comment` event with typed `review_thread`
- resolve/dismiss through middleman API updates Forgejo and SQLite
- multiline selection behavior matches capability:
  - native range if supported
  - split per-line comments if not supported

The Forgejo branch is not done until the container test passes locally when the env gate is enabled.

### Gitea Handling In This Branch

Forgejo and Gitea share gitealike code, but do not automatically enable Gitea capabilities unless Gitea container coverage is also added and passes.

If Gitea is not tested in this branch:

```go
ReviewDraftMutation: false
ReviewThreadResolution: false
ReadReviewThreads: false
SupportedReviewActions: nil
NativeMultilineRanges: false
```

## Stack Part 3: GitLab Provider

Branch: `inline-review-gitlab`

Goal: implement GitLab after the common API and Forgejo provider are clean.

Before enabling capabilities, verify and test GitLab APIs for:

- draft notes or direct merge request discussions
- discussion positions and line ranges
- publishing staged comments from local draft rows
- approval support
- request-changes equivalent, if any
- discussion resolve/unresolve
- review-thread/discussion ingestion

Use existing GitLab Docker Compose fixture where practical:

- `scripts/e2e/gitlab/docker-compose.yml`
- `scripts/e2e/gitlab/bootstrap.sh`
- `internal/server/gitlab_container_e2e_test.go`

GitLab capabilities must stay disabled until provider-specific tests pass.

Required GitLab test scenarios:

- publish local draft to real or fake GitLab API
- ingest discussions/review threads into `middleman_mr_review_threads`
- resolve discussion through middleman API
- reject unsupported review actions
- multiline position mapping if enabled

If GitLab lacks a native `request_changes` equivalent, do not advertise `request_changes`.

## Stack Part 4: GitHub Provider

Branch: `inline-review-github`

Goal: implement GitHub last because it needs additional REST and GraphQL wrapper work and is harder to integration test locally.

Add explicit `internal/github.Client` methods:

- create pull request review from local draft comments using `gh.PullRequestReviewRequest`
- submit review with `COMMENT`, `APPROVE`, or `REQUEST_CHANGES`
- delete pending review only if implementation creates one
- list review comments/threads needed for ingestion
- resolve/unresolve review threads through GraphQL

Important correction: do not use a nonexistent `gh.PullRequestReviewComment` draft type. For create-review payloads, use `gh.DraftReviewComment` inside `gh.PullRequestReviewRequest`.

GitHub publish must:

- convert local draft rows to `[]*gh.DraftReviewComment`
- set `CommitID`
- set `Path`, `Body`, `Side`, `Line`
- set `StartSide` and `StartLine` for native multiline ranges
- use review event `COMMENT`, `APPROVE`, or `REQUEST_CHANGES`
- reject stale diff head SHA before provider calls

GitHub thread ingestion must:

- read review threads/comments through GraphQL or REST plus GraphQL node IDs
- persist provider review/thread/comment IDs in DB metadata
- emit/update `review_comment` timeline events
- support GraphQL `resolveReviewThread` and `unresolveReviewThread`

Testing:

- provider mapping unit tests for single-line and multiline payloads
- stale diff rejection test
- GraphQL mutation input tests
- server e2e using fake GitHub provider for API shape
- live GraphQL query-shape validation only under the repo's existing live-test gate

## PR Activity View

`EventTimeline.svelte` renders `review_comment` events through `DiffReviewThreadSnippet.svelte`.

Behavior:

- show inline review parts in the selected PR timeline
- show resolved state
- resolve/dismiss calls the provider resolve endpoint with internal thread ID
- server looks up provider IDs from DB metadata
- on success, refresh PR detail
- do not add inline review comments as standalone global activity feed rows

## Workspace View

Workspace diff mode must hide or disable:

- line comment buttons
- range selection composer
- draft tray
- publish/discard actions
- resolve/dismiss controls

The review draft store must clear itself when workspace diff state replaces PR diff state.

## Cross-Stack Acceptance Criteria

The full stack is complete when:

- baseline compiles with unsupported stubs for every provider
- Forgejo passes fake-provider tests and the opt-in Docker Compose container test
- GitLab passes provider-specific tests before its capabilities are enabled
- GitHub passes provider-specific REST/GraphQL mapping tests before its capabilities are enabled
- generated OpenAPI artifacts are current
- frontend uses generated client paths and `providerItemPath`
- workspace diff never exposes review controls
- legacy query-param detail URLs remain unsupported

## Explicit Non-Goals

- Do not support or redirect legacy query-param detail URLs.
- Do not advertise provider capabilities that are not implemented and tested.
- Do not rely on provider-native pending drafts in v1.
- Do not expose provider IDs in public API responses unless a concrete UI need is introduced.
- Do not enable inline review for single-commit or arbitrary range diff views until coordinate mapping is proven.
