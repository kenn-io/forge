# Kata Integration

Use this document for Kata daemon discovery, pinned integration reads, Forge-owned
associations, and ownership boundaries. Workspace contracts remain in
[`workspace-apis.md`](./workspace-apis.md); frontend interaction contracts remain
in [`ui-interaction-contracts.md`](./ui-interaction-contracts.md).

## Ownership

- Kata is an external task domain, not a platform provider. Canonical task data
  stays in Kata daemons and must not be copied into Forge's provider registry or
  SQLite task tables.
- Forge alone owns links between Forge subjects and canonical Kata identities.
  Kata has no reverse-link table or Forge-specific linkage API
  (`internal/server/kata/links.go`).
- Existing `WorkspaceKataMetadata` remains a frozen identity/display hint for
  Kata-backed workspace rows. Forge does not refresh task titles or other task
  presentation into those rows (`internal/db/types.go::WorkspaceKataMetadata`).
- Provider-item links use the stable repository row plus a nonblank provider
  external ID. Missing external identity requires resync and must never fall
  back to an item route number
  (`internal/server/kata/links.go::Handler.resolveProviderKataLinkSubject`).
- Workspace effective links deduplicate by daemon and issue UID while preserving
  intrinsic, direct, and inherited provenance. Only the workspace's direct row
  is unlinkable (`internal/server/kata/effective_links.go::mergeKataLinkCandidate`).

## Product Surface

- Forge has no global `/kata` route, header tab, task browser, graph, history,
  recurrence editor, task mutation UI, snapshot API, event relay, or generic
  Kata proxy.
- Provider issue, pull request, and workspace detail surfaces show Forge-owned
  associations and one selected read-only task detail
  (`frontend/src/lib/components/kata/KataLinksPanel.svelte`).
- Remote fleet workspace detail must hide Kata controls until fleet transport
  exposes the narrow Kata routes; never send a remote workspace identity to the
  local daemon integration (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`).
- Task presentation comes from the release-pinned `@kenn-io/kata-ui` package.
  Forge association controls stay outside the package projection/error boundary
  (`frontend/src/lib/components/kata/KataIssueDetailView.svelte`).
- New Workspace searches references on an explicitly selected usable daemon and
  submits the canonical result to the create-or-reuse API. Roster state never
  establishes a globally active daemon
  (`frontend/src/lib/components/terminal/NewWorkspaceDialog.svelte`).
- Docs uses its pinned daemon to resolve open or completed task references;
  bare short IDs must be unique. It opens Kata with `noopener,noreferrer` and
  renders no inline detail (`frontend/src/App.svelte::openKataReference`).
- Kata project mappings remain in Settings even though Kata is not a top-level
  mode (`frontend/src/lib/components/settings/KataProjectMappingsSettings.svelte`).

## Daemon Discovery And Reads

- `$KATA_HOME/config.toml` is the daemon catalog. Forge config and legacy URL
  environment variables are not daemon authorities
  (`internal/kata/catalog.go::LoadCatalog`).
- A catalog entry is either a static remote URL or `local = true`, never both.
  Local entries resolve dynamically from Kata runtime records so restarts and
  Unix-socket rotation do not require a Forge restart
  (`internal/kata/runtime.go::DiscoverLocalDaemonURL`).
- Local runtime records may resolve only to loopback HTTP or local Unix sockets;
  process discovery does not make a non-local address trusted
  (`internal/kata/runtime.go::isLocalDaemonAddress`).
- Roster responses and errors redact URL credentials and token material
  (`internal/kata/validation.go::RedactURL`).
- Narrow reference, detail, and launch reads resolve the daemon from the route's
  daemon ID. Browser input never supplies daemon credentials or an ambient
  active-daemon fallback (`internal/server/kata/read_routes.go`).
- Forge requires Kata v0.14.3 or newer with API schema `>=0.9.0 and <0.11.0`.
  Mark other or missing schemas incompatible, fail narrow reads with a typed 503,
  and surface upgrade guidance in the UI (`internal/server/kata/client.go::supportsKataAPISchema`).
- Reference lists expose only open tasks, including empty-query autocomplete;
  completed tasks remain reachable through exact reference and issue-UID reads
  (`internal/server/kata/read_routes.go::Handler.listKataReferences`).
- Open-only reads must fill the requested result count before filtering can hide
  later tasks; fail closed when the daemon's bounded window cannot prove completeness
  (`internal/server/kata/client.go::kataDaemonClient.References`).
- Resolve qualified Docs references against Kata's complete `qualified_id`; never
  reinterpret its qualifier as a project UID or display name
  (`internal/server/kata/client.go::kataDaemonClient.resolveQualifiedIssueReference`).
- A remote daemon URL may carry a reverse-proxy path prefix; narrow API paths
  append beneath that prefix (`internal/server/kata/client.go::kataDaemonClient.get`).
- Treat daemon launch targets as untrusted: both server and browser accept only
  absolute HTTP(S) URLs with a host and no user info
  (`internal/server/kata/client.go::validateKataLaunchTarget`).
- The retained server client supports redirect-safe HTTP, HTTPS, and Unix-socket
  reads, uses total request deadlines, disables connection reuse for per-request
  owned transports, and bounds response bodies
  (`internal/server/kata/daemon_transport.go`, `internal/server/kata/client.go`).
- Detail responses carry the health endpoint's `api_schema_version`. Missing or
  unsupported versions render a typed incompatibility state rather than passing
  data to the shared projection component
  (`frontend/src/lib/components/kata/KataLinksPanel.svelte`).

## Freshness And Failure

- Contextual Kata panels defer association and task-detail reads until their tab
  is active, then load each Forge subject once before the normal freshness rules
  apply (`frontend/src/lib/components/kata/KataLinksPanel.svelte`).
- An active, visible pane refetches associations after activation, focus, or
  visibility recovery when no read succeeded or its snapshot is at least 15 seconds old
  (`frontend/src/lib/stores/kata-links.svelte.ts::refreshAssociationsWhenStale`).
- Its 30-second loop refreshes only the selected detail and stops while inactive,
  hidden, or destroyed (`frontend/src/lib/stores/kata-links.svelte.ts`).
- Daemon, link, and task-detail failures remain isolated per task. A failing Kata
  read must not break the surrounding provider/workspace detail surface or hide
  Forge's unlink/open/create-workspace controls.
- Effective-link hydration is `unavailable` when no candidate hydrates and
  `partial` only when at least one does; local workspaces remain actionable in
  either degraded state (`internal/server/kata/effective_links.go::hydrateKataLinkCandidates`).
- Effective-link hydration admission is shared across requests, both globally
  and per daemon, so multiple panels cannot multiply upstream concurrency
  (`internal/server/kata/effective_links.go::Handler.kataLinkHydrationSlots`).
- Existing local workspaces remain reusable during daemon failures. New creation
  replaces request hints with an exact pinned-daemon reference before repository
  resolution and persistence (`internal/server/kata/workspace.go::Handler.canonicalKataWorkspaceMetadata`).
- Effective links retain typed workspace resolution status, source, and reasons.
  Resolver faults make the response partial without hiding usable task detail
  (`internal/server/kata/effective_links.go`).
- Kata workspace creation state is shared by daemon and issue UID across panel
  remounts. Reconcile transient results only with later association reads and
  session deletion tombstones (`frontend/src/lib/stores/kata-workspace-create.svelte.ts`).
- A linked-task action may update or navigate only while its initiating Forge
  subject and Kata selection remain current; creation still publishes its global
  result before that presentation fence (`frontend/src/lib/components/kata/KataLinksPanel.svelte`).
- Linked-task workspace actions honor the surrounding surface's disabled state
  during deletion and route transitions (`frontend/src/lib/components/kata/KataLinksPanel.svelte`).

## Related Context

- [`workspace-apis.md`](./workspace-apis.md): Kata workspace identity, creation,
  reuse, and repository mapping.
- [`ui-interaction-contracts.md`](./ui-interaction-contracts.md): contextual
  detail selection, refresh, and external launch behavior.
- [`docs-mode.md`](./docs-mode.md): folder-pinned Kata reference launches.
