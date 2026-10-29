# Provider Architecture

Use this document when adding a provider or changing the provider split. For
identity and sync invariants, also read
[`context/platform-sync-invariants.md`](./platform-sync-invariants.md).

## Package Split

- Public `platform` owns provider-neutral types, capability interfaces, typed errors,
  and registry lookup; SQL projections stay internal (`internal/platformdb`).
- Public `platform/<provider>` owns API transport and normalization into `platform`
  types; orchestration, server, config, and UI consume only neutral interfaces or DB rows.
- Dependencies stay acyclic: a provider package never imports server code, config
  startup, or another provider, and `platform` never imports provider SDKs.
- Public provider and App libraries have no transitive application, SQLite, config, or
  credential-discovery dependency; credential selection, cache policy, and admission
  stay internal (`cmd/kenn-forge/provider_startup.go::defaultProviderFactories`).
- Scoped installation tokens stay outside ordinary sync credential caches; any scoped
  cache is keyed by the complete scope (`githubapp/client.go::Client.CreateInstallationToken`).
- App discovery pages are observations, not atomic inventories; callers own total
  budgets and must not equate stopping with exhaustion (`githubapp/discovery.go::InstallationPage`).
- Installation absence requires the expected App's authenticated exact-ID read; JWT
  rejection does not prove key revocation, and repository 404s prove neither
  (`githubapp/installation.go::Client.CheckInstallation`).

## Adding A Provider

- Implement only the optional interfaces the provider truly supports; unsupported
  features are absent from the type and false in `Capabilities()`.
- Preserve provider IDs, external IDs, web and clone URLs, default branches, and
  canonical repo paths when normalizing.
- Add provider-aware identity and provider-ID reconciliation tests before relying on sync.
- Shared Forgejo/Gitea behavior runs against both clients through one adapter contract;
  keep SDK, version, timeline, Actions, and review differences local
  (`internal/testutil/gitealiketest/contract.go::Run`).

## Capability Model

- Capability flags and implemented interfaces must agree.
- Advertise only capabilities from the live provider registry; an empty or unavailable
  registry has none, and never synthesize a full GitHub set
  (`internal/server/httpapi/repository_resolver.go::RepositoryResolver.Capabilities`).
- `PlatformAuthenticated` is host telemetry, not an operation-admission signal;
  spoke-local Git admission uses the exact repository route.
- Handlers check capabilities before mutations; a missing capability is a feature-level
  failure, not a whole-provider failure.
- Read sync continues for supported resources when optional resources such as
  releases, tags, CI, or comments fail or are unsupported.
- Never fake GitHub behavior for another provider; add provider-specific normalization
  or explicit unsupported-capability handling.
- Branch updates acknowledge queued work, never completion; pin the rendered head,
  never replay an accepted or uncertain update, and observe head changes through refresh
  (`platform/github/update_branch.go::Client.UpdatePullRequestBranch`).
- Provider-backed comment deletes remove local rows only after sync observes provider
  absence; the DELETE itself changes no SQLite comment state
  (`internal/server/pullapi/routes.go::Handler.deleteComment`).
- Authoritative comment replacement updates comment rows and the parent count in one
  transaction (`internal/db/queries.go::ReplaceMRCommentEvents`).

### Workflows

- Workflow approval advances an existing run and dispatch starts a new one; keep them
  separate and never assume GitHub Actions semantics (`platform/client.go::WorkflowDispatcher`).
- Workflow contracts stay optional and provider-neutral; a provider must not advertise
  workflow capabilities before implementing the interfaces.
- Revalidate live definitions, environments, SHA, and inputs immediately before the
  single dispatch; never retry uncertain writes or persist definitions
  (`internal/server/workflowapi/routes.go::Handler.dispatch`).
- Catalog partial availability is per definition: missing or undecodable files become
  unavailable rows, while cancellation, auth, server, transport, and rate failures abort
  (`platform/github/provider.go::workflowDefinitionReadMustAbort`).
- actionlint owns the GitHub workflow grammar; never hand-parse workflow YAML. The
  module is pinned to the fork behind rhysd/actionlint#730 until a release builds
  against yaml/v4 rc.6 (`go.mod`).

### Operation Availability

- Repo-level gates (capability, write credentials, rate limits, repo-wide viewer
  permissions) live in repo operations; item-level gates only in detail responses, never
  repo settings or list summaries
  (`internal/server/operationapi/operation_availability.go::Handlers.RepoOperationsForMergeRequest`).
- Provider-specific facts such as the viewer identity come through provider
  interfaces; server code never shells out to provider CLIs or matches identities itself.
- Viewer-resolution errors fail open in availability because the provider mutation
  remains authoritative.
- Enforce item-level gates at every mutation entry point and every UI availability
  signal; self-approval both rejects (draft publish, `/approve`) and hides the action.

### Head Binding

- Merge and approve bind to the locally synced head the user reviewed, so a push
  after review is rejected instead of acting on unreviewed code.
- Every MergeRequest response exposes `platform_head_sha`; an optional
  `expected_head_sha` must match the stored head before the provider is called.
- `mutation_head_binding` marks providers (GitLab) where the pin is required and
  approval is provider-gated; elsewhere the pin is advisory beyond the checks below.
- On enforcing providers a missing local head fails closed with 409 and never syncs;
  conflict reasons and client recovery are owned by [`error-handling.md`](./error-handling.md).
- Merge pins are enforced upstream wherever the API allows: GitHub `sha`, GitLab `sha`,
  Gitea/Forgejo `head_commit_id`.
- Gitea/Forgejo SDKs report any non-2xx merge as merged-false with nil error; capture
  rejections at the HTTP layer or a rejected merge is recorded as success
  (`platform/gitealike/merge_rejection.go::MergeRejectionCaptureTransport`).
- Gitea/Forgejo answer 409 for unrelated conflicts too; ambiguous 405/409 responses are
  `stale_state` only after a live read proves the head moved.
- Only GitLab gates approvals atomically on `sha`. GitHub/Gitea/Forgejo approvals are
  pre-checked against the live head and verified after submission.
- If the head moved during submission, or cannot be read at all, the approval is revoked
  upstream and the caller gets `stale_state`; an unproven approval must not stand.
- Revocation runs as the approving token user; failure tells the user the approval may
  still stand.
- The SPA captures the pin when a merge or approval form opens, so a background refresh
  cannot rebind an open form to a head the user has not seen.

### GitLab Mutation Semantics

GitLab declares the full mutation capability set, but several operations differ
from GitHub/Forgejo/Gitea. Do not assume equivalence from the flags alone.

- `merge` accepts under the project's merge method and may not create a merge commit;
  treat `AllowMergeCommit` as "non-squash accept allowed".
- `squash_option` bounds the squash flag both ways: `never` forbids squash, `always`
  forbids non-squash accepts.
- Rebase cannot be requested per merge; it returns `unsupported_capability` with
  capability `merge_method_rebase`.
- GitLab has no `request_changes` review state; publishing one returns
  `unsupportedCapability` (`review_action_request_changes`).
- GitLab approvals carry no body: a non-empty approve body is posted as a separate note
  first, so a retry after approval failure repeats the comment.
- GitLab note responses omit the discussion ID; edits must preserve the stored
  `thread_id` instead of clearing it.

## Label Capabilities

- `read_labels` and `label_mutation` are true only when the provider implements the
  matching interfaces; never expose editable UI or mutation routes from fallback capabilities.
- Label mutation replaces the full label set and returns provider-normalized labels.
- GitHub PR labels use issue-label APIs; that mapping stays inside the provider, never
  in server handlers or frontend code.

## Route Model

- Every documented operation is hub-only provider, provider with a spoke-local
  overlay, or spoke-local; live-registry coverage rejects gaps
  (`internal/server/routepolicy/provider_route_policy.go::ProviderRouteDeclarations`).
- Spokes proxy hub-only operations; mixed reads attach only the connected spoke's
  workspace references, and federation callers get provider-only responses.
- Mixed reads preserve hub membership, order, and cursors, and join the local overlay by
  provider item identity, never numeric SQLite IDs (`internal/providerplane/descriptor.go::ItemIdentity`).
- Spokes use the hub's provider identity; the federation bearer only authenticates the
  spoke, and the hub client never forwards caller credentials or follows redirects
  (`internal/providerplane/client.go::ReadJSON`).
- Only the hub's provider control plane owns provider clients, mutations, accounting,
  and workers; local Git credentials need no provider API work, and `--disable-sync`
  keeps the foreground plane (`cmd/kenn-forge/provider_startup.go::buildServeControlPlanes`).
- Spokes model the hub-owned provider as absence, never an empty or disabled registry;
  hub-owned HTTP/MCP work requires a validated hub client (`internal/server/server.go::newServer`).
- Clone-backed provider pages stay spoke-local: the spoke gets a scoped repository or
  diff descriptor from the hub and fetches its own clone; the hub never sends Git content.
- A spoke never creates a provider-item cache from a descriptor
  (`internal/server/pullapi/routes.go::Handler.resolvePullCloneSnapshot`).
- Diff descriptors come from one serialized hub read; an invalid, mismatched, or older
  descriptor fails closed (`internal/providerplane/descriptor.go`).
- During a hub outage clone-backed pages return `hubUnavailable` even when an old spoke
  clone exists.
- Review drafts, MCP workflow state, notification state, and other provider-adjacent
  records share the hub's ownership and provider scopes.
- Settings compose hub-owned provider policy with spoke-owned execution policy; peers get
  a provider-only DTO and cannot set hub-local execution policy
  (`internal/server/spokeapi/federation_provider_settings.go::ProviderSettingsResponse`).

### Repo-scoped REST routes

- Routes omit the host only when the provider default host applies; other instances use
  `/host/{platform_host}/...` (for example `/api/v1/host/gitlab.example.com/pulls/gitlab/group%2Fsub/project/12`).
- Never add `/repos/{owner}/{name}/pulls/{number}/...` compatibility routes for
  repo-scoped provider work.
- Generated clients and `frontend/src/lib/api/provider-routes.ts` are the single
  frontend path builder for these routes.
- `OperationID` is a generated-client contract: keep default-host and host-prefixed
  variants paired with the same `Summary` and tag, and reserve `-on-host` for the latter.

## Frontend Threading

- Frontend state keeps a reusable provider ref (provider, host, owner, name, repo
  path) and builds repo-scoped requests only through the shared route helpers.
- Never hand-build `/api/v1` URLs or assume GitHub defaults in components or stores.
- Browser resource URLs use the configured API base so non-root deployments keep their
  `base_path` (`frontend/src/lib/api/runtime.ts::apiBaseURL`).

## Notification Identity

Notification persistence is provider-neutral even though GitHub is the only source today.

- Notification item identity is `(platform, platform_host, platform_notification_id)`.
- Notification repo scope is `(platform, platform_host, repo_owner, repo_name)`.
- Sync watermarks are keyed by full repository identity, never host alone; one
  repository's failure must not block watermarks for healthy repositories on the host.
- Blank provider values are invalid in notification paths; never silently treat a
  missing provider as GitHub.
- Any GitHub-shaped compatibility fields stay at the server boundary, never in DB or
  provider abstractions.

## Landed-Work Evidence

- The landing API uses local objects only; callers collect and supply evidence for the
  exact prepared interval (`landedwork/prepare.go::Prepare`).
- Missing history and exhausted budgets remain gaps, never empty complete inventories.
- Full history requires explicit FromRoot bounds; an omitted base never opts in.
- The root is an origin only after complete discovery and physical verification
  (`landedwork/direct.go::directPush`).
- PR-head refs may be fetched after collection, but the pinned query and observed source
  SHAs must stay unchanged (`landedwork/analyze.go::Analyze`).
- Unsupported landing methods stay unresolved; generic squash proof never establishes a
  provider's squash capability.
- Landing ownership is explicit; correspondence labels are never historical provider
  merge actions (`landedwork/evidence.go::Landing`).
- Automatic single-parent proofs need every alternative conclusive and every match owning
  the same origin; unavailable evidence cannot lose to a match
  (`landedwork/alternatives.go::resolveAlternatives`).
- A conclusive unequal pair disproves a range despite other ambiguous pairs; never shorten
  a matching origin (`landedwork/range.go::rangeCorrespondence`).
- Rewritten ranges compare exact edit bytes, not whitespace-insensitive patch IDs; empty
  or duplicate rewritten edits stay unproven.
- A rewritten range cannot contain another change's declared terminal (outside that
  change's source) or an ancestor of its own source head.
- Direct-push origins need complete inventory and an unblocked, unowned spine commit;
  they establish neither a pusher nor a trusted update time
  (`landedwork/direct.go::classifyDirectPushes`).
- Integrated candidates own nothing: prove exact containment in an accepted ordinary
  merge, never through another integrated candidate (`landedwork/origins.go::integratedThrough`).
- Git's commit-graph can outlive commit objects; verify required commits physically,
  including each landing's first parent (`landedwork/proof.go::proveAt`).
- Nonlinear squash sources need complete ancestry, a unique merge base, and an exact
  three-way tree match; rewritten ranges still need a linear source
  (`landedwork/single_parent.go::squashCorrespondence`).
- Across different bases, repeated text needs an exact three-way location witness
  (`landedwork/edits.go::compareTreeEdits`).
- A squash onto a target already carrying some source edits needs an exact three-way
  witness and must exclude a longer rebase replaying source commits in order; an empty
  terminal uses the same witness (`landedwork/squash_witness.go::witnessedSquash`).
- Another change's declared terminal, or a source-head ancestor outside the source, is
  never a replay (`landedwork/squash_witness.go::rebaseReplayPossible`).
- A terminal outside the interval with a complete source owns none of it unless its first
  max(1, source length) first parents reach an interval commit that is a source commit or
  not a source-head ancestor (`landedwork/origins.go::reachesInterval`).
- Synthesized merge objects belong only to the disposable view; caller objects are
  read-only alternates (`landedwork/git.go::openView`).
- Merge proofs must work on Git 2.38 (Debian 12, macOS): pin the base with synthesized
  commits, not `merge-tree --merge-base` (Git 2.40) (`landedwork/merge_tree.go::withMergeBase`).
- Overlap conflicts block the proven landing ranges, not unrelated earlier commits;
  unknown-start gaps still block from the base (`landedwork/origins.go::rejectOverlaps`).
- Collection preserves unfinished candidates and the exact prepared query; missing
  provider evidence is never direct-push evidence (`landedwork/collect/collect.go::Collect`).
- Titles, roles, and lifecycle times are observations, not landing proof; complete
  landing coverage does not certify complete attribution (`platform/landing_evidence.go::LandingChange`).
- Metadata-only rechecks replace the entire observation, including absent fields; proof
  changes keep the initial incomplete observation (`landedwork/collect/candidates.go::observe`).
- Account identity needs the enclosing provider and instance; never infer a human or a
  missing ID from a login (`platform/landing_evidence.go::Account`).
- Malformed object IDs stay in observations, not analyzer candidates; a provider gap must
  not become a fatal caller-input error (`landedwork/collect/candidates.go::candidate`).
- Reject an over-budget query without a result; never trim commits or gaps to fit, and
  charge query records even for incomplete or unsupported work (`landedwork/collect/limits.go::validate`).
- GitHub associations can name upstream PRs from a fork; keep their target identity
  without fetching that number locally.
- Foreign associations prove local absence only after all of the repository's own merged
  changes are candidates (`landedwork/collect/collect.go::collector.establishLocalAbsence`).
- A change above its list cap is complete only through its reported base's three-dot
  range at the reported count and head; GitHub ranges cap at 10,000
  (`platform/github/landing_evidence.go::Provider.LandingEvidenceSupport`).
- Collector call limits do not bound HTTP retries or absence-confirmation reads; callers
  put wire limits below authentication (`platform/landing_evidence.go::LandingEvidenceReader`).
- GitHub collection relies on REST 2022-11-28 terminal semantics, not the newer API's
  omitted field; enterprise coverage is unverified.
- Only GitHub.com has a collector reader; GitLab still needs the rewritten-commit contract
  checked, and terminal-only Forgejo/Gitea lookups cannot prove absence in a rewritten range.

## Cached Archive Consumers

- Archive export reads one saved database view and never refreshes provider data or marks
  items hot; consumers keep coverage gaps rather than treating export time as freshness
  (`internal/archive/snapshot.go::Service.Snapshot`).
- Author association belongs to each authored item or review, never the enclosing PR's
  author; missing association is unknown and conveys no trust (`internal/platformdb/persist.go::DBMREvent`).
- Cached zero sizes lack observation metadata; export them as unavailable until storage
  distinguishes measured zero from missing.
- Archive references must survive renames; ambiguous route reuse or out-of-scope targets
  stay coverage gaps, never invented links.
- Reject oversized exports before loading item and review text; exports never silently
  discard candidates (`internal/db/queries_archive_snapshot.go::MeasureArchiveSnapshot`).
