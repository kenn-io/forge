# ADR 0004: Make Kata the sole owner of Kata UI

Date: 2026-08-08

## Status

Accepted

## Context

Kata now ships and owns its full browser application. Forge still carries the
older full Kata mode and copied task components, creating two implementations
of the same product experience. Forge nevertheless owns cross-product
workflows that Kata cannot replace: provider and workspace associations,
inline maintainer context, and Forge workspace lifecycle.

## Decision

Forge will remove its duplicate Kata mode and retain only the maintainer
workflows that cross the product boundary:

- associate one or more Kata issues with a provider issue, pull request, or
  Forge workspace;
- inspect a linked Kata issue inline through a read-only component owned by
  Kata;
- open that issue in Kata for editing; and
- create or reopen a Forge workspace from a Kata issue.

Forge must not keep a copied or independently reimplemented Kata issue UI. Kata
will publish the shared presentation package, and Kata's standalone web app and
Forge will consume the same component implementation.

## Goals

- Make Kata the only owner of Kata task presentation and editing behavior.
- Remove Forge's full `/kata` route, task browser, graph, recurrences, task
  mutations, snapshot/event UI, and copied Kata components.
- Preserve Kata-backed workspace creation and existing Kata workspace rows.
- Support multiple linked Kata issues per Forge subject.
- Keep all cross-product association metadata in Forge and support explicit
  links for GitHub, GitLab, Forgejo, and Gitea.
- Preserve stable provider identity across repository renames.
- Degrade per daemon or per task instead of blanking every linked task.

## Non-Goals

- Editing Kata tasks inside Forge.
- Embedding Kata's full standalone application in an iframe or webview.
- Recreating Kata graphs, recurrences, lists, filters, event history, or task
  mutation controls in Forge.
- Persisting Kata task bodies, comments, status, or snapshot cursors in Forge;
  retained Kata workspace identity/launch metadata may continue to carry title
  and reference hints captured when the workspace is created.
- Inferring associations from titles, body text, mutable repository names,
  URLs, or issue numbers without provider-verified stable identity.
- Automatically associating provider items with tasks independently imported
  or synchronized by Kata.
- Adding compatibility redirects, configuration aliases, or proxy fallbacks
  for the removed Kata mode.

## Product Ownership Boundary

### Kata-owned shared UI

The Kata repository publishes a versioned `@kenn-io/kata-ui` package. The
package exports:

- a read-only issue-detail component;
- the component's input and host-action types; and
- pure projection helpers needed to turn Kata's generated issue-detail wire
  response into the component model.

The component renders the issue title and body, status and properties,
checklist metadata, labels, links, parent and children, comments, and claim
state available from `GET /api/v1/issues/{uid}`. Graphs, recurrences, mutation
dialogs, list state, and event history are excluded from the Forge-facing
detail contract.

The package performs no network requests and owns no daemon selection, route,
workspace, or persistence state. It accepts neutral optional host actions so
Forge can supply `Open in Kata`, `Create workspace`, and `Open workspace`
without the package depending on Forge.

Kata's standalone web application must import its detail presentation through
this package boundary. A build or component test will guard against replacing
that import with a second local implementation. Forge pins an exact released
package version rather than a floating range.

The package also owns its supported Kata API-schema predicate. Its projection
helpers ignore additive unknown fields and safely default optional fields that
older supported daemons omit. Forge's daemon roster probe reads
`api_schema_version` from `GET /api/v1/health` before rendering detail. The
version is daemon-scoped because detail reads are pinned to that daemon. A
missing or unsupported version makes that daemon's tasks typed incompatible
instead of reaching the component, so mixed daemon versions degrade
independently.

### Kata-owned launch API

Kata will retain `GET /api/v1/issues/{uid}` as the canonical detail read and add
one narrow read API, `GET /api/v1/ui/launch-target`. The request carries
`issue_uid`; the response returns a validated canonical HTTP(S) browser URL for
the standalone Kata issue or an unavailable reason. It never returns a daemon
token, session credential, Unix-socket path, or URL containing userinfo. Kata's
normal standalone session/login flow remains responsible for authentication.

The operation enters Kata's generated OpenAPI contract and clients. Forge does
not read Kata's database or import tables directly. Kata persists no metadata
whose purpose is to associate provider items or Forge subjects with Kata tasks;
independent Kata import provenance remains a Kata implementation detail.

### Forge-owned integration

Forge continues to own:

- Kata daemon catalog discovery, health, selection, secure credential
  forwarding, and response redaction;
- exact Kata project-to-repository resolution;
- Forge workspace persistence, creation, setup, reuse, and launch;
- explicit association persistence and subject inheritance;
- stable provider-item identity lookup from Forge's synced provider data; and
- integration placement in pull request, issue, workspace, Settings, and New
  Workspace surfaces.

The browser never receives daemon credentials. Forge's browser API uses typed
server-side Kata client calls rather than exposing a generic proxy.

## Explicit Association Model

Forge adds a `kata_issue_links` table. Each row has exactly one Forge subject:

- provider pull request: stable Forge repository row plus the provider's stable
  item external ID;
- provider issue: stable Forge repository row plus the provider's stable item
  external ID; or
- workspace: durable Forge workspace ID.

Each row also records `daemon_id`, the last verified `project_uid`, `issue_uid`,
and creation timestamps. `issue_uid` is canonical within a daemon and remains
the lookup key if Kata moves the task to another project; a live detail response
supplies the current project UID for workspace resolution and deep links.

Provider link creation requires a nonblank provider item external ID. The API
returns a typed, actionable resync-required error when Forge has not backfilled
that identity, and the database constraint rejects blank or whitespace-only
values. Forge never falls back to an item number because doing so would collapse
unverified subjects under the partial unique index.

Partial unique indexes make link creation idempotent:

- provider subjects are unique by subject kind, repository row, provider item
  external ID, daemon ID, and issue UID;
- workspace subjects are unique by workspace ID, daemon ID, and issue UID.

The database check constraint requires exactly the columns appropriate for the
chosen subject kind. Provider routes resolve owner/name and item number to the
stable repository and item rows before reading or writing links. Repository
renames therefore do not orphan associations.

Forge persists no general task presentation cache. Retained
`WorkspaceKataMetadata` remains the identity and launch snapshot for existing
and newly created Kata workspaces, including its project name, title, and
short/qualified reference hints. Inline detail reads never refresh those hints.
An explicit link remains as an identifiable unavailable row if the daemon is
down or the task is deleted, so the user can still unlink it.

## Effective Association Resolution

For a provider issue or pull request, Forge returns explicit links stored for
that provider item. A task independently imported or synchronized by Kata is
not associated automatically; the user links it explicitly in Forge.

For a workspace, Forge returns the union of:

- the workspace's intrinsic Kata identity when `item_type == "kata_task"`;
- explicit links stored directly for the workspace;
- effective links of its owning provider issue or pull request; and
- effective links of its associated pull request, when present.

Results are deduplicated by daemon ID and issue UID. Intrinsic, direct explicit,
and inherited provenance remain distinguishable because a workspace can remove
only links stored directly on that workspace.

Live hydration runs with bounded parallelism and independent deadlines. A
failed daemon contributes a diagnostic while links on healthy daemons still
hydrate. The response distinguishes complete, partial, and unavailable state.
The resolver fetches a bounded live summary for each effective identity so the
list can show reference, title, and status without persisting them. Loading the
selected task's complete detail remains a separate request.

The selected task's detail is loaded independently through its pinned daemon.
Changing the ambient default daemon cannot redirect a detail load, link write,
workspace action, or launch action to another Kata instance.

## Forge API Surface

Forge exposes typed subject-specific operations rather than a polymorphic
browser payload that can invent subject identity:

- list effective Kata links for a pull request;
- list effective Kata links for a provider issue;
- list effective Kata links for a workspace;
- create and delete an explicit link on each supported subject;
- read one pinned linked issue detail;
- search task references on one pinned daemon;
- resolve safe standalone launch information; and
- create or reuse a Kata-backed workspace.

Explicit link creation accepts a canonical Kata task identity returned by
Forge's reference search. The server verifies the issue live against the pinned
daemon before inserting the row. Deletion removes only the direct explicit
provenance; an inherited or intrinsic identity remains in the effective result.

The retained project-mapping and workspace-creation endpoints continue to use
the existing exact repository resolver. The create action is omitted when no
unambiguous eligible repository exists. An existing workspace changes the
action to Open.

## Forge UI

### Linked detail tabs

Pull request and provider issue detail gain a `Kata` tab. Workspace detail uses
the same tab in its right sidebar. The tab is available in its empty state so a
user can create the first explicit link.

When links exist, the tab renders a compact list above the selected detail. A
row shows the canonical reference, title, status, daemon when disambiguation is
needed, and whether the link is direct, inherited, or intrinsic. Selection is
local to the subject and does not create a Forge route. Duplicate provenance
never creates a duplicate row.

The selected detail is rendered by `@kenn-io/kata-ui`. Forge supplies neutral
host actions:

- `Open in Kata` when a safe standalone launch target exists;
- `Create workspace` when the task resolves to an eligible repository and no
  workspace exists; or
- `Open workspace` when Forge already has the task workspace.

Forge owns the surrounding list, loading/error state, and link management.
`Unlink` appears only for a direct explicit link. An inherited or intrinsic row
remains until its owning workflow changes.

Each task has isolated loading and failure state. An explicit unavailable task
keeps its identity and unlink action. A hydration failure appears as a
partial-results warning and does not replace successfully loaded tasks.

The selected detail loads when the tab opens, exposes manual Refresh, and
refetches when the Kata tab or browser window regains focus after at least 15
seconds. It also polls every 30 seconds only while that tab is selected and the
document is visible; polling stops immediately when hidden, parked, or
unmounted. Effective association hydration refetches on tab/focus activation,
but the 30-second loop refreshes only the selected detail.

The existing responsive detail-tab behavior is reused on narrow layouts.
Responsive presentation must not introduce a new route family or alter subject
identity.

### Link workflow

`Link Kata issue` opens a task-reference search pinned to the chosen daemon.
The picker supports all configured healthy daemons and identifies the daemon in
its result. Selecting an existing effective task is idempotent. The dialog does
not expose task editing or general Kata browsing.

### New Workspace workflow

Forge's New Workspace experience gains a `Kata issue` source alongside ad-hoc
repository work. The user selects a daemon, searches task references, and
creates or opens the mapped workspace. This preserves the global “create
workspace from Kata issue” workflow without retaining a task browser.

The same workspace action is available from linked inline detail. Both entry
points call the same typed Forge endpoint and publish through the existing
workspace creation/launch state so remounts cannot duplicate creation or
launch.

### Settings and cross-product links

Kata daemon health and project-to-repository mapping remain in Settings under a
Kata integration section. They are no longer presented as mode visibility.

Docs task references and every `Open in Kata` action use the safe standalone
launch target. Forge's global palette no longer searches Kata tasks because
there is no Forge-owned task route; task search remains scoped to linking and
workspace creation.

This intentionally changes Docs references from opening Forge-owned inline
detail to opening Kata's standalone issue route. Adding the shared read-only
component to Docs may be evaluated later, but it is not part of this removal.

## Removal Scope

Forge removes:

- the `/kata` route, route state, header navigation, mode palette entry, and
  `modes.kata` setting;
- the full Kata feature workspace, list/sidebar, graph, recurrence, filter,
  mutation, and event-stream controllers;
- copied Kata detail, discussion, properties, checklist, action, and overflow
  components;
- frontend task mutation clients, graph/view builders, full snapshot
  projection, event stream, and Kata browser persistence;
- the generic `/kata/proxy` browser route;
- the full task snapshot and task event-stream server routes, caches,
  coordinators, loaders, and enrichment pipeline; and
- docs, screenshots, tests, fixtures, and generated contracts that describe
  Forge's Kata mode.

Forge retains or replaces with narrow equivalents:

- Kata catalog/runtime discovery and secure daemon transports;
- daemon roster and health;
- task-reference search;
- pinned issue-detail reads;
- explicit and intrinsic association resolution;
- project mapping diagnostics;
- Kata workspace creation/reuse;
- Kata workspace metadata and agent context; and
- safe standalone Kata launch resolution.

Existing Kata-backed workspace rows, item keys, branches, worktrees, runtime
sessions, and metadata remain valid. No migration rewrites them into ad-hoc or
provider workspaces.

`modes.kata` and `/kata` are removed directly. The implementation must not add
a legacy config alias, route redirect, fallback wrapper, or translation shim.

## Error Handling

- Link creation is idempotent and returns the effective association after the
  uniqueness constraint settles concurrent requests.
- A live validation failure prevents inserting an explicit link.
- A deleted or unreachable task retains explicit identity and can be unlinked.
- Hydration errors are per task and produce partial results.
- Forge uses Kata health reachability and authentication state to decide
  availability. `api_schema_version` remains diagnostic metadata and does not
  gate Kata-backed workflows.
- Detail, workspace, and launch operations are pinned to the task's daemon.
- No safe HTTP(S) browser origin means `Open in Kata` is unavailable with an
  explanation; Forge never falls back to a Unix-socket URL or configured URL
  containing credentials.
- Workspace mapping ambiguity or absence omits the create action and reports
  the existing resolver's diagnostic in the surrounding Forge panel.
- A detail component failure cannot remove the task list or unlink controls.

## Delivery Order

1. Kata releases `@kenn-io/kata-ui` and changes its standalone web app to
   consume the shared issue-detail export.
2. Kata adds the generated safe launch-target API with authorization coverage.
3. Forge updates its Go Kata dependency/client and pins the exact shared UI
   package release.
4. Forge adds explicit association storage, effective-link APIs, inline tabs,
   linking, and the New Workspace Kata source.
5. Forge removes the full Kata route, UI, browser proxy, snapshot/event server,
   generated contracts, and obsolete documentation.

Step 4 must land before step 5 because the existing `/kata` feature is Forge's
only create-from-task entry point; the current New Workspace dialog has no Kata
source.

The Forge integration must not merge with copied temporary components while
waiting for the Kata package. The Kata release is a prerequisite, not a later
cleanup.

## Verification

### Kata

- Component tests cover read-only issue fields, comments, links, claim state,
  neutral host actions, and the absence of edit controls when callbacks are not
  supplied.
- Package tests cover supported and incompatible daemon schema versions plus
  tolerant projection of additive and missing optional detail fields.
- A package-boundary test proves Kata's standalone app consumes the exported
  detail component.
- API tests cover health schema-version advertisement, launch-target
  authorization, and safe launch targets.
- Generated API drift, Svelte checks, browser tests, production build, and Go
  tests pass in Kata.

### Forge backend

- Migration and query tests cover subject constraints, partial uniqueness,
  idempotent concurrent creation, deletion, and repository rename survival.
- Resolution tests cover intrinsic workspace tasks, direct workspace links,
  owner-item inheritance, associated pull request inheritance, and provenance
  deduplication without consulting Kata import mappings.
- API tests cover all four provider routes for explicit links, multi-daemon
  hydration, partial failure, deleted tasks, pinned detail, blank provider
  external IDs, mixed compatible/incompatible daemon versions, safe launch
  URLs, and workspace create/open behavior.
- Generated API artifacts are regenerated and drift checks pass.

### Forge frontend

- Svelte unit and browser tests cover empty, single, multiple, duplicate
  provenance, loading, partial-error, unavailable explicit task, unlink,
  incompatible daemon, visible-only polling, focus refresh, manual refresh,
  create/open workspace, and narrow-layout states.
- New Workspace tests cover daemon selection, reference search, mapping absence,
  creation reuse, and launch state.
- Full Vitest runs after the final frontend or test edit.
- The full affected Playwright suite runs after the final Playwright fixture or
  spec edit.
- Production frontend and Go builds pass.
- Non-mutating lint passes after the final relevant edit.

### Removal guards

Tests or static checks assert that Forge no longer ships:

- `/kata` route parsing or rendering;
- `modes.kata` generated settings;
- the generic Kata browser proxy;
- task snapshot or event-stream routes; or
- copied full Kata feature/component modules.

User documentation describes only linking, read-only inline detail, opening the
standalone Kata UI, Kata project mappings, and Kata-backed workspaces.
