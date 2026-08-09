# Kata Mode

Use this document for Kata daemon discovery, selection, proxying, and ownership
boundaries. Task snapshot and workspace contracts remain in
[`workspace-apis.md`](./workspace-apis.md); frontend route and interaction
contracts remain in [`ui-interaction-contracts.md`](./ui-interaction-contracts.md).

## Ownership

- Kata is a non-provider mode. Task data remains in external Kata daemons and
  must not be copied into kenn-forge's provider registry or SQLite task tables.
- kenn-forge persists only its own workspace links and browser preferences; Kata
  task, snapshot, and cursor authority remains external or process-local
  (`internal/server/kata_snapshot_cache.go::newKataSnapshotCacheWithConfig`).
- Kata task workspace submission captures stable task identity and optional launch intent, then backend lifecycle events
  own completion. The application workflow records `workspace_created`, launches only after `workspace_status` reports
  ready, and navigates only while the initiating Kata surface is current
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::createWorkspaceForSelectedIssue`).

## Daemon Discovery

- `$KATA_HOME/config.toml` is the daemon catalog and source of the active daemon;
  kenn-forge config and legacy URL environment variables are not catalog
  authorities (`internal/kata/catalog.go::LoadCatalog`).
- A catalog entry is either a static remote URL or `local = true`, never both.
  Local entries resolve dynamically from live Kata runtime records so daemon
  restarts and Unix-socket rotation do not require a kenn-forge restart
  (`internal/kata/runtime.go::DiscoverLocalDaemonURL`).
- Local runtime records may resolve only to loopback HTTP or local Unix sockets;
  a process record does not make a non-local address trusted
  (`internal/kata/runtime.go::isLocalDaemonAddress`).
- Roster responses and errors redact daemon URL credentials and token material
  (`internal/kata/validation.go::RedactURL`).

## Selection And Transport

- Browser requests select daemons by kenn-forge's private selector header. The
  proxy strips that header and browser credentials before forwarding and injects
  only the selected daemon's resolved auth
  (`internal/server/kataapi/proxy.go::selectKataProxyDaemon`).
- Mutations and recurrence reads pin the daemon from the accepted snapshot;
  ambient active/default daemon fallback must not redirect an action after a
  switch (`frontend/src/lib/api/kata/taskClient.ts::pinnedDaemonHeaders`).
- Ordinary proxy requests have a total deadline, including response bodies;
  the long-lived event stream is exempt
  (`internal/server/kataapi/proxy.go::kataProxyDeadlineHandler.ServeHTTP`).
- A non-GET proxy transport failure invalidates the original daemon's snapshot
  authority and returns `mutationOutcomeUnknown`; clients must reconcile rather
  than replay the write (`internal/server/kataapi/proxy.go::newKataDaemonProxyEntryWithTransport`).
- Response-body loss and malformed successful acknowledgements also produce
  `mutationOutcomeUnknown`; issue creation without the revision needed for a
  requested metadata follow-up is instead a known partial outcome
  (`frontend/src/lib/api/kata/taskClient.ts::createKataTaskAPI`).
- In recurrence patches, an omitted optional template field means "leave unchanged" while `null` means "clear".
  Kata authority normalizes a cleared owner or priority to absence, so lost-response reconciliation must compare a
  requested `null` with authoritative `undefined` as the same applied outcome
  (`frontend/src/lib/features/kata/kata-mutation-evidence.ts::recurrencePatchMatches`).
- Raw Kata events are invalidation transport, not browser rendering authority.
  The browser reloads its exact snapshot intent and accepts task, detail,
  history, graph, and workspace targets atomically
  (`frontend/src/lib/features/kata/kataWorkspaceAuthorityController.svelte.ts::KataWorkspaceAuthorityController`).
- Cross-surface navigation carries daemon, project, task, and status authority.
  UID-only links must resolve an isolated detail read before routing, especially
  for daemon-bound Docs folders (`frontend/src/App.svelte::openAuxiliaryKataIssue`).

## Related Context

- [`workspace-apis.md`](./workspace-apis.md): task snapshots, enrichment,
  workspace identity, and repository mapping.
- [`ui-interaction-contracts.md`](./ui-interaction-contracts.md): accepted
  authority, routing, persistence, and mutation recovery.
- [`testing.md`](./testing.md): Kata component, event-stream, graph, and e2e
  boundaries.
