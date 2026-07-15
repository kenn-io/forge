# Kata, Docs, And Messages Modes

## Purpose

Middleman will absorb an existing local-first task, markdown, and message UI as
first-class modes. This is an adaptation project, not a rewrite. The existing
behavior, route tests, component tests, and e2e harnesses are migration assets
and should be imported and adjusted to middleman's package names, route
namespace, config model, and application shell.

The integrated product should feel like one middleman app:

- Kata mode talks to external Kata daemons.
- Docs mode browses, views, edits, searches, and publishes configured markdown
  folders.
- Messages mode talks to a configured msgvault server and exposes a message-oriented
  search/detail/thread workflow.

Middleman must not move Kata task data into its own SQLite database as part of
this work. Task data remains owned by external Kata daemons.

These modes deliberately sit beside middleman's provider registry rather than
inside it. Provider-neutral identity and capability rules still govern PR/MR
and provider issue features; Kata, Docs, and Messages use their own domain
boundaries because they do not represent repository-provider resources.

## Naming

Use middleman names in committed code, plans, docs, UI routes, and commit
messages.

- Backend task-daemon code lives under `internal/kata`.
- Backend msgvault adapter code lives under `internal/messages/msgvault`.
- Backend docs code lives under `internal/docs`.
- Frontend mode code should use `kata`, `docs`, and `messages` package or file
  names where a user-facing domain name is required.
- The user-facing mode label and route are `Messages`. Msgvault remains the
  current backend adapter name, API route prefix, config key, and OpenAPI tag.

Do not preserve source-app product names, headers, localStorage namespaces,
route prefixes, or config names unless a compatibility requirement is explicitly
approved later.

## Architecture

Middleman remains the single HTTP server and Svelte app shell. New capabilities
plug into the existing Huma `/api/v1` API, OpenAPI generation workflow,
embedded SPA, config watcher, and app router.

Backend packages:

- `internal/kata` owns Kata daemon catalog loading, runtime discovery, target
  validation, daemon health probing, reverse proxy transport, and request
  selection.
- `internal/docs` owns registered markdown folder operations: folder registry,
  filesystem safety, tree/file/blob CRUD, markdown search, ignore handling, git
  status/change detection, and git publish.
- `internal/messages/msgvault` owns msgvault client behavior, health/capability probing,
  configuration validation, proxy endpoints, HTML sanitization, inline image
  handling, remote image gating, and per-upstream handle caches.
- `internal/server` registers the public REST routes, translates errors into
  middleman's problem envelope, and wires config-backed handlers into the server
  lifecycle.

Frontend packages:

- Reuse middleman's `App.svelte`, header, router, theme, base-path handling,
  generated API client, and shared UI primitives.
- Import and adapt existing Kata, Docs, and Messages feature/components rather
  than recreating them.
- Drop the imported app shell, topbar, global mode selector, mock bootstrap, and
  standalone daemon URL storage when equivalent middleman infrastructure exists.
- Keep mode-specific UI state local to each feature or an appropriate Svelte 5
  rune store.

## Configuration

Middleman config owns docs folders and msgvault settings. It does not own Kata
daemon definitions.

Add middleman config sections:

```toml
[[doc_folders]]
id = "notes"
name = "Notes"
path = "~/Documents/notes"

[msgvault]
url = "http://127.0.0.1:8080"
api_key_env = "MSGVAULT_API_KEY"
```

Docs folder config is persisted through middleman settings/config save paths.
Folder paths are expanded, made absolute, and validated for safe access before
use.

Msgvault config follows the existing secret posture:

- `api_key_env` is preferred.
- literal API keys are only kept if the existing imported behavior requires
  them initially; avoid adding new UI affordances that write secrets to disk.
- Setup/configure endpoints may write a small overlay file only if that is the
  safest adaptation path. If an overlay is used, document its middleman path and
  ensure normal config saves do not fold overlay-only secret metadata into the
  main config file.
- Msgvault secrets or secret-adjacent setup state must not enter the in-memory
  `config.Config` value that `Save()` serializes. Middleman's config save path
  rewrites the whole file from `Config` through `configFile`, so unrelated
  settings saves would otherwise persist overlay-only values.
- Adding docs and msgvault fields requires updating both `config.Config` and
  the on-disk `configFile` mirror, plus the manual mapping in `Save()`.

Kata daemon discovery follows Kata's own files and environment:

- `KATA_HOME` defaults to `~/.kata`.
- The shared daemon catalog is `$KATA_HOME/config.toml`.
- The catalog contains `active_daemon` and `[[daemon]]` entries.
- Each daemon entry maps `name`, `local`, `url`, `token`, `token_env`, and
  `allow_insecure`.
- A `local = true` catalog entry remains dynamically resolved. Middleman should
  not replace it with a static URL at startup.
- Runtime discovery reads Kata runtime records under `$KATA_HOME/runtime/...`,
  honoring `KATA_DB` when computing the runtime directory.
- `KATA_URL` and `KATA_TOKEN` may be kept only as temporary imported fallback
  behavior if tests already cover it; the preferred source is the Kata catalog.

## HTTP API

New public middleman API routes live under `/api/v1`. Generated-client routes
participate in the existing OpenAPI workflow. Hidden passthrough routes are
mounted through Huma with docs/spec output disabled when a generated type would
be misleading.

Kata routes:

- `GET /api/v1/kata/daemons` lists resolved daemons, default status, redacted
  target, auth kind, health, source, and local-start hints.
- `ANY /api/v1/kata/proxy/{path...}` is a hidden Huma passthrough, modeled on
  the existing roborev proxy registration pattern. It forwards to the selected
  daemon while preserving the daemon API path after the proxy prefix and stays
  out of generated clients.
- The selected daemon is identified by a middleman-owned header, for example
  `X-Middleman-Kata-Daemon`.
- Unknown daemon selection returns a typed problem response.
- No configured or discoverable daemon returns a typed service-unavailable
  problem response.

Docs routes:

- `GET /api/v1/docs/folders`
- `POST /api/v1/docs/folders`
- `PATCH /api/v1/docs/folders/{id}`
- `DELETE /api/v1/docs/folders/{id}`
- `GET /api/v1/docs/browse`
- `GET /api/v1/docs/folders/{id}/tree`
- `GET /api/v1/docs/folders/{id}/file`
- `PUT /api/v1/docs/folders/{id}/file`
- `POST /api/v1/docs/folders/{id}/file`
- `DELETE /api/v1/docs/folders/{id}/file`
- `POST /api/v1/docs/folders/{id}/file/actions/rename`
- `GET /api/v1/docs/folders/{id}/blob`
- `GET /api/v1/docs/search`
- `GET /api/v1/docs/folders/{id}/search`
- `GET /api/v1/docs/folders/{id}/git`
- `GET /api/v1/docs/folders/{id}/git/changes`
- `POST /api/v1/docs/folders/{id}/git/publish`

Msgvault routes use `/api/v1/msgvault` even if the browser-facing page is
`/messages`:

- `GET /api/v1/msgvault/health`
- `POST /api/v1/msgvault/configure`
- `GET /api/v1/msgvault/search`
- `GET /api/v1/msgvault/aggregates`
- `GET /api/v1/msgvault/threads/{conversation_id}`
- `GET /api/v1/msgvault/messages/{id}`
- `GET /api/v1/msgvault/messages/{id}/inline`
- `GET /api/v1/msgvault/messages/{id}/remote-image/{token}/{idx}`

Route registration rules:

- Every Huma operation gets explicit `OperationID`, summary, and exactly one
  route tag.
- Regenerate OpenAPI artifacts with `make api-generate` after route/type
  changes.
- Binary/blob endpoints must document binary responses so generated clients do
  not treat bytes as JSON.
- Mutating local filesystem/config routes are loopback-only.
- Preserve middleman's CSRF/origin protections for local state-changing
  requests, with the explicit policy in the Security section below.

## Security

The imported capabilities expand middleman from a read-mostly provider dashboard
into a local file editor, git publisher, daemon proxy, and msgvault HTML/image
surface. Security work is part of the adaptation, not an assumed property of
the current server.

CSRF/body policy:

- Middleman's current mutation gate applies to every non-GET `/api/` request
  and requires `Content-Type: application/json`.
- Docs mutation routes should keep that gate and JSON-wrap request bodies,
  including markdown file writes. Do not send raw markdown bytes to the write
  route.
- The Kata proxy needs an explicit allowlist because it is a passthrough and
  may need to forward daemon requests whose content type is not JSON. Split the
  gate into a cross-site check and a JSON content-type check: all mutating API
  requests still reject cross-site `Sec-Fetch-Site`, while the hidden Kata
  proxy may bypass the JSON content-type check only when the request satisfies
  the same-origin/none fetch-site policy. If `Sec-Fetch-Site` is absent on a
  non-JSON proxy mutation, reject it unless a later implementation adds an
  equivalent same-origin proof.

Loopback and local-surface policy:

- Add real per-route loopback gating for docs folder/file mutations, docs
  browse, git publish, msgvault configure, and any local config mutation.
- The gate checks `RemoteAddr` after base-path stripping and returns a typed
  403 problem response for non-loopback callers.
- The default loopback bind is not sufficient as the only control because the
  configured host can be widened.
- Read-only docs routes that expose configured folder contents should be
  reviewed individually before allowing non-loopback access. Start restrictive
  for file/blob reads if the implementation cannot prove a safe deployment
  story.

Other imported protections:

- Preserve msgvault HTML sanitization, inline image handling, remote image
  tokenization, content-type allowlists, and SSRF protections.
- Preserve daemon URL/token redaction in logs and roster responses.
- Preserve git publish command/path safety and never allow arbitrary shell
  interpolation.

## Config Reload

Middleman's config watcher currently hot-reloads only selected field groups and
marks startup-bound fields as restart-required. The new fields must be
classified explicitly.

- `doc_folders` is hot-reloadable. External config edits rebuild the docs
  registry and update `s.cfg`; UI folder mutations also update the in-process
  registry before saving.
- Msgvault overlay/configure is hot-reloadable for the initial adaptation. The
  configure path updates the msgvault handler/client in place, and config reload
  rebuilds the handler state when the msgvault block or overlay changes. The
  handler/client swap must be concurrency-safe for in-flight requests and should
  follow the existing config reload lock discipline instead of adding an
  unrelated lock path.
- Kata daemon catalog changes are not middleman config changes. The daemon
  roster is resolved from Kata files on demand or through a short-lived cache,
  so a restart is not required for catalog/runtime changes.
- If a field is intentionally restart-required, add it to
  `startupConfigSnapshot`; otherwise copy it during `applyConfigChange`.

## Error Handling

Use middleman's RFC 9457 problem envelope. UI behavior branches on stable
`code` and `details`, not prose.

New or reused codes should cover:

- invalid docs path or path escape;
- duplicate docs folder;
- docs config save unavailable;
- docs file already exists;
- docs file not found;
- git publish conflict;
- Kata daemon not configured;
- unknown Kata daemon;
- Kata daemon unreachable;
- msgvault absent;
- msgvault misconfigured;
- msgvault unavailable;
- msgvault unauthorized;
- unsupported msgvault search mode;
- invalid msgvault setup input.

Prefer existing wire codes (`badRequest`, `validationError`, `notFound`,
`conflict`, `forbidden`, `upstreamError`, `serviceUnavailable`) plus structured
details before adding new global codes. Add new codes only when the frontend
needs a distinct recovery branch.

## Frontend Design

Middleman gets additional top-level routes:

- `/kata`
- `/docs`
- `/messages`

The default route remains middleman's current activity/review workflow unless
the user explicitly changes product navigation later.

Header/navigation changes:

- Add compact and wide navigation entries for Kata, Docs, and Messages. The visible
  Messages navigation entry is backed by `internal/messages/msgvault` and `/api/v1/msgvault`.
- Hide Messages when msgvault is absent unless the setup/configure flow should be
  visible.
- Keep existing repo selector behavior isolated to provider-backed middleman
  modes; do not force it into Kata, Docs, or Messages.
- Preserve existing base-path handling and embedded-mode behavior.
- Treat these modes as desktop-first in the initial adaptation. Mobile `/m`
  routes and forced mobile presentations should continue to target existing
  activity/PR/issue workflows until a phone-specific workflow is designed.

Kata frontend adaptation:

- Keep the existing task workspace behavior and daemon switcher semantics.
- Ready Kata workspaces use the issue-workspace association order: prefer one unique
  upstream branch/remote match, then one unique local branch/HEAD match; absent or
  ambiguous matches stay unassociated (`internal/workspace/monitor.go::detectAssociatedPR`).
- Association uses repo sync and local Git independently of Kata daemon availability,
  and must not change `ItemType`, `ItemKey`, or `KataMetadata`
  (`internal/workspace/monitor.go::refreshWorkspaceAssociation`).
- Treat task lists as trees: a row stays out of the top-level projection only
  when its parent is present in the same result set (so it can fold under that
  ancestor once expanded). A child whose parent is absent — e.g. a search or
  filter that matches the child but not the parent — must still render and be
  selectable as its own top-level row instead of being dropped; otherwise the
  header counts it while the list shows "No tasks". Recursive row rendering must
  support nested subtasks beyond one level
  (`frontend/src/lib/components/kata/KataIssueList.svelte::topLevelIssues`,
  `frontend/src/lib/components/kata/KataIssueList.svelte::row`,
  `frontend/src/lib/stores/kata-workspace.svelte.ts::selectableViewIssues`).
- Task-list header controls should expand every visible task tree recursively
  through the task-detail API, and collapse should hide cached descendants
  without reintroducing them as top-level flat rows.
- Restored nested-task ancestor reconstruction is presentation-only. Temporary
  ancestors and each contextual successor bypass only the active status filter
  for the selected path; unrelated children still obey that filter and all
  other active filters. Context rows do not affect task counts, membership
  checks, or persisted workspace state. Their synthetic edges and reveal-owned
  expansion disappear when the reveal is cleared or superseded. Clicking a
  contextual row's disclosure control or invoking the task-list Expand all
  control promotes that row to user-owned expansion; ordinary selection does
  not. User-owned expansion survives reveal cleanup. An authoritative
  child response replaces cached edges and a transient refresh failure retains
  the seeded path for that reveal. A task
  admitted by raw filtered membership remains selected if resolution finds a
  cycle, missing parent, depth limit, missing ancestor detail, ancestor 404, or
  a transient ancestor request failure; only the temporary reveal chain is
  omitted, and transient failure remains retryable. Definitive absence of the
  selected task itself still clears persisted selection. Ancestor reconstruction
  walks serially to a maximum depth of 32 with one active walk per workspace.
  Route change, selection change, switch, or unmount aborts the walk where
  supported and prevents subsequent ancestor requests. At most one stale
  non-abortable request may drain; further changes coalesce to the latest
  selected UID. Retry starts a new walk only for the current UID and generation.
  Candidate ancestor data remains transaction-local and is published only after
  the candidate workspace is accepted.
- Project-scoped task filters must resolve the Kata project UID and read the
  daemon's project issue list instead of filtering the all-project issue list
  locally (`frontend/src/lib/api/kata/taskClient.ts::searchProject`).
- Kata workspace owns a dedicated task client; accepted provenance pins its
  selector, row actions, reloads, workspace identity, events, and stream while
  other surfaces follow their own selection (`frontend/src/App.svelte::kataWorkspaceAPI`).
- Removed accepted daemons remain selected and visibly unavailable until the
  user chooses a configured daemon (`frontend/src/lib/features/kata/KataDaemonSwitcher.svelte::displayId`).
- Daemon switching is disabled during initial bootstrap, writes, view work
  (including live-event refresh callbacks), and switches. Restoration is one
  discriminated transaction state, not a set of independent booleans. Each
  variant owns its accepted or candidate stores, daemon binding, route
  signature, generation, persistence delta, Retry owner, and stream state;
  transitions are the only operations allowed to publish those resources:

  | State             | Displayed daemon      | Persisted preference  | Accepted binding / staged reads                                    | Live workspace and actions                                                  |
  | ----------------- | --------------------- | --------------------- | ------------------------------------------------------------------ | --------------------------------------------------------------------------- |
  | accepted          | accepted daemon       | accepted daemon       | shared binding is accepted daemon                                  | accepted snapshot, one stream, interactive                                  |
  | provisional       | prior accepted daemon | prior accepted daemon | shared binding stays prior; candidate reads name target explicitly | prior snapshot displayed inert; target state staged                         |
  | rollback          | prior accepted daemon | prior accepted daemon | shared binding restored to prior accepted daemon                   | prior snapshot displayed inert while network state is rebuilt               |
  | terminal-retained | prior accepted daemon | prior accepted daemon | prior accepted daemon                                              | last accepted snapshot retained read-only; stream stopped; Retry exposed    |
  | terminal-cold     | none                  | unchanged or none     | none                                                               | inert empty workspace; stream stopped; Retry exposes initial restoration    |
  | superseded        | prior accepted daemon | prior accepted daemon | restored prior accepted daemon                                     | late target success/error is inert; current route owns the next transaction |

  A target is accepted only after catalog load, route restoration, and required
  cursor acceptance. Before that commit, target persistence deltas, route
  cleanup, selection cleanup, cursor state, and stream startup remain staged in
  a candidate workspace. The displayed snapshot, persisted preference, shared
  request binding, candidate read target, and accepted live workspace are
  separate concepts and must not be inferred from one another
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::switchKataDaemon`).
  Only `accepted` owns a live stream. Leaving it detaches that stream before
  candidate reads begin. Provisional, rollback, terminal, and superseded states
  own no stream. Stream callbacks and queued events carry daemon plus generation;
  detached or superseded delivery is discarded and recovered through cursor
  catch-up. Acceptance atomically starts exactly one stream after cursor
  acceptance.

- A routed Retry captures the full route signature plus a monotonically
  increasing restoration generation. Any route change invalidates the
  generation before restoring the prior accepted daemon. A running request need
  not be physically abortable, but its late success or rejection must not
  mutate the store, reinstall Retry, publish route cleanup, persist target
  state, or start a target stream. Cancelling a settled or running Retry performs
  full fallback rehydration: catalog, accepted route state, health validation,
  cursor catch-up, persistence deltas, and exactly one stream restart. A terminal
  state records URL changes without applying them. Retry captures the then-current
  full route signature and a new generation, never the originally failed
  signature; a route change during Retry supersedes that attempt and leaves Retry
  available for the newest signature.
- Target failures use network-backed rollback except for the initial and routed
  zero-progress cursor outcomes below. Rollback restores the prior accepted
  daemon, or the roster default when no explicit accepted preference exists. If
  target and rollback both fail after a workspace was accepted, enter
  `terminal-retained`: retain that snapshot read-only, stop its stream, suppress
  persistence and route reconciliation, and expose Retry. If initial restoration
  has no accepted snapshot, enter `terminal-cold` with an inert empty workspace
  and Retry. Retained Retry revalidates the prior daemon, catches up its cursor,
  applies selection and route correction, and restarts one stream before actions
  resume; cold Retry repeats initial acceptance without publishing provisional
  state.
- Cursor catch-up is serialized with SSE delivery and scoped by daemon plus
  workspace generation. Cursor state is session-local memory: it is neither
  written to daemon workspace persistence nor restored after page reload.
  Acceptance is defined by transaction and outcome:

  | Transaction                   | Failure before progress                                                                                                                                     | Partial progress then failure                                                                                          | Success                                      |
  | ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | -------------------------------------------- |
  | Initial, no accepted snapshot | enter `terminal-cold`; publish no persistence or stream; Retry owns the latest full route signature                                                         | accept target degraded at the last cursor, commit it, start one stream, and bind cursor Retry to the target generation | accept and commit target; start one stream   |
  | Routed, prior snapshot exists | remain `provisional`; retain the prior snapshot read-only with unchanged binding and persistence; start no stream; routed Retry owns the captured signature | accept target degraded, commit it, start one stream, and bind cursor Retry to the target generation                    | accept and commit target; start one stream   |
  | Manual switch                 | enter network-backed rollback; target publishes nothing                                                                                                     | accept target degraded, commit it, start one stream, and bind cursor Retry to the target generation                    | accept and commit target; start one stream   |
  | Rollback or rehydration       | enter retained terminal when a prior snapshot exists, otherwise cold terminal; start no stream                                                              | accept recovered daemon degraded at the last cursor and start one stream                                               | accept recovered daemon and start one stream |

  Cursor catch-up uses 100-event pages and coalesces each accepted batch into one
  view/detail reconciliation. Implementations must impose a finite acceptance
  budget of 1,000 events or two seconds and perform one authoritative reset at
  the daemon high-water cursor when either bound is exceeded rather than issue an
  unbounded request chain. Cursor and stream
  delivery remain serialized through the same daemon/generation queue.

  A cursor Retry whose daemon or generation no longer matches is inert. If
  accepted delivery removes the selected task from raw filtered membership,
  clear in-memory detail, replace the route with `issue` removed, and set only
  that daemon snapshot's `selectedIssueUID` to null; retain its view, filters,
  scope, and accepted cursor progress.

- Cross-mode task links carry their daemon target in route state until the
  workspace accepts that switch; linked issue selection must not run against
  the previously accepted daemon (`frontend/src/App.svelte::openKataIssue`).
- Daemon-route cleanup replaces the transient history entry. Unknown targets
  stay unresolved with an error, and failed initial acceptance restores the
  prior daemon preference (`frontend/src/lib/features/kata/KataWorkspace.svelte::routedDaemonError`).
- Route changes preempt routed restoration, routed Retry, manual switching, and
  fallback rehydration by invalidating the owning full-route signature and
  generation. Non-abortable requests may finish, but late target results are
  inert. Routed restoration and fallback immediately restart against the latest
  route on the prior accepted daemon; a manual switch restores the accepted
  binding/stream and lets the level-triggered reconciler apply the new route.
  Rollback continues only to recover the prior daemon, then applies the latest
  route. Terminal states suppress route effects until Retry accepts a workspace.
  Each workspace permits one current restoration chain and one draining stale
  non-abortable request per catalog, view, detail, cursor, or ancestor request
  class. Further route changes coalesce to the latest full signature. All task
  client methods accept `AbortSignal`; the proxy deadline remains the outer bound
  for requests that cannot abort
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::fullRouteSignature`).
- Kata route synchronization is level-triggered after workspace acceptance:
  the reconciler converges the workspace to the canonical URL whenever they
  differ. Persistence inheritance is initial-bootstrap-only. On that bootstrap,
  an explicit URL `view`, `scope`, or `issue` overrides only its corresponding
  persisted field; an omitted field inherits the persisted value, including a
  persisted null selection, or the default when no snapshot exists. After mount,
  omission means clear that route-owned field rather than re-inherit persistence:
  omitted `issue` deselects and persists null, omitted `scope` becomes unscoped,
  omitted `view` becomes the canonical default, and omitted `daemon` means the
  already accepted daemon rather than a new persisted lookup. Persisted
  non-route filters remain in force. A definitive task 404 clears in-memory
  detail and the route; transient catalog, list, detail, or cursor failure
  preserves source state for Retry. Invalid explicit scope canonicalizes to an
  unscoped route without deleting the saved daemon snapshot. Invalid persisted
  scope invalidates the entire daemon snapshot so its remaining fields cannot be
  partially inherited. Accepted state replaces the current history entry;
  interactions load optimistically and emit their matching route update.
- Routed issue selection waits only for the view/scope load whose complete
  route signature matches the current route. Superseded list loads are not
  awaited, and request/generation guards discard late results so stale work
  neither delays current selection nor aborts a newer detail request
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::reconcileRoute`).
- Daemon-scoped persistence contains view, filters, scope, and selected issue.
  Selection provenance is explicit: persisted selection is inherited only during
  initial bootstrap and changes only after a definitive replacement; explicit
  routed selection is staged and replaces that daemon snapshot only after detail
  and workspace acceptance; routed 404 clears only the route-owned candidate;
  default null selection is written only on commit; direct accepted selection
  persists after definitive detail success and direct deselection persists null;
  accepted membership loss or definitive absence clears the accepted daemon's
  saved selection. Invalid persisted scope invalidates that whole snapshot,
  whereas invalid explicit scope affects only the route candidate. Contextual
  ancestors are never selection or membership evidence. Persistence is versioned
  per daemon; unsupported old schemas are discarded rather than migrated. Storage
  reads and writes are best-effort and failures expose non-blocking unsaved-status
  feedback. Cross-tab events merge per daemon and revision so one daemon cannot
  overwrite another. Global browser layout preferences continue to persist
  independently. Scroll, expansion, pending work, and task caches do not persist.
- Event-driven proxy reads keep switching fail-closed until they settle. The
  Kata proxy applies a 30-second total deadline to ordinary TCP and Unix-socket
  requests, including response bodies, while the live event stream stays
  exempt. Rejection releases the view-work gate and propagates to stream
  reconnect handling
  (`internal/server/kata_proxy.go::newKataDaemonProxyEntryWithTimeout`).
- Replace direct daemon URL/localStorage bootstrap with calls to middleman's
  Kata daemon roster and proxy.
- Use a middleman-owned selector header for proxied daemon requests.
- The reachable-task graph is an alternate task-list pane, not detail content:
  launch it from a row or task detail action, load the REACHABLE graph from
  Kata's native daemon graph endpoint, and route graph node clicks through
  the existing task selection/detail path
  (`frontend/src/lib/features/kata/KataWorkspace.svelte`,
  `frontend/src/lib/features/kata/KataReachableGraph.svelte`,
  `frontend/src/lib/features/kata/kataReachableGraph.ts`; detailed design:
  `docs/superpowers/specs/2026-06-29-kata-reachable-graph-design.md`).
- Keep isolated-daemon e2e harness safeguards.

Docs frontend adaptation:

- Reuse the folder tree, markdown viewer/editor, outline, search, add-folder,
  rename, delete, and publish flows.
- Switch APIs to generated middleman clients and `/api/v1/docs`.
- Keep markdown image/blob handling, autocomplete behavior, and git publish UI.
- Adapt task reference navigation to middleman's Kata route model.

Messages frontend adaptation:

- Reuse search, facets, saved views, list, detail, thread, setup, linked
  messages, sanitization fallback, inline-image, and remote-image behavior.
- Switch APIs to generated middleman clients and `/api/v1/msgvault`.
- Keep the user-facing content workflow message-oriented, while the implementation
  package/routes identify the backend as msgvault.
- Adapt task-linking flows to middleman's Kata route model.
- Audit all timestamp handling. API timestamps stay UTC RFC3339; conversion to
  local time happens only in Svelte presentation code.

## Data Flow

Kata mode:

1. Frontend requests `/api/v1/kata/daemons`.
2. Server loads the Kata catalog from `$KATA_HOME/config.toml`.
3. Static daemon entries are validated and resolved.
4. Local daemon entries resolve on demand from runtime records.
5. Frontend sends task API requests through `/api/v1/kata/proxy/...` with the
   selected daemon header.
6. Server proxies to the chosen daemon and returns upstream responses, except
   for local selection/routing failures that use middleman's problem envelope.

Docs mode:

1. Server builds a docs registry from middleman config.
2. Frontend loads folders and selected tree/file state through `/api/v1/docs`.
3. Reads and searches are allowed for configured folders.
4. Writes, folder mutation, rename, delete, and publish routes enforce
   loopback/CSRF constraints.
5. Config persistence updates in-memory docs state and then serializes the
   whole middleman config file through the existing save path.

Messages mode:

1. Frontend probes `/api/v1/msgvault/health`.
2. Health reports absent, misconfigured, degraded, or OK state.
3. Search/detail/thread requests flow through `internal/messages/msgvault`.
4. HTML is sanitized before returning to the UI.
5. Inline and remote images are served through middleman-controlled endpoints
   with imported SSRF and content-type protections.
6. Configure updates the chosen middleman config/overlay path and refreshes the
   handler state without a restart.

## Test Migration

Existing tests are required migration inputs. Do not replace them with thinner
coverage. Adapt names, imports, route prefixes, generated clients, and fixtures
so the same behavior remains covered in middleman.

Imported tests must also be adapted to middleman's house style:

- use testify `require`/`assert` instead of `t.Fatal`, `t.Fatalf`, `t.Error`,
  or `t.Errorf`;
- use an `assert := Assert.New(t)` helper when a test has more than three
  assertions;
- use `openTestDB(t)` or the appropriate testutil database helper for
  DB-backed tests;
- route HTTP behavior through `srv.ServeHTTP` and choose
  `internal/server/apitest/` or `internal/server/e2etest/` according to
  `context/testing.md`;
- run Go tests with `-shuffle=on` and do not use `-v` unless needed for a
  specific failure;
- keep Playwright e2e coverage for user-visible frontend behavior.

Backend test inventory:

- Kata catalog loading from `$KATA_HOME/config.toml`.
- Kata runtime discovery and local daemon resolution.
- Kata target validation, token handling, health probing, selector behavior,
  redaction, proxy routing, and no-daemon failures.
- Docs folder registry, path safety, tree/file/blob CRUD, search, ignore
  handling, git status, git changes, git publish, and config save behavior.
- Msgvault client behavior, health states, configure validation, search,
  aggregates, thread/message fetches, sanitization, inline images, remote images,
  cache invalidation, and upstream failures.
- Security tests for `Sec-Fetch-Site` cross-site rejection, JSON content-type
  enforcement, per-route loopback gates, and disabled third-party API docs
  surfaces where relevant.
- Route metadata/OpenAPI tests for every new Huma operation.

Frontend test inventory:

- Kata completion requires deterministic coverage for initial and running
  routed-Retry supersession (late success and late rejection), settled Retry
  cancellation with full fallback catalog/cursor/stream restoration, manual
  switch route/unmount cancellation, rollback failure and retained/cold terminal
  Retry, all cursor acceptance outcomes, invalid explicit versus persisted
  scope, initial inheritance versus post-mount omission for every route field,
  bare-route deselection persistence, and contextual ancestor status filtering
  and expansion ownership. Transition-table tests assert displayed daemon,
  accepted binding, persistence delta, Retry owner, queued-event disposal, and
  zero-or-one stream ownership for every transaction/cursor outcome.
  Deterministic coverage also owns catch-up budget reset, stale-request
  coalescing, the 32-request ancestor bound, storage unavailable/quota behavior,
  per-daemon cross-tab merging, and old-schema discard. Full-stack coverage must
  include failed routed Retry followed by successful acceptance, same-mounted
  late-resolve and late-reject route supersession, and initial zero-progress
  `terminal-cold` failure followed by a URL change and successful Retry against
  the latest full route signature with exactly one target stream, in Chromium
  and Firefox; component/store tests own the remainder of the race matrix.
- Kata API wrappers, daemon switcher, route parsing, stores, task workspace,
  issue detail/list/actions, metadata editors, recurrence, command palette, and
  e2e harness behavior.
- Docs API wrappers, markdown parsing/rendering, folder tree, editor/viewer,
  search, add/rename/delete folder/file flows, autocomplete, and publish dialog.
- Messages API wrappers, visibility, setup dialog, search query builder, saved
  views, facets, list/detail/thread, linked messages, and sanitizer fallback UI.
- Router/header mode switching and base-path behavior in the middleman shell.

E2E requirements:

- Keep the isolated external Kata daemon harness and its refusal to use
  production Kata homes or databases.
- Add middleman server e2e tests with real SQLite for config-backed docs and
  msgvault behavior.
- Use generated clients for integration-style API tests where practical.
- Run Go tests with `-shuffle=on`.
- Run the affected Playwright e2e suite after final frontend/test edits.

## Migration Order

Land this work in reviewable slices. The default route remains unchanged and
new navigation can stay hidden until each mode passes its migrated tests.

1. Import test fixtures and non-UI backend domain packages into middleman with
   minimal package renaming. Done when migrated unit tests compile under
   middleman's conventions.
2. Add middleman config structs, load/save behavior, config reload
   classification, and tests for docs folders and msgvault settings. Done when
   external config edits and UI saves have explicit test coverage.
3. Add `internal/kata` discovery/proxy code and adapt all daemon unit tests.
   Done when catalog/runtime/local resolution tests pass.
4. Register `/api/v1/kata` roster routes and hidden proxy routes; adapt
   proxy/health/selection HTTP tests. Done when JSON and non-JSON mutation
   behavior is covered through the global CSRF gate.
5. Import `internal/docs`, register `/api/v1/docs`, adapt docs API/security
   tests, and regenerate clients. Done when filesystem mutation and read
   exposure policy is covered by wire-level tests.
6. Import `internal/messages/msgvault`, register `/api/v1/msgvault`, adapt msgvault API
   tests, and regenerate clients. Done when configure hot-reload and handler
   rebuild behavior is covered.
7. Move frontend domain code into middleman package layout, preserving tests.
   Done when imported component/API tests pass with middleman clients.
8. Add middleman router/header modes behind hidden or guarded navigation and
   adapt each feature to generated middleman clients. Done when shell mode
   switching and base-path tests pass.
9. Restore cross-mode links between Kata tasks, docs references, and msgvault
   messages. Done when links are covered in focused frontend and API tests.
10. Keep visible navigation guarded while the remaining state contracts are
    implemented and validated.
11. Add versioned per-daemon persistence, corrupt/old-schema discard,
    storage-failure UI, and cross-tab merge behavior.
12. Add the discriminated restoration state and common candidate-workspace
    transaction primitive, including side-effect ownership and exactly-one-stream
    invariants.
13. Add daemon/generation cursor transport, serialized cursor/SSE delivery,
    stale-request coalescing, and bounded catch-up/reset behavior without
    selection mutation.
14. Add URL/persistence precedence and selection provenance, then add
    cursor-driven membership reconciliation using that policy.
15. Add routed target staging and acceptance commit points.
16. Add routed Retry, latest-signature terminal semantics, cancellation, and
    supersession.
17. Add manual target staging using the same candidate primitive.
18. Add rollback, retained/cold terminal recovery, and default-daemon fallback.
19. Add bounded temporary ancestor reconstruction and reveal ownership.
20. Run focused transition, persistence, cursor-budget, cancellation, and
    stream-invariant tests.
21. Run full-stack routed and terminal-cold failure-to-success transactions,
    including same-mounted late resolve/reject, in Chromium and Firefox.
22. Flip visible navigation as a separate edit.
23. Rerun affected shell, routing, frontend, Chromium, and Firefox suites after
    the navigation edit.

## Documentation Updates

Update project ground-truth docs as part of the adaptation:

- Revise CLAUDE.md/AGENTS.md project overview, architecture, planned project
  structure, planned key files, and conventions so future work treats Kata,
  Docs, and Messages as first-class modes.
- Add context documents for Kata daemon integration, Docs filesystem safety, and
  Msgvault integration if the imported behavior is too large to keep in one
  design spec.
- Update README feature and configuration sections when the modes are exposed
  to users.
