# Effect Frontend Async Design

Date: 2026-08-02

## Goal

Replace Kenn Forge's bespoke frontend async orchestration with Effect. The
migration covers requests, polling, retries, ordered writes, SSE, NDJSON,
WebSockets, xterm, clipboard access, observers, timers, storage, and
third-party callbacks.

The result should make async behavior easier to read and extend. Ordering,
cancellation, retries, errors, and cleanup must be explicit. User-visible
behavior stays the same unless the old behavior came only from accidental
timing.

## Scope

The production audit and migration cover:

- `frontend/src`;
- the code currently under `packages/ui/src`, which will move into the
  frontend; and
- the async fetch and countdown in `packages/github-app-ui/src`.

The GitHub App setup page remains an independent embedded build artifact. It
is not a second application boundary inside the main Svelte app. Its async work
will still move to a scoped Effect program at its own entrypoint.

The work does not change backend API behavior or add new product features. API
changes are allowed only if migration exposes an existing contract defect that
cannot be fixed correctly in the client.

## Source layout

`@kenn-forge/ui` has one production consumer: `frontend`. It is not a reusable
application boundary. Move `packages/ui/src` into `frontend/src/lib` and update
the build, test, alias, formatting, and code-generation paths. Delete the
workspace package after all imports point at the frontend tree.

Keep files grouped by domain rather than creating a second generic Effect
application tree:

- provider workflows: pulls, issues, detail, diff, activity, sync;
- Kata;
- Docs and repository browser;
- Roborev;
- terminal and workspaces; and
- application services such as startup, settings, transport, and browser
  integrations.

Effect programs should live beside the domain that owns them. Shared modules
are limited to real cross-domain services and policies. Svelte rune state stays
with the feature that renders it.

`Provider.svelte`, `ForgeClient`, the package barrels, and the package-level
context indirection are removed as their consumers migrate. Do not replace
them with a compatibility wrapper.

## Effect dependencies and source reference

Use `effect@beta`. Add aligned `@effect/*` packages only when the browser and
test runtimes need them. The expected initial set is:

- `effect@beta`;
- the aligned browser platform package; and
- the aligned Vitest integration.

`bun run effect:prepare` provides the pinned local Effect source checkout used
for implementation research. Local skill guides remain the first reference;
the pinned source resolves API details not covered by those guides.

## Runtime and layers

The main frontend has one `ManagedRuntime`. `frontend/src/main.ts` builds it
from one live application layer and passes its execution interface to
`App.svelte`. `App.svelte` installs that interface through typed Svelte context
once. Features read it from context without prop drilling. Root teardown and
test unmount dispose the runtime.

There are no per-store, per-route, or per-feature managed runtimes. Route and
feature lifetimes are child scopes or owned fibers inside the application
runtime.

The GitHub App setup document does not share the main app runtime because it is
a separate HTML artifact served by a short-lived loopback flow. Its entrypoint
runs one scoped Effect program and interrupts that program on teardown.

Layers provide real boundaries:

- generated OpenAPI client transport;
- browser storage and clipboard;
- EventSource, streaming fetch, and WebSocket construction;
- observer and terminal construction where a third-party API must be wrapped;
  and
- live implementations of application services.

Tests replace these boundaries with layers. Do not create services that merely
rename a single pure function or forward one method. Provide layers at runtime
and test boundaries instead of calling `Effect.provide` throughout business
logic.

Reusable operations use `Effect.fn`. Named operations carry spans and useful
context. Normal interruption does not produce error logs.

Migrated TypeScript uses no `any`, `as` casts, namespaces, or unsafe type
assertions. Unknown external values are decoded or narrowed at their boundary.
When a type becomes difficult to express without an escape hatch, simplify the
interface or add a properly typed helper instead.

## Generated OpenAPI types

The generated TypeScript client and schema remain the authority for HTTP wire
contracts. Move them into the frontend tree and update `make api-generate` to
write to the new location.

Use generated `components`, `operations`, and `paths` types directly for HTTP
requests, responses, settings, and problem bodies. Readable aliases may point
to generated types, as the current `api/types.ts` does. They must not copy the
same structure into handwritten interfaces.

Effect wraps generated-client execution and classifies transport or API
failures. It does not introduce a parallel HTTP model. Runtime schemas are for
boundaries the OpenAPI output does not validate or describe, including SSE,
NDJSON, WebSocket messages, browser storage, and third-party callback data. If
a runtime decoder covers an HTTP edge case, its output must remain assignable
to the generated wire type rather than becoming a competing exported model.

## Data flow

Every migrated feature follows one flow:

```text
user or browser event
  -> Effect command or Stream
  -> typed service result or domain event
  -> feature presentation controller
  -> Svelte rune state
  -> rendered UI
```

Svelte runes own presentation state and derived values. They do not own retry
counters, polling loops, abort controllers, ordering flags, sockets, timers,
or cleanup registries.

Feature controllers consume Effect results or streams and update rune state.
Large API snapshots that are replaced as a unit should use `$state.raw`.
Ordinary synchronous DOM interaction stays in Svelte. An interaction that
starts async work launches an Effect program through the runtime context.

## Concurrency and ordering

Async behavior is classified by policy. There is no general-purpose custom
concurrency helper.

| Behavior                                                    | Policy                                                                            | Effect tools                                 |
| ----------------------------------------------------------- | --------------------------------------------------------------------------------- | -------------------------------------------- |
| Route changes, searches, and detail selection               | Latest request wins; interrupt the previous request                               | `FiberHandle`                                |
| Independent keyed loads                                     | Latest request per stable domain key                                              | `FiberMap`                                   |
| Comments, settings, optimistic mutations, and ordered saves | Process commands in submission order                                              | `Queue` and a scoped consumer                |
| Duplicate refresh or startup demand                         | Share one in-flight operation; retain results only under an explicit cache policy | `Cache` with explicit expiry or invalidation |
| Independent bulk reads                                      | Run in parallel up to an explicit bound                                           | semaphore                                    |
| Polling and reconnect                                       | Run one iteration at a time with declared cadence and backoff                     | `Stream` and `Schedule`                      |
| Browser and third-party callbacks                           | Publish into a typed queue or stream                                              | `Queue` and `Stream`                         |

Keys for provider-owned work always include the full provider-aware identity:
provider, platform host, owner, repository, and item number where applicable.
Kata and Docs keep their own established identities and are not forced through
provider abstractions.

`Ref` holds sequence numbers, checkpoints, and desired state when a stale
result needs an explicit guard. An optimistic rollback applies only if the
failed command still owns the current version. An older failure cannot undo a
newer success.

Poll loops start the next iteration only after the current one completes.
There are no overlapping `setInterval` callbacks. Background fibers must be
owned by a scope, `FiberHandle`, or `FiberMap`; detached `void` promises and
unowned callback retry loops are not allowed.

## Errors and recovery

Expected failures use the typed error channel. Schema-shaped errors should use
`Schema.TaggedErrorClass`; local non-serializable failures may use
`Data.TaggedError`.

The main failure families are:

- transport and connectivity failures;
- invalid external payloads;
- generated API problems with stable `code` and typed `details`;
- browser capability or permission failures;
- stream closure and protocol failures; and
- domain conflicts such as stale state.

Foreign exceptions from browser and third-party APIs are wrapped at the
boundary and retain their cause for diagnostics. Components may display the
server's human-readable detail, but all behavior branches on generated problem
codes and details.

Recovery rules are explicit:

- retry only idempotent operations whose typed failure is classified as
  transient;
- use shared bounded schedules with jitter for transient retry;
- handle `rateLimited.retryAfter` as a rate-limit gate, not generic retry;
- do not automatically retry a mutation unless its contract proves the replay
  safe;
- refresh and require review after a stale conflict instead of replaying the
  mutation;
- present validation, permission, unsupported capability, and not-found
  failures immediately; and
- treat interruption as cancellation, not a banner-worthy failure.

Defects represent bugs or broken invariants. Log them with operation context
at the application boundary and show a safe generic failure. Do not convert
expected domain failures into defects to simplify types.

## Resource ownership

| Resource                                          | Owner                    |
| ------------------------------------------------- | ------------------------ |
| Main Effect runtime and application services      | root application scope   |
| Polling, SSE, reconnect loops, and feature queues | route or feature scope   |
| Workspace WebSocket and terminal session          | workspace scope          |
| xterm, observers, and DOM-bound libraries         | element attachment scope |
| Requests, delays, and retries                     | operation fiber          |
| GitHub App setup fetch and countdown              | setup document program   |

Use `Effect.acquireRelease`, scoped layers, and finalizers. Interrupting an
owner releases every child resource, including pre-header streaming fetches,
active readers, sockets, observers, timers, queues, and third-party instances.

`{@attach}` bridges DOM-node lifetime to Effect for xterm and observers. The
attachment starts one scoped resource and returns a synchronous cancellation
function. `onDestroy` may interrupt one owning feature scope. It must not hold
a list of bespoke cleanup calls.

Do not use Svelte `$effect` for fetching, retries, polling, ordering, or
resource management. It remains available for presentation-only integration
where Svelte has no more direct primitive. Prefer `$derived`, event handlers,
typed context, `createSubscriber`, and attachments according to Svelte's own
ownership model.

## Direct cutover

Migrate one subsystem at a time. For each subsystem:

1. Identify the meaningful observable behavior and existing coverage.
2. Add or adjust a focused test for the policy being changed.
3. Implement the Effect program and boundary adapters.
4. Switch every caller in that subsystem.
5. Delete the replaced promise chains, callback orchestration, timers, abort
   plumbing, and cleanup flags in the same change.
6. Run the affected checks before moving to the next subsystem.

Untouched subsystems may keep their existing implementation until their
cutover. Do not add a Promise compatibility API, dual execution path, fallback
store, or legacy alias between the two models.

The final audit classifies every remaining production use of `Promise`,
`async`, timers, animation callbacks, `fetch`, EventSource, WebSocket,
observers, and third-party event registration. Allowed exceptions are narrow:

- generated client internals called only through the Effect transport;
- dynamic imports required for code splitting;
- Svelte or browser callbacks that form the boundary of an owned attachment;
- synchronous Svelte event handlers; and
- presentation-only Svelte scheduling such as `tick` where no application
  orchestration is involved.

Everything else moves behind an Effect program or adapter. The audit is review
evidence, not a test that forbids deleted names forever.

Adding later async behavior should require choosing one of the documented
concurrency policies, writing an Effect program against existing services, and
projecting its result into feature-local rune state. It must not require a new
runtime, global store, retry helper, or cleanup registry.

## Four-PR stack

Deliver the work as one linear stack with four PRs. The current branch is the
bottom branch.

### PR 1: one frontend tree and Effect foundation

- preserve the existing pinned Effect source setup;
- move `packages/ui/src` into `frontend/src/lib` and remove the workspace
  package boundary;
- relocate generated clients and update generation/build/test paths;
- install aligned Effect packages;
- add the main runtime, typed context, live/test layers, transport, shared
  errors, schedules, and browser adapters; and
- migrate the GitHub App setup fetch and countdown.

This PR may move the existing `Provider.svelte` into the frontend temporarily.
It does not add a compatibility adapter. The provider disappears when its
stores cut over in PR 2.

### PR 2: shared application and provider workflows

- startup and backend readiness;
- settings and persistence;
- pulls, issues, detail, diff, activity, and sync;
- provider event streams and optimistic mutations; and
- removal of `Provider.svelte`, `ForgeClient`, and their old store wiring.

### PR 3: feature streams

- Kata snapshot authority and SSE;
- Roborev NDJSON, polling, and mutations;
- Docs workflows; and
- repository-browser workflows.

### PR 4: terminal resources and final cleanup

- terminal and workspace orchestration;
- xterm, WebSocket, clipboard, resize, and observer lifetimes;
- final raw-async inventory and removal of obsolete helpers; and
- top-of-stack production build and full affected verification.

Use `gh stack` non-interactively. Adopt the current branch as the bottom,
create each later layer with an explicit descriptive branch name, and use
ordinary staged Git commits. Submit draft PRs with `gh stack submit --auto`,
replace the generated descriptions immediately with prepared prose, and inspect
the result with `gh stack view --json`.

When a lower layer needs a fix, change that branch and rebase the branches above
it. Do not hide a lower-layer dependency in a later PR.

## Kata tracking and identifier privacy

After the implementation plan is approved, search Kata before creating work.
Use one parent task for the migration and one child for each PR. The child
relationships mirror the stack order. Close each child when its PR slice is
verified, with its commit as evidence; close the parent only after every child
is complete.

Kata identifiers are internal. Keep them in Kata and local execution notes
only. They must never appear in:

- branch names;
- commit subjects or bodies;
- PR titles or descriptions; or
- GitHub comments.

## PR descriptions

PR descriptions follow the repository's short format: a small plain-language
bullet list of what changed. They contain no test plan, implementation detail,
checklist, task identifier, or marketing language.

Before publishing each description, use the `unslop` skill with the crisp
preset:

1. Extract facts and technical terms that must survive.
2. Scan the draft for banned phrases and structural patterns.
3. Rewrite it in short, direct language.
4. Validate fact preservation.
5. Rerun the phrase scan and readability metrics.
6. Score it against the rubric and require at least 32/40.

## Testing

Each subsystem cutover follows TDD. Tests prove Kenn Forge's policy and
integration boundaries, not Effect, Svelte, the generated client, or browser
library behavior.

### Effect tests

Use `@effect/vitest` as the normal test API:

- `it.effect` for Effect programs;
- `layer(...)` only when a service instance is intentionally shared across a
  suite;
- separate one-test `it.layer(...)` blocks when a stateful service instance
  must be rebuilt for every test;
- `TestClock` for polling, retry, reconnect, debounce, and countdown timing;
- controlled transport and browser layers rather than global monkey patches;
  and
- scoped tests for finalizers and interruption.

Protect the policies that caused the current brittleness:

- latest selection wins and stale results do not commit;
- keyed work uses the full provider-aware identity;
- ordered commands execute in submission order;
- optimistic rollback cannot overwrite newer success;
- duplicate demand shares one operation;
- only classified transient failures retry;
- interruption does not retry or display an error;
- reconnect resumes from the correct checkpoint; and
- every owned resource releases on success, failure, and interruption.

Do not test that Effect queues, schedules, or scopes behave as documented.
Assert the application result that depends on the selected policy. Avoid real
sleeps and flaky-test retries for deterministic concurrency behavior.

### Svelte and browser tests

Component and app-harness tests cover rune projections, loading and error
states, routing, action availability, and the command launched by a user
interaction.

Use Vitest browser tests when native focus, keyboard behavior, storage,
clipboard, matchMedia, computed style, or another real browser primitive is
the owner. Use Playwright only for workflows that need browser navigation,
canvas/xterm, geometry, WebSockets, tmux, screenshots, or a real server
boundary. Do not duplicate a backend/server contract in Playwright when a
component or browser test already proves its presentation.

Add an Effect test with `TestClock` for the GitHub App setup countdown and its
cleanup policy. Keep the existing Playwright coverage for the visible
countdown, automatic submission, and stale-link behavior without exposing a
test clock through the production page.

### Verification by stack layer

Every PR runs:

- the complete Vitest suite;
- frontend formatting, lint, type, and Svelte checks;
- its affected Vitest browser projects; and
- its affected Playwright suites before push.

PR 1 also runs the GitHub App setup Playwright suite and verifies that
`make api-generate` writes the relocated generated files without an unintended
wire diff.

The top PR additionally runs:

- a fresh production frontend build before full-stack tests;
- all affected mock and full-stack Playwright suites in their configured
  browser projects;
- the full Go suite; and
- the final raw-async inventory.

## Completion criteria

The migration is complete when:

- `packages/ui` no longer exists as a workspace package;
- the main frontend has exactly one managed runtime;
- the GitHub App setup page's async work is scoped Effect code;
- generated OpenAPI TypeScript types remain the HTTP wire authority;
- every production async path is either Effect-owned or one of the explicit
  boundary exceptions;
- no feature owns bespoke retry, ordering, cancellation, or cleanup machinery;
- all old compatibility-free cutovers have removed their replaced code;
- observable ordering and recovery behavior is covered at the narrowest useful
  test boundary; and
- every stack layer passes its required verification.
