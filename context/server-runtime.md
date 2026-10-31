# Server Runtime

Use this document for daemon startup, discovery, request-origin validation,
the root event stream, telemetry, the activity relay, and execution workers.
Federation peers and the hub event stream live in
[`fleet-architecture.md`](./fleet-architecture.md).

## Telemetry

- Startup and daily daemon telemetry send tracked repository counts only in the ranges documented
  in [Telemetry](../docs/configuration.md#telemetry) (`cmd/kenn-forge/notifications.go::captureDaemonActive`).
- Full sync passes report `sync_failed` once per source when a repository newly fails; a successful
  read wins within each repository/source, and checks 404s count as `not_found` (`internal/github/sync_outcomes.go::syncPass.observe`).
- Observe only repository, list, and checks reads; exclude local filesystem/capacity errors, budget
  waits, disabled features, and non-check item reads. HTTP 304 counts as success.
- A successful source read clears its outage state. Reasons are `auth`, `rate_limited`, `not_found`,
  or `other`, never identifiers (`internal/github/sync_outcomes.go::Syncer.finishSyncPass`).
- An outage may report again after restart or first repository ID resolution.
- Setup counts as finished once a ready workspace or a readable PR or issue exists (for an active
  spoke, through the hub). Worker daemons and daemons booted with telemetry off never check
  (`internal/server/setup.go::Server.HasSetupWork`).
- `telemetry.setup_finished` means accepted, not delivered, and is written only after the reporter
  queues the event. Upgrades of databases that already held work must not count as first-run setups
  (`cmd/kenn-forge/notifications.go::setupFinished.capture`).
- Liveness and ready probes expose the running build identity without a workspace bearer; builds
  without VCS metadata must stamp the full commit (`internal/server/routepolicy/health_routes.go::HealthyResponse`).
- Screen telemetry uses Kit's durable claims per installation and UTC day; preserve existing claims
  during upgrades (`internal/telemetry/daily.go::initializeDailyClaims`).
- Browser screens, visits, and core actions share kit-ui's daily permission gate. Quiet pauses visit
  timing until a later UTC day; preserve accrued time, expire pending core actions with their day,
  and a `disabled` reply clears all claims (`frontend/src/lib/app/telemetry.ts::startRuntimeTelemetry`).
- Core actions count once per installation and UTC day with `surface: web`: `pr_viewed` per provider
  while PR detail is on screen in a visible tab, `workspace_opened` per workspace type.
- A workspace counts as opened only on its own visible page or when a session or quick action
  launches in it; embedded display sends nothing, so new entry points need no telemetry hooks.
- `search_empty` counts distinct submitted queries with empty server results, resetting when text
  changes (`frontend/src/lib/app/telemetry.ts::searchEmptyReporter`).
- `error_shown` counts a failed core action when its error comes on screen, once until that error
  unmounts; retry starts another attempt (`frontend/src/lib/components/shared/ErrorShown.svelte`).
- Launch flashes count only when the document is visible at failure time; telemetry preserves their
  presentation (`frontend/src/lib/stores/workspace-quick-actions.ts::runWorkspaceQuickAction`).
- Outcomes go through kit-ui's `reportVisit`, which waits through permission checks and drops reports
  during backoff; posted failures are not replayed.
- Add screen names to both the daemon allowlist and the SPA list; the daemon rejects unknown
  screens (`frontend/src/lib/app/telemetry.ts::SCREEN_NAMES`).

## Tracing

- OpenTelemetry export is opt-in through the standard `OTEL_*` environment; the local stack and
  its 7-day retention live in `tools/observability` (maintainer decision: one `grafana/otel-lgtm`
  container, Grafana published over Tailscale).
- Each process names its service only when `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` do
  not, because agents inherit the daemon's environment (`cmd/kenn-forge/telemetry_init.go::initTelemetry`).
- Subprocess spans name only the executable and git subcommand; arguments can carry credentials
  (`internal/tracing/tracing.go::StartSubprocess`).
- ACP owners and shared hosts join the launching trace through `TraceContext` in their config file;
  their start span ends when they listen, so later chat work does not hang off it.
- The browser exports Effect spans only through the daemon relay, which forwards OTLP/HTTP JSON to
  the daemon's own trace endpoint and exists only while the daemon exports OTLP over HTTP. The SPA
  learns this from `window.__KENN_FORGE_TRACE_EXPORT__` (`internal/tracing/relay.go::TraceRelay`).
- Relay requests get no HTTP server span, or every export would trace itself
  (`internal/server/otel_middleware.go::otelTraceable`).
- Every outgoing HTTP client wraps the transport that reaches the network, beneath ETag caches, sync
  budgets, and auth, with `tracing.HTTPTransport`, so a span measures only the remote call
  (`internal/tracing/httpclient.go::HTTPTransport`). Forge peers (fleet members, the hub, devboxes)
  use `tracing.PeerHTTPTransport`, the only variant that sends `traceparent` and baggage; third-party
  APIs get no trace context. `http.DefaultTransport` is never replaced, because dependencies
  type-assert it to `*http.Transport` and would panic. forbidigo rejects direct uses of it, but a
  client with a nil `Transport` bypasses tracing silently, so check new clients by hand.
- Helpers that clone a client's `*http.Transport` (`hardened*`, `fleettunnel.Registry.Transport`)
  receive the untraced transport. The tracing wrapper goes on afterwards, where the client is used.
- Client spans are named `<METHOD> <host>`, and `url.full` drops the query string and userinfo.
  Requests that carry an `Upgrade` header or accept `text/event-stream` or `application/x-ndjson` get
  no client span. Stream-only clients that send no such `Accept` header (the federation event stream,
  the activity relay subscription) stay unwrapped, and so do `TraceRelay` and the OTLP exporters.
- A generated API request names the Effect span making it as its `traceparent` parent unless a
  workspace-switch interaction trace is live, whose trace ID stays the profiling join key
  (`frontend/src/lib/instrumentation/traceContext.ts::traceHeadersForRequest`).

## Package layout

- `internal/server` keeps `Server`, its constructors, and route wiring; handler areas live in
  sibling packages (`settingsapi`, `activityapi`, `syncevents`, ...), where a moved `Server` method
  becomes a method on that package's `Handlers`.
- Mutable `Server` fields are shared with those `Handlers` by pointer, never copied, so reloads and
  locks stay global (`internal/server/handler_wiring.go::Server.wireHandlers`).
- A `Server` literal outside `newServer` must go through `wiredServer`, or its handler fields stay
  nil (`internal/server/handler_wiring.go::wiredServer`).

## Frontend assets

- Probe the development frontend over HTTP; Vite's parent can stay alive after a reload stops
  listening. Use the repository-local launcher (`scripts/dev-stack-frontend.sh`).
- Vite routes live at root even with a backend base path; login redirects must translate back to
  that namespace. The static dev-auth proxy check requires the tested helper (`frontend/src/lib/dev/authBootstrapProxy.ts::authBootstrapProxy`).
- Precompress final assets after Vite rewrites preload URLs; encoded responses must decode to the
  exact bytes served for the same asset (`frontend/vite.config.ts::precompressAssets`).
- Embed only Brotli copies to limit distribution size; other encodings use on-demand compression
  (`internal/server/compression/spa_handler.go::serveCompressedAsset`).

## Activity relay boundary

- A protocol version check never authorizes breaking existing clients; keep wire fixtures
  independent of shared Go types (`internal/activityrelay/protocol.go::Open`).
- The relay is a separate binary in this module so CI tests it with Forge; it must not import the
  SPA or depend on a running daemon (`cmd/kenn-forge-relay/main.go`).
- Public ingress authenticates GitHub signatures; private feed authorization belongs to the
  transport. Never put the feed behind the public proxy (`internal/activityrelay/http.go::Handlers`).
- The relay is stateless and best effort: no database, cursor, retention, or replay. Lagging
  subscribers rely on ordinary syncing; never reintroduce durable resume or document delivery as
  guaranteed (`internal/activityrelay/broadcast.go::Broadcaster`).
- The feed is one SSE stream per Forge with a 20-second keepalive. A slow subscriber drops hints
  rather than blocking ingress; three silent intervals mean stalled and reconnect (`internal/activityrelay/protocol.go::Stream.Read`).
- The relay client's HTTP/2 health check must ping only after silence longer than the keepalive;
  otherwise a half-open pooled connection carries every retry (`cmd/kenn-forge/notifications.go::relayHTTPClient`).
- Retain only routing metadata in transit, never raw webhooks, names, content, actors, refs,
  hashes, or secrets, including in proxy and diagnostic logs (`internal/activityrelay/http.go::reduce`).

## Execution workers

- An execution worker owns one Unix account's worktrees and runtimes; its bearer authorizes only
  the execution routes. Never enable Serve-header authentication or provider/fleet administration
  in this role (`internal/server/devbox_worker.go`).
- The Linux GitHub broker follows App installation access, with no separate repository list, and
  checks each developer's current permission before issuing a single-repository token (`internal/devbox/broker.go::Broker.Credential`).
- Fork credentials serve `git` only (helper, clone/fetch, admission), never `push` or `pr`. The
  fork's immediate organization parent supplies access checks and tokens (`internal/devbox/broker.go::Broker.forkUpstream`).
- Fork credentials keep the fork's ID and default branch; erase evicts only the parent's tokens, and
  installation-wide lookup tokens are metadata-read only and never leave the broker.
- Service accounts skip developer checks; only their configured repository IDs and those
  repositories' contributor forks are admitted, and a service account with no grant fails startup.
  Compare IDs after the identity check, never names (`internal/devbox/broker.go::validateRepositoryGrants`).
- GitHub, not the broker, decides which contributor-fork branches accept a push.
- Repository names can be reused: stale token scope gets one refresh with full admission checks,
  without evicting other repositories' credentials (`internal/devbox/broker.go::Broker.credential`).
- Writable tokens request Workflows write only when the installation grants it; cached tokens must
  match the requested permissions.
- The broker authenticates Unix peer UIDs and keeps personal GitHub credentials off workers; human
  commit identity and the App push actor are distinct, and GitHub rulesets enforce protected branches.

## Startup Contracts

- Bare `kenn-forge` is help-only, `serve` is foreground, and background lifecycle is
  `daemon start|status|stop|restart` (`cmd/kenn-forge/cli.go::newRootCommand`).
- Until the full server swaps in, the listener answers only startup probes and holds every other
  request rather than serving partial routes; holding must not consume a request's body read budget
  (`internal/server/hostapi/startup_handler.go::SwitchHandler.hold`).
- Trailing slashes never change routing (`/base` serves as `/base/`). `base_path` cannot start with
  a root-served segment (`api`, `healthz`, `livez`) (`internal/config/config.go::rootRouteSegments`).
- Hub readiness must not wait for repository discovery; restore the verified catalog, then discover
  and sync in the background (`cmd/kenn-forge/main.go::resolveStartupRepos`).
- Background discovery replaces the tracked set under `configReloadMu`, so repository add, import,
  refresh, and delete must take that lock (`internal/server/configreload/config_reload.go::Handlers.InitializeProviderRepositories`).
- Development reloads must allow the full daemon shutdown budget; a short kill delay interrupts
  runtime cleanup (`internal/shutdownbudget/budget.go::Total`).
- Keep process-compose the backend's only restart owner; nested Air rerun loops spawn competing
  servers. Watch inputs must exclude generated clients and assets (`process-compose.yml`).
- Verify reloads against the real checkout, not just scratch trees: single-file
  watches traverse their parent, so local worktrees and caches can disable the
  entire backend watcher while health checks still pass (`process-compose.yml`).
- Restart Vite after dependency lock or manifest changes: its source-package
  aliases retain paths resolved at startup, so new application code can import
  missing exports from an older package (`frontend/vite.config.ts`).
- A pull of new commits must rebuild the canonical checkout even when only
  documentation changed; source-file events alone do not satisfy the update
  contract (`process-compose.yml`).
- Listener flags belong only to `serve`; explicit flags beat environment values, and startup logs
  redact endpoint credentials (`internal/cli/serve/serve.go::NewCommand`).
- The local image starts the binary directly with authenticated wildcard defaults; persist its
  uid-1000 home and keep deployment-owned agent CLIs and Roborev separate (`Dockerfile`).
- The image's listener is plain HTTP: publish it only on host loopback or a private proxy network,
  and use HTTPS for external browser access and token bootstrap (`docs/configuration.md`).
- `daemon start` is idempotent: reuse requires verified identity for the same resolved `data_dir`;
  incompatible versions require `daemon restart` (`internal/daemonruntime/lifecycle.go::NewManager`).
- Concurrent startups must end with one API token, minted under the data-directory lock
  (`internal/runtimelock/token.go::EnsureAuthToken`).
- Startup rejects a malformed node ID and validates it before a replacement stops the old daemon
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`).
- Lifecycle commands use caller-spelled paths only for initial default migration, then canonical
  identity everywhere (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.lockMutationConfig`).
- Default-home upgrades copy the legacy config and its credential files before marking completion;
  explicit config paths and `KENN_FORGE_HOME` never relocate config (`internal/config/legacy_migration.go::migrateLegacyConfig`).
- Provider credential or identity failures never take down the local UI: failures attributable to
  one host drop only that host until restart; unattributable ones disable provider sync
  (`cmd/kenn-forge/provider_startup.go::buildProviderControlPlaneOrDegraded`).
- Provider API origins and cleartext acknowledgements are startup-bound; reload reports
  `restart_required` rather than claiming they changed (`internal/server/configreload/config_reload.go::startupPlatformTransports`).
- Every role gets lazy Git routing; only hubs build provider API, sync, archive, notification, and
  deferred-work machinery. `--disable-sync` never weakens spoke absence (`cmd/kenn-forge/provider_startup.go::buildServeControlPlanes`).
- A sealed active spoke never needs the hub at process start; its event lifecycle owns
  reconnection (`cmd/kenn-forge/spoke_startup.go::activateFederationSpokeAtStartup`).

## Startup Lock

- Config and data-directory identity must converge across relative, symlinked, and
  filesystem-equivalent aliases before hashing or comparison (`internal/config/data_dir.go::CanonicalDataDir`).
- Linux without procfs keeps the symlink-resolved spelling rather than failing config-backed
  commands (`internal/pathidentity/canonical_linux.go::CanonicalExisting`).
- Lock order is canonical config identity, then sorted `data_dir` identities; release but never
  unlink the stable lock paths (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`).
- Mutations reload config after locking and retry if `data_dir` changed; start, stop, and restart
  lock both prior and current `data_dir`. Detached children reject a config that differs.
- Prove each config-attributed candidate individually; multiple records need one authoritative
  lock-metadata match before shadows are removed (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.runtimeForConfig`).
- An unauthenticated record is removable only while its authoritative lock is free or its former
  `data_dir` no longer exists (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.readCleanupRuntimeStatus`).
- Config-identified records beat legacy records; a live legacy record is attributable only while
  its `data_dir` matches current config (`internal/daemonruntime/runtime.go::ConfigRuntimes`).
- An authenticated pre-config-identity daemon matches status only when both discovery and status
  omit `config_path` (`cmd/kenn-forge/daemon_lifecycle.go::runtimeStatusMatches`).
- Stop and restart prove the exact record before signaling; moved candidates must also match config
  path and PID, while version differences stay stoppable (`internal/daemonruntime/lifecycle.go::FindVerifiedRecord`).
- Stop waits for process exit before replacement and signals only PIDs in the caller's PID
  namespace; a daemon in another namespace stays discoverable and reusable (`internal/daemonruntime/lifecycle.go::Stop`).
- Stop prefers authenticated runtime state; malformed TOML is consulted only when no attributable
  daemon exists (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.loadMutationConfigOrRuntime`).
- Unix sends one SIGTERM, then requires manual recovery; force-kill cannot rule out PID reuse
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.stopLocked`).
- Never delete `kenn-forge.lock`: unlinking a held file lets another daemon lock a new inode on the
  same data directory. Only lock acquisition proves liveness (`internal/runtimelock/lock.go::Acquire`).

## Discovery And Readiness

- Every ready server publishes `daemon.<pid>.json` in config home, even when `data_dir` moves; the
  record is for discovery and never replaces the data-directory lock (`internal/daemonruntime/runtime.go::Publish`).
- Publish the record only once the server is accepting connections; lifecycle discovery still
  proves the published loopback identity before reuse (`cmd/kenn-forge/main.go::serveReadyListener.Accept`).
- Identity proof establishes the startup owner, not readiness; background start succeeds only after
  `/healthz` and a re-proof of the record (`cmd/kenn-forge/start_background.go::waitForBackgroundReadiness`).
- Status uses authenticated config identity before TOML, so invalid config cannot strand an
  attributable daemon (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.Status`).
- Discovery requires a live PID and a token-derived proof bound to the record's service, version,
  PID, network, and address; one typed owner builds and validates the record (`internal/daemonruntime/runtime.go::Compatible`).
- `GET /api/ping` exposes standard identity; the private proof path accepts only direct loopback
  requests (`internal/server/authapi/daemon_access.go::DaemonRequestPolicy.Admit`).
- A sealed spoke that cannot activate still serves local workspaces and reports `action_required`
  or `incompatible`; it never builds a local provider control plane as a fallback (`cmd/kenn-forge/spoke_startup.go`).
- Local thin clients never infer startup-bound middleware policy from reloadable config; derive
  request metadata from the runtime record (`internal/daemonclient/daemonclient.go::Discover`).

## Transport Trust Boundary

- Never carry mutation identity in DELETE bodies: the native desktop transport drops them. Use path
  or query identity (`internal/server/activityapi/huma_routes.go::Handlers.UnsetStarred`).
- Loopback TCP is the required cross-platform transport; Unix sockets and named pipes are not.
  Background startup rejects non-loopback listeners (`cmd/kenn-forge/start_background.go::validateBackgroundConfig`).
- `[api.tailscale_serve]` requires API auth and one allowlisted identity header; it trusts local
  processes, so it suits only trusted hosts. Policy edits need restart and never change federation
  credentials (`internal/server/authapi/daemon_access.go::DaemonRequestPolicy.AcceptsTailscaleServeUser`).
- Browser WebSocket origins, including peer-issued `forge_session` cookies, must use HTTPS and match
  the requested Forge authority (`internal/server/authapi/api_auth.go::BrowserWebSocketOriginAllowed`).
- `fleet setup` keeps Forge on loopback with API auth; `--tailscale` owns one Serve mapping, while
  `--origin` leaves ingress to the operator (`internal/fleetsetup/setup.go::configureCandidate`).
- Serve identity is the tokenless path for people; enrollment and peer traffic keep tokens. Re-runs
  keep allowed logins and the HTTPS port. `--origin` never enables identity mode, since only Serve
  replaces client identity headers (`internal/fleetsetup/setup.go::planTailscaleLogins`).
- Setup services run as the selected current user with no network-vendor or shell dependency
  (`internal/fleetsetup/service.go`).
- The daemon bearer and browser cookie are full local-user credentials; federation bearers are a
  separate route-limited principal (see `fleet-architecture.md`).
- `forge_auth` is Secure only for direct TLS or trusted forwarded HTTPS
  (`internal/server/authapi/api_auth.go::Handlers.HandleAuthBootstrap`).
- HSTS is emitted, after host validation, only for verified HTTPS (`internal/server/server.go::Server.ServeHTTP`).
- `X-Kenn-Forge-Node-ID` is diagnostic only: the bearer establishes identity, and a differing header
  is rejected (`internal/server/api_auth.go::Server.authorizeFederationRequest`).
- Only the startup-bound bearer with exact loopback authority and peer and no forwarding headers
  bypasses proxy Host interpretation; cookies never qualify, and it works with API auth off
  (`internal/server/authapi/daemon_access.go::DaemonRequestPolicy.Admit`).
- Discovery sends only a random challenge until the endpoint proves the daemon token and full
  identity over direct loopback (`internal/daemonruntime/lifecycle.go::discovery.probe`).
- Wildcard listeners publish a concrete loopback discovery identity; direct admission still needs
  its exact authority and a local peer (`internal/daemonruntime/runtime.go::NewIdentity`).

## Host And Origin Boundary

- Host validation is the DNS-rebinding boundary and runs before API auth, cross-origin protection,
  and routing; never move it behind middleware that trusts credentials first (`internal/server/server.go::Server.ServeHTTP`).
- Mutation CSRF uses `http.CrossOriginProtection`; requests without browser origin metadata stay
  available to native and generated clients (`internal/server/streamapi/server.go::CheckCrossOrigin`).
- Trusted forwarded-host support adds validation of the forwarded authority; it never replaces
  validation of the raw backend `Host` (`internal/server/hostapi/host_check.go::CheckHost`).
- Behind a trusted forwarded host, mutation origin checks compare with that public authority, not
  the proxy's backend `Host`.

## Event Replay

- Workflow dispatch progress belongs to the server and the returned run ID; a dispatch accepted
  without an ID reports tracking unresolved and never searches (`internal/server/workflowapi/dispatch_follow.go::Handler.followDispatch`).
- SSE event IDs are process-scoped replay cursors, not durable sequence numbers
  (`internal/server/syncevents/event_hub.go::EventHub.ReplaySnapshotSince`).
- A cursor older than the ring or ahead of the process head emits `reconnect.stale`; the client
  must drop incremental assumptions and refetch (`internal/server/streamapi/server.go::Handlers.HandleSSE`).
- The frontend checkpoint advances only after an event's consequences succeed and stays monotonic;
  buffer pressure reconnects from the last accepted ID (`frontend/src/lib/stores/provider-events-workflow.ts::providerEventsProgram`).
- `sync_status`, `config.changed`, and `hub_connection_changed` are latest-value events replayed to
  fresh subscribers (`internal/server/syncevents/event_hub.go::EventHub.enqueueCachedLocked`).
- The hub's federation event stream serves from this same replay ring, filtered to provider-owned
  events; its rules live in `fleet-architecture.md`.

## Long-Lived Transport Inventory

- Register ownership for every new Huma operation, including host-scoped variants; one omitted
  declaration breaks the shared route index for all requests (`internal/server/provider_route_policy.go::providerRouteIndex`).
- Long-lived HTTP and WebSocket contracts derive from Huma registrations; catch-all proxies declare
  finite streaming variants, and tracing uses the same inventory (`internal/server/transport_inventory.go::NewTransportInventory`).
- Federation SSE drops the whole-request timeout but keeps bounded dial, TLS, response headers,
  origin pinning, and redirect refusal (`internal/providerplane/client.go::hardenedStreamingClient`).
- An annotated proxy stream is served only when the request explicitly accepts its declared media
  type (`internal/server/httpapi/transport.go::ValidateTransportAccept`).
