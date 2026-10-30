# Workspace APIs

Use this document for changes to workspace creation, reuse, identity, routes,
or item-to-workspace association.

These APIs manage **kenn-forge-owned workspaces**: durable local execution
contexts for tracked PRs, provider issues, mapped Kata tasks, and ad-hoc work in
a tracked repository. They are not a generic Git worktree browser and not an
embedder protocol for arbitrary host state.

## Purpose

- Persist a kenn-forge workspace entry for a tracked item.
- Materialize that entry as a local Git worktree plus tmux session.
- Let the UI reopen the same workspace from `/workspaces` or `/terminal/:id`.
- Carry enough item metadata to render the correct sidebar behavior.

### Repository identity

- Workspace responses must keep `repo.platform_repo_id`; frontend detail caches need that
  permanent identity (`internal/server/workspaceapi/helpers.go::repoRefFromParts`).
- Provider workspaces persist by internal repository catalog ID. Routes resolve their current
  occupant first: a rename follows the same repository, while route reuse is a separate identity.
- A workspace whose repository row was deleted keeps a null `repo_id`; lifecycle code retires it
  only through dirty-aware deletion, never discards uncommitted work, and never resolves it
  through the current route occupant (`internal/workspace/launch_spec.go::Manager.RequireWorkspaceLaunchSpec`).
- A repository referenced by a workspace is a durable identity tombstone: host purge deactivates it
  and releases its route instead of deleting it (`internal/db/queries.go::DB.PurgeOtherHosts`).
- Setup verifies the stable repository ID independently of its route, before and after cloning.
  Managed clones partition storage by that ID, configured bases must match it, and route reuse
  must never share checkout state (`internal/workspace/manager.go::Manager.workspaceSetupGitDir`).
- Recovery and cleanup accept every bare clone in the repository's identity namespace, whatever
  route cloned it. Fetches into a managed clone retarget an old origin to the current route and use
  the required provider credential (`internal/workspace/manager.go::Manager.withCurrentManagedCloneOrigin`).
- A backfilled workspace may keep a route-keyed clone from any route with one stable owner; route
  reuse excludes that path (`internal/workspace/manager.go::Manager.workspaceManagedClonePaths`).
- New worktrees live at the plain `<platform>/<host>/<owner>/<name>/<item>` path so people can find
  them; only when another stable repository has owned that route does the directory become
  `<name>-<id>`; do not add identity segments to the common path (`internal/workspace/manager.go::Manager.workspaceRepoDir`).
- Execution workers may set `execution_worker.worktree_dir` for new worktrees; metadata and clones
  stay under `data_dir`, and existing workspace paths remain authoritative.
- Persisted `worktree_path` values must be absolute: setup runs Git from the clone directory while
  API reads resolve relative to the server process.
- Managed clones keep `core.bare=true` in shared config and override it per linked worktree;
  repository tools rely on the shared value to recognize the layout.
- Managed clones and local base checkouts use the same exact provider-host cleartext
  acknowledgement; local bases never weaken the transport policy (`internal/workspace/manager.go::ValidateWorktreeBasePath`).
- Configured bases resolve their read remote by repository identity, rejecting ambiguous matches or
  tracking-namespace overlap.
- Branch sync validates every URL and uses the configured remote's credentials; a first push may
  create a missing fork branch but never force-update it (`internal/workspace/branch_sync.go::pushWorktreeBranch`).

### Handler boundaries

- Workspace and Projects request state stays below the root server composition boundary; the
  handler gets deep-copied config snapshots, never the root mutable config or its mutex
  (`internal/server/workspaceapi/config.go::ConfigSnapshot`).
- Construct manager, runtime, tmux, clock, and enrichment dependencies before `Start`; nobody
  mutates them afterward (`internal/server/workspaceapi/handler.go::Deps`).
- Workspace clock overrides stay scoped to the handler; replacing the root clock changes unrelated
  domains' timestamps.
- Fleet consumes Workspace-owned summary and runtime snapshots, never the Workspace manager or root
  server (`internal/server/workspaceapi/fleet_snapshot.go::FleetSnapshot`).

## Endpoint Intent

- `POST /workspaces`: create or reuse a PR-backed workspace.
- `POST /issues/{provider}/{owner}/{name}/{number}/workspace` (with `/host/{platform_host}/...`
  for non-default hosts): create or reuse an issue workspace from the repo's current `origin/HEAD`.
  - Directory recovery takes no path and applies only when the workspace row is absent; an
    occupied deterministic path keeps its actual branch (`internal/workspace/manager.go::Manager.CreateIssue`).
  - Recovery validates repository provenance before persistence and again during setup; a
    deterministic path is not identity without a matching origin. Pending recovery must adopt the
    directory, never create or clean it up, and retry/delete preserve it until setup publishes the branch.
- Selections and loaded item details carry the provider's stable ID through local, fleet, and devbox
  creation, which rejects a different active repository at the route and unknown or inactive IDs;
  route-only callers keep current-route semantics (`internal/server/httpapi/repository_resolver.go::RepositoryResolver.LookupSelection`).
- `POST /repo/{provider}/{owner}/{name}/workspaces`: create or reuse an ad-hoc workspace with no
  source item. Its branch is its identity (`adhoc:<branch>`, `item_number` 0), so number-derived
  fallbacks must exclude it; requesting the same branch again returns the existing workspace.
  - Local branch or ref-namespace conflicts get a random four-character suffix as the persisted
    identity. Setup never rewrites it: a later Git ref collision fails explicitly.
  - Once pushed, a linked `associated_pr_number` is the workspace's item identity for display,
    links, and search; never gate PR affordances on `item_type == "pull_request"` alone.
  - PR state and title come from that PR locally and in hub enrichment, but it is not the source
    item, so `Source*` fields stay empty (`internal/db/queries.go::scanWorkspaceSummary`).
  - A hub may route creation to itself or a writable spoke; a spoke offers only itself.
  - A commit-pinned ad-hoc workspace persists its full source revision before setup and cannot
    reuse a branch; renames never change that revision, and source reads verify the actual
    checkout and dirtiness (`internal/workspace/source_revision.go::Manager.SourceState`).
- Kata routes under `/kata/daemons/{daemon_id}/` search references, return read-only issue detail,
  and resolve Kata's safe external launch target on an explicitly pinned daemon; Forge never
  caches or persists the task payload.
- `POST /kata/workspaces`: create or reuse a Kata-task workspace; it never resolves or syncs a
  provider issue row.
  - Reuse identity is daemon ID plus issue UID, independent of the task's project, so project
    moves reopen the existing workspace; concurrent creates must never duplicate it.
- Terminal and mobile pickers read the projected snapshot and never fan out per-host reads; remote
  actions require advertised availability.
- A hub may proxy workspace operations to an active member by stable node ID; a spoke operates only
  on `self`, and other spokes are summary-only, so mutation affordances exist only on the owner and
  the hub (`internal/server/fleetapi/fleet_proxy.go::Handler.resolveFleetHostTarget`).
- Spoke raw workspaces carry stable repository identity but no provider overlay or local numeric
  repository ID; the hub owns enrichment.

### Coordinator chat routes

- `GET`, `POST .../commands`, and `POST .../restore` under
  `/workspaces/{id}/runtime/sessions/{session_key}/chat` let a coordinator drive a chat over HTTP
  under normal API auth; execution workers accept only their bearer token (`internal/server/workspaceapi/acp_http.go`).
- A chat that is not attached first reconnects the workspace's chats as opening it does.
- A stored chat returns only `exited: true` when its owner is known gone (seen exiting, or a
  `cannot_reload` relaunch); any other attach failure, such as workspace setup, is `503`.
- A transcript `index` can shift by one when a prompt or steer inserts its user message before
  output that arrived while sending; pagers may see a message twice (`submissionId` identifies it).
- Owner refusals are `409` with the owner code in `details.reason`; a disconnected or stopped chat
  is `503`; anything else is `400` (`internal/server/workspaceapi/acp_http.go::chatCommandProblem`).
- A command whose change took effect is accepted even if the save after it fails; the chat's `error`
  reports the failure.
- Restore applies only to coordinator-held chats and refuses unsupervised or taken-over chats,
  agents without `loadSession`, and workspaces that setup or deletion owns.
- After a `cannot_reload` failure, reads, commands, and opens skip the chat instead of starting an
  agent each time; an explicit restore retries, and success or stop clears the mark.

### Creation and launch specifications

Launch specifications, source-visibility leases, and hub-outage behavior are owned by
[`fleet-architecture.md`](./fleet-architecture.md) (Provider Facts For Local Execution).

- Issue branch overrides, branch/directory reuse, worktree base paths, and assignment suppression are
  spoke-local request policy, never persisted in hub provider facts.
- Preparation inventory must close its SQLite cursor before per-workspace reads; the bounded read
  pool cannot nest them (`internal/db/queries_workspace_launch_specs.go::DB.ListUnpreparedProviderWorkspacesAt`).
- Creation is event-confirmed, not response-owned: `workspace_created` carries the persisted ID and
  a `created` flag; clients load that ID to recover canonical identity.
- Create endpoints may return 202 with a pre-existing workspace.
- The created event precedes setup status and duplicates are idempotent; with stale replay, list
  reconciliation recovers identity and status without replaying launch or navigation.
- Automatic self-assignment is opt-in and runs only after a new PR/issue workspace persists,
  independent of setup; it preserves assignees and never rolls back on upstream failure.
- Background assignment keeps the launch repository's stable ID locally and through federation; a
  reused route never changes its target (`internal/server/workspaceapi/auto_assign.go::Handler.AutoAssignProviderWorkspaceItem`).

### Spare worktrees

- Inspect warm clones locally for branch conflicts; setup owns the fresh fetch. Cold admission still
  creates the clone so its branches join conflict handling.
- Warm every repository that has had a workspace, even after deletion or restart, keeping one
  detached spare per Git directory and destination parent, with no runtime sessions.
- Populate spares outside the repository lock; readiness must survive cancellation without exposing
  incomplete files or blocking foreground creation.
- Claim only clean, owned spares at the freshly fetched revision; leave changed or foreign spares.
- Compare warm claims and cold creation against `main` with benchstat when changing them or kwt/Kit
  lifecycle (`internal/workspace/hot_worktree_test.go::BenchmarkHotWorktree`).

### Enrichment and activity

- List and detail reads return persisted plus last-known-good enrichment without foreground Git or
  tmux probes; stale components refresh in bounded background work.
- Background Git enrichment never takes optional repository locks, so it cannot interfere with a
  maintainer's own Git commands.
- Activity and Issue/PR responses share one cached subject snapshot: every resolvable subject keeps
  a workspace reference, while tmux activity is optional and ephemeral.
- Spokes overlay their snapshot onto hub pull, issue, and Activity rows by stable identity,
  preserving membership, order, and cursors; assignment filters use hub state.
- Enrichment is best-effort on Issue/PR detail (failures omit workspace metadata); list and
  Activity reads fail fast because the snapshot affects ordering and identity.
- Only enabled workspace recency merges the snapshot into Threaded and Mobile Activity, after
  provider-event filters and including eventless subjects; disabled mode is provider-event-only.
- With recency enabled, `#number` search and author filters also match eventless workspace
  subjects in the same scope, and matching events keep recency across polls; disabled mode returns
  provider results unchanged.
- Key the snapshot by stable repo ID and canonical item type; normalize wire `"pr"` to
  `"pull_request"` before lookup (`internal/server/itemapi/helpers.go::WorkspaceItemTypeFromActivity`).
- Subject metadata uses JSON-backed SQLite relations so many workspaces cannot exhaust bind
  variables. Lists always expose `last_workspace_activity_at`; provider activity is the default.
- An issue keeps its own workspace affordance after its work resolves to a PR; the PR gets the tmux
  recency candidate, used only when recency is enabled.
- Tmux recency publishes at most once per rolling minute per workspace after the first sample;
  older samples never regress it.
- Aggregate enrichment is fresh only after divergence and tmux both complete. Each component keeps
  its own cadence (30s divergence, 5s tmux) and errors; one's success never clears another's.
- Divergence refresh re-validates from a stat-only Git-directory fingerprint and runs Git only when
  it moved or the forced interval elapsed; explicit refreshes always probe.
- Worktree-only edits never move the fingerprint. Fingerprinting never spawns Git, and no timer
  probes an unread workspace.
- A partial tmux probe replaces last-known-good only when an answering session shows the agent
  working; the failed session may be the working one.
- Each workspace admits one background enrichment flight at a time.
- Do not rely on cached earlier tmux samples to rescue later probes under load; they expire before
  the next refresh.
- `workspace_status` is emitted only for durable changes (first completion, divergence, error
  state), never tmux-only movement; unconditional broadcasts cause a permanent refetch loop.
- Client detail stores ignore content-identical refreshes; equal-but-new objects re-render the
  whole panel every cycle (`frontend/src/lib/stores/detail.svelte.ts::applyRefreshedDetail`).

### `DELETE /workspaces/{id}`

Deletion ordering lives in [`workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md).

- Confirmed removal emits full provider, repository, and item identity; merge cleanup and ordinary
  deletion share this lifecycle.
- Admission is a compare-and-swap from `ready`, `error`, or `deletion_failed`; a setup that wins
  gets a conflict, retries conflict until teardown ends, and one failed request cannot reopen setup
  while another owns teardown (`internal/db/queries.go::DB.BeginWorkspaceDeletion`).
- Conflicts use stable `worktreeDirty`, `workspaceSetupInProgress`, and
  `workspaceDeletionInProgress` codes; clients offer force deletion only for a dirty worktree.
- On a setup/deletion conflict, clients refresh into the authoritative lifecycle state instead of
  assuming an outcome.
- Setup rejects occupied destinations before clone/fetch and again under the repo lock; failed setup
  must never remove replacement artifacts (`internal/workspace/manager.go::createWorkspaceCheckout`).
- Publish the workspace-ID marker before fallible post-add configuration; a failed marker write
  keeps the checkout without granting rollback authority.
- Existing-worktree reuse accepts only a non-symlink root, revalidated under the repo lock.
- Setup rollback requires a matching workspace-ID marker; preserve unmarked or mismatched roots.
- Explicit deletion may accept a live same-repository registration at the recorded path without a
  marker; this never extends to rollback, and a symlink to another worktree is not the workspace.
- Only exact, non-symlink Git roots whose `.git` matches their registration count as live;
  unrelated paths are preserved.
- A recorded symlink to another same-repository worktree permits row deletion while keeping the
  link and target.
- Pre-lock cleanup observations are advisory; revalidate authority as the first locked action.
- A force delete keeps the workspace row when Git cannot remove a live owned worktree; only errors
  proving it absent or corrupt continue to branch and row cleanup.

## Data Model Intent

- Tracking targets never change ownership, branch association, push routing, or cleanup
  eligibility; merged targets do not close workspaces.
- Target lists combine explicit and implicit links; UI removal dismisses either until a deliberate
  revisit, without removing associations. Unavailable metadata keeps undismissed links.
- Devbox target links and dismissals belong to the worker; identity and display metadata come from
  the controller. Dismissal needs only stable identity.
- Tracking a devbox target needs no Git admission or worker provider replicas
  (`internal/server/devbox_targets.go::Server.updateDevboxWorkspaceTarget`).
- A spoke's sparse or stale catalog must not limit target visits; refresh the hub descriptor by
  stable identity before every visit.
- `item_type` is `pull_request`, `issue`, or `kata_task`. Kata workspaces key by a composite of
  daemon ID, project UID, and issue UID so IDs from different Kata scopes cannot collide;
  their `item_number` is `0` and must not be used for identity.
- Kata branch and worktree leaves add a short stable hash of that composite to the readable slug,
  so project-scoped visible task IDs do not collide in one repository.
- Pull detail prefers a direct PR workspace, then the newest issue, Kata, or ad-hoc association;
  status is not a selection input (`internal/db/queries.go::DB.GetWorkspaceLinkedToMRForProvider`).
- Merge-request sync writes head-repo trust only to direct PR workspaces; associations are
  presentation links.
- PR workspaces show PR/Reviews sidebars; issue workspaces show the issue sidebar without the PR
  path; Kata workspaces show the read-only contextual Kata links panel.
- Summaries join the owning item by full provider identity; when it has not synced,
  `item_last_activity_at` stays absent rather than invented. Kata workspaces never join provider
  item tables.

### Kata repository resolution

Resolution is deliberately exact (`internal/server/kata/workspace.go::Handler.resolveKataWorkspaceRepoResolution`).

- Manual mappings key by optional daemon ID plus project UID and point to a known repository
  identity, including registered Projects; removing a watched repo does not delete an override.
- First, watched exact repos with `worktree_base_path` whose `.kata.toml` `project.uid` or
  `project.identity` equals the project UID; several matches are ambiguous. A clone may match by
  name only when it has neither identifier, and then exactly one case-insensitive name must match.
- Next, exactly one non-stale registered Project with provider identity, by case-insensitive exact
  name, preferring `.kata.toml` over display name; distinct matching checkouts are ambiguous.
- Last, one synced repo matched by exact or globbed config without readable metadata, by name.
- A registered match carries its checkout into creation; a configured clone carries its base path.
- Ambiguous, mismatched, or missing matches hide Create/Open workspace, and the Kata link keeps a
  typed resolution reason.
- Settings lists each project with the resolver's status and source; the selector defaults only to
  an inferred identity match and persists repository identity.

### Repository hooks and worktree library

- Managed-clone hooks carry the exact common Git directory and ownership flag; never infer ownership
  from paths or run them for `worktree_base_path` repositories.
- Roborev setup requires common-directory hooks, trusts the default branch only from fetched remote
  refs, and adds a snapshot exclude that never replaces user exclusion rules; each agent launch
  rebuilds it best-effort (`internal/workspace/repository_hooks.go::refreshManagedCloneExclude`).
- A custom effective hooks directory prevents hook initialization; shared hook changes roll back
  unless registration succeeds.
- Only regular root config files on the trusted commit count; a Roborev snapshot path must match it
  or use `.roborev` (`internal/workspace/repository_hooks.go::Manager.setupManagedRepositoryHooks`).
- Roborev initialization is optional and only warns; repository identity validation is mandatory.
- Reusable worktree lifecycle belongs in the public `kwt` library over Kit; add customization there
  while preserving Forge behavior. Forge owns provider policy, persistence, runtimes, recovery names,
  and cleanup authority.
- Projects and item workspaces share repository coordination, which must exist without the item
  manager (`internal/workspace/repository_worktrees.go::NewRepositoryCoordinator`).
- Registration scans, orphan preservation, and branch deletion stay in the shared library.
- Item Git disables native hooks. Projects native hooks run under the repository lock, so
  synchronous reentry into Forge mutation is unsupported; setup/teardown scripts run outside it,
  with removal revalidated afterward.
- After Projects registration failure, preserve changed checkouts, report retained artifacts, never
  discard rollback errors or imply the destination is clear; the problem keeps the cause's status
  and code and sets `details.cleanupIncomplete` (`internal/server/workspaceapi/projects_handlers.go::withRetainedArtifacts`).
- Classify same-repository merge requests by provider-hosted project identity, not origin URL,
  which may be a local mirror.

## Agent Launch Context

- Agent families (Codex, Pi, Claude) are selected by case-folded target-name prefix.
- Codex and Pi get generated context followed by root `AGENTS.md` verbatim in `AGENTS.override.md`,
  appended only from a non-symlink regular file up to 1 MiB; Claude gets context-only
  `CLAUDE.local.md`, which is additive. Setup writes no instruction file.
- A root-confined first-line marker owns refreshes: only marked files are updated; unmarked override files,
  symlinks, and root `AGENTS.md`/`CLAUDE.md` stay untouched.
- Context carries source identity and PR push-target facts agents cannot read from the worktree.
- Source-system prose is XML-escaped inside `<untrusted-source-text>` fences, the prompt-injection
  boundary; one-line identifier normalization is not a trust boundary, so new prose fields go
  through the fence.
- Ignore the generated path through the worktree's private exclude, not tracked `.gitignore`; if it
  would stay visible to Git, the write fails.
- Head-repo trust comes from the persisted launch specification on every lifecycle admission; setup,
  retry, branch sync, agent launch, and hook context fail when it cannot be validated or persisted
  (`internal/workspace/launch_spec.go::Manager.RequireWorkspaceLaunchSpec`).
- Hub sync reclassifies tracking workspaces from each accepted MR snapshot best-effort; an
  unknown-head snapshot never downgrades a known fork (`internal/github/index_sync.go::reclassifyWorkspaceHeadRepoTrust`).
- Head-repo classification keys on the workspace repository ID; provider workspaces without one fail
  unresolved, and renames must never turn a known head into `unknown`.
- Launch-spec refresh keeps stable repository and branch identity even when another repository holds
  the old route, and creation admission never blocks it or spoke preparation. A changed identity conflicts; an expired
  lease plus hub outage is retryable (`internal/server/provider_state_handoff.go::Server.RefreshWorkspaceLaunchSpec`).
- Removed or inaccessible PRs fail closed: they contribute no title, URL, branch, or push target to
  generated context (`internal/workspace/agent_context.go::PrepareAgentLaunchContext`).
- Branch push and pull use the route returned by launch-spec validation, never one captured before a
  refresh (`internal/workspace/branch_sync.go::Manager.validateBranchSyncLaunchSpec`).
- Summaries resolve repositories by stored stable ID, so renames keep projections and inactive
  repositories drop them; unresolved legacy rows need an unambiguous active route.
- Agent launches validate the launch specification and renew an expired lease while preparing
  context; a hub outage or hidden source blocks provider-backed launch, while ad-hoc and Kata stay local.
- The launch API accepts only the target and display region.
- Creation launches an agent only after an explicit target choice on the create split button;
  primary creation never launches, and a fork-PR choice shares the manual Launch-menu trust boundary.
- Each recorded agent runtime adds one durable launch event per session; cleanup never erases this
  preference history (`internal/db/workspace_agent_usage.go::DB.RecordWorkspaceRuntimeSession`).
- A settings save replaces `agents` and `launch_targets` from its response, so fixes update
  availability without a reload.
- Startup and `config_changed` hot reload hydrate the same `GET /settings` fields from two call
  sites; a field added to only one goes stale after a config edit
  (`frontend/src/lib/stores/settings-hydration.ts::applySettingsHydration`).

### Quick actions and agent handoff

- Quick actions are the one backend-owned launch: after creation the header posts the agent and
  prompt to `POST /workspaces/{id}/runtime/agent-handoffs`; existing local and devbox workspaces offer
  them too, fleet peers do not (no handoff route).
- The endpoint waits for readiness and launches in the workflow region; `acp_options` are refused
  for non-ACP targets before waiting, and input validation always precedes waiting.
- Native Claude prompt arguments are recorded as delivered so retries never type them twice; other
  agents retry only the typed input-mode-not-ready signal.
- Handoff pacing and retry rules belong in the shared `agenthandoff` package that the MCP spawn tool
  also drives, never in either caller (`internal/workspace/agenthandoff/agenthandoff.go::Deliver`).
- Handoffs run on their own context so navigating away cannot strand an agent without its prompt;
  shutdown cancels it before HTTP drain, and a request after cancellation never starts a wait.
- A post-launch delivery failure leaves the runtime running and returns `session_key`, `target_key`,
  and `initial_message_state`; the header reports a launched-but-promptless agent, not a failed
  start, and only `not_delivered` is worded as a definite loss.
- Quick action prompts are normalized at config load so a saved action is never rejected after its
  workspace exists. Nothing is queued in frontend launch state; the session arrives via runtime events.

## Agent Activity Hooks

- Kit owns hook profiles, config mutation, payload normalization, and native responses; thin clients
  relay normalized events and the daemon owns activity transitions.
- Claude `SessionStart` context is regenerated from persisted workspace metadata, never read from
  instruction files.
- Install and uninstall target all Kit profiles by default or one with `--agent`; they preserve
  unrelated handlers and never enable agent consent or auto-approval.
- Reuse Codex approvals only for identical hook hashes in sibling worktrees, including through
  `sh -c`/`bash -c` launchers; preserve disabled hooks and existing decisions (`internal/codexhooks/trust.go::ReuseApprovals`).
- Named Codex profiles keep native hook review because the app-server cannot select profiles.
- Live reports rank approval, input, working, done, then idle. Row activation keeps completion and
  sort position; outside that sort it acknowledges a completion, and a new timestamp resurfaces it.
- Hook installs require absolute data roots and preserve config symlinks; report matching uses
  canonical paths.
- The active sidebar polls every five seconds, and hook receipt fails open.

## Diff Scopes

- Diffs compare against local `HEAD`, the pushed branch, or the merge target, which exists only when
  the server resolves a real base branch from a synced merge request (own `item_number` or
  `associated_pr_number`); otherwise it is the non-actionable "workspace merge target branch not available".
- Spoke PR targets use launch specifications, renewing expired visibility through the hub; never
  require spoke-local PR cache rows.
- The Diff panel opens on `HEAD` when dirty or dirtiness is unknown, the pushed branch for unpushed
  commits with an upstream, else the merge target; a user's pick wins for the panel's lifetime.
- The server is authoritative for availability. The UI hides target controls without a PR identity,
  which is necessary but not sufficient, so clients treat the unavailable response as expected, not
  an error (known gap: the summary has no resolved-merge-target signal yet).

## Diff Snapshot Coherence

- Files and patches project from one immutable snapshot and are retained only as a matching
  revision pair; preview membership is revision-pinned, but new-side bytes stay live.
- Repeat visits restore bounded browser snapshots by host and full diff query and revalidate without
  blanking the pair (`frontend/src/lib/stores/diff.svelte.ts::startWorkspaceDiff`).
- The cache is stale-while-revalidate with last-known-good fallback; `jellydator/ttlcache/v3` owns
  storage and expiry, the server wrapper owns coherence, validation, selection leases, and publication.
  Only selected workspaces get proactive refresh (`internal/server/workspaceapi/workspace_diff_cache.go::workspaceDiffCache`).
- A response is user-visibly stale only when a bounded head-only probe confirms a HEAD mismatch;
  cache age, timeouts, and resolution failures never warn.
- Local diff selection reuses the singleton SSE connection; selection leases and fleet watches run
  only while the Diff pane is visible.
- Late workspace subscribers receive current connected state; fan-out is lossless while subscribed.
- The server subscribes before taking the selection lease and emits `workspace_diff_ready` only for
  cold or coalesced preparation. Ready never cancels an active read; only `workspace_diff_changed`
  advances the visible refresh token.
- Fleet selection holds the remote lease through `/workspaces/{id}/diff/watch` via the fleet proxy;
  switching replaces the watch. Empty or foreign tokens report changed, a `409` during creation is
  retried, and unsupported watches fall back to request-driven loading.
- A repeat workspace visit restores its cached sidebar; a cold visit shows a neutral placeholder.
- Workspace switching keeps runtime and shell reads on their own critical path; the diff panel mounts
  after matching metadata plus runtime state or a runtime error, so a runtime failure never hides details.
- Cleanup from an older load of the same workspace must not abort its replacement.
- Manual refresh validates every cached key of the workspace even when provider refresh fails;
  responses and runtime readiness never wait on Git.
- Failed refreshes keep the last-known-good snapshot unless a missing or rejected scope invalidates
  it; commit-list errors stay in the picker. Cold loads expose blocking errors.
- Preserving refreshes publish only a fresh coherent pair and retry stale responses with capped
  backoff, since same-fingerprint validation emits no change event.
- Watcher hints validate selected keys even inside the freshness interval; periodic validation must
  never bypass the cold-failure retry backoff.
- The inactive-cache budget (128 MiB) evicts least-recently-used inactive entries, never active
  snapshots; a new snapshot stays pinned long enough for its paired `/diff` read, and selected keys
  lose protection after 10 idle minutes. Selected and pair-retained snapshots may exceed the budget.
- Publishing a dirty snapshot requires matching before/after refs and fingerprints; repository-local
  attributes are inputs. Commit/range checks use the head commit's attributes, live snapshots the worktree's.
- Whitespace-only classification never hides mode or type changes, and stays memory-bounded without
  per-file Git subprocesses.
- Untracked reads share one host-parallelism-sized read budget with fingerprinting, keep path order,
  and stay cancellable (`internal/workspace/diff.go::untrackedReadPool.run`).
- Snapshot versions are opaque equality tokens; ordering comes from SSE event IDs. A `snapshot_changed`
  preview conflict reloads the pair once only while that load still owns the store; stale preview
  work fails without mutation.
- Diff and preview paths match the current path first; old rename paths are fallback aliases only.
- Live worktree reads use `os.Root` containment: final symlinks read as links, regular files are
  identity-checked across the open, intermediate symlinks stay inside the worktree, non-regular
  untracked files are rejected, and cached diff membership never authorizes traversal (`internal/workspace/diff_snapshot.go::fingerprintWorktreePath`).

## Worktree Branch Names

- An unavailable branch name never fails creation: an unusable PR head branch degrades to
  `kenn-forge/pr-<n>`, a numbered variant, then a detached checkout (`internal/workspace/manager.go::addFallbackWorktree`).
- Forge owns the synthetic names and may delete them; any other pre-existing branch is user-owned
  and must keep pointing where it did.
- Ad-hoc workspaces reserve their final hashed branch identity before setup.
- PR worktree creation and reuse sync the local base branch. Managed clones follow rewritten
  upstream bases after saving the old tip under `refs/kenn-forge/base-backups/<sha>`; checked-out,
  diverged user-checkout, and ref-blocked bases stay untouched (`internal/workspace/manager.go::Manager.syncWorkspaceBaseBranch`).

## Branch Upstream

- The branch's Git upstream config is the single source of truth for push, pull, and unpushed flags;
  without one they report nothing.
- A PR workspace without an upstream (every fork head) counts against the fetched merge-request
  head ref, flagged `commits_vs_pr_head`; clients never offer branch sync for flagged counts.
- Fork pushes never move that ref, so it is re-fetched while it lags the provider head, without
  stalling other workspaces; the provider's pull ref can trail a push.
- A configured upstream with a missing tracking ref exposes `branch_upstream_missing` so the UI can
  offer the push that creates or verifies it.
- Every path that creates a PR-owned branch configures its upstream when identity is known, on the
  same identity-selected remote used for checkout, which may not be `origin`.
- Passive enrichment never contacts remotes: Push is offered only for a missing tracking ref or
  commits ahead, and a zero-ahead branch deleted remotely stays in-sync until local state changes.
- Issue, Kata, and ad-hoc workspaces create untracked branches; a same-named remote ref is not
  authority to adopt an upstream.
- PR upstream wiring requires a head-repository identity matching the base's provider, host, and
  full path; matching SHAs prove nothing because forks keep commit IDs. Fork heads stay untracked,
  and unknown heads fail closed before Git access.
- The pushed-head observer may repair a missing upstream only when a current hub candidate proves a
  same-repository head on the PR or synthetic branch with an existing tracking ref, rechecked under
  the repository lock; ordinary observer reads stay lock-free (`internal/workspace/pushed_head_gitdir.go::gitdirRemoteHeadReader.SetBranchUpstream`).

## Pushed-Head Refresh Convergence

- A tracking-ref/provider-head mismatch is not proof of a local push: when the PR advanced
  elsewhere, the local ref is stale. Retry failed syncs, but stop once a sync for the same observed
  SHA succeeded and the head still differs, or the PR re-syncs forever; a ref move restarts it.
- The observer's read path spawns no processes (go-git in process); do not reintroduce per-pass Git
  subprocesses, since only upstream repair and a lagging head-ref fetch run Git (`internal/workspace/pushed_head_gitdir.go::gitdirRemoteHeadReader`).

## Sidebar Ordering

The workspace sidebar has two separate activity concepts:

- `Activity`: terminal/runtime activity, ordered by `tmux_last_output_at` with
  `created_at` as the fallback.
- `Item activity`: provider item activity, ordered by `item_last_activity_at`
  with `created_at` as the fallback.
- `Agent status` groups approval, input, working, done, idle, then rows without a report; ties use
  hook recency, item activity, then creation (`frontend/src/lib/components/terminal/workspaceListSort.ts`).
- Each flat sort shows its driving timestamp as an unlabeled relative time.

Keep these modes distinct. Do not relabel `Activity` to mean provider PR/issue
activity, and do not add compatibility aliases for old sort values without an
explicit migration reason.

`Org / repo` is the grouped ordering mode. Timestamp sorts are flat lists, with
ties broken deterministically by workspace ID so the visible order does not
shift between refreshes.

## Testing Expectations

- Changes to summary fields or sorting inputs need coverage at the boundary a client observes: DB
  summaries, the `/api/v1/workspaces` JSON, and the frontend sort mode and fallback.
- Provider-backed lifecycle fixtures must seed the matching launch specification even when they seed
  hub PR rows. Same-repo specs need the synthetic `refs/pull/<n>/head` on the base remote; fork specs
  carry the exact fork URL and branch setup fetches.

## Non-Goals

- Represent arbitrary worktrees discovered on a host machine.
- Mirror an external workspace tree or host inventory.
- Serve as a generic Git automation API outside kenn-forge's workspace lifecycle.

## Related context

- [`context/workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md)
  documents runtime-session exit, tmux persistence, and destructive ordering
  rules that sit underneath these APIs.
