# Platform Sync Invariants

Use this document for changes that touch provider-aware repository identity,
sync, import, server routes, settings, or API responses. For package layout,
provider interfaces, and the checklist for adding a new provider, read
[`context/provider-architecture.md`](./provider-architecture.md) first.

## Identity

Repository identity is `(platform, platform_host, key)`. The key is a positive
integer repository ID for every provider except Bitbucket Cloud, which uses its
repository UUID (`platform/repository_key.go::RepositoryKey`). GitHub's `node_id`
is never identity. Owner, name, and `repo_path` are only the current route.

### Repository keys

- Every Go value that identifies a repository holds a `platform.RepositoryKey`, never
  a bare integer ID or separate UUID field; its fields stay unexported so no code can
  check half of it (maintainer decision).
- Flat `platform_repo_id`/`bitbucket_repository_uuid` values exist only in encodings
  (SQL, JSON, federation payloads, config); decode to a key where the value enters.
- Never add a parallel JSON struct that copies fields to encode a keyed type; derive the
  flat form from the type itself (maintainer decision, `platform/repository_key_json.go`).
- Exception: an outbound DTO whose Go use is only encoding may hold the flat fields,
  set only from `RepositoryKey.Wire`.
- OpenAPI names and shapes for keyed types do not change; a huma API serving them must be
  built with `WithRepositoryKeyWireSchemas` or its validator rejects the flat members.
- A type embedded in another DTO must not define `MarshalJSON`; the promoted method drops
  the outer fields. MCP tool schemas stay flat for the same reason.
- Only provider adapters read the key's ID or UUID kind directly.
- Frontend maps and persisted state key repositories by `repositoryKeyString`, whose
  `id|<n>` form keeps already-stored integer keys valid (`frontend/src/lib/api/repository-key.ts`).
- Never hash a UUID into an integer (`internal/server/httpapi/repository_resolver.go::PlatformRepoRef`).
- An absent Cloud UUID is stored as empty text, never the nil UUID's canonical string
  (`internal/db/repository_key.go::repositoryKeyArgs`).
- Clone partitions, cooldown and credential-alias keys, archive snapshot IDs, and
  provider-state source keys format integer keys as the decimal ID so existing state
  keeps its names (`internal/gitclone/clone.go::WithRepositoryIdentity`).

### Routes and observation

- Owner/name is edge input: resolve it to a repository once where it enters and key
  everything below by the repository; a request carrying a provider ID resolves by that
  ID (`internal/db/queries.go::DB.GetRepoByIdentity`).
- Keep provider calls route-based where the provider API is; the ID confirms which
  repository answered. Do not rewrite owner/name reads into ID reads (maintainer decision).
- Observing a repository upserts by key and moves owner/name in place; another active
  row on that route is deactivated and re-resolves by its own key
  (`internal/db/repository_catalog.go::DB.ObserveRepository`).
- There are no route generations, fences, or observation watermarks; a route changing
  hands mid-pass self-heals on the next pass.
- Every repository row has a verified provider key; nothing creates route-only rows, and
  route-only callers look up the active occupant or leave the link unset.
- Hub descriptors are provider observations like sync; spokes never create a catalog
  row from owner/name alone.
- GitHub rows still holding a pre-integer `github_node_id` block GitHub observations
  for that host until converted, so history cannot split across rows
  (`internal/db/repository_catalog.go::ErrGitHubRepositoryConversionPending`).
- Spokes never sync, so they convert pending GitHub rows through the hub on each
  connection; without it every hub descriptor fails.
- Two node IDs can name one repository; the duplicate merges into the existing row,
  and every column holding a `forge_repos` id must be listed in `repositoryOwnedColumns`
  (`internal/db/repository_catalog.go::mergeDuplicateRepositoryTx`).
- A route that its repository leaves, or another repository takes, clears state keyed
  only by that route (`internal/db/repository_catalog.go::deleteRepositoryRouteScopedStateTx`).
- Scope repository-owned data by internal repository ID; workspaces follow renames and a
  replacement repository on the old route never inherits them
  (`internal/workspace/manager.go::Manager.verifyWorkspaceRepository`).
- Saved repository-filter presets resolve by stable identity, reject unverified members,
  and never fall back to a new occupant of the stored route
  (`internal/config/config.go::RepoPresetRepository`).
- Offline startup and reload preserve pinned identity through a matching verified
  reference or leave the entry untracked; a saved route cannot establish identity
  (`internal/github/repo_config_resolver.go::FallbackConfiguredRepoRefs`).
- Compare repositories with `platform.RepositoryIdentity` equality via `Identity()`,
  never field by field (`internal/db/types.go::ActiveRepo`).
- `ActiveRepo.Row()` is not nil-safe; handle a `nil, nil` not-found lookup first or
  `make nilaway` flags every downstream dereference.

### Route fields

- `platform` is a kind from the canonical provider list in `CLAUDE.md`;
  `platform_host` is the normalized host and preserves ports.
- `owner` and `name` are provider-canonical display/config fields; `repo_path` carries
  the full provider path when `owner/name` is not enough.
- GitLab nested namespaces make `repo_path` mandatory (`group/subgroup/project` has owner
  `group/subgroup`); GitHub may omit it when the path is exactly `owner/name`.
- Preserve provider-canonical repository-path casing unless provider metadata opts into
  case folding (`platform/metadata.go::LowercaseRepoNames`).
- Gitea `base_url` changes API transport only; `platform_host` stays identity, and
  authenticated paths reject mismatched or unacknowledged plain-HTTP clone URLs
  (`platform/gitea/client.go::validateRepositoryCloneURL`).

### Repository metadata

- Persist provider-supplied web URLs, clone URLs, default branches, platform IDs, and
  external IDs instead of reconstructing them from host/owner/name.
- Every settings-refresh branch must write repository metadata; a branch that skips it
  leaves `default_branch` empty and silently degrades worktree diff stats to bare HEAD.
- Omitted provider fields keep stored values; merge settings are written only when the
  provider returns the complete group (`internal/db/repository_catalog.go::DB.UpdateRepoProviderObservation`).
- A failed settings write aborts the pass before item indexing; new rows start with
  viewer merge permission off.

### Hidden repositories

- Hidden-from-UI is keyed by the stable catalog row, never config routes: renames keep
  it, route reuse never inherits it, and only exact configured repositories can be hidden.
- Visibility mutations resolve by stable provider ID and reject non-active rows
  (`internal/server/settingsapi/settings_handlers.go::Handlers.ApplyVisibilityUnderReconciliationRead`).
- Removing the last exact entry releases the hidden preference on every config-change
  path, and a racing visibility mutation must not recreate it; globs expose no
  visibility controls (`internal/server/settingsapi/settings_handlers.go::Handlers.ReconcileOrphanedRepoVisibility`).
- Filtering is server-owned and applies only to interactive repository catalogs; item
  feeds, direct routes, and settings stay unfiltered
  (`internal/server/repoapi/helpers.go::Handlers.FilterHiddenRepos`).

## Provider Hosts And Tokens

- Self-hosted hosts are hostnames with optional ports, not URL paths.
- A missing token fails only the provider host that needs it.
- Each (provider, host) has its own fallback token chain; when providers sharing a
  hostname disagree, the ownerless host clone fallback is disabled rather than borrowing
  a credential (`internal/config/config.go::Config.CloneTokenDescriptors`).
- Runtime route resolution must honor that disabled state, never falling through to
  another provider's unscoped route.
- Two providers on one hostname is a config-validation edge, not a real deployment;
  behavior tests use one provider per hostname.
- Non-GitHub repositories on one (provider, host) must declare equivalent effective
  chains (`internal/config/config.go::Config.ValidateRepoTokenSourceConsistency`).
- Check that consistency against each repository's own descriptor;
  `ProviderTokenSources` deduplicates by key and would hide the conflict.
- GitHub may additionally define exact-repository and owner authorization routes.
- Reload credential probes cover only hosts registered at startup, so a degraded host
  cannot block unrelated config changes
  (`internal/server/configreload/config_reload.go::Handlers.ValidateReloadProviderTokenSources`).
- Reloads keep boot-tracked matches for degraded hosts; newly unavailable hosts stay
  untracked until restart.
- Provider tokens stay lazy when sync is disabled
  (`cmd/kenn-forge/provider_startup.go::registerProviderTokenSources`).
- Background refresh uses the gated registry; only explicit foreground operations use
  direct access, and retained refresh interfaces recheck the gate when invoked
  (`internal/github/sync.go::Syncer.SyncRegistry`).
- Provider clients register by `(platform, platform_host)`; a new provider must not
  masquerade as another.
- GitHub rate trackers and sync budgets are keyed by (host, authenticated identity), so
  PAT routes for one user share a budget; other providers stay host-scoped.
- Authenticated-viewer lookups run only for repositories the request selected
  (`internal/server/authapi/viewer_identity.go::Handlers.ResolveAuthenticatedViewerLogins`).

### Fallback token lookup

- Lookup order is repo `token_file`, repo `token_env`, a covered App installation for
  reads, owner PAT, platform token, public-host default env, then the provider CLI.
- `gitlab.com` has no implicit env var; `codeberg.org` uses `KENN_FORGE_FORGEJO_TOKEN`
  and `gitea.com` uses `KENN_FORGE_GITEA_TOKEN`.
- Every non-GitHub chain ends on a host-scoped CLI candidate (glab, or fj for
  Forgejo/Gitea); CLI candidates never count as configured credentials
  (`internal/config/config.go::descriptorCredentialAvailable`).
- Token files are read lazily so atomic replacement rotates credentials without
  rebuilding clients; reload probes reuse the shared App installation-token cache
  (`internal/tokenauth/source.go::SourceSet.ProbeToken`).
- Provider CLI tokens are evicted only when the provider rejects that exact token or
  the descriptor changes (`internal/tokenauth/source.go::ManagedSource.cliToken`).
- GitHub CLI fallback always passes `--hostname`; only `github.com` may retry bare
  `gh auth token`, since a bare retry elsewhere could authenticate the wrong account
  (`internal/config/config.go::ghAuthTokenForHost`).
- GitLab CLI fallback uses `glab config get token --host`, never
  `glab auth status --show-token`, which fails offline and writes to stderr.
- glab returns `GITLAB_TOKEN`, `GITLAB_ACCESS_TOKEN`, or `OAUTH_TOKEN` for every host, so
  the lookup strips them (`internal/config/provider_cli_tokens.go::GitLabCLITokenForHost`).
- fj has no command that prints a stored token, so Forgejo/Gitea fallback reads fj's
  `keys.json` (`internal/config/provider_cli_tokens.go::ForgejoCLITokenForHost`).
- Expired fj OAuth entries are skipped because fj refreshes them itself; an unknown expiry
  encoding is live, a missing file is a missing credential, a malformed file an error.

### Git credentials and clones

- Managed Git authorization is selected by full repository identity: exact repository
  routes beat owner routes, which beat the host fallback.
- GitHub smart HTTP never uses an App installation token.
- Clone storage and coordination keys partition providers sharing a host and distinct
  repositories sharing a route (`internal/gitclone/clone.go::WithRepositoryIdentity`).
- Authenticated workspace operations validate effective origin fetch and push
  destinations before resolving a credential.
- A renamed repository keeps earlier bare clones in its identity namespace; linked
  worktrees cannot move between bare repositories
  (`internal/workspace/manager.go::Manager.workspaceManagedCloneCandidates`).
- Spoke clone work validates the descriptor remote against the exact identity, then
  requires the spoke's exact credential route.
- Credential-required contexts never fall back to anonymous Git (public standalone
  clones are unaffected); missing spoke credentials return `gitCredentialUnavailable`
  (`internal/gitclone/clone.go::WithRequiredCredential`).
- A failed repository-browser refresh keeps the prior clone, and readers never see a
  partial one (`internal/gitclone/repo_browser.go::refreshRepoBrowserClone`).
- Write scopes are required only for mutation capabilities; read scope covers repository
  metadata, merge requests, issues, comments, commits, tags, releases, and CI.

## Sync Capabilities

- Saved branch tips may outlive objects in rebuilt or pruned clones; never infer a force
  push from missing history (`internal/github/sync.go::Syncer.syncDefaultBranchActivity`).
- `platform.Capabilities` has no "reports a review decision" flag; only GitHub sets
  `ReviewDecision`, so callers compare the provider kind directly.
- `merge_status_observed_at` is the oldest counted observation time, null on empty input
  or any null time, never the zero time (`internal/archive/snapshot.go::minObservedAt`).
- Never put foreground deadlines on a shared provider HTTP client; scope them to the
  operation context (`platform/gitlab/client.go::NewClient`).
- Provider clients with a local sync budget must use the shared budget transport, or
  refusal-window identity is lost (`internal/github/budget_transport.go::WrapSyncBudgetTransport`).
- Resolve opaque provider repo IDs by `repo_path` before numeric-only operations
  (`platform/gitlab/client.go::projectScopedArg`).
- `priority_repo` reorders a full run; `only_repo` restricts every repo-derived phase
  without delaying full-run cadence. Resolve both by identity and never fall back from
  an invalid exclusive scope (`internal/github/sync.go::runOnce`).
- Locked pull requests keep upstream state, but list filters and counts treat them as
  closed and detail shows only the Locked chip (`internal/db/queries.go::DB.ListMergeRequests`).
- GitHub GraphQL bulk fetch, ETag recovery, and detailed diffs are GitHub-only
  optimizations kept optional around the neutral persistence path.
- Background and watched GitHub detail syncs and PR-page polls may use the persisted PR
  ETag; explicit `SyncMROnProvider` refreshes bypass it.
- Child datasets and freshness writes are fenced to the parent snapshot revision; local
  event writes advance it so older syncs cannot overwrite them
  (`internal/db/queries_snapshot_children.go::CommitMergeRequestChildSnapshot`).
- Complete comment and inline-review sets replace; submitted reviews stay additive.
- Omitted assignees are unknown; only a provider-confirmed empty set is unassigned, and
  the API keeps absent versus `[]` distinct (`internal/db/queries_assignees.go::unassignedCondition`).
- Unexpected or ambiguous mutation outcomes trigger a best-effort authoritative detail
  refresh with periodic sync as fallback; never persist a local mutation fence that can
  block future actions indefinitely.

### Issue-PR references

- A PR reference is historical evidence of a provider-observed cross-reference; it
  survives upstream mention removal and needs no tracked repository or local PR row.
- Ingest provider-native reference events and query the persisted graph for list
  filters; never parse Markdown or add reads while serving issue lists
  (`internal/db/queries.go::issuePRReferenceFromEvent`).
- An edge links to pulls only through the tracked repository ID, never route, so renames
  keep it and a later route occupant never inherits it (`internal/db/queries.go::upsertIssueEventsTx`).
- Historical references cannot prove a current relationship; a current-link capability
  needs an authoritative replaceable provider snapshot.

### Review suggestion application

- Suggestion apply is an all-or-nothing batch against the expected head; partial
  success is invalid (`platform/client.go::ReviewSuggestionApplier`).
- The server rebuilds each suggestion from persisted thread metadata and never trusts
  client IDs, ranges, paths, or patch content; the UI preview mirrors server parsing
  (`internal/server/pullapi/diff_review_handlers.go::Handler.applyReviewSuggestions`).
- Providers report every rate bucket an apply consumes; an empty or unknown report fails
  closed as `rate_limited`, and handlers re-check buckets just before the provider call.
- Apply is open-PR-only and never falls back to the base repository as a write target;
  reasons are owned by [`error-handling.md`](./error-handling.md).
- The live open/branch re-check is best-effort; expected-head binding is the final
  integrity check.
- Post-apply refresh must rerun after any in-flight sync for the same PR, which may
  predate the commit (`internal/server/syncevents/detail_sync.go::Handlers.EnqueueDetailSyncOrRerun`).
- The UI exposes apply only when the thread head matches the known current head; stale
  batched suggestions cannot reach submit but stay removable.
- A suggestion preview from a cached diff whose `diff_head_sha` no longer matches is
  missing context until reloaded; PR diff responses always carry `diff_head_sha`.
- Duplicate thread IDs, overlapping ranges, missing heads, renamed/deleted/binary files,
  unsupported encodings, and padded paths fail the entire batch with stable reasons.

## Historical Archive

- Archive is a scheduling and progress mode over normal sync, not a second sync engine;
  completeness is scoped by full repository identity (`internal/db/queries_archive.go::GetArchiveProgress`).
- Created-order inventory requires the historical capability; updated-order maintenance
  does not. Each page advances an opaque cursor or reports exhaustion explicitly.
- Hydration runs canonical item sync; only a successful complete sync records an archive
  outcome, and provider finalizers run after that commit (`internal/archive/hydrate.go::hydrateItem`).
- Only parent lookups classified as removed, moved, or inaccessible are terminal; generic
  and child-dataset not-found stay retries, and canonical content is untouched
  (`internal/archive/hydrate.go::archiveTerminalSyncOutcome`).
- A successful non-GitHub feature-metadata confirmation is also repository-accessibility
  evidence and must not be repeated before marking the parent absent.
- Removed-upstream parents stay stored for rediscovery but are excluded from public reads
  and reports; inaccessible rows stay visible because lost access does not prove removal.
- Maintenance rediscovery reopens terminal items for hydration; unsupported and blocked
  items stay excluded (`internal/db/queries_dataset_progress.go::reopenArchiveItemProgressTx`).
- Issue and merge-request inventory coverage is independent of child-dataset coverage;
  feature absence records `unsupported` and completes only that stream.
- Repository-specific feature absence exhausts the current maintenance stream without
  replacing established historical coverage (`internal/archive/maintenance.go::promptPages`).
- When a provider advertises a stream again, reconciliation reopens unsupported
  inventory with unknown coverage (`internal/db/queries_archive.go::ReconcileArchiveCoverage`).
- A bare optional `DiffSyncError` does not block historical-activity completion; wrapped
  or joined hard failures still retry (`internal/github/sync.go::SyncArchiveItem`).
- Every configured repository starts provider-neutral discovery; never restore
  provider-specific closed-item cursors or translate legacy formats
  (`internal/archive/service.go::EnsureConfigured`).
- Full-archive promotion keeps the discovery creation time as the first maintenance
  boundary; the later promotion time can skip items (`internal/db/queries_archive.go::StartFullArchives`).
- Repositories dropped from config pause with `configuration_removed` and keep content;
  re-adding clears only that automatic pause, never an operator pause.
- Reconcile configured repositories only at startup or config reload; idle scheduler
  passes stay read-only unless they claim work (`internal/archive/scheduler.go::RunPass`).
- A close actor is current only when the latest authored close event matches `closed_at`
  (`internal/db/queries_archive.go::RequeueArchiveLifecycleDetails`).
- Lifecycle details are marked checked only after provider completeness gates pass.
- Authentication and repository-blocked errors defer all archive work until an explicit
  retry clears the repository error (`internal/archive/scheduler.go::archiveRepoDeferred`).
- Archive admission is provider-host scoped, and index, notification, and active-detail
  work outrank archive requests, preempting them before provider I/O
  (`internal/github/sync.go::Admit`).
- Split-auth live work holds every credential identity it may spend; register work
  inside shared execution functions so no entry point bypasses admission
  (`internal/github/notifications_sync.go::notificationProviderWork`).
- Archive admission requires the declared minimum cost; gitealike merge-request reads
  may exceed the estimate to finish their atomic dataset.
- Detailed reports use schema `kenn-forge-archive-report/1` with half-open UTC windows;
  a reopened issue has no close row
  (`internal/db/queries_archive_report.go::archiveReportActivityQuery`).
- Daemon CLI JSON must reject any other report schema and round-trip every recognized
  report kind and field (`cmd/kenn-forge/archive_cli.go::archiveReportFromAPI`).

## Label Catalogs And Mutations

- Label pickers use the cached repo catalog, not labels on visible items; historical
  assigned labels persist until item sync removes them.
- Stale catalog reads return cached labels immediately and enqueue a deduped refresh;
  catalog errors must not fail normal PR/issue sync.
- Label mutations replace the full name set, reject missing, empty, duplicate, or
  non-catalog names, call the provider first, then persist the returned labels.

## Provider Event Mutations

- Comment deletion is provider-authoritative: call the provider first and keep the
  SQLite event until authoritative sync removes it; rejection leaves local state intact.
- Direct event URLs are provider data; keep a stored URL when a partial response omits
  it. GitLab Notes derive `parent URL + #note_<id>` because they expose none
  (`platform/gitlab/normalize.go::noteDirectURL`).

## GitLab Shape

- Note IDs identify comments; discussion IDs identify reply and resolution targets.
  Reads supporting threaded mutations must keep Discussions grouping
  (`platform/gitlab/normalize.go::NormalizeMergeRequestDiscussions`).
- Prefer the stored provider ID after resolution and keep `path_with_namespace` as `repo_path`.
- `iid` values are repo-scoped numbers; persist provider object IDs separately and scope
  events by provider identity so equal GitHub/GitLab IDs never collide.
- Private Markdown upload web URLs reject API-token auth; translate only repo-scoped
  upload URLs to the project-upload API and never proxy arbitrary provider URLs
  (`platform/gitlab/markdown_images.go::GetMarkdownImage`).
- Archive discussions normalize as ordinary or inline comments; never synthesize
  submitted reviews from notes or approvals (`platform/gitlab/client.go::Capabilities`).
- Issue event hydration keeps authored close and reopen system notes; ordinary comment
  reads exclude system notes (`platform/gitlab/normalize.go::normalizeIssueSystemNote`).
- Historical merge-request inventory is unsupported: offset pagination cannot guarantee
  completeness across equal `created_at` ties.
- Maintenance inventories walk `updated_at` newest-first; rows that move ahead before
  consumption stay eligible under the next scan's inclusive watermark
  (`platform/gitlab/pages.go::listInventoryIssuesPage`).
- Merge reports use `merge_commit_sha`, falling back to `squash_commit_sha`
  (`platform/gitlab/normalize.go::normalizeMergeRequest`).

## Forgejo And Gitea Shape

- Route and config identity stay `(provider, host, owner, name)` with optional
  `repo_path`; still persist provider repo and object IDs when available.
- Self-hosted instances are separate provider-host entries from Codeberg (Forgejo) and
  gitea.com (Gitea) even when owner/name pairs match.
- Shared review operations map SDK failures through the provider error mapper so reads
  and mutations keep typed platform errors.
- Workflow approval and ready-for-review stay hidden or return `unsupported_capability`
  until proven per provider by interface and capability tests.
- Gitea 1.24 timelines encode `label` as an object where the SDK expects an array;
  normalize it at the HTTP boundary (`platform/gitea/timeline_transport.go::timelineLabelTransport`).
- Forgejo pull-request JSON is authoritative for merge metrics the SDK drops; omitted
  counters keep stored values while explicit zero replaces them
  (`platform/gitealike/mergeable_capture.go::MetricsForPullRequest`).
- Gitea reads workflow runs only on 1.26+ `/actions/runs`; older hosts stay status-only.
  Gitealike CI must not duplicate a check already reported as a commit status.
- Archive access probes keep transient and rate-limit failures retryable; only
  authoritative permission or not-found responses become permanent
  (`platform/gitealike/pages.go::classifyLookupOutcome`).
- Inline review hydration is complete-or-error: never publish a partial review dataset
  or report an incomplete explicit sync as successful
  (`internal/github/sync.go::syncProviderMRReviewThreads`).

## Markdown Media

- Markdown SVGs can be opened as documents; serve them with a sandboxed origin and
  scripts disabled on every response, including cache hits
  (`internal/server/providerapi/markdown_images.go::Handlers.getMarkdownImageFor`).
- The markdown image cache is keyed by stable repository identity, never route, so a
  reused route never receives the previous repository's bytes; provider-marked `Mutable`
  sources are cached for minutes, not a year (`internal/server/providerapi/markdown_images.go::markdownImageCacheKey`).

Rendered markdown plays video only where the provider itself plays it.

- GitHub: a paragraph whose only content is a bare attachment URL (repo-asset URLs only
  for the route's own repository), after a probe confirms video; raw `<video>` keeps
  only attachment-URL sources.
- GitLab: image syntax with a video extension (mp4, m4v, mov, webm, ogv); raw `<video>`
  is removed.
- Gitea/Forgejo: no player conversion; raw `<video>` is kept and attachments load
  directly from the browser.
- Bitbucket: raw `<video>` is removed. Markdown without a repository keeps raw `<video>`.
- Source-browser previews never play video, matching GitHub's blob viewer.
- Players use `preload="none"` and no `autoplay`: Firefox reads ahead on every preloaded
  player and can exhaust the browser's HTTP/1.1 connections to the daemon
  (`frontend/src/lib/utils/markdown.ts::normalizePlayer`).
- Players fill a fixed 16:9 frame so first play never resizes; a player that cannot load
  becomes a notice linking to the original attachment.
- Frame content stays phrasing-level; a block element splits the paragraph an inline
  GitLab video sits in when `{@html}` re-parses it
  (`frontend/src/lib/utils/markdown-video-toggle.ts::initMarkdownVideoToggle`).
- Opening the gallery viewer pauses inline players; a player resumes only if the viewer
  closes while showing it playing. Collapsing a player pauses it.
- GitHub and GitLab media go through the credentialed media route
  (`read_markdown_media`); adapters accept only their own attachment URL shapes and
  forward only single-range `Range` requests.
- GitLab serves media as `application/octet-stream`, so its type comes from the
  extension; disallowed types are `unsupported_media_type`
  (`platform/gitlab/markdown_media.go::Client.OpenMarkdownMedia`).
- GitLab images and videos share one upload rule, and only uploads on the platform host
  reach the proxy (`frontend/src/lib/utils/markdown.ts::gitLabUploadURL`).
- Only the wait for upstream headers is bounded; the body streams uncached and
  uncompressed, since compression would buffer the stream
  (`internal/server/compression/compression.go::shouldBypassCompression`).
- Media must never hold shutdown or app requests, and a truncated stream must abort the
  connection rather than end like a whole video (`internal/server/httpapi/stream.go::CopyStream`).
- A media stream whose client leaves a write unread for 15 seconds ends; browsers can
  hold a buffered video connection open indefinitely and re-request by range later.
- Poster and length previews come from one detached player copy at a time per page,
  outside the document so its errors never reach the fallback notice
  (`frontend/src/lib/utils/markdown-video-preview.ts::observeMarkdownVideoPreviews`).
- WebKit reaches no frame with `preload="metadata"`, and Firefox and WebKit download
  whole files with it; cross-origin sources without CORS show only their length.
- GitHub player-versus-link depends on upload metadata Forge cannot see, so the frontend
  probes with `Range: bytes=0-0` GET (signed storage rejects HEAD), never reading the body
  (`frontend/src/lib/utils/markdown-media.ts::resolveMarkdownMediaOutcomes`).
- At most two probes run per page so the app keeps connections; only proven outcomes
  (video, or `415`) are cached, and other failures render a link and probe again.
- First paint uses already-proven outcomes so known videos mount as players, and
  highlighting never waits on a probe.

## Import And Routes

- Import requests and routes carry `provider`, `platform_host`, and either `repo_path`
  or exact `owner/name`; route shape rules live in [`provider-architecture.md`](./provider-architecture.md).
- Frontend route state encodes slashes, host ports, mixed case, and special characters
  exactly once, through the shared provider route helpers.
- `removed_upstream` items stay stored for sync repair but are hidden from every public
  item read, projection, and provider mutation; `inaccessible` items stay visible
  (`internal/server/pullapi/helpers.go::visibleMergeRequest`).
- Every item-specific provider access path, including queued and deferred work, rechecks
  visibility first; archive-budget hydration bypasses it so a reappearing item can be repaired.
- Workspace and Fleet projections keep local records but omit removed parent metadata.
- Visible stack members are renumbered contiguously after filtering, and pull-list stack
  placement must match the detail stack context (`internal/db/queries_stacks.go::ListStackPlacementsForMRs`).

## Testing

- Use the GitLab CE container fixture when a change needs real GitLab REST behavior, and
  the Forgejo/Gitea container fixtures when fake transports cannot prove gitealike REST.

## Bitbucket

- Cloud repository identity is the repository UUID, not `platform_repo_id`; pull and
  issue numbers stay repository-scoped (`platform/bitbucket/normalize.go::repository.normalize`).
- Cloud issues and PRs number separately, so item-reference resolution must send and
  honor the `item_type` hint as for GitLab (`internal/server/itemapi/huma_routes.go::Handlers.ResolveItem`).
- Drain Cloud pages outside the SDK, whose auto-paging ignores later decode errors
  (`platform/bitbucket/client.go::collect`).
- Cloud cannot reopen declined PRs; keep combined state/content mutation disabled
  (`platform/bitbucket/client.go::Client.Capabilities`).
- Cloud PR embeds omit clone links and abbreviate hashes; resolve canonical URLs and full
  hashes (`platform/bitbucket/read.go::Client.normalizePull`).
- Cloud fetches use source branches; its pull ref is local storage
  (`internal/workspace/manager.go::fetchWorkspaceMergeRequestHeadRefWithGit`).
- `bitbucket.org` uses Cloud; other hosts use Data Center's distinct API
  (`cmd/kenn-forge/provider_startup.go::defaultProviderFactories`).
- Data Center `/scm/` and SSH ports describe transport, not identity; preserve execution
  URLs (`platform/remote.go::repositoryRemote`).
- Bitbucket Git credential parsing must be provider-scoped; colons are valid in other
  providers' opaque tokens (`internal/gitclone/clone.go::gitRunnerAuthed`).
- Cache complete Bitbucket permission inventories for five minutes per credential and
  workspace/server, matching stable repository IDs (`platform/bitbucket/permissions.go`).
- Cloud reviewer nicknames are non-unique; display account names but submit stable IDs,
  never inferring IDs from text (`platform/bitbucket/review.go::ListReviewerAccounts`).
- Keep Data Center comments and threads disabled until current wire data is verified
  (`platform/bitbucketdc/client.go::Client.Capabilities`).
- Data Center approvals are participant state keyed by stable user ID and verified
  reviewed commit (`platform/bitbucketdc/mutation.go::Client.ApproveMergeRequest`).
- Validate Bitbucket diff fetches against the API-observed head; Data Center private PR
  refs may be missing or stale (`internal/gitclone/bitbucket.go::FetchBitbucketMergeRequestHead`).
