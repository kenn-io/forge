# Remote Head Refresh Design

## Goal

Refresh PR detail and CI state when a middleman-managed workspace's remote PR
head changes.

The trigger should be the provider-visible branch update, not a local command
shape such as "an agent ran `git push`." Detecting the remote head makes the
feature work for launched agents, base workspace terminals, IDE Git
integrations, manual shells, and pushes from another machine. Failed pushes and
local-only commits naturally do not trigger a refresh.

The user-visible result is:

- A PR detail tab updates soon after the branch backing that PR moves on the
  remote.
- Pending CI starts refreshing without the user leaving and reopening the PR.
- Issue workspaces that become associated with a PR can gain the PR tab from
  the same monitoring path.
- Long-disconnected browser clients either replay the missed refresh events or
  receive the existing `reconnect.stale` signal and refetch from scratch.

## Current Behavior

Middleman already has several pieces of this flow, but no remote-head observer.

- `internal/workspace/monitor.go` runs once at startup and every minute. It
  only examines ready issue workspaces with no `associated_pr_number`. It
  matches the current branch/upstream against open PR rows already present in
  SQLite, then stores the first unique match.
- `internal/server/server.go` broadcasts `workspace_status` and `data_changed`
  when that association changes.
- `internal/server/detail_sync.go` deduplicates background PR and issue detail
  syncs and broadcasts `data_changed` after a detail sync completes.
- `internal/server/event_hub.go` assigns every SSE event a monotonic id, keeps a
  replay ring, replays events newer than `Last-Event-ID`, and emits
  `reconnect.stale` when the cursor is outside the retained history.
- `packages/ui/src/stores/events.svelte.ts` handles `data_changed`,
  `sync_status`, and `reconnect.stale`. `data_changed` currently causes broad
  pull, issue, and activity reloads.
- `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte` opens its
  own EventSource for `workspace_status` and refetches the active workspace
  when the event's `id` matches.

The current PR monitor does not ask the provider or remote repository whether a
branch changed. It cannot detect that a PR-backed workspace was pushed, and it
cannot discover a new PR unless a normal sync has already inserted the PR row.

## Design Decision

Add a `RemoteHeadObserver` for middleman-owned workspaces.

The observer checks the remote branch SHA for workspaces that either own a PR or
can be associated with one. When the observed remote head for a PR changes, it
enqueues a PR detail sync, then emits targeted SSE events so connected clients
can refresh the right UI state without waiting for a broad `data_changed`
reload.

This design deliberately keeps the observer provider-neutral:

- Use the persisted PR row as the preferred source of branch identity:
  `HeadRepoCloneURL`, `HeadBranch`, `PlatformHeadSHA`, provider kind, host, and
  repo path.
- Use local Git upstream state only as a fallback or as association evidence for
  issue workspaces.
- Use provider identity everywhere: `(provider, platform_host, repo_path,
  owner, name, number)`.
- Keep provider-specific optimizations optional behind the platform capability
  model.

The first implementation should use Git remote inspection as the common path:
`git ls-remote <clone-url> refs/heads/<head-branch>`. Provider API fast paths
can be added later only if they fit behind a neutral remote-head capability and
respect existing rate-limit budgets.

## Alternatives Considered

### Git wrapper around launched agents

Middleman could prepend a private `git` wrapper to launched agent `PATH` and
notify the server when `git push` exits successfully.

This is useful as a future acceleration path, but it is not sufficient as the
primary design. It misses absolute-path Git invocations, non-agent terminals,
IDE pushes, and pushes from other machines. It also reports a command success,
not the provider-visible branch state.

### Git Trace2 command observation

Launched sessions can set Git Trace2 environment variables and stream
successful `push` command exits back to middleman.

This is less invasive than a wrapper, but it still observes local command
execution rather than remote state. It also provides weak ref-level detail and
does not cover external pushes.

### Existing PR monitor only

The existing PR monitor can remain as a narrow association helper, but it should
not be stretched into remote-change detection. It only handles issue
workspaces, relies on already-synced PR rows, and emits broad invalidations.

## Workspace Eligibility

The observer scans ready middleman workspaces. It does not discover arbitrary
host worktrees and does not become a generic Git automation API.

A workspace is eligible for remote-head observation when:

- `status == "ready"`;
- `worktree_path` is non-empty;
- the workspace belongs to a tracked repo with full provider identity;
- either:
  - `item_type == "pull_request"`, using `item_number` as the PR number;
  - `item_type == "issue"` and `associated_pr_number` is set;
  - `item_type == "issue"` and local branch/upstream evidence can identify one
    open PR uniquely, as the current PR monitor does.

For issue workspaces with no associated PR, the observer may share the current
association logic with the PR monitor. If it finds a unique PR, it persists
`associated_pr_number` before checking the remote head. That persistence still
emits `workspace_status` so the terminal PR tab becomes available.

## Remote Head Resolution

For each eligible PR, resolve the branch to observe in this order:

1. Use the synced PR row's head clone URL and head branch.
2. If the PR row has no usable head clone URL, use the workspace branch's
   upstream remote URL and upstream branch when they uniquely match the PR row.
3. If neither source is available, skip the workspace for that pass and log at
   debug level.

The observed ref is `refs/heads/{head_branch}` on the resolved head repository,
not the base repository's default branch and not the local worktree's current
`HEAD`.

Fork PRs are expected to work because the PR row carries the head repository
clone URL. Nested GitLab namespaces work because the route and sync identity
remain provider-aware even though the Git remote check uses a clone URL.

When `git ls-remote` returns no matching ref, treat it as a soft miss. The
branch may have been deleted, permissions may be stale, or the provider may be
temporarily unavailable. Do not clear the last observed SHA based on a missing
ref alone.

## Observation State

Keep the last observed remote head in process memory, not SQLite.

`platform_head_sha` remains the durable "provider state persisted by PR sync."
The observer's last-seen SHA is only a debounce and edge-detection cache for the
current middleman process. It does not need to survive daemon restart, does not
belong in migrations, and should disappear when the process exits.

Recommended in-memory shape:

```go
type remoteHeadKey struct {
	WorkspaceID  string
	Provider     platform.Kind
	PlatformHost string
	RepoPath     string
	ItemType     string
	ItemNumber   int
	RemoteURL    string
	BranchName   string
}

type remoteHeadObservation struct {
	SHA                   string
	ObservedAt            time.Time
	LastRefreshEnqueuedAt time.Time
	FailureCount          int
	BackoffUntil          time.Time
}
```

The map is workspace-scoped because issue workspaces can become associated with
a PR after creation and because workspace deletion should drop local observation
state. The provider fields are part of the key so duplicate-looking branch names
on different provider hosts do not collide.

On the first successful observation for a workspace/PR pair in a process, record
the SHA but do not emit a refresh event unless it differs from the current
synced PR `platform_head_sha`. This prevents startup from producing a burst of
refreshes for already-current rows while still catching remote moves that
happened while middleman was stopped.

When middleman restarts, the observer starts with an empty map. The first pass
compares the remote SHA to the durable PR row's `platform_head_sha`; that is
enough to catch remote movement that happened while middleman was offline. After
detail sync persists the new provider head, subsequent first observations are
quiet.

## Scheduling

Run the observer as a background loop when workspace support is configured.

- Initial pass: after server startup, once the workspace manager is available.
- Steady-state pass: every 20-30 seconds, with jitter to avoid synchronized
  remote requests across many repos.
- Manual kick: after workspace setup completes and after an issue workspace gains
  an associated PR.
- Backoff: per `(provider, platform_host, repo_path, branch)` after transient Git
  failures.

Remote checks should be bounded:

- Limit concurrent `ls-remote` calls per provider host.
- Deduplicate checks for workspaces that point at the same remote URL and branch.
- Use short command timeouts.
- Strip Git environment using the same `gitenv.StripAll` pattern used by the
  existing workspace monitor.

The observer should not block normal sync or workspace setup. Failures log a
warning only when they persist past the first failure; otherwise they are debug
noise.

## Refresh Queues

When a changed remote SHA is detected:

1. Persist the new observed SHA and observation time.
2. Emit `workspace_remote_head_changed`.
3. Enqueue a high-priority PR detail sync for the changed PR.
4. Emit `workspace_pr_refresh_queued`.
5. When detail sync completes, emit `pr_detail_refreshed` and the existing
   `data_changed`.
6. If the refreshed PR has pending CI or workflow approval uncertainty, enqueue
   lower-priority CI refresh work.
7. Emit `pr_ci_refresh_queued` and `pr_ci_refreshed` around CI refresh work.

The visible PR should win over background work. If the active detail tab matches
the changed PR, the client may call the existing detail refresh path immediately
after receiving `workspace_remote_head_changed` or
`workspace_pr_refresh_queued`. Other clients can wait for `pr_detail_refreshed`
or broad `data_changed`.

The first implementation may route all detail work through the existing
`enqueueDetailSync` dedupe map. If CI refresh remains synchronous-only, add a
small background wrapper that calls the existing CI refresh logic and broadcasts
completion.

## SSE Events

All new events go through the existing `EventHub`. They receive monotonic ids,
are stored in the replay ring, and participate in `Last-Event-ID` replay. They
must use JSON-serializable payloads.

### `workspace_remote_head_changed`

Sent when the observer sees a PR head branch SHA change on the remote.

```json
{
  "workspace_id": "ws_123",
  "provider": "github",
  "platform_host": "github.com",
  "repo_path": "acme/widgets",
  "owner": "acme",
  "name": "widgets",
  "number": 42,
  "old_sha": "1111111",
  "new_sha": "2222222",
  "branch": "feature/widgets",
  "observed_at": "2026-05-20T14:15:00Z"
}
```

Client behavior:

- If the active workspace id matches, refetch the workspace summary only if
  workspace fields changed in the same pass.
- If the active PR detail matches, mark the detail stale or start an immediate
  detail refresh spinner.
- Do not reload pull, issue, and activity lists solely because this event
  arrived.

### `workspace_pr_associated`

Sent when an issue workspace gains `associated_pr_number`.

```json
{
  "workspace_id": "ws_123",
  "provider": "github",
  "platform_host": "github.com",
  "repo_path": "acme/widgets",
  "owner": "acme",
  "name": "widgets",
  "issue_number": 7,
  "pr_number": 42,
  "associated_at": "2026-05-20T14:15:00Z"
}
```

Client behavior:

- Terminal workspace views refetch that workspace and show the PR tab.
- List and activity views do not need to reload unless followed by
  `data_changed`.

The server may also keep the existing `workspace_status` event for compatibility
in the same pass. New clients should prefer `workspace_pr_associated` because it
contains enough context to avoid guessing why the workspace changed.

### `workspace_pr_refresh_queued`

Sent after a remote-head change enqueues PR detail sync.

```json
{
  "workspace_id": "ws_123",
  "provider": "github",
  "platform_host": "github.com",
  "repo_path": "acme/widgets",
  "owner": "acme",
  "name": "widgets",
  "number": 42,
  "head_sha": "2222222",
  "priority": "high",
  "queued_at": "2026-05-20T14:15:01Z"
}
```

Client behavior:

- Active PR detail can show a syncing state.
- Passive views do not reload yet; they should wait for completion or stale
  replay recovery.

### `pr_detail_refreshed`

Sent after background PR detail sync completes.

```json
{
  "provider": "github",
  "platform_host": "github.com",
  "repo_path": "acme/widgets",
  "owner": "acme",
  "name": "widgets",
  "number": 42,
  "head_sha": "2222222",
  "synced_at": "2026-05-20T14:15:04Z",
  "warnings": []
}
```

Client behavior:

- Matching PR detail reloads only itself.
- Pull list and activity stores may reload if they currently contain that repo
  or if they do not yet support targeted item invalidation.
- The existing broad `data_changed` remains during the migration so old clients
  keep working.

### `pr_ci_refresh_queued`

Sent when lower-priority CI refresh work is scheduled after a head change or
after detail sync finds pending checks.

```json
{
  "provider": "github",
  "platform_host": "github.com",
  "repo_path": "acme/widgets",
  "owner": "acme",
  "name": "widgets",
  "number": 42,
  "head_sha": "2222222",
  "priority": "low",
  "queued_at": "2026-05-20T14:15:05Z"
}
```

Client behavior:

- Matching PR detail may keep CI spinners visible.
- Do not refetch broad lists yet.

### `pr_ci_refreshed`

Sent after a background CI refresh completes.

```json
{
  "provider": "github",
  "platform_host": "github.com",
  "repo_path": "acme/widgets",
  "owner": "acme",
  "name": "widgets",
  "number": 42,
  "head_sha": "2222222",
  "refreshed_at": "2026-05-20T14:15:20Z",
  "warnings": []
}
```

Client behavior:

- Matching PR detail reloads itself or only the CI slice if that store boundary
  exists by implementation time.
- Pull list can reload if the aggregate CI status changed.
- Existing pending-CI polling remains as a fallback, but the event-driven path
  should make it less visible.

## SSE Replay And Stale Cursors

New events rely on the current SSE replay contract rather than inventing a
second delivery mechanism.

- Each event is assigned an id by `EventHub.Broadcast`.
- Browser EventSource automatically sends `Last-Event-ID` after reconnect.
- The server replays ring-buffered events newer than that cursor.
- If the cursor is too old or ahead of the server lifetime, the server emits
  `reconnect.stale`.

Client handling of `reconnect.stale` remains broad: reload pulls, issues,
activity, and sync status. Workspace terminal views should also refetch their
active workspace and runtime state when they add stale handling. This guarantees
that a slept laptop does not miss a remote-head refresh forever.

Targeted events must be idempotent. A client may receive:

- `workspace_remote_head_changed`, then `workspace_pr_refresh_queued`, then
  `pr_detail_refreshed`;
- only `pr_detail_refreshed` after replay if earlier events were outside the
  buffer and `reconnect.stale` caused a broad reload;
- duplicate-looking payloads for the same SHA if two workspaces point at the
  same PR.

Clients should dedupe by provider ref plus SHA where needed, not by SSE id.

## Frontend Store Changes

Extend `createEventsStore` with optional callbacks for the new event types.

Keep the existing `onDataChanged` behavior until targeted invalidation is proven.
Then move high-traffic views toward targeted reloads:

- `onPRDetailRefreshed(ref)`: if the visible detail matches, call
  `detail.refreshDetailOnly`.
- `onPRCIRefreshed(ref)`: if the visible detail matches, refresh detail or CI.
- `onWorkspacePRAssociated(payload)`: active terminal workspace refetches its
  workspace summary.
- `onReconnectStale`: also refetch active workspace state when the terminal view
  owns an EventSource.

The terminal view currently opens a separate EventSource only for
`workspace_status`. It should either:

- continue using a local EventSource but subscribe to
  `workspace_pr_associated` and `reconnect.stale`; or
- move workspace-status handling into the shared events store so the app has one
  EventSource.

The second option is cleaner long-term, but the first is acceptable for the
initial implementation if it keeps the change smaller.

## Error Handling

Remote-head observation errors must not fail normal sync.

- Missing ref: debug log and keep the previous observed SHA.
- Auth or network failure: warn only after repeated failures for the same
  remote/ref and apply backoff.
- Invalid clone URL or branch name: debug log and skip until the next PR sync
  updates the row.
- Detail sync failure after a detected remote move: emit no completion event,
  keep the queued/stale state bounded by existing detail sync retries and user
  refresh actions.
- CI refresh failure: emit `pr_ci_refreshed` with warnings only if a response was
  persisted; otherwise rely on the existing warning/toast paths when the user
  opens or refreshes the PR.

The observer should never mutate local Git state. It must not fetch into the
worktree, checkout branches, update refs, or change branch upstreams.

## Testing

Backend tests:

- Observer detects a remote SHA change via a fake `ls-remote` runner and
  enqueues one PR detail refresh for the provider-aware PR ref.
- First observation records SHA in memory without enqueueing when it equals
  `platform_head_sha`.
- First observation enqueues refresh when it differs from `platform_head_sha`.
- Multiple workspaces pointing at the same remote branch dedupe remote checks
  and do not enqueue duplicate in-flight detail syncs.
- Issue workspace with a newly detected PR emits `workspace_pr_associated`,
  preserves the existing `workspace_status` compatibility event, and then
  observes the associated PR head.
- Missing remote ref and transient command failure do not clear in-memory
  observation state.

Server/SSE tests:

- New events are framed with ids and replayed through `Last-Event-ID`.
- `reconnect.stale` remains the only stale-cursor signal and is emitted before
  live events after reconnect.
- Payloads include provider, platform host, repo path, owner, name, number, and
  SHA fields.

Frontend tests:

- Events store parses the new event payloads and routes them to optional
  callbacks.
- Active PR detail refreshes on `pr_detail_refreshed` for the same provider ref
  and ignores other PRs.
- Workspace terminal refetches on `workspace_pr_associated` for the active
  workspace and shows the PR segment.
- `reconnect.stale` refetches broad app state and active workspace state.

E2E tests:

- A ready PR workspace whose remote branch moves causes the active PR detail to
  refresh without navigating away from the terminal or PR tab.
- An issue workspace that creates/pushes a branch and later gains a synced PR
  shows the PR tab after the association event.

## Acceptance Criteria

- Remote PR head changes are detected without relying on local `git push`
  command interception.
- PR detail sync is enqueued once per changed provider-aware PR head while work
  is already in flight.
- CI refresh work is lower priority than detail sync and does not block the UI.
- SSE events are targeted, replayable, JSON-serializable, and provider-aware.
- Existing `data_changed`, `workspace_status`, `sync_status`, and
  `reconnect.stale` behavior continues to work for old clients.
- Long-disconnected clients recover through replay or broad stale refetch.
- The observer never mutates local Git state.

## Out Of Scope

- Replacing provider webhooks. Middleman remains local-first and pull-based.
- Generic discovery of arbitrary Git worktrees outside middleman-managed
  workspaces.
- A Git wrapper or Trace2 command observer. These can be added later as latency
  optimizations, not correctness mechanisms.
- Removing broad `data_changed` invalidation in the first implementation.
- A dedicated frontend CI slice store. Detail refresh is acceptable until store
  boundaries justify a narrower API.
