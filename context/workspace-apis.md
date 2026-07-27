# Workspace APIs

These APIs manage **middleman-owned workspaces**: durable local execution
contexts for tracked PRs, provider issues, mapped Kata tasks, and ad-hoc work in
a tracked repository. They are not a generic Git worktree browser and not an
embedder protocol for arbitrary host state.

## Purpose

- Persist a middleman workspace entry for a tracked item.
- Materialize that entry as a local Git worktree plus tmux session.
- Let the UI reopen the same workspace from `/workspaces` or `/terminal/:id`.
- Carry enough item metadata to render the correct sidebar behavior.
- Keep Workspace and Projects request state below the root server composition
  boundary. The handler receives deep-copied committed config snapshots; it
  never retains the root mutable config pointer or mutex
  (`internal/server/workspaceapi/config.go::ConfigSnapshot`).
- Construct Workspace manager, runtime, tmux, clock, and enrichment policy
  before handler startup; production and test callers must not mutate
  dependencies or test controls after `Start` (`internal/server/workspaceapi/handler.go::Deps`).
- Workspace clock overrides remain scoped to the handler; replacing the root
  server clock also changes unrelated domain timestamps
  (`internal/server/server.go::newServer`).
- Fleet consumes Workspace-owned summary and runtime snapshots, never the
  Workspace manager or root server receiver
  (`internal/server/workspaceapi/fleet_snapshot.go::FleetSnapshot`).

## Endpoint Intent

- `POST /workspaces`: create or reuse a PR-backed workspace.
- `POST /repos/{owner}/{name}/issues/{number}/workspace`: create or reuse an
  issue-backed workspace; these start from the repo's current `origin/HEAD`,
  not from a PR head branch.
- `POST /repo/{provider}/{owner}/{name}/workspaces`: create or reuse an ad-hoc
  workspace for new work with no source item. Its branch is its identity: the
  item key is `adhoc:<branch>` and `item_number` stays 0, so item-key fallbacks
  derived from the number must exclude this type
  (`internal/db/queries.go::workspaceItemTypeKeysByNumber`). Requesting the same
  branch twice returns the existing workspace.
- `GET /kata/tasks/snapshot`: the browser's sole Kata task authority. Daemon,
  scope, project, status authority, selected task, and graph source form request
  identity; selected detail, history, graph, and workspace target belong to the
  same accepted snapshot, while query, owner, and label remain local presentation
  state (`internal/server/kata_snapshot_frontend.go::kataTaskSnapshot`).
- Middleman persists no Kata task, snapshot, or cursor state. Its only Kata
  authority storage is a bounded, non-touching in-memory TTL cache
  (`internal/server/kata_snapshot_cache.go::newKataSnapshotCacheWithConfig`).
- Global Kata issue/event reads establish workspace authority and invalidation;
  selected detail uses generated issue-detail, while complete retained history
  uses project events with at most one valid purge reset and no global fallback
  (`internal/server/kata_snapshot_enrichment.go::kataSnapshotEnricher`).
- Cache capacity may evict Kata authority or enrichment entries but must never
  truncate an API result; daemon invalidation clears every cached read for that
  daemon (`internal/server/kata_snapshot_cache.go::kataSnapshotCache`).
- The 128 MiB Kata authority/graph response and authority-cache ceilings bound
  input and retention, not peak heap; transient processing amplification is acceptable
  (`internal/server/kata_client.go::kataGeneratedResponseLimit`).
- Do not approximate heap use with finer payload quotas absent an observed problem.
- Oversized project history must keep paginating selected history without
  retaining the complete project stream, but aggregated selected history is
  bounded by `kataSelectedHistoryMaxBytes`; exceeding it degrades the history
  stage instead of growing memory without bound
  (`internal/server/kata_snapshot_enrichment.go::loadProjectEvents`).
- Initial project-event miss coalescing is daemon + exact epoch + project;
  selected UID belongs only to oversized selected-history fallback flights
  (`internal/server/kata_snapshot_enrichment_cache.go::projectEvents`).
- `GraphFetchedAt` identifies the daemon read that produced the graph and stays
  stable across cache hits (`internal/server/kata_snapshot_enrichment.go::loadGraph`).
- Selected detail and graph enrichment are revision-fenced against the
  authority and cached under that revision; a mismatch is stale and retries
  through epoch invalidation, never merged into an accepted snapshot
  (`internal/server/kata_snapshot_enrichment.go::validateKataGraph`).
- `GET /kata/tasks/events`: compact reset/invalidation transport only. Replay
  starts at the accepted snapshot cursor; raw daemon events never enter browser
  authority (`frontend/src/lib/features/kata/kataWorkspaceAuthorityController.svelte.ts::KataWorkspaceAuthorityController`).
- Cursorless live-only catch-up establishes its stream before invalidating; publishing
  first creates an unreplayable mutation gap between snapshot reload and subscription
  (`internal/server/kata_event_hub.go::runSupervisor`).
- Frontend Kata event streaming must use the generated runtime client with stream
  parsing; raw fetch paths bypass base-path and tracing policy
  (`frontend/src/lib/api/kata/eventStream.ts::readKataEventStream`).
- Auxiliary selected-detail reads must remain independent from shared global/all
  authority refresh
  (`frontend/src/lib/features/kata/kataAuxiliaryAuthority.svelte.ts::selectIssue`).
- Every frontend Kata mutation and recurrence request is explicitly pinned to
  the accepted snapshot daemon; ambient active/default daemon fallback is
  forbidden (`frontend/src/lib/api/kata/taskClient.ts::pinnedDaemonHeaders`).
- Frontend mutation results are acknowledgement-only `{ changed }`; canonical
  project/task identity and rendered state come from a newly accepted snapshot,
  never mutation response payloads (`frontend/src/lib/api/kata/taskTypes.ts::KataTaskMutationResponse`).
- `GET /kata/tasks/references`: middleman's global Kata reference service; it
  defaults to open tasks for autocomplete, while navigation explicitly requests
  `status=all` and routes from the returned canonical task status.
  Rank exact short, qualified, or UID matches before substring matches and only
  then apply the response limit. The returned `reference` decides whether a
  short ID is globally unique; syntax-specific consumers may wrap that identity
  but must not reconstruct it from display fields. Consumers needing status,
  metadata, or closed tasks must use a snapshot. Selected link peers may be
  best-effort enriched into the snapshot catalog without joining
  `member_issue_uids`; browsers never issue detail reads to hydrate link rows
  (`internal/server/kata_snapshot_enrichment.go::loadLinkedPeerCatalog`).
- `POST /kata/workspaces`: create or reuse a Kata-task-backed workspace. Kata
  tasks are not provider issues, so this path never resolves or syncs a
  provider issue row.
- `GET /workspaces`: list middleman's persisted workspaces for the workspaces
  page and terminal picker.
- `GET /workspaces/{id}`: load one persisted workspace for terminal view.
- List/detail reads return persisted plus last-known-good enrichment without
  foreground git or tmux probes; stale components reconcile through bounded
  background workers (`internal/server/workspaceapi/workspace_enrichment.go::toCachedWorkspaceResponse`).
- `enrichment_status` is aggregate across reads and refresh/push/pull responses:
  failed reconciliation retains last-known-good components while preserving
  failure status/error
  (`internal/server/workspaceapi/workspace_enrichment.go::refreshWorkspaceResponse`).
- Overlapping tmux probes wait for the active sample within the caller budget;
  fallback carries an error only when waiting or sample production fails
  (`internal/server/workspaceapi/routes_handlers.go::probeOneTmuxSession`).
- Background completion emits `workspace_status` only for durable changes:
  first completion, divergence movement, or error-state change — never for
  tmux-activity-only movement, and tmux prune broadcasts only when it pruned.
  Unconditional broadcasts made every client refetch schedule the next
  enrichment, a permanent refresh loop
  (`internal/server/workspaceapi/workspace_enrichment.go::workspaceEnrichmentBroadcastWorthy`).
- Client detail stores mirror this: a background poll or sync whose payload is
  content-identical (ignoring fetch timestamps) must not replace displayed
  store state — equal-but-new objects re-render the whole panel every cycle
  (`packages/ui/src/stores/detail.svelte.ts::applyRefreshedDetail`).
- `DELETE /workspaces/{id}`: tear down a middleman-managed workspace and its
  local resources.

## Data Model Intent

- `item_type`: whether the workspace belongs to a `pull_request`, provider
  `issue`, or `kata_task`.
- `item_key`: the canonical owner key within the repo/workspace namespace. PR
  and provider issue workspaces use the decimal item number as a string; Kata
  task workspaces use an opaque composite of Kata daemon ID, project UID, and
  issue UID so issue IDs from different Kata scopes cannot collide.
- `item_number`: the provider item number within the repo. For Kata task
  workspaces this is `0` and must not be used for owner identity.
- `git_head_ref`: the Git branch name middleman opens in the worktree.
  Kata-task workspaces keep a readable slug from `short_id`, `qualified_id`, or
  issue UID, but the branch/worktree leaf must also include a short stable hash
  of daemon ID, project UID, and issue UID so project-scoped visible task IDs do
  not collide in the same watched repo.
- `item_last_activity_at`: the synced provider item activity timestamp for the
  owning PR or issue, when middleman has that owner item row.

These fields exist so PR-backed workspaces show PR/Reviews sidebars, while
issue-backed workspaces show the issue sidebar and disable the PR/reviews path.
Kata-backed workspaces show an embedded live Kata task pane using the same task
detail component as the Kata browser.

Workspace summaries join the owning PR or issue row by full provider identity:
`platform`, `platform_host`, `repo_owner`, `repo_name`, `item_type`, and
`item_number`. A PR workspace uses `middleman_merge_requests.last_activity_at`;
an issue workspace uses `middleman_issues.last_activity_at`. Kata workspaces do
not join provider item tables and leave provider item activity absent. If the
owning provider item has not synced yet, the summary leaves
`item_last_activity_at` absent rather than inventing a value.

Kata task repository resolution is deliberately exact. Manual settings mappings
key by optional daemon ID plus Kata project UID and point to a known repository
identity, including registered Middleman Projects. Removing a watched repo does
not delete an override because a registered Project may still own that identity
(`internal/config/config.go::validateKataProjectRepoMappings`,
`internal/server/kata_workspace.go::kataManualWorkspaceTarget`). Automatic
resolution first uses watched exact repos with `worktree_base_path` whose clone
contains a matching `.kata.toml`. Matching first compares both explicit
identifiers, `project.uid` and `project.identity`, to the Kata project UID. If
either identifier matches exactly, that clone is a candidate; if more than one
clone matches, the result is ambiguous. Name fallback through `.kata.toml` is
only allowed per clone when that clone has no usable `project.uid` or
`project.identity`, and then exactly one case-insensitive `project.name` match
is required. If no `.kata.toml` mapping matches, the
resolver may fall back to a case-insensitive exact match between the Kata
project and exactly one non-stale registered Middleman Project with provider
identity; use `.kata.toml` before display/repository name. Distinct matching
registered checkout paths are ambiguous. A unique registered match carries its
checkout through workspace creation, while a configured clone carries its own
base path. Only then may one synced repo matched by exact
or globbed config and lacking readable project metadata resolve by name.
Ambiguous, mismatched, or missing matches
mean the Create/Open
workspace button must not render
(`internal/server/kata_workspace.go::resolveKataWorkspaceRepo`).

Settings lists each selected-daemon Kata project with the status and source from
the workspace resolver. Its selector lists repository identities known from
exact watched repositories, currently matched tracked repositories, or
non-stale registered Projects. It defaults only to an inferred identity match
and persists that repository identity
(`internal/server/kata_workspace.go::getKataProjectMappings`).

Persisted workspace `worktree_path` values should be absolute. Workspace setup
runs `git worktree add` from the managed clone or configured base checkout, so
relative paths would be interpreted relative to that Git directory while later
API reads interpret them relative to the middleman server process.

Keep Git worktree and merge-request lifecycle semantics in
`go.kenn.io/kit/git/managed`; Middleman supplies application policy instead of
maintaining a local lifecycle fork (`internal/server/projects_handlers.go::createWorktreeOnDisk`).
Classify same-repository merge requests with the provider-hosted project
identity, not the effective origin URL: the origin may be a local mirror
(`internal/server/projects_handlers.go::createProjectWorktreeFromMergeRequest`).

All workspace API timestamps are emitted as UTC RFC3339 strings. Keep timestamp
normalization in the DB/server boundary; the Svelte UI can present local time
where needed.

## Agent Launch Context

Agent launch selects Codex and Claude families by case-folded target-name prefix.
Codex receives generated workspace context followed by root `AGENTS.md` verbatim
in `AGENTS.override.md`; only a non-symlink regular file up to 1 MiB is appended,
otherwise the override is context-only (`internal/workspace/agent_context.go::readRepositoryAgentInstructions`).
Claude receives context-only `CLAUDE.local.md` because its local file is additive
(`internal/workspace/agent_context.go::agentContextRelPath`).
No instruction file is written during setup.

The first-line marker owns refreshes; ownership detection is root-confined and
reads only the bounded marker prefix. Middleman updates only marked files.
Unmarked `AGENTS.override.md`/`CLAUDE.local.md` files, symlinks, and root
`AGENTS.md`/`CLAUDE.md` stay untouched. The content carries source identity
(kind, repo, item number, URL) and PR push target facts agents cannot read from
the worktree. Source-system prose (titles, Kata project names) is XML-escaped
inside `<untrusted-source-text>` fences — the prompt-injection boundary.
External identifiers are only normalized to one line, which preserves Markdown
structure and is not a trust boundary; new free-prose fields must go through the
fence.

Before writing, middleman ignores the generated path through the worktree's
private exclude file, not tracked `.gitignore`. If the path would remain
visible to Git, the write fails.

## Agent Activity Hooks

- User-level hooks are single-target: install merges, uninstall preserves other
  handlers, and the last install wins (`internal/agentactivity/integration.go::Install`).
- Matching live runtime/worktree reports use approval, input, working, idle priority;
  latest state expires after 30 minutes or session exit, then tmux resumes (`internal/agentactivity/`, `internal/server/workspaceapi/lifecycle.go::Handler.HandleRuntimeSessionExit`).
- Hook installs require absolute data roots and update symlink targets instead of replacing
  config links; report/worktree matching uses canonical filesystem paths (`cmd/middleman/agent_hook_cli.go::agentHookStateDir`,
  `internal/agentactivity/integration.go::resolvedConfigWritePath`, `internal/agentactivity/store.go::canonicalWorkspacePath`).
- Installed hooks and the server must resolve the same report directory, so both go
  through `internal/agentactivity/store.go::StateDir`.
- The active sidebar polls every five seconds, and hook receipt fails open (`frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte::onMount`, `cmd/middleman/agent_hook_cli.go::runAgentHookReceiver`).

## Diff Scopes

Workspace diffs compare against local `HEAD`, the pushed branch, or a merge
target. The merge-target scope exists only when the server can resolve a real
merge target branch, not merely when the workspace carries a PR identity.
Resolution requires all of: a positive PR number (PR-backed workspaces use their
own `item_number`; issue-backed and Kata-backed workspaces use
`associated_pr_number`), a synced repo row, a synced merge request row, and a
non-empty base branch on that row. When any of those is missing the API returns
"workspace merge target branch not available" and treats it as the
non-actionable state.

The server is authoritative for availability. The sidebar hides the
merge-target-dependent controls (both the Target scope control and the commit
range picker) whenever the workspace has no PR identity, which is necessary but
not sufficient: a workspace whose PR identity is present but whose merge request
row is unsynced, removed, or has no base branch can still surface those controls
and then receive the unavailable response. Clients must treat the unavailable
response as expected rather than an error, and a future change should expose a
resolved-merge-target signal on the workspace summary so the UI gate matches the
server check exactly.

## Diff Snapshot Coherence

- Files and patches project from one immutable snapshot. Preview membership is
  revision-pinned too, but new-side bytes remain live and may move afterward
  (`internal/server/workspaceapi/workspace_diff_cache.go::workspaceDiffCache`).
- Cache entries are stale-while-revalidate with last-known-good fallback.
  `jellydator/ttlcache/v3` owns entry storage, TTL expiration, and inactive-entry
  cost pressure through separate protected and cost-limited pools; middleman's
  wrapper owns snapshot coherence, bounded validation, selection leases, and
  publication. Only selected workspaces
  receive proactive refresh leases; ordinary entries validate on demand
  (`internal/server/server.go::streamEvents`).
- A workspace response is user-visibly stale only when a bounded, coalesced
  head-only probe confirms cached/current Git HEAD mismatch and queues refresh;
  cache age, probe timeout, and resolution failure do not warn (`internal/server/workspaceapi/workspace_diff_cache.go::workspaceDiffCache.Get`).
- A local workspace becomes selected through its scoped SSE stream. The server
  subscribes that stream before acquiring the selection lease, then emits
  `workspace_diff_ready` only when cold/coalesced default-HEAD preparation
  completes; a warm cache hit needs no readiness event. This ordering prevents
  fast preparation from racing ahead of the selecting browser.
- Fleet selection uses `/workspaces/{id}/diff/watch` through the fleet proxy to
  hold the remote lease, prewarm HEAD, and relay opaque versions. Switching
  aborts and replaces the watch, so only the selected remote workspace refreshes
  (`internal/server/workspaceapi/routes_handlers.go::watchWorkspaceDiff`). Empty or foreign
  tokens return `changed=true`; matches wait 25 seconds and timeout unchanged.
  A `409` while the workspace is still being created is transient and the client
  retries it with the normal watch backoff; unsupported watch responses remain
  terminal and fall back to request-driven diff loading.
- Workspace switching keeps runtime and shell reads on their own critical path.
  The previous sidebar is replaced immediately by a neutral placeholder. The
  new diff panel mounts after matching workspace metadata and either matching
  runtime state or a terminal runtime error, so a runtime API failure cannot
  leave workspace details hidden forever. Panel cancellation uses a per-load
  token in addition to workspace identity: cleanup from an older same-workspace
  load must not abort its replacement.
- Manual workspace refresh schedules asynchronous validation for every cached
  key belonging to that workspace, whether or not the workspace currently has
  a local selection lease, even when provider refresh later fails. Workspace
  responses and runtime readiness never wait on Git. Failure preserves the
  last-known-good snapshot; preserving browser refreshes retry with capped,
  cancelable backoff only when retained files and diff share a snapshot version,
  while cold loads expose blocking errors
  (`packages/ui/src/stores/diff.svelte.ts::loadWorkspaceDiff`).
  A changed fingerprint publishes through `workspace_diff_changed` when ready.
  Watcher hints validate selected keys even inside the freshness interval;
  periodic validation makes stale snapshots eligible for refresh, while the
  bounded queue is not a hard completion deadline
  (`internal/server/fleetapi/fleet_worktree_links.go::Handler.notifyWorktreeStatsChanged`). One background worker
  serializes proactive validation; foreground cold reads bypass that queue.
  Entryless cold failures stay with selection prewarm's five-second retry;
  periodic validation handles only published entries, so its one-second cadence
  cannot bypass cold backoff
  (`internal/server/workspaceapi/workspace_diff_cache.go::validateSelected`).
- The 128 MiB inactive-cache budget evicts least-recently-used inactive
  entries, never active snapshots. A newly published snapshot has a one-minute
  files/diff revision lease so an oversized `/files` response cannot evict
  itself before its pinned `/diff` read. Selected keys stop being protected
  after 10 minutes without access. Selected and pair-retained snapshots have
  zero eviction cost and may temporarily put the total working set above the
  inactive-cache budget
  (`internal/server/workspaceapi/workspace_diff_cache.go::maintainLocked`).
- Publishing a dirty-worktree snapshot requires matching before/after resolved
  refs and fingerprints; repository-local attributes are fingerprint inputs.
  Commit/range generated-file checks use the resolved head commit as the Git
  attribute source, while live worktree snapshots intentionally use worktree
  attributes (`internal/workspace/diff_snapshot.go::PrepareDiffSnapshot`).
- Whitespace-only classification is post-processing over aggregate Git output;
  raw mode/type changes remain substantive even when content differs only in
  whitespace. Ambiguous multi-hunk files compare complete old/new record
  sequences in Go. One batch Git process streams old blobs into ordered
  whitespace-normalized record digests, keeping memory bounded without per-file
  subprocesses (`internal/workspace/diff_whitespace.go::readWhitespaceBlobDigests`).
- Untracked-file content is required for binary detection, line totals, and
  synthetic patches, but snapshot preparation does not read an unbounded path
  list serially. Tracked/untracked fingerprint hashing and untracked patch
  construction share one file-read budget sized from the Go runtime's host
  parallelism at process start, so cache validation cannot multiply I/O
  concurrency. Results retain path order and cancellation propagates between
  files and read chunks
  (`internal/workspace/diff.go::untrackedReadPool.run`).
- Snapshot versions are opaque equality tokens. Clients may compare only for
  equality; ordering and replay position come from SSE event IDs, not from
  parsing the version or revision fields. A typed `snapshot_changed` preview
  conflict reloads the coherent files/diff pair once only while the preview's
  captured load token and generation still own the store; stale preview work
  fails without mutation
  (`packages/ui/src/stores/diff.svelte.ts::loadWorkspaceFilePreview`).
- Preserving refreshes publish only a fresh coherent files/diff pair; stale
  responses retain the visible pair and retry because same-fingerprint
  validation emits no change event
  (`packages/ui/src/stores/diff.svelte.ts::loadWorkspaceDiff`).
- Workspace diff and preview paths identify the current path first. Old paths
  are fallback aliases only, since a rename source can coexist with a new file
  at that path (`internal/server/workspaceapi/routes_handlers.go::filterWorkspaceDiffSnapshotPath`).
- Live worktree reads use Go's `os.Root` containment. Final symlinks are read as
  links, regular files are identity-checked across the open, and intermediate
  symlinks may resolve only within the worktree. Untracked patch reads and
  fingerprints use the same rooted opens, reject non-regular files, and remain
  cancellable while hashing; cached diff membership never authorizes traversal
  (`internal/workspace/diff_snapshot.go::fingerprintWorktreePath`).

## Worktree Branch Names

An unavailable branch name must never fail workspace creation: an unusable PR
head branch degrades to `middleman/pr-<n>`, then to a numbered variant of it,
then to a detached checkout with no managed branch
(`internal/workspace/manager.go::addFallbackWorktree`). Middleman owns the
synthetic name and its numbered variants and may delete them during cleanup;
any other pre-existing branch is user-owned and must keep pointing where it did.

## Branch Upstream

The branch's git upstream config (`branch.<name>.remote`/`.merge`) is the
single source of truth for every sync-derived workspace surface:
`commits_ahead`/`commits_behind` in the list response, the sidebar
ahead/behind arrows, push, pull, and unpushed-commit flags. All of them
silently report nothing when the upstream is missing, so every path that
creates a workspace-owned branch should configure it when repository identity
is known. Upstream wiring requires a non-empty head-repository identity whose
provider, host, and full repository path match the base repository; matching
commit SHAs are not identity evidence
because forks preserve commit IDs. Unknown and fork heads stay untracked. The
pushed-head observer may repair a missing upstream only when the current MR row
proves the head is in the base repository, the checked-out branch is the PR
head or synthetic branch, and the remote-tracking ref exists.

Test fixtures that seed PR rows must either carry a same-repo
`HeadRepoCloneURL` or publish `refs/pull/<n>/head` on the fixture remote:
unknown-provenance setup resolves heads exclusively through the fork-safe ref
and fails outright when the remote does not serve it.

## Pushed-Head Refresh Convergence

A tracking-ref/provider-head mismatch is not proof of a local push: when the
PR advanced from another checkout, the local tracking ref is the stale side
and a provider sync can never converge. The observer therefore enqueues a PR
sync on mismatch, retries on failure after `pushedHeadRefreshRetryInterval`,
but must stop once a refresh for the same observed SHA succeeded and the
provider head still differs (`LastRefreshSucceededAt >= LastRefreshEnqueuedAt`)
— otherwise the visible PR is re-synced and re-rendered forever. A tracking-ref
move restarts the cycle.

## Sidebar Ordering

The workspace sidebar has two separate activity concepts:

- `Activity`: terminal/runtime activity, ordered by `tmux_last_output_at` with
  `created_at` as the fallback.
- `Item activity`: provider item activity, ordered by `item_last_activity_at`
  with `created_at` as the fallback.

Keep these modes distinct. Do not relabel `Activity` to mean provider PR/issue
activity, and do not add compatibility aliases for old sort values without an
explicit migration reason.

`Org / repo` is the grouped ordering mode. Timestamp sorts are flat lists, with
ties broken deterministically by workspace ID so the visible order does not
shift between refreshes.

## Testing Expectations

Workspace API changes that alter summary fields or sorting inputs need coverage
at the boundary a client observes:

- DB summary tests should prove PR-backed, issue-backed, Kata-backed, and
  unsynced-owner workspaces expose the expected `item_last_activity_at` shape.
- Server/API tests should assert `/api/v1/workspaces` returns the generated JSON
  field for synced owner items and omits it for missing owner rows.
- Frontend sidebar tests should cover the relevant sort mode and fallback.
- Visible workspace sidebar changes need affected Playwright coverage before
  pushing.

## Non-Goals

- Represent arbitrary worktrees discovered on a host machine.
- Mirror an external workspace tree or host inventory.
- Serve as a generic Git automation API outside middleman's workspace lifecycle.

## Related context

- [`context/workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md)
  documents runtime-session exit, tmux persistence, and destructive ordering
  rules that sit underneath these APIs.
