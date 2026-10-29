# Kata Integration

Use this document for Kata daemon discovery, pinned integration reads, Forge-owned
associations, and ownership boundaries. Workspace contracts remain in
[`workspace-apis.md`](./workspace-apis.md); frontend interaction contracts remain
in [`ui-interaction-contracts.md`](./ui-interaction-contracts.md).

## Ownership

- Kata is an external task domain, not a platform provider. Canonical task data
  stays in Kata daemons and must not be copied into Forge's provider registry or
  SQLite task tables.
- Forge alone owns links between Forge subjects and canonical Kata identities;
  Kata has no reverse-link table or Forge-specific linkage API.
- `WorkspaceKataMetadata` is a frozen identity/display hint for Kata-backed
  workspace rows; Forge does not refresh task titles or other presentation into
  those rows (`internal/db/types.go::WorkspaceKataMetadata`).
- Provider-item links use the stable repository row plus a nonblank provider
  external ID. Missing external identity requires resync and must never fall
  back to an item route number.
- Workspace effective links deduplicate by daemon and issue UID while
  preserving intrinsic, direct, and inherited provenance. Only the workspace's
  direct link is unlinkable.

## Product Surface

- Forge has no global `/kata` route, header tab, task browser, graph, history,
  recurrence editor, task mutation UI, snapshot API, event relay, or generic
  Kata proxy.
- Provider issue, pull request, and workspace detail surfaces show Forge-owned
  associations and one selected read-only task detail.
- Remote fleet workspace detail hides Kata controls until fleet transport
  exposes the narrow Kata routes; never send a remote workspace identity to the
  local daemon integration.
- Task presentation comes from the release-pinned `@kenn-io/kata-ui` package.
  Forge association controls stay outside the package's projection/error
  boundary.
- New Workspace searches references on an explicitly selected usable daemon.
  Roster state never establishes a globally active daemon.
- Docs uses its pinned daemon to resolve open or completed task references;
  bare short IDs must be unique. It opens Kata with `noopener,noreferrer` and
  renders no inline detail.
- Kata project mappings remain in Settings even though Kata is not a top-level
  mode.

## Daemon Discovery And Reads

- Workspace task targets reuse Kata links, never provider rows. Target
  mutations and the Kata MCP add tool are enabled only for a resolved remote
  catalog entry or a discovered configured local daemon.
- `$KATA_HOME/config.toml` is the daemon catalog. Forge config and legacy URL
  environment variables are not daemon authorities.
- A catalog entry is either a static remote URL or `local = true`, never both.
  Local entries resolve dynamically from Kata runtime records, so daemon
  restarts and socket rotation do not require a Forge restart.
- Local runtime records may resolve only to loopback HTTP or local Unix
  sockets; process discovery does not make a non-local address trusted.
- Roster responses and errors redact URL credentials and token material.
- Reads resolve the daemon from the route's daemon ID. Browser input never
  supplies daemon credentials or an ambient active-daemon fallback.
- Successful Kata health is authoritative for availability. Forge exposes
  `api_schema_version` only for diagnostics and never gates workflows on it;
  reachability and authentication failures return a typed 503.
- A missing or unsupported `api_schema_version` on a detail response renders a
  typed incompatibility state instead of passing data to the shared projection.
- Reference lists expose only open tasks, including empty-query autocomplete;
  completed tasks stay reachable through exact reference and issue-UID reads.
- Open-only reads must fill the requested count before filtering can hide later
  tasks, and fail closed when the daemon's bounded window cannot prove
  completeness (`internal/server/kata/client.go::kataDaemonClient.References`).
- Resolve qualified Docs references against Kata's complete `qualified_id`;
  never reinterpret its qualifier as a project UID or display name.
- A remote daemon URL may carry a reverse-proxy path prefix; API paths append
  beneath it.
- Treat daemon launch targets as untrusted: server and browser accept only
  absolute HTTP(S) URLs with a host and no user info.
- Daemon reads are redirect-safe, use total request deadlines, and bound
  response bodies.

## Freshness And Failure

- Contextual Kata panels defer association and task-detail reads until their
  tab is active. Associations refetch on activation, focus, or visibility
  recovery when stale; the periodic loop refreshes only the selected detail and
  stops while the pane is inactive or hidden.
- Daemon, link, and task-detail failures stay isolated per task. A failing Kata
  read must not break the surrounding provider/workspace detail or hide Forge's
  unlink/open/create-workspace controls.
- Effective-link hydration is `unavailable` when no candidate hydrates and
  `partial` when only some do; local workspaces stay actionable in either state.
- Resolver faults make an effective-link response partial without hiding usable
  task detail.
- Hydration admission is shared across requests, globally and per daemon, so
  multiple panels cannot multiply upstream concurrency.
- Existing local workspaces stay reusable during daemon failures. New creation
  replaces request hints with an exact pinned-daemon reference before
  repository resolution and persistence.
- Kata workspace creation state survives panel remounts, keyed by daemon and
  issue UID.
- A linked-task action may update or navigate only while its initiating Forge
  subject and Kata selection remain current.
- Workspace creation records its shared result even when that subject or
  selection is no longer current; only navigation and errors are fenced.
- Linked-task workspace actions honor the surrounding surface's disabled state
  during deletion and route transitions.

## Related Context

- [`workspace-apis.md`](./workspace-apis.md): Kata workspace identity, creation,
  reuse, and repository mapping.
- [`ui-interaction-contracts.md`](./ui-interaction-contracts.md): contextual
  detail selection, refresh, and external launch behavior.
- [`docs-mode.md`](./docs-mode.md): folder-pinned Kata reference launches.
