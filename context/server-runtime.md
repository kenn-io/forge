# Server Runtime

- Startup and daily daemon telemetry send tracked repository counts only in the
  ranges documented in [Telemetry](../docs/configuration.md#telemetry)
  (`cmd/kenn-forge/notifications.go::captureDaemonActive`).
- Full sync passes report `sync_failed` once per source when a repository newly
  fails; checks 404s count as `not_found`. A successful read wins within each
  repository/source
  (`internal/github/sync_outcomes.go::syncPass.observe`).
- Observe repository, list, and checks reads; exclude local filesystem/capacity
  errors, budget waits, disabled features, and non-check item reads.
  HTTP 304 counts as success (`internal/github/sync_outcomes.go::syncPass.observe`).
- A successful source read in a full pass clears its in-memory outage state.
  Reasons are `auth`, `rate_limited`, `not_found`, or `other`, without identifiers
  (`internal/github/sync_outcomes.go::Syncer.finishSyncPass`).
- An outage may report again after restart or first repository ID resolution
  (`internal/github/sync_outcomes.go::Syncer.finishSyncPass`).
- Setup checks run on non-worker daemons with telemetry enabled at startup:
  at startup, each minute, and at shutdown (bounded to 900 ms). A ready
  workspace or a readable PR or issue qualifies: tracked local DB rows without
  workspace snapshots, or the active spoke's hub provider source. Either pull or
  issue source alone qualifies; inactive spokes skip remote checks
  (`internal/server/setup.go::Server.HasSetupWork`).
- The `telemetry.setup_finished` marker is written only after the reporter
  queues the event, and acceptance is remembered while the marker write retries.
  The marker means accepted, not delivered. Migration 69 writes it without
  reporting for databases that already held work or a sealed spoke
  preparation, so upgrades do not count as first-run setups (`cmd/kenn-forge/notifications.go::setupFinished.capture`).
- Startup liveness and ready health probes expose the same running build identity
  without a workspace bearer. Builds without VCS metadata must stamp the full
  commit (`internal/server/routepolicy/health_routes.go::HealthyResponse`).
- Screen telemetry uses Kit's durable claims per installation and UTC day;
  preserve existing claims during upgrades
  (`internal/telemetry/daily.go::initializeDailyClaims`).
- Browser screens, visits, and core actions share kit-ui's daily permission gate;
  quiet pauses visit timing until a later UTC day checks permission again;
  preserve accrued time, and expire pending core actions with their UTC day.
  Core actions use kit-ui's `reportDaily` claims, which a `disabled` reply
  clears with the screen claims
  (`frontend/src/lib/app/telemetry.ts::startRuntimeTelemetry`).
- Core action telemetry uses the same Kit daily claims: `pr_viewed` per provider
  and `workspace_opened` per workspace type, once per installation and UTC day.
  Both require `surface: web`. PR detail must be on screen in a visible browser
  tab. A workspace counts only on its own visible page or when a session or
  quick action launches in it; embedded display and clicks inside it send
  nothing, so new entry points need no telemetry hooks. Visibility changes
  reevaluate the daily count after midnight.
- Browser `search_empty` counts distinct submitted queries with empty server results,
  resetting when text changes (`frontend/src/lib/app/telemetry.ts::searchEmptyReporter`).
- `error_shown` counts a failed core action when its error comes on screen, once
  until that error unmounts; retry starts another attempt
  (`frontend/src/lib/components/shared/ErrorShown.svelte`).
- Launch flashes count only when the document is visible at failure time;
  telemetry preserves their existing presentation
  (`frontend/src/lib/stores/workspace-quick-actions.ts::runWorkspaceQuickAction`).
- Outcomes go through kit-ui's `reportVisit`: they wait through permission checks,
  and kit-ui drops those reported during backoff; posted failures aren't replayed
  (`frontend/src/lib/app/telemetry.ts::startRuntimeTelemetry`).
- Add screen names to both the daemon allowlist and the SPA list; the daemon rejects
  unknown screens
  (`frontend/src/lib/app/telemetry.ts::SCREEN_NAMES`).

## Package layout

- `internal/server` keeps the `Server` type, its constructors, and route
  wiring; handler areas live in sibling packages (`settingsapi`, `activityapi`,
  `syncevents`, ...). A moved `Server` method becomes a method on that package's
  `Handlers`, which `wireHandlers` builds right after allocating the server.
  Mutable `Server` fields are shared by pointer, never copied, so reloads and
  locks stay global (`internal/server/handler_wiring.go::Server.wireHandlers`).
- A `Server` literal outside `newServer` must go through `wiredServer`, or its
  handler fields stay nil (`internal/server/handler_wiring.go::wiredServer`).

## Frontend assets

- Probe the development frontend over HTTP; Vite's parent process can stay
  alive after a config reload stops listening. Use the repository-local launcher
  so updates use the installed project toolchain (`scripts/dev-stack-frontend.sh`).
- Vite routes live at root even when the backend has a base path; login redirects
  must translate back to that namespace. The static dev-auth proxy check requires
  the tested helper (`frontend/src/lib/dev/authBootstrapProxy.ts::authBootstrapProxy`).
- Precompress final emitted assets after Vite rewrites preload URLs; encoded
  responses must decode to the exact bytes served for the same asset identity
  (`frontend/vite.config.ts::precompressAssets`).
- Embed only Brotli asset copies to limit distribution size; other accepted
  encodings remain available through on-demand compression
  (`internal/server/compression/spa_handler.go::serveCompressedAsset`).

## Activity relay boundary

- Protocol metadata must be additive; a version check never authorizes breaking existing clients.
  Keep fixed producer and consumer wire fixtures independent of shared Go types.
  (`internal/activityrelay/protocol.go::Open`)
- Build the relay as a separate binary in this module so CI can test it with Forge; it must not
  import the SPA or depend on a running Forge daemon. (`cmd/kenn-forge-relay/main.go`)
- Public ingress authenticates GitHub signatures; private feed authorization belongs to the
  transport. Never put the feed behind the public proxy. (`internal/activityrelay/http.go::Handlers`)
- The relay is stateless and best effort: a hint is offered to every open subscription and
  discarded. No database, cursor, retention, or replay; a disconnected or lagging subscriber
  relies on ordinary syncing. Do not reintroduce durable resume, and do not document delivery as
  guaranteed. (`internal/activityrelay/broadcast.go::Broadcaster`)
- The feed is one SSE stream per Forge with a 20-second keepalive comment; a subscriber that
  stops reading drops hints rather than blocking webhook ingress, and a subscriber that hears
  nothing for three keepalive intervals treats the stream as stalled and reconnects. Graceful
  relay shutdown cancels open streams before waiting on the HTTP server.
  (`internal/activityrelay/http.go::serveStream`, `internal/activityrelay/protocol.go::Stream.Read`)
- The relay client's HTTP/2 health check must ping only after silence longer than the keepalive;
  without it a half-open pooled connection carries every retry after a timeout resets the stream.
  (`cmd/kenn-forge/notifications.go::relayHTTPClient`)
- Retain only routing metadata in transit, never raw webhooks, names, content, actors, refs,
  hashes, or secrets; this also applies to proxy and diagnostic logging. (`internal/activityrelay/http.go::reduce`)

Use this document for daemon startup, discovery, request-origin validation,
and the root event stream.

## Execution workers

- An execution worker owns one Unix account's worktrees and runtimes; its unscoped bearer
  authorizes only the registered execution routes. Never enable Serve-header authentication
  or provider/fleet administration in this role. (`internal/server/devbox_worker.go`)
- The Linux GitHub broker follows App installation access, without a separate repository list,
  and checks each enrolled developer's current permission before returning a single-repository
  token. (`internal/devbox/broker.go::Broker.Credential`)
- Forks serve `git` for the helper, Forge clone/fetch, and worker admission; `push`/`pr`
  are refused. Forks use their immediate organization parent for admission;
  erase resolves the same parent and evicts only its tokens.
  The upstream supplies all access checks, tokens, and caching; credentials retain
  the fork's ID and default branch for worker admission. Lookups reuse an installation-wide
  token with metadata read only; it stays in the broker.
  (`internal/devbox/broker.go::Broker.forkUpstream`)
- Service accounts skip the developer checks, so their configured write and read repository IDs
  admit those repositories and their contributor forks via the upstream's grant; GitHub decides
  which fork branches accept a push. Other repositories are refused, and a service account without
  any repository grant fails broker startup. Compare IDs after the identity check, never names.
  (`internal/devbox/broker.go::validateRepositoryGrants`)
- Repository names can be reused; stale broker token scope gets one refresh with full admission
  checks, without evicting other repositories' credentials. (`internal/devbox/broker.go::Broker.credential`)
- Writable broker tokens request Workflows write only when the installation grants it;
  cached tokens must match the requested permissions. Read-only tokens stay unchanged.
  (`internal/devbox/broker.go::Broker.credential`)
- The broker authenticates Unix peer UIDs and keeps personal GitHub credentials off workers.
  Human commit identity and the App push actor are distinct; GitHub rulesets enforce
  protected-branch restrictions. (`internal/devbox/broker.go`)

## Startup Contracts

- Until the full server swaps in, the primary listener answers only startup
  probes (`/livez`, the not-ready `/healthz`, and the discovery ping) and holds
  every other request for the full server instead of serving partial routes.
  Waiting must not spend a held request's body read budget, and bodyless
  requests keep net/http's own deadline handling so held streams survive
  (`internal/server/hostapi/startup_handler.go::SwitchHandler.hold`).
- Trailing slashes never change routing: the server serves `/base` as `/base/`
  and the frontend router maps `/base`, `/base?…`, and `/base#…` to the app
  root. `base_path` cannot start with a root-served segment (`api`, `healthz`,
  `livez`), which would make that route ambiguous
  (`internal/server/server.go::serveBareBasePath`,
  `frontend/src/lib/stores/router.svelte.ts::stripBase`,
  `internal/config/config.go::rootRouteSegments`).
- Hub readiness must not wait for repository discovery: restore the verified catalog
  locally, then resolve current configuration and start sync in the background
  (`cmd/kenn-forge/main.go::resolveStartupRepos`). Background discovery holds
  `configReloadMu` while it replaces the tracked set, so repository add, import,
  refresh, and delete handlers take that lock before applying their change
  (`internal/server/config_reload.go::InitializeProviderRepositories`).

- Bare `kenn-forge` is help-only, `serve` is foreground, and background
  lifecycle management is under `daemon start|status|stop|restart`
  (`cmd/kenn-forge/cli.go::newRootCommand`).
- Development reloads must allow the full daemon shutdown budget; a short
  Air or supervisor kill delay interrupts runtime cleanup
  (`internal/shutdownbudget/budget.go::Total`).
- Foreground listener flags belong only to `serve`; preserve explicit changed-state
  above environment values and redact endpoint credentials from startup source logs
  (`internal/cli/serve/serve.go::NewCommand`, `cmd/kenn-forge/main.go::logRuntimeConfig`).
- The locally built image starts the binary directly with authenticated wildcard defaults;
  persist its uid-1000 home and keep deployment-owned agent CLIs and Roborev separate
  (`Dockerfile`).
- The image's listener is plain HTTP: publish it only on host loopback or a private
  proxy network, and use HTTPS for external browser access and token bootstrap
  (`Dockerfile`, `docs/configuration.md::Container startup`).
- `daemon start` is idempotent: reuse requires verified identity for the same
  resolved `data_dir`; incompatible versions require `daemon restart`
  (`internal/daemonruntime/lifecycle.go::NewManager`).
- Lifecycle startup mints the API token under the authoritative data-directory
  lock; atomic serialized publication makes concurrent paths retain one
  credential (`internal/runtimelock/token.go::EnsureAuthToken`).
- Each data directory owns one random 128-bit node ID. Startup serializes its
  first write, rejects malformed existing identity, and validates it before a
  daemon replacement can stop the old process (`internal/runtimelock/node_id.go::EnsureNodeID`,
  `cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`).
- Lifecycle commands retain caller-spelled paths for initial default migration,
  then freeze canonical identity for locks, reloads, runtime records, and
  comparisons (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.lockMutationConfig`).
- Default-home upgrades copy the legacy config and its referenced credential
  files before marking completion and relocating the database; explicit config
  paths and `KENN_FORGE_HOME` never relocate config
  (`internal/config/legacy_migration.go::migrateLegacyConfig`).
- Sync-enabled startup keeps the local UI available when provider credentials or
  identity initialization is unavailable. Failures attributable to one provider
  host drop only that host while healthy hosts keep syncing; unattributable
  failures degrade to no provider sync. Dropped hosts serve cached data until a
  later restart (`cmd/kenn-forge/provider_startup.go::buildProviderControlPlaneOrDegraded`).
- A fully sealed active spoke never requires hub reachability at process start;
  its event lifecycle owns reconnection after transient network or DNS outages
  (`cmd/kenn-forge/spoke_startup.go::activateFederationSpokeAtStartup`).
- Provider API origins and cleartext acknowledgements are startup-bound. Config
  reload reports `restart_required` when either changes instead of claiming the
  boot-time client and clone policy were updated
  (`internal/server/configreload/config_reload.go::startupPlatformTransports`).
- Every role gets lazy Git routing; only hubs construct provider API,
  accounting, sync, archive, notification, and deferred-work machinery.
  `--disable-sync` suppresses hub refresh and never weakens spoke absence
  (`cmd/kenn-forge/provider_startup.go::buildServeControlPlanes`,
  `cmd/kenn-forge/main.go::run`).

- Keep process-compose as the backend's only restart owner; nested Air rerun
  loops can spawn competing servers while one holds the data-directory lock.
  Watch inputs must exclude generated clients and assets (`process-compose.yml`).

## Startup Lock

- Config identity must converge across relative, symlinked, and filesystem-equivalent
  name aliases before hashing or comparison
  (`internal/daemonruntime/runtime.go::CanonicalConfigPath`).
- Data-directory identity must converge across symlinked and filesystem-equivalent
  name aliases before hashing, comparison, or runtime publication
  (`internal/config/data_dir.go::CanonicalDataDir`).
- Linux without procfs retains the already symlink-resolved spelling instead of
  making all config-backed commands fail
  (`internal/pathidentity/canonical_linux.go::CanonicalExisting`).
- Background lifecycle mutations lock canonical config identity before sorted
  resolved `data_dir` identities; release but never unlink the stable
  cross-process paths (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`).
- Mutations reload config after locking and retry if `data_dir` changed;
  detached children reject a config that differs from the locked identity
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`, `cmd/kenn-forge/start_background.go::validateBackgroundLaunchConfig`).
- Start, stop, and restart use canonical config identity to lock both prior and
  current `data_dir` before reusing, replacing, or stopping a moved daemon
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.prepareConfigMutation`).
- Lifecycle mutations proof-authenticate config-attributed candidates individually;
  multiple records require one authoritative lock-metadata match before shadows
  are removed (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.runtimeForConfig`).
- An unauthenticated record is removable only while its authoritative lock is
  free or its former `data_dir` no longer exists
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.readCleanupRuntimeStatus`).
- Config-identified records take precedence over legacy records; a live legacy
  record remains attributable only while its `data_dir` matches current config
  (`internal/daemonruntime/runtime.go::ConfigRuntimes`).
- Authenticate moved legacy candidates before rejection; discard an unauthenticated
  stale record only when its authoritative data-directory lock is free
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.runtimeForConfig`).
- An authenticated pre-config-identity daemon matches authoritative status only
  when both discovery and status omit `config_path`; current-directory attribution
  above remains the boundary (`cmd/kenn-forge/daemon_lifecycle.go::runtimeStatusMatches`).
- Stop and restart prove the exact candidate record before signaling; moved
  candidates must also match authoritative config path and PID, while version
  differences remain stoppable
  (`internal/daemonruntime/lifecycle.go::FindVerifiedRecord`).
- Stop through kit's `daemon.Stop` and wait for process exit before replacement.
  Stop signals only a PID proven to be in the caller's PID namespace; a proven
  daemon in another namespace stays discoverable and reusable by start
  (`internal/daemonruntime/lifecycle.go::Stop`).
- Stop prefers authenticated config-identified runtime state under lifecycle locks;
  malformed TOML is consulted only when no attributable daemon exists
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.loadMutationConfigOrRuntime`).
- Daemon stop waits through the sum of every bounded shutdown phase plus process
  exit margin (`internal/shutdownbudget/budget.go::Total`).
- Unix sends one SIGTERM before requiring manual recovery; force-kill escalation
  cannot rule out PID reuse
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.stopLocked`).
- `kenn-forge.lock` is a stable lock target, not a disposable liveness sentinel.
  Never delete it: unlinking a held file lets another daemon lock a different
  inode and use the same data directory concurrently
  (`internal/runtimelock/lock.go::Acquire`).
- Release the OS lock while leaving the file in place. A file's existence says
  nothing about daemon liveness; only lock acquisition does
  (`internal/runtimelock/lock.go::Handle.Release`).

## Config Reload

- A server with no in-memory config reports reload disabled before reading a
  changed config file; inspect the pointer under `CfgMu` before computing runtime
  overrides (`internal/server/configreload/config_reload.go::Handlers.ApplyConfigChange`).

## Discovery And Readiness

- Every ready server publishes the standard `daemon.<pid>.json` record in
  config home, even when `config.toml` changes `data_dir`; the record is a
  discovery surface and does not replace the authoritative data-directory
  lock/status (`internal/daemonruntime/runtime.go::Publish`).
- Publish a runtime record only after the startup handler is installed and the
  HTTP server enters its listener accept loop; lifecycle discovery still proves
  the published loopback identity exactly before reuse (`cmd/kenn-forge/main.go::serveReadyListener.Accept`).
- Early identity proof establishes the startup owner, not application readiness;
  background lifecycle success requires `/healthz` and then re-proves the exact
  runtime record (`cmd/kenn-forge/start_background.go::waitForBackgroundReadiness`).
- Status uses authenticated config identity before TOML, including for moved runtimes;
  invalid config cannot strand an attributable daemon, while proof-unavailable state
  may report only the configured `data_dir` lock
  (`cmd/kenn-forge/daemon_lifecycle.go::daemonLifecycle.Status`).
- Record metadata is string-valued `node_id`, `host`, `port`, `read_only=false`,
  `require_auth`, `data_dir`, canonical `config_path`, and canonical
  `base_path`; `auth_token_path` is present only when auth is enabled, and
  `mcp_listen_addr` only when MCP is enabled. One typed owner builds and
  validates it;
  discovery still requires a live PID and a token-derived proof bound to the
  record's service, version, PID, network, and address; lifecycle selection and
  cleanup consider only PIDs in the caller's namespace
  (`internal/daemonruntime/runtime.go::Compatible`, `internal/daemonruntime/runtime.go::ConfigRuntimes`).
- The ready handler exposes standard identity at `GET /api/ping`; its private
  proof path binds the same identity to the published record and only accepts
  direct loopback requests
  (`internal/server/authapi/daemon_access.go::DaemonRequestPolicy.Admit`).
- Spoke activation is part of startup readiness for federation, not for local
  execution. A sealed spoke that cannot activate still serves local workspaces,
  logs `action_required` or `incompatible` with a reason, and supplies that
  reason to its local-only fleet projection. It must never construct a local
  provider control plane as a fallback (`cmd/kenn-forge/spoke_startup.go`,
  `cmd/kenn-forge/main.go`).

## Transport Trust Boundary

- Mutation identity must not depend on DELETE request bodies: the native desktop
  transport drops them before requests reach the daemon. Use path or query identity
  instead (`internal/server/activityapi/huma_routes.go::Handlers.UnsetStarred`).
- Loopback TCP is the required cross-platform transport; Unix sockets and
  named pipes are not lifecycle requirements. Background startup rejects
  non-loopback listeners before launching
  (`cmd/kenn-forge/start_background.go::validateBackgroundConfig`).
- `[api.tailscale_serve]` requires API authentication and one allowlisted identity
  header; browser WebSocket origins must use HTTPS and match the requested Forge
  authority. Peer-issued `forge_session` cookies get the same origin check.
  The startup-bound mode trusts local processes and suits only trusted hosts;
  policy edits require restart and never change federation credentials
  (`internal/server/authapi/api_auth.go::BrowserWebSocketOriginAllowed`,
  `internal/config/config.go::Config.validate`,
  `internal/server/authapi/daemon_access.go::DaemonRequestPolicy.AcceptsTailscaleServeUser`).
- `fleet setup` keeps Forge on loopback with API auth; `--tailscale` owns one
  Serve mapping, while `--origin` leaves ingress to the operator
  (`internal/fleetsetup/setup.go::configureCandidate`).
- Tailscale Serve identity is the tokenless path for people once a fleet is
  wired; enrollment and peer traffic keep their tokens. `--tailscale` setup
  keeps previously allowed logins and the existing origin's HTTPS port so a
  re-run never locks a user out or moves an enrolled origin. `--origin` never
  enables identity mode, because only Serve is known to replace client-supplied
  identity headers (`internal/fleetsetup/setup.go::planTailscaleLogins`).
- Setup services run as the selected current user and have no network-vendor or
  shell dependency (`internal/fleetsetup/service.go`).
- The daemon bearer and browser cookie remain full local-user credentials.
  Federation bearers authenticate through a separate branch, attach an exact
  node-ID principal to the request, and can dispatch only method/path pairs in
  the closed peer-route inventory
  (`internal/server/api_auth.go::Server.authorizeFederationRequest`,
  `internal/federationauth/authenticator.go::Authenticator.RequiredScope`).
- `forge_auth` is Secure only for direct TLS or trusted forwarded HTTPS
  (`internal/server/authapi/api_auth.go::Handlers.HandleAuthBootstrap`).
- HSTS is emitted after host validation only for verified HTTPS requests
  (`internal/server/server.go::Server.ServeHTTP`).
- An optional `X-Kenn-Forge-Node-ID` header is diagnostic only: the bearer
  establishes identity, and a supplied header that differs from its subject is
  rejected (`internal/server/api_auth.go::Server.authorizeFederationRequest`).
- Only the startup-bound bearer with exact loopback authority/peer and no
  forwarding headers bypasses proxy Host interpretation; cookies never qualify,
  and the bearer remains available when general API auth is off
  (`internal/server/authapi/daemon_access.go::DaemonRequestPolicy.Admit`).
- Discovery sends only a random challenge until the endpoint proves the daemon
  token and full runtime identity; the proof route requires the exact direct
  loopback authority without forwarding headers
  (`internal/daemonruntime/lifecycle.go::discovery.probe`).

- Authenticated wildcard listeners publish a concrete loopback discovery identity;
  direct proof/bearer admission still requires its exact authority and a local peer
  with no forwarding headers (`internal/daemonruntime/runtime.go::NewIdentity`).

## Host And Origin Boundary

- Host validation is the DNS-rebinding boundary and runs before API auth,
  cross-origin protection,
  and route handling. Do not move it behind middleware that trusts request
  credentials first (`internal/server/server.go::Server.ServeHTTP`).
- Mutation CSRF protection uses `http.CrossOriginProtection`; requests without browser
  origin metadata remain available to native and generated API clients, while Huma
  operations enforce their own body media types (`internal/server/streamapi/server.go::CheckCrossOrigin`).
- Trusted forwarded-host support adds validation of the canonical forwarded
  authority; it never replaces validation of the raw backend `Host`
  (`internal/server/hostapi/host_check.go::CheckHost`).
- After trusted forwarded-host validation, mutation origin checks compare with
  that public authority rather than the reverse proxy's backend `Host`
  (`internal/server/streamapi/server.go::CheckCrossOrigin`).

## Event Replay

- Workflow dispatch progress belongs to the server and the returned run ID;
  an accepted dispatch without an ID reports tracking unresolved and never starts a search
  (`internal/server/workflowapi/dispatch_follow.go::Handler.followDispatch`).
- SSE event IDs are process-scoped replay cursors, not durable sequence
  numbers. Reconnects may replay only IDs retained by the current process's
  ring (`internal/server/syncevents/event_hub.go::EventHub.ReplaySnapshotSince`).
- A cursor older than the ring or ahead of the current process head emits
  `reconnect.stale`; the client must discard incremental assumptions and perform
  an authoritative refetch (`internal/server/streamapi/server.go::Handlers.HandleSSE`).
- The frontend checkpoint advances only after an event's Effect consequences succeed;
  overlapping owners must keep it monotonic, and buffer pressure reconnects from the
  last accepted ID (`frontend/src/lib/stores/provider-events-workflow.ts::providerEventsProgram`).
- The hub's authenticated `/api/v1/federation/events` stream reuses the
  same replay ring but emits only provider-owned event types. Workspace,
  runtime, Docs, Kata, config, and spoke-connection events never cross that
  boundary (`internal/server/federation_events.go::Server.streamFederationEvents`).
- A spoke keeps the hub cursor private and republishes decoded provider
  events through ordinary local `EventHub.Broadcast`. Remote IDs never advance
  the browser's local replay floor; stale or undecodable remote state triggers
  an authoritative provider refresh instead
  (`internal/providerplane/events.go::EventClient`,
  `internal/server/syncevents/federation_events.go::Handlers.ReceiveHubEvent`).
- A federation-only SSE comment marks the replay/live boundary. Spokes remain
  provider-unavailable while replay drains, then reconcile authoritative state
  before announcing the hub connection; replayed status cannot
  overwrite that recovery snapshot
  (`internal/server/syncevents/federation_events.go::WriteFederationReplayComplete`).
- `sync_status`, `config.changed`, and `hub_connection_changed` are
  latest-value events cached in local ID order for fresh browser subscribers
  (`internal/server/syncevents/event_hub.go::EventHub.enqueueCachedLocked`).

## Long-Lived Transport Inventory

- Register ownership for every new Huma operation, including host-scoped variants;
  an omitted declaration breaks the shared route index for all requests
  (`internal/server/provider_route_policy.go::providerRouteIndex`).
- Long-lived HTTP and WebSocket contracts derive from the Huma registrations;
  catch-all proxies declare finite streaming variants on their operation, and
  tracing consumes the same inventory (`internal/server/transport_inventory.go::NewTransportInventory`).
- Federation SSE clears the whole-request client timeout but retains bounded
  dialing, TLS negotiation, response headers, header bytes, origin pinning, and
  redirect refusal. Its request context owns the response lifetime
  (`internal/providerplane/client.go::hardenedStreamingClient`).
- An annotated proxy stream is served only when the request explicitly accepts
  its declared media type (`internal/server/httpapi/transport.go::ValidateTransportAccept`).
