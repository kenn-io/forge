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
- Replace direct daemon URL/localStorage bootstrap with calls to middleman's
  Kata daemon roster and proxy.
- Use a middleman-owned selector header for proxied daemon requests.
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
10. Flip visible navigation for completed modes, run focused tests
    slice-by-slice, then run full affected Go/frontend/e2e suites before
    merging.

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
