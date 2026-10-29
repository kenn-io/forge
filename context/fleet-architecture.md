# Fleet Architecture

Use this document for federation settings, snapshot aggregation, spoke routing,
or remote workspace and session operations.

## Devbox targets

- Devboxes are first-class execution targets: measure latency and GitHub calls against local
  workflows, and remove repeated work without caching membership or permission decisions
  (`internal/devbox/broker.go::Broker.Credential`).
- A successful push records the acknowledged commit in its tracking ref, even with main-only
  fetch configs; do not refetch just to update it, which repeats broker authorization (`internal/workspace/branch_sync.go::pushBranch`).
- Hubs and standalone controllers own devbox connections and route directly to workers; spokes
  stay local-only. Devbox keys are separate from fleet node keys, and worker bearer tokens never
  reach browser data (`internal/server/devboxes.go`).
- Devboxes are execution targets, not enrolled fleet peers. Roster, accounts, tools and deployment
  belong to an operator's private provisioning repository; public Forge owns the generic runtime
  and connection contract (`docs/devboxes.md`).
- Registries and controllers upgrade independently; new worker features must leave existing
  personal assignments usable during automatic rollout (`internal/devbox/registry.go::Assignment`).
- Service workers keep the bot's execution and commit identity; the human supervising one is a
  separate owner. Verify both identities when connecting (`internal/devbox/connections.go::Connections.Connect`).
- Machine lists, workspace selectors, and execution badges identify devboxes explicitly; they have
  no browser UI and must not appear as fleet spokes (`frontend/src/lib/components/layout/ForgeSelector.svelte`).
- Maintenance blocks workspace creation without hiding existing workspaces or marking the worker
  unreachable (`internal/fleet/enrich.go::buildHost`).
- Pending or failed devbox discovery must not block creation; the backend admits the saved devbox.
  Fresh maintenance and provider restrictions still block, and an unavailable default never
  silently creates locally (`frontend/src/lib/stores/workspace-target.svelte.ts`).
- Validate saved default destinations on config load and settings writes, but preserve valid
  devbox selections while disconnected (`internal/config/devbox.go::ValidateDefaultExecutionTarget`).
- Devbox REST and terminal traffic must bypass environment proxies; worker bearer credentials
  belong only on the direct tailnet connection (`internal/server/devboxes.go::registerDevboxTerminalAPI`).
- Devbox repository admission and launch context use GitHub's integer repository ID, the same ID
  that scopes App installation tokens (`internal/server/workspaceapi/execution_worker.go::Handler.admitWorkerRepository`).
- ACP chat uses the owning host's agent configuration and workspace directory; its relay carries
  whole transcript snapshots, which exceed terminal frame limits (`internal/server/fleetapi/fleet_proxy.go::Handler.serveFleetWebSocketProxy`).

## Ownership And Topology

Channels between peers and the packages that own them:

```text
spoke -> hub provider reads/writes  internal/providerplane client, spokeapi HubProviderSource,
                                    routepolicy ProviderProxy
hub -> spoke provider events (SSE)  providerplane EventClient -> syncevents ReceiveHubEvent
hub -> spoke raw snapshots          fleetapi fleet_hub; aggregation and projection in internal/fleet
hub -> spoke REST/WebSocket proxy   fleetapi fleet_proxy
enrollment and protocol version     internal/federation
peer bearers and route scopes       internal/federationauth
```

- A fleet is a one-hop execution view over independent daemons, not a replicated database. Each
  host stays authoritative for its local repositories, workspaces, runtimes, and execution mutations.
- Fleet identity is the data directory's random node ID; hostnames, listener addresses, and display
  names never establish it. Hub and spoke IDs must differ (`internal/runtimelock/node_id.go::EnsureNodeID`).
- `fleet.role` defaults to `hub`; a spoke requires a validated hub binding. Federation requires API
  authentication, the root base path, and a canonical HTTPS `fleet.base_url` (`internal/config/config.go::Fleet.Validate`).
- A spoke always needs hub access. Hub-to-spoke access is optional (`outbound_disabled`); omit such
  a member from workspace snapshots and reject remote execution without treating it as an outage
  (`internal/server/fleetapi/fleet_hub.go::Handler.fetchMemberResults`).
- Provider repository settings come from the hub, but each spoke owns its `worktree_base_path`
  keyed by stable identity; mutable or reused routes cannot move it to another repository (`internal/config/config.go::Repo`).
- Settings-carried repository observations use hub timestamps; spoke clocks never advance route
  freshness (`internal/server/syncevents/federation_provider_settings.go::Handlers.BuildProviderSettingsProjection`).
- Disabling federation must leave local settings readable so the operator can re-enable it;
  hub-owned settings may be absent while disabled (`internal/server/settings_handlers.go::Server.getSettings`).
- Spoke Settings loads node-local controls without hub I/O; hub-owned controls stay unavailable
  until provider settings report loaded (`frontend/src/lib/components/settings/SettingsPage.svelte`).
- Raw snapshots contain only producer-local facts, never fetched aggregates or observer
  permissions (`internal/server/fleetapi/fleet_adapter.go::Handler.buildLocalRaw`).
- Hub provider enrichment keys by stable repository identity and item number; local numeric
  repository IDs never cross the federation wire (`internal/fleet/provider_enrichment.go::EnrichProviderState`).
- A spoke pulls provider state for its own workspaces from the hub independently of the aggregate,
  because an outbound-disabled or unreachable spoke is absent from it; only a failed pull falls back
  to aggregate state (`internal/server/fleetapi/fleet_hub.go::Handler.pullHubProviderState`).
- Spokes replace their aggregate entries with fresh local authority, and raw endpoints never
  re-export aggregates, keeping data flow acyclic (`internal/server/fleetapi/fleet_hub.go::Handler.buildFleetSnapshot`).
- A spoke may use hub routes only after config, enrollment, and database seal match and remote
  identity, protocol, and activation validate. A sealed spoke booted disabled makes no startup
  egress and publishes a disconnected provider state (`cmd/kenn-forge/spoke_startup.go::activateFederationSpokeAtStartup`).
- Fleet consumes detached Workspace-owned summaries and runtime snapshots, not Workspace managers
  or root mutable config (`internal/server/fleetapi/handler.go::ConfigSnapshot`).
- Workspace overlays keep the local shell but omit links and metadata for removed source or
  associated items; inaccessible items remain visible (`internal/server/fleetapi/fleet_adapter.go::worktreeFromWorkspace`).

## Snapshot And Routing Contracts

- Protocol-3 enrollments upgrade only through the offline native migration, not wire compatibility;
  keep that transition only until maintained fleets migrate and rollback retention closes
  (`cmd/kenn-forge/fleet_migrate.go::migrateFleetProtocol`).
- Accept the historical protocol-3 `coordinator_node_id` seal encoding only during offline
  migration; current-protocol validation stays strict (`internal/db/protocol_migration.go::validateMigratingSpokePreparationSeal`).
- Snapshots use the shared federation protocol version, not a separate schema version; protocol
  version and node ID must match exactly (`internal/server/fleetapi/fleet_hub.go::Handler.fetchRawSnapshot`).
- Each projected host's config key is its node ID. Self uses validated `fleet.base_url`; remote
  origins come from enrollment, never self-reporting (`internal/fleet/enrich.go::buildHost`).
- Enrolled identity is immutable until revocation: reload keeps the boot role, hub binding, and
  origin; setup and restart reject an origin mismatch (`cmd/kenn-forge/spoke_startup.go::validateFederationHubOrigin`).
- A failed, incompatible, or unauthenticated member degrades only itself to an unreachable summary;
  it must not fail local or other member results (`internal/server/fleetapi/fleet_hub.go::Handler.fetchMemberRaw`).
- Structured peer rejections keep an operator-actionable cause; do not collapse enrollment or
  identity failures to an HTTP status (`internal/server/fleetapi/fleet_hub.go::federationPeerResponseError`).
- The hub uses the smaller of its own and the spoke-requested member budget; the spoke allows twice
  that budget for the full response (`internal/server/fleetapi/fleet_routes.go::Handler.getSnapshotAggregate`).
- Member requests use the current `fleet.peer_timeout`; fixed transport deadlines must not cap
  hot-reloaded values (`internal/server/fleetapi/fleet_enrollment.go::hardenedFederationMemberHTTPClient`).
- Hubs show the fleet-wide workspace surface. Spokes keep the full host directory for navigation
  but project only their own actionable workspace data (`internal/fleet/enrich.go::ProjectForObserver`).
- Hub Activity workspace indicators include the fleet; peer provider responses omit them so spokes
  apply local ownership (`internal/server/activityapi/huma_routes.go::Handlers.ListActivityService`).
- Activity never waits on member fan-out beyond a short bound; missing or stale results refresh in
  the background, one refresh at a time (`internal/server/fleetapi/activity_workspaces.go::Handler.latestActivityPeerResults`).
- Workspace lists use inline projected summaries without per-host fan-out; remote actions require
  the owner's projected availability. Incomplete aggregates keep absent-host rows; authoritative
  views keep only explicitly degraded hosts (`frontend/src/lib/components/terminal/workspace-list-schema.ts::retainDegradedHostWorkspaces`).
- Hub workspace rows show a compact execution-host badge; spoke rows stay unlabeled. Fleet chrome
  is reserved for actionable degradation (`frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`).
- The reserved `self` alias and the local node ID address local. An active spoke routes only when
  config matches its enrollment, which supplies the destination (`internal/server/fleetapi/fleet_proxy.go::Handler.resolveEnrolledSpoke`).
- Proxies preserve the owner's status, problem envelope, and safe end-to-end headers; responses
  never carry peer cookies, redirects, auth challenges, or hop-by-hop headers (`internal/server/fleetapi/fleet_proxy.go::Handler.serveRemoteFleetRESTProxy`).

## Background Monitors

- The local snapshot read path never does git or tmux I/O; three Fleet-owned monitors (worktree
  discovery, worktree stats, tmux) keep it fresh and must stay change-driven and cheap while idle.
- Monitors run only on demand: a live event subscriber or a recent snapshot read. Hubs read spokes
  through raw snapshots without an event stream, so that read is the demand signal
  (`internal/server/fleetapi/fleet_monitor_gate.go::runFleetMonitorLoop`).
- A git-directory fingerprint is a change hint, not proof: resample after the max age regardless,
  and take the full path when it cannot be computed (`internal/server/fleetapi/fleet_git_fingerprint.go::worktreeStatsFingerprint`).
- Unstaged edits and untracked files do not touch the git directory. Fleet's on-demand
  `RefreshWorktreeStats`/`RefreshProjectInventory` ignore the fingerprint; Workspace lifecycle
  mutations reach them through deps injected in `internal/server/server.go` and must keep calling them.
- Every discovery git spawn goes through `internal/procutil`.
- Exit status 1 from `ps` or `pgrep` means no match; any other failure is a probe error, never an
  empty process tree (`internal/server/fleetapi/fleet_tmux_monitor.go::probeFleetProcessTrees`).

## Transport Trust Boundary

- Supported fleets expose every peer only through operator-controlled private ingress such as a
  tailnet. HTTPS and federation bearers still authenticate peers; private reachability does not replace them.
- An activated hub and its spokes are one trust domain; a compromised active peer equals
  compromised fleet administration. Route scopes enforce ownership, not a hostile-peer or
  multi-tenant sandbox (`internal/server/routepolicy/provider_route_policy.go::ProviderRouteDeclarations`).
- Pending peers are not active: pending hub bearers have only enrollment access; pending spoke
  bearers have preparation, handoff, and enrollment access. Provider reads require activation
  (`internal/server/authapi/api_auth.go::PendingProviderRouteAllowed`).
- Activation gives the hub workspace mutation and terminal attachment authority on a spoke
  (`internal/federationauth/scope.go::HubToSpokeScopes`).
- Issuing and transferring an enrollment token approves eventual active membership, so transfer it
  only to a machine trusted with fleet provider data; the preparation seal proves retry-safe
  handoff, not a second approval (`internal/server/fleetapi/fleet_enrollment.go::Handler.sealSpokePreparation`).
- A federation principal may revoke only its own enrollment; local daemon authentication remains
  the hub operator's path for revoking any member (`internal/server/fleetapi/fleet_enrollment.go::Handler.revokeEnrollment`).
- A valid federation bearer takes precedence over optional ingress user identity so proxied peer
  requests keep their scoped spoke principal (`internal/server/api_auth.go::Server.authorizeAPIRequest`).
- Cross-node browser sign-in uses peer federation bearers, not Tailscale, because an active fleet
  is one trust domain. Only active grants carry `browser.login` (`internal/federationauth/scope.go::ScopeBrowserLogin`).
- Login tickets are single-use, short-lived, peer-bound, never logged, and consumed only by a
  non-API page load (`internal/server/api_auth.go::Server.handleLoginTicketBootstrap`).
- A peer-issued `forge_session` equals the local browser cookie only while the issuing peer's
  enrollment stays active, rechecked per request (`internal/server/api_auth.go::Server.hasValidBrowserSession`).
- Browser-login links are minted only with local or session credentials, never peer bearers; `409`
  means no direct credential and the selector falls back to a plain link (`internal/server/fleetapi/fleet_browser_login.go::Handler.resolveBrowserLoginTarget`).
- Peer credentials require the current enrollment and configured binding; activation upgrades both
  directions (`internal/server/api_auth.go::Server.federationPrincipalEnrollmentState`).
- Active peer bearers work only after the activation-lease handshake and while both copies of the
  24-hour lease are current and federation is enabled; explicit hub rejection never falls back to a
  cached lease (`internal/server/api_auth.go::Server.allowsActivationLeaseHandshake`).
- Lease-unaware or expired peers may reach only the identity preflight, the activation handshake,
  or revocation.
- Inbound federation bearers are stored only as digests, and revocation takes effect on the next
  authentication attempt (`internal/federationauth/store.go::Store.Authenticate`).
- An enrollment's reserved inbound credential cannot authenticate until atomically bound to the
  remote subject (`internal/federationauth/store.go::Store.BindInbound`).
- Federation authorization is fail-closed: an uninventoried method/path or a missing route scope is
  forbidden even for a valid bearer (`internal/federationauth/authenticator.go::authorizedRoutes`).
- Provider calls require the exact federation protocol header; a mismatch is explicit and never
  falls through to local state (`internal/server/api_auth.go::Server.authorizeFederationRequest`).
- Match authorization against the escaped path; decoding `%2F` first splits nested provider owners
  into extra segments (`internal/server/authapi/api_auth.go::Handlers.CanonicalAPIPath`).
- Members accept only canonical HTTPS origins. Proxied requests drop browser credentials and
  provenance, resolve the destination first, then add only that member's bearer; redirects are
  refused (`internal/server/fleetapi/fleet_enrollment.go::hardenedFederationProxyHTTPClient`).
- Ordinary peer reads stay time-bounded; long REST proxies rely on request cancellation because
  clone and diff-watch handlers legitimately outlast snapshot deadlines.
- REST proxies buffer and bound the request body before dialing; raw Huma adapter handlers do not
  enforce operation body limits (`internal/server/fleetapi/fleet_proxy.go::BufferProxyRequestBody`).
- WebSocket attach trace spans end after connection setup, before the long-lived bridge
  (`internal/server/fleetapi/fleet_proxy.go::startFleetAttachSpan`).
- Remote terminals are WebSocket-to-WebSocket bridges; the hub never allocates a proxy PTY or
  rewrites the spoke's session backend (`internal/server/fleetapi/fleet_proxy.go::Handler.serveFleetWebSocketProxy`).
- Federated clone URLs use HTTPS or SSH; HTTP only on loopback, because Forge cannot prove a remote
  route is tunnel-encrypted (`internal/providerplane/client.go::ValidateFederationWorkspaceLaunchSpecResponse`).
- Hub-supplied durable Git facts require spoke-side validation, including during preparation
  before the spoke role is saved (`internal/server/spokeapi/spoke_preparation.go::Handlers.RefreshSpokePreparationLaunchSpecs`).

## Provider Facts For Local Execution

- The hub is the only authority for provider-backed launch facts; spokes need no local repository,
  pull, or issue replicas (`internal/server/spokeapi/provider_sources.go::HubProviderSource.ResolveWorkspaceLaunchSpec`).
- Manual refresh, automatic assignment, and merge-request worktree facts also cross the provider
  plane: the hub does provider API work, the spoke only local Git and workspace work
  (`internal/server/provider_state_handoff.go::Server.federationRefreshWorkspaceLaunchSpec`).
- Clone-backed GitLab merge-request reads fetch only that merge request's head ref; the managed
  clone has no wildcard MR refspec (`internal/gitclone/clone.go::Manager.FetchMergeRequestHead`).
- Hub activity responses exclude its local workspace overlays and carry the hub's workspace-recency
  policy for spokes to apply; provider markdown images stay hub-owned (`internal/server/activityapi/huma_routes.go::Handlers.overlayLocalActivityWorkspaces`).
- Streaming provider routes (markdown video) have no whole-request timeout and are copied as they
  arrive, since video plays for minutes; other routes keep the buffered cap (`internal/server/routepolicy/provider_proxy.go::ProviderProxy.ServeHTTP`).
- A launch spec is bound to its exact request; stable identity permits owner/name refresh only
  within the same provider and host. Spokes validate the binding and clone URL, require an exact Git
  credential route, and reject historically occupied routes (`internal/providerplane/client.go::ValidateFederationWorkspaceLaunchSpecResponse`).
- Federated ad-hoc and MCP workspace creation resolve a hub descriptor before local lookup, so new
  repositories need no settings refresh (`internal/server/spokeapi/provider_sources.go::HubProviderSource.ResolveRepositoryRoute`).
- Registered spoke projects resolve stable hub identity during preparation and registration; raw
  fleet snapshots remain read-only (`internal/server/spokeapi/spoke_preparation.go::Handlers.ReconcileSpokePreparationProjects`).
- Base and fork clone URLs must resolve to canonical provider repository routes; URL syntax alone
  is not an authorization boundary (`internal/providerplane/client.go::FederationRemoteRepositoryRoute`).
- The workspace row and launch spec commit atomically before setup; later lifecycle work consumes
  that persisted spec, never spoke provider tables (`internal/db/queries_workspace_launch_specs.go::DB.CreateWorkspaceWithLaunchSpec`).
- Launch-spec refresh follows stable identity across renames and commits the verified route with
  the spec (`internal/db/queries_workspace_launch_specs.go::DB.PutRefreshedWorkspaceLaunchSpec`).
- Source visibility is a strict 15-minute hub lease for setup and provider-backed Git work; expired
  leases must refresh, hub failure is typed and retryable, and a hidden source stays unavailable
  (`internal/workspace/launch_spec.go::LaunchSpecRefreshError`).
- Runtime and agent sessions never renew the visibility lease. Ad-hoc and Kata workspaces stay spoke-local.

## Provider Event Flow And Availability

- `fleet.enabled` is live: disabling stops hub-backed provider work and the inbound stream;
  enabling opens a fresh stream whose replay barrier refreshes authoritatively (`internal/server/spokeapi/federation_events.go::HubEventLifecycle`).
- Spokes open one authenticated `events.read` SSE stream with the exact protocol header and no
  redirects. The hub sends only provider-owned events; workspace, runtime, Docs, Kata, config, and
  spoke-connection events never cross (`internal/server/federation_events.go::Server.streamFederationEvents`).
- Hub event IDs stay private to that stream; spokes re-stamp accepted events locally, so remote IDs
  never move the browser's checkpoint (`internal/server/syncevents/federation_events.go::Handlers.ReceiveHubEvent`).
- The hub marks the replay/live boundary with a federation-only SSE comment. Spokes stay
  provider-unavailable while replay drains, then refresh projections and `sync_status` before
  announcing the hub connected; replayed status cannot overwrite that recovery
  (`internal/server/syncevents/federation_events.go::Handlers.ResynchronizeHubProviderState`).
- Stale or undecodable remote state triggers an authoritative refresh. Initial connection announces
  no outage before its first attempt; a real failure does.
- While disconnected, the UI keeps loaded provider projections behind a stale-data banner and
  disables Sync and detail Refresh; it refreshes the selected projection before removing the banner
  (`frontend/src/lib/app-stores.svelte.ts::createAppStores`).
- A successful sync-status probe may restore hub availability while a projection-specific refresh
  error stays visible.
- Opening a tracked pull with no cached row runs one item sync through the hub, not a full
  repository sync; a missing repository directs the user to settings (`frontend/src/lib/stores/detail.svelte.ts::createDetailStore`).
- Spokes have no provider failover: their provider credentials are Git routes only, so hub loss
  pauses provider reads, refreshes, and writes while local execution continues (`cmd/kenn-forge/provider_startup.go::buildServeControlPlanes`).

## Configuration And Lifecycle

- Fresh-hub replacement is re-enrollment, not archive migration; preserve local execution identity
  and follow `docs/federated-fleet.md#replace-the-hub` (`internal/server/fleetapi/fleet_enrollment.go::Handler.joinFederation`).
- Federation protocol version 4 requires an exact match, with no translation or fallback
  (`internal/federation/protocol.go::ProtocolVersion`).
- Activation leases version separately inside protocol 4: upgraded hubs isolate lease-unaware
  spokes until they upgrade and reactivate, without re-enrollment (`internal/federation/protocol.go::ActivationLeaseVersion`).
- Enrollment accepts only canonical HTTPS origins. Each token is consumed by its first accepted
  request; retry or rekey needs a new token (`internal/federation/enrollment_store.go::Store.Begin`).
- A new token recovers an ambiguously accepted join with matching origins; bound or
  preparation-started enrollments are never replaced, and a hub must revoke or migrate its members
  before becoming a spoke (`internal/server/fleetapi/fleet_enrollment.go::Handler.joinFederation`).
- Enrollment secrets are printed once and enter `fleet join` only via hidden prompt, stdin, or
  `--token-file`. The sole pre-auth route is the exact enrollment POST (`internal/server/authapi/api_auth.go::Handlers.IsPreEnrollmentRequest`).
- The token deadline governs only starting enrollment; preparation pins the enrollment until
  activation, abort, or revocation. Joining never changes `fleet.role` or restarts Forge
  (`internal/federation/enrollment_store.go::Store.CleanupExpired`).
- `fleet prepare-spoke` is resumable and talks only to the local daemon. It proves the hub
  reachable and pins the enrollment before sealing local writes; earlier hub failure leaves the
  standalone provider plane open.
- A daemon without a local federation enrollment starts with provider writes open and restores no
  preparation state (`internal/server/server.go::newServer`).
- Once quiescing begins, the provider-write barrier survives restarts. Only
  `fleet abort-preparation` reopens it before activation, after admitted work drains
  (`internal/server/spoke_preparation.go::Server.abortFederationSpokePreparation`).
- After abort, a spoke-shaped process needs a restart before standalone provider work; forced abort
  keeps a revocation-only tombstone so hub cleanup stays possible.
- Forced abort with an unavailable hub still reports the enrollment the operator must revoke there.
- Preparation drains provider writes and deferred merges and freezes notification acknowledgement
  admission before reading handoff state. It hands off review drafts and user-authored workflow
  rows; seeded defaults stay local (`internal/db/queries_provider_state_handoff.go::DB.ListProviderStateForHandoff`).
- Preparation persists refreshed launch specs only after the spoke's exact Git credential route
  resolves.
- Handoff imports are content-addressed: identical state returns the same receipt, and different
  state reports both digests without overwriting either side (`internal/server/spoke_preparation.go::Server.prepareFederationSpoke`).
- The preparation seal is retry-safe and bound to the enrollment and handoff; a changed binding
  conflicts. Saving the spoke role reports `restart_required`; Forge never restarts itself
  (`internal/server/spoke_preparation.go::Server.persistPreparedSpokeRole`).
- Activation retries are bounded, reuse the sealed enrollment, and never duplicate membership.
  Renewal requires the enrollment to stay active, so a reply racing revocation cannot restore the lease
  (`internal/server/fleetapi/fleet_enrollment.go::Handler.activateEnrollment`).
- The hub must validate its own issued preparation seal before accepting an activation retry.
- Retryable activation failures back off for one minute; definitive rejection clears the lease and
  stops renewal until restart. `action_required` and `incompatible` keep local execution while
  suppressing routes (`cmd/kenn-forge/spoke_startup.go::activateFederationSpokeAtStartup`).
- Hub-initiated revocation is retry-safe on both sides: the spoke keeps the hub bearer only as an
  exact-revoke tombstone, and the hub records revocation before fallible cleanup
  (`internal/server/fleetapi/fleet_enrollment.go::Handler.revokeEnrollment`).
- Ordinary fleet settings update only operator preferences; enrollment owns role, hub binding, and
  membership, so a stale browser save cannot replace them (`internal/server/settingsapi/settings_routes.go::Handlers.UpdateFleetSettings`).
- Leaving requires a revoked enrollment and a stopped daemon and keeps current local execution
  state, never a pre-enrollment database; hub-owned provider state and credentials stay on the hub (`cmd/kenn-forge/fleet_leave.go::leaveFleet`).
- A revoked local receipt alone does not prove hub cleanup after forced abort; leave requires the
  matching hub binding before deleting the revocation credential.
- Membership and ordinary fleet settings reload live. Role, local and hub origins, API
  authentication, and session-monitor policy report `restart_required` until the process matches
  (`internal/server/configreload/config_reload.go::startupConfigSnapshot.RestartRequiredFor`).
- A daemon booted with fleet disabled cannot activate federation by reload unless API
  authentication was already active (`internal/server/streamapi/server.go::Handlers.ActiveFleetConfigSnapshotLocked`).
- Fleet workers start after Workspace and shut down before it; detailed shutdown rules live in
  [`workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md).
