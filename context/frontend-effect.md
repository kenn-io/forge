# Frontend Effect

Use this document for frontend async ownership, Effect workflows, services,
layers, schemas, typed errors, resource lifetimes, and Effect-specific tooling.
Interaction semantics remain in [`ui-interaction-contracts.md`](./ui-interaction-contracts.md),
and API error contracts remain in [`error-handling.md`](./error-handling.md).

## Installed References

- Read the installed packages the frontend compiles against before assuming an API
  from a newer Effect release: `frontend/node_modules/effect/` (its `README.md`,
  `AGENTS.md`, and `src/`), `@effect/platform-browser`, `@effect/vitest`, and
  `@effect/language-service` (README and `schema.json`).
- The language-service README recommends `@effect/tsgo` for TypeScript 7, but
  kenn-forge uses TypeScript 5.9, so the installed language service is the supported tool.

## Application Ownership

- The browser app owns one `ManagedRuntime`, supplied through Svelte context
  (`frontend/src/lib/app/runtime.ts::makeAppRuntime`). Never create feature or
  component runtimes or detach async work from its scope.
- Promise-required library callbacks may observe `AppExecution.exit`, but the command
  still starts through `runCommand`, never a second runtime or detached owner.
- The standalone GitHub App setup entrypoint owns one scoped Effect program, not
  another managed SPA runtime (`packages/github-app-ui/src/setup-program.ts`).
- Effect root lifetimes survive back-forward cache restores: the SPA lives for the
  JavaScript realm, and standalone entrypoints ignore persisted page hides for teardown.
- Accepted work belongs to an application-scoped workflow or service when it must
  survive route changes or component teardown. Components project state, capture
  intent, and start synchronous commands; they own no durable retry, ordering,
  uncertainty, or deadline state.
- Component lifetime owns polling and live-event subscriptions; teardown interrupts
  that owner. Accepted queued mutations stay application-owned, and post-teardown hub
  demand stays pending for a remounted owner.
- App-wide health polling belongs to the root runtime lifetime so every route
  observes daemon availability (`frontend/src/App.svelte::roborevPollingExecution`).
- Application ownership lasts for the browser runtime unless explicitly persisted.
  Reload may discard presentation and deferred launch intent, but persisted server
  authority must remain discoverable afterward.
- Settings page reads follow previously accepted saves but stay page-owned; leaving
  Settings cancels reads without cancelling saves or blocking later writes.
- Cancel a form's wait for accepted work when its host changes, even if the user
  returns; the application queue still owns the accepted mutation.
- Use scoped acquisition and finalizers for listeners, streams, readers, abort
  controllers, timers, presenters, and workflow owners; tear down at the same lifetime
  boundary that acquired the resource.
- Publish an owner registry entry and install its finalizer in one uninterruptible
  handoff. After an ordered queue admits a non-idempotent command, pending-state
  publication and executor release are likewise one uninterruptible handoff.
- Shared diff workers start on first diff demand and live until app disposal; mounting
  the app or disposing one diff never creates or terminates the pool.
- Diff context prefetch is app-scoped, foreground-prioritized, and concurrency-bounded;
  generation cancellation must not release an active slot until its shared read settles.

## Concurrency Contracts

- Use Effect concurrency, queues, fibers, schedules, and interruption instead of
  bespoke Promise generations, overlapping timers, or boolean race guards. Latest-wins,
  single-flight, ordered, and lossless are different contracts; preserve each explicitly.
- Provider list, activity, and sync controllers expose synchronous launchers; their
  Effect workflows own cancellation, shared demand, bounded reads, and cadence.
- Repeated reads of the same PR or issue list query share the pending request; only a
  changed query replaces it, so polling cannot starve a slow response.
- Detail and diff selection reads are latest-wins; identical full-provider keys share
  only active reads, and selection changes interrupt the transport
  (`frontend/src/lib/effect/latest-shared-read.ts::makeLatestSharedRead`).
- Fleet snapshot consumers share pending reads; completed results do not suppress later
  refreshes, and releasing one consumer does not cancel another.
- Navigation retains successful previews for explicit commit/range scopes; head and
  workspace previews follow their mutable refresh generations.
- Timer polls use the shared visibility-aware helper: hidden documents stop polling and
  refresh at once when shown; event-driven refreshes ignore visibility
  (`frontend/src/lib/effect/poll-while-visible.ts::pollWhileVisible`).
- Callback streams use explicit bounded buffers: pullable producers suspend for
  backpressure; callback-only sources fail with a typed transient overflow so
  reconnect/replay recovers.
- Provider live updates checkpoint only after their handler succeeds and keep the cursor
  across owner handoffs; transient failures reconnect visibly under capped backoff,
  decode failures stop visibly until an explicit reconnect.
- Provider mutations share one app-scoped acknowledged queue: order by submission, key
  rollback by full item identity plus mutation family, never retry writes, and turn
  `stale_state` into refresh-and-review without replay
  (`frontend/src/lib/stores/ordered-mutations.ts::ProviderMutations`).
- Run Effects only at host boundaries through the application runtime. Reusable
  business operations return `Effect` and normally use `Effect.fn`; inline
  orchestration may use `Effect.gen`.

## Workflow Actions

- Workflow Actions is a plain app-scoped store that reads on demand and applies server
  events; it owns no polling loops, queues, or reconciliation. Dispatch follow-through
  lives on the server (`internal/server/workflowapi/dispatch_follow.go`).
- Workflow caches and progress key by provider, host, and permanent repository ID;
  renames keep successful state, while a replacement route occupant gets separate state.
- Relay workflow events refresh only the mounted Actions page's selected repository and
  workflow; add no hidden-page fetches or second batching timer.
- Reads are latest-wins per repository; a stale catalog or run response never replaces
  newer data.
- One dispatch cycle exists per workflow. Success, rejection, uncertainty, and
  definition conflict leave presentation only through the explicit new-cycle command;
  retry returns to fresh confirmation and never replays the POST.
- Closing a workflow dialog preserves its dispatch and reload outcome.
- Definition-reload failures are cycle state separate from general read errors; a
  successful reload clears that workflow's cycle.

## Errors And External Data

- Expected failures stay typed in the Effect error channel. Wrap browser, transport,
  and library failures in the existing tagged domain errors, never `unknown`, global
  `Error`, or thrown exceptions.
- Decode untrusted event, storage, and non-generated payloads with Schema. Generated
  OpenAPI shapes import the Orval models (`frontend/src/lib/api/generated/models/`,
  `frontend/src/lib/api/types.ts`); never duplicate them or add runtime guards around them.
- Build API paths, including raw `orvalRequest`, EventSource, and Roborev calls, with
  the generated Orval `get*Url` helpers; only endpoints outside OpenAPI, such as
  `/ws/v1` terminals, are hand-built.
- A non-idempotent transport failure may be uncertain: keep a fence and read fresh
  authority before retrying. Definite rejection, uncertain outcome, and acknowledged
  success followed by refresh failure need different UI state.
- New settings fields must participate in save-recovery comparisons; reading unchanged
  settings cannot confirm a requested change took effect
  (`frontend/src/lib/stores/settings-workflow.ts::settingsMatchRequest`).
- Presentation callbacks must not change command acknowledgement. Publish
  identity-scoped authoritative outcomes before component-liveness checks, and gate
  only local presentation on the current view.

## Svelte Boundary

- Effect services and state machines that own application work live in `.ts` or
  `.svelte.ts` modules; Svelte components derive visible state and delegate commands.
- When a Svelte `$effect` starts a runtime command, wrap the `runCommand` call itself
  in `untrack`; fibers begin synchronously, so untracking only program construction
  still subscribes the Svelte effect to internal rune reads.
- An execution from `runCommand` is component-owned only when its work should stop
  with that component; application-owned accepted work is never returned as a
  component cleanup callback.

## Tooling

- `frontend/tsconfig.json` loads `@effect/language-service` for editors; use the
  workspace TypeScript version.
- The normal frontend check runs the non-mutating diagnostics CLI:
  `node node_modules/@effect/language-service/cli.js diagnostics --project tsconfig.json --format text --severity error`
  (from `frontend/`).
- Never run `effect-language-service patch`; mutating installed compiler files is not
  a reproducible check. Warnings and suggestions stay editor guidance.
- A local diagnostic override is acceptable only for a demonstrated tooling limitation
  and must explain why TypeScript still preserves the error and requirements channels.
- For every frontend Effect change, run the language-service diagnostics, affected
  Effect/Vitest tests, `svelte-check`, and the frontend lint.
