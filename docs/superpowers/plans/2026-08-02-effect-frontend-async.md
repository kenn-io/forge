# Effect Frontend Async Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every production frontend async orchestration path with Effect while preserving user-visible behavior and making cancellation, ordering, retries, streams, and cleanup explicit.

**Approved spec/design:** `docs/superpowers/specs/2026-08-02-effect-frontend-async-design.md`

**Architecture:** Collapse `packages/ui/src` into `frontend/src/lib`, then build one browser `ManagedRuntime` whose layer owns generated-client transport, browser boundaries, and domain workflows. Effect programs own work and resources; feature-local Svelte rune controllers only project results into render state. The GitHub App setup artifact keeps its independent entrypoint but runs one scoped Effect program.

**Tech Stack:** Svelte 5.56, TypeScript 5.9, Effect `4.0.0-beta.102` with aligned `@effect/platform-browser` and `@effect/vitest`, generated OpenAPI TypeScript types, Vite+, Vitest, Vitest Browser, Playwright, Bun, Kata, and `gh stack`.

## Global Constraints

- Use the pinned Effect source prepared by `bun run effect:prepare`; verify `.repos/effect` exists and install the exact package version declared by that checkout before Effect work.
- The main SPA has exactly one `ManagedRuntime`, created in `frontend/src/main.ts`; no store, route, or feature may create another.
- `App.svelte` installs only a typed execution interface in Svelte context. Feature lifetimes are scopes or owned fibers in that runtime.
- The GitHub App setup artifact runs one scoped Effect program at its own entrypoint; it is not a second runtime inside the main SPA.
- Keep generated `components`, `operations`, and `paths` authoritative for HTTP wire types. Do not copy generated request, response, settings, or problem shapes into handwritten interfaces.
- Use `FiberHandle` for latest-wins, `FiberMap` for latest-per-key, acknowledged `Queue` consumers for ordered writes, `Cache` for explicitly bounded in-flight sharing, semaphores for bounded parallel reads, and `Stream` plus `Schedule` for polling and reconnect.
- Every ordered queue entry carries a `Deferred<Exit<...>>`; the consumer records each command exit, completes that acknowledgement, and continues after failures. Use capacity 64 with producer backpressure unless a task names a smaller bound. Scope shutdown interrupts the active command and completes every pending acknowledgement with `CommandQueueClosed` so callers never hang.
- Every `Cache` declares capacity, time-to-live, and invalidation. Use capacity 64 and a two-second TTL for ordinary in-flight read sharing; exceptions such as the one-entry startup cache and retained repository previews are stated in their owning tasks.
- Reconnect streams retry transient failures indefinitely with exponential delay capped at 30 seconds and project a visible reconnecting state. Non-transient decode/capability failures end the stream in a typed disconnected state and require explicit refresh; no finite retry exhaustion disappears silently.
- Provider work keys always include `provider`, `platformHost`, `owner`, `name`, and the item number when applicable.
- Retry only typed transient failures from idempotent work. Treat `rateLimited.details.retryAfter` as a gate. Never blindly retry mutations.
- Use typed failures for expected outcomes, interruption for cancellation, and defects only for broken invariants.
- Use `Effect.acquireRelease`, scoped layers, and finalizers for EventSource, streaming fetch, readers, WebSockets, xterm, observers, timers, queues, and third-party registrations.
- Migrated production TypeScript uses no `any`, `as` casts, namespaces, unsafe assertions, detached `void` promises, or unowned callback loops.
- Keep ordinary synchronous Svelte events in Svelte. Launch async work through the runtime context. Prefer `$state.raw` for replaced API snapshots, `$derived` for derived state, `createContext`, and `{@attach}` for DOM-owned resources.
- Cut over each subsystem directly. Delete replaced Promise chains, timers, abort plumbing, flags, aliases, and cleanup registries in the same task. Add no Promise compatibility API, dual path, fallback store, package alias, or legacy wrapper.
- Tests cover Kenn Forge policy and owned seams, not Effect, browser, Svelte, or generated-client library behavior. Stateful layers use separate one-test `it.layer(...)` blocks.
- Before editing any `.svelte` or `.svelte.ts` file, use the Svelte skills; before finalizing each file, run `vp exec -- svelte-mcp svelte-autofixer FILE --svelte-version 5` with `FILE` replaced by that exact repo-relative filename.
- Never use npm, `--no-verify`, `git commit --amend`, or task identifiers in branch names, commit text, PR text, or GitHub comments.
- Run migration Kata commands with `--workspace /tmp --project forge`; this selects the same ledger without allowing the Kata CLI to rewrite the repository's tracked project locator in `.kata.toml`.
- Before every commit: run the task's tests, invoke `context-sync --commit`, invoke the mandatory commit skill, and scan staged content plus the complete proposed commit message for every migration Kata identifier.
- Before every PR: run the full affected frontend suite, capture a visible UI artifact when the PR changes visible UI, scrub private data, run the description through `unslop` with the crisp preset, and verify no Kata identifier appears.

---

## File and Interface Map

The source collapse preserves the suffix after `packages/ui/src/` under `frontend/src/lib/`. For example, `packages/ui/src/stores/pulls.svelte.ts` becomes `frontend/src/lib/stores/pulls.svelte.ts`. Relative imports within the moved tree remain relative. Imports from the existing frontend become direct relative imports; the `@kenn-forge/ui` package name and its barrel are not retained as aliases.

The only path collision is `components/detail/LabelPicker.test.ts`; merge both suites into `frontend/src/lib/components/detail/LabelPicker.test.ts` before removing the old tree.

New cross-domain files have one responsibility each:

- `frontend/src/lib/app/runtime.ts`: `AppRuntime`, `AppServices`, and construction of the single managed runtime.
- `frontend/src/lib/app/runtime-context.ts`: typed Svelte `createContext<AppRuntime>()` pair.
- `frontend/src/lib/app/mount.ts`: one scoped root mount program that unmounts Svelte and then completes managed-runtime disposal.
- `frontend/src/lib/app/layer.ts`: final live layer assembly only.
- `frontend/src/lib/api/generated-api.ts`: generated OpenAPI client service and typed request execution boundary.
- `frontend/src/lib/api/effect-errors.ts`: shared typed transport, problem, payload, capability, and stream failures.
- `frontend/src/lib/api/retry-policy.ts`: bounded transient retry schedule and classification entrypoint; no mutation retry.
- `frontend/src/lib/effect/ordered-command-queue.ts`: bounded ordered entries, per-command acknowledgements, failure isolation, and shutdown completion.
- `frontend/src/lib/browser/storage.ts`: separately tagged local and session storage services with decoded values.
- `frontend/src/lib/browser/event-source.ts`: acquired EventSource construction and event stream.
- `frontend/src/lib/browser/streaming-fetch.ts`: abortable pre-header fetch, reader ownership, and byte stream.
- `frontend/src/lib/browser/web-socket.ts`: acquired WebSocket construction using the Effect browser socket constructor.
- `frontend/src/lib/browser/observers.ts`: acquired ResizeObserver, MutationObserver, and IntersectionObserver adapters.
- `frontend/src/lib/testing/effect-layers.ts`: reusable controlled boundary layers only when at least two tests use them.
- `packages/github-app-ui/src/setup-program.ts`: decoded flow load, TestClock-driven countdown, and form-submit command.

Domain programs stay beside their owners. Existing store filenames remain so rendering imports stay stable, but each async store gains an Effect workflow module when orchestration would otherwise obscure rune projection, for example `stores/detail-workflow.ts`, `features/kata/kata-workflow.ts`, and `components/terminal/terminal-session.ts`.

The runtime interface is deliberately narrow:

```ts
import type { Effect as EffectType } from "effect/Effect";
import type { Exit as ExitType } from "effect/Exit";
import type { Error as LayerError, Success as LayerSuccess } from "effect/Layer";
import type { ManagedRuntime as ManagedRuntimeType } from "effect/ManagedRuntime";
import { AppLiveLayer } from "./layer.js";

export type AppServices = LayerSuccess<typeof AppLiveLayer>;
export type AppLayerError = LayerError<typeof AppLiveLayer>;

export interface CommandRunOptions<E> {
  readonly operation: string;
  readonly safeContext: Readonly<Record<string, string | number | boolean>>;
  readonly onFailure: (failure: E | AppLayerError) => void;
}

export interface AppExecution<A, E> {
  readonly interrupt: () => void;
  readonly await: EffectType<ExitType<A, E | AppLayerError>>;
}

export interface AppRuntime {
  readonly runCommand: <A, E>(
    program: EffectType<A, E, AppServices>,
    options: CommandRunOptions<E>,
  ) => AppExecution<A, E>;
}

export type OwnedAppRuntime = AppRuntime & Pick<ManagedRuntimeType<AppServices, AppLayerError>, "disposeEffect">;

export function makeAppRuntime(): OwnedAppRuntime;
```

Use these direct module imports from `effect/Effect`, `effect/Fiber`, `effect/Layer`, and `effect/ManagedRuntime` if the beta root barrel cannot express the types without a module-qualified type.

## Shared Commit and Identifier Gate

At every commit step below, first construct the full proposed subject, body, and attribution in a shell variable named `commit_message`, then run this fail-closed identifier materialization and scan in the same shell. It resolves each exact title separately, requires exactly five unique identifiers, and aborts on lookup, parse, count, staged-diff, or message failure:

```bash
set -euo pipefail
migration_titles=(
  'Migrate Kenn Forge frontend async orchestration to Effect'
  'Effect frontend foundation and source consolidation'
  'Effect provider and application workflows'
  'Effect feature streams'
  'Effect terminal resources and final async audit'
)
identifier_file="$(mktemp)"
for title in "${migration_titles[@]}"; do
  search_json="$(kata search --workspace /tmp --project forge "$title" --json)"
  matches="$(printf '%s' "$search_json" | jq -c --arg title "$title" \
    '[.results[].issue | select(.title == $title)]')"
  test "$(printf '%s' "$matches" | jq 'length')" -eq 1
  internal_id="$(printf '%s' "$matches" | jq -er \
    '.[0].short_id | select(type == "string" and length > 0)')"
  printf '%s\n' "$internal_id" >> "$identifier_file"
done
test "$(sort -u "$identifier_file" | wc -l | tr -d ' ')" -eq 5
while IFS= read -r internal_id; do
  if git diff --cached -- . | rg -F "$internal_id" >/dev/null; then
    printf 'internal identifier found in staged content\n' >&2
    exit 1
  fi
  if printf '%s' "$commit_message" | rg -F "$internal_id" >/dev/null; then
    printf 'internal identifier found in commit message\n' >&2
    exit 1
  fi
done < "$identifier_file"
scripts/context-sync --check
```

Then complete the repository-local `context-sync --commit` workflow and invoke the mandatory commit skill. The commit skill must inspect status, diff, and recent history, stage only the named files, preserve hooks, add a substantive why-focused body, and create a new commit. Never put the Kata reference into `commit_message`.

## PR 1: One Frontend Tree and Effect Foundation

### Task 1: Track the Work Internally and Adopt the Bottom Stack Branch

**Files:**

- No tracked file changes.
- Internal state only: Kata issue records, task-local exact identifiers, and `gh stack` metadata.

**Interfaces:**

- Produces: one Kata parent, four ordered child issues, and a locally tracked stack whose bottom is `effect-runtime-foundation`.
- Privacy boundary: returned Kata references stay only in shell variables and Kata.
- Privacy boundary: exact identifiers may also be materialized in a mode-0600 file under `/tmp` for fail-closed scans and exact closure; that file is never added to Git or copied into GitHub text.

- [ ] **Step 1: Confirm the approved plan and search before creating**

```bash
kata quickstart
kata search --workspace /tmp --project forge \
  "Migrate Kenn Forge frontend async orchestration to Effect" --agent
```

If an open issue already covers the exact four-layer migration, use it as `parent_ref`. Otherwise create it idempotently:

```bash
parent_json="$(kata create --workspace /tmp --project forge \
  'Migrate Kenn Forge frontend async orchestration to Effect' \
  --body 'Replace bespoke frontend async orchestration with Effect in four stacked pull requests: foundation, provider workflows, feature streams, and terminal resources.' \
  --idempotency-key 'kenn-forge-effect-frontend-async-2026-08-02' --json)"
parent_ref="$(printf '%s' "$parent_json" | jq -er \
  '(.issue.short_id // .short_id) | select(type == "string" and length > 0)')"
```

- [ ] **Step 2: Create the four children with stack ordering**

```bash
pr1_json="$(kata create --workspace /tmp --project forge \
  'Effect frontend foundation and source consolidation' \
  --body 'Collapse the UI package, relocate generated clients, add the main runtime and browser boundaries, and migrate GitHub App setup.' \
  --parent "$parent_ref" --idempotency-key 'kenn-forge-effect-frontend-pr1-2026-08-02' --json)"
pr1_ref="$(printf '%s' "$pr1_json" | jq -er \
  '(.issue.short_id // .short_id) | select(type == "string" and length > 0)')"

pr2_json="$(kata create --workspace /tmp --project forge \
  'Effect provider and application workflows' \
  --body 'Migrate startup, settings, provider reads, events, diffs, and ordered mutations; remove Provider and ForgeClient.' \
  --parent "$parent_ref" --blocked-by "$pr1_ref" \
  --idempotency-key 'kenn-forge-effect-frontend-pr2-2026-08-02' --json)"
pr2_ref="$(printf '%s' "$pr2_json" | jq -er \
  '(.issue.short_id // .short_id) | select(type == "string" and length > 0)')"

pr3_json="$(kata create --workspace /tmp --project forge 'Effect feature streams' \
  --body 'Migrate Kata, Roborev, Docs, and repository-browser streams and mutations.' \
  --parent "$parent_ref" --blocked-by "$pr2_ref" \
  --idempotency-key 'kenn-forge-effect-frontend-pr3-2026-08-02' --json)"
pr3_ref="$(printf '%s' "$pr3_json" | jq -er \
  '(.issue.short_id // .short_id) | select(type == "string" and length > 0)')"

pr4_json="$(kata create --workspace /tmp --project forge \
  'Effect terminal resources and final async audit' \
  --body 'Migrate workspace and terminal resources, then remove all remaining bespoke async orchestration.' \
  --parent "$parent_ref" --blocked-by "$pr3_ref" \
  --idempotency-key 'kenn-forge-effect-frontend-pr4-2026-08-02' --json)"
pr4_ref="$(printf '%s' "$pr4_json" | jq -er \
  '(.issue.short_id // .short_id) | select(type == "string" and length > 0)')"

identifier_state='/tmp/kenn-forge-effect-identifiers.json'
(umask 077
  jq -n \
    --arg parent "$parent_ref" --arg pr1 "$pr1_ref" --arg pr2 "$pr2_ref" --arg pr3 "$pr3_ref" --arg pr4 "$pr4_ref" \
    '{
      "Migrate Kenn Forge frontend async orchestration to Effect": $parent,
      "Effect frontend foundation and source consolidation": $pr1,
      "Effect provider and application workflows": $pr2,
      "Effect feature streams": $pr3,
      "Effect terminal resources and final async audit": $pr4
    }' > "$identifier_state")
jq -e 'length == 5 and ([.[]] | unique | length == 5)' "$identifier_state" >/dev/null
```

- [ ] **Step 3: Configure and adopt the existing bottom branch non-interactively**

```bash
git config rerere.enabled true
git config remote.pushDefault origin
test "$(git branch --show-current)" = "effect-runtime-foundation"
gh stack init --base main effect-runtime-foundation
gh stack view --json
```

Expected: one unsubmitted bottom layer based on `main`. Do not put any value from `*_ref` into stack commands.

### Task 2: Collapse the UI Workspace Package Into the Frontend

**Files:**

- Move: every file under `packages/ui/src/` to the same relative path under `frontend/src/lib/`.
- Merge: `packages/ui/src/components/detail/LabelPicker.test.ts` and `frontend/src/lib/components/detail/LabelPicker.test.ts`.
- Delete: `packages/ui/package.json`, `packages/ui/tsconfig.json`, and the now-empty `packages/ui/` tree.
- Delete after import rewrites: `frontend/src/lib/index.ts`.
- Modify: `frontend/src/App.svelte`, `frontend/src/lib/api/types.ts` after the move, all frontend imports beginning `@kenn-forge/ui`, `frontend/package.json`, `package.json`, `vite.config.ts`, `frontend/vite.config.ts`, `Makefile`, `scripts/dev-backend-build.sh`, `scripts/lint-api-urls.mjs`, `scripts/lint-api-urls.test.mjs`, `bun.lock`, and any `CLAUDE.md` or living `context/` path references selected by `context-sync --commit`.
- Generated destination: `frontend/src/lib/api/generated/client.ts`, `frontend/src/lib/api/generated/schema.ts`, and `frontend/src/lib/api/roborev/generated/schema.ts`.

**Interfaces:**

- Produces: direct frontend-relative imports; no `@kenn-forge/ui` package or alias.
- Preserves: generated client signatures, all component/store exports, and `Provider.svelte` temporarily at `frontend/src/lib/Provider.svelte`.
- Produces: exact generated aliases for existing HTTP shapes and explicitly named normalized domain refinements only where the UI needs non-null arrays or a narrowed enum.

- [ ] **Step 1: Record the pre-move behavioral baseline**

Run:

```bash
bun run --cwd frontend test
bun run check
```

Expected: PASS. This is the refactor baseline; do not add an absence test for the package.

- [ ] **Step 2: Move the tree with `apply_patch` and resolve the one collision**

Use `apply_patch` move directives while preserving each path suffix after `packages/ui/src/` under `frontend/src/lib/`. Merge the two `LabelPicker` suites into one file with both `describe` blocks and one `afterEach(cleanup)` registration. Delete the moved barrel instead of preserving it.

- [ ] **Step 3: Rewrite imports and consolidate dependencies/config**

Replace each package import with the exact moved module, for example:

```ts
import type { PullRequest } from "./lib/api/types.js";
import PRListView from "./lib/views/PRListView.svelte";
import { createPullsStore } from "./lib/stores/pulls.svelte.js";
```

Move the Tiptap, ProseMirror, Shiki, Svelte Tiptap, and other production dependencies from `packages/ui/package.json` into `frontend/package.json`. Remove all `uiPkg`, `uiIndex`, and `@kenn-forge/ui` Vite aliases and optimize-dependency entries. Change unit-test discovery to include both `src/**/*.{test,spec}.?(c|m)[jt]s?(x)` and `../packages/github-app-ui/src/**/*.{test,spec}.?(c|m)[jt]s?(x)`; only the removed `../packages/ui/src` include disappears. Change generation and lint scan paths from `packages/ui/src` to `frontend/src/lib`.

Replace parallel HTTP declarations in the moved `api/types.ts` with generated aliases. Use exact aliases when the shape already matches:

```ts
export type CICheckWire = components["schemas"]["CICheck"];
export type DiffResponseWire = components["schemas"]["DiffResponse"];
export type FilesResponseWire = components["schemas"]["FilesResponse"];
export type DiffFileWire = components["schemas"]["DiffFile"];
```

The cached `CIChecksJSON` string can carry the existing optional `required` extension even though that embedded JSON is not a structured OpenAPI response, so name the normalized domain type instead of pretending it is the wire type:

```ts
export type CICheck = CICheckWire & { readonly required?: boolean };
```

Do not keep handwritten copies of `DiffResponse`, `FilesResponse`, `DiffFile`, `Hunk`, or `Line`. Task 8 may derive narrower normalized UI types from these aliases after decoding, but their values must remain assignable to the generated wire types.

Refresh the workspace lock after the manifest consolidation, then prove it is stable:

```bash
bun install
bun install --frozen-lockfile
```

- [ ] **Step 4: Regenerate and prove the wire output did not change**

```bash
cp frontend/src/lib/api/generated/schema.ts /tmp/kenn-forge-schema-before.ts
cp frontend/src/lib/api/generated/client.ts /tmp/kenn-forge-client-before.ts
make api-generate
cmp /tmp/kenn-forge-schema-before.ts frontend/src/lib/api/generated/schema.ts
cmp /tmp/kenn-forge-client-before.ts frontend/src/lib/api/generated/client.ts
make roborev-api-generate
```

Expected: both Kenn Forge generated comparisons are equal; Roborev generation writes only to the relocated path.

- [ ] **Step 5: Verify the direct cutover**

```bash
! rg -n '@kenn-forge/ui|packages/ui' frontend package.json vite.config.ts Makefile scripts --glob '!docs/**'
bun install --frozen-lockfile
bun run --cwd frontend test
bun run check
```

- [ ] **Step 6: Commit the source consolidation**

Use the shared commit gate and commit skill with subject:

```text
refactor: keep the application UI in one frontend tree
```

The body must explain that the workspace package had one consumer and made imports, generation, and runtime ownership harder to follow. Do not mention the internal issue.

### Task 3: Add the Effect Runtime, Typed Failures, Transport, and Browser Layers

**Files:**

- Modify: `frontend/package.json`, `packages/github-app-ui/package.json`, `bun.lock`, `frontend/src/main.ts`, and `frontend/src/App.svelte`.
- Create: `frontend/src/lib/app/runtime.ts`, `frontend/src/lib/app/runtime-context.ts`, `frontend/src/lib/app/mount.ts`, `frontend/src/lib/app/layer.ts`, `frontend/src/lib/api/generated-api.ts`, `frontend/src/lib/api/effect-errors.ts`, `frontend/src/lib/api/retry-policy.ts`, `frontend/src/lib/browser/storage.ts`, `frontend/src/lib/browser/event-source.ts`, `frontend/src/lib/browser/streaming-fetch.ts`, `frontend/src/lib/browser/web-socket.ts`, `frontend/src/lib/browser/observers.ts`, `frontend/src/lib/effect/ordered-command-queue.ts`, `frontend/src/lib/testing/effect-layers.ts`.
- Test: `frontend/src/lib/api/generated-api.test.ts`, `frontend/src/lib/api/retry-policy.test.ts`, `frontend/src/lib/browser/resources.test.ts`, `frontend/src/lib/effect/ordered-command-queue.test.ts`; adapt the existing App harness to assert root disposal.

**Interfaces:**

- Produces: `makeAppRuntime()`, metadata-bearing `AppRuntime.runCommand`, `AppExecution`, `getAppRuntime`, `setAppRuntime`, `GeneratedApi`, `TransientTransportError`, `ApiProblemError`, `InvalidExternalPayload`, `transientRetrySchedule`, acknowledged `OrderedCommandQueue`, and acquired browser resource constructors.
- Consumes: generated `paths` and `components` from `frontend/src/lib/api/generated/schema.ts`.

- [ ] **Step 1: Install aligned beta packages with Bun**

```bash
effect_version="$(jq -r '.version' .repos/effect/packages/effect/package.json)"
test "$effect_version" = '4.0.0-beta.102'
test "$(jq -r '.version' .repos/effect/packages/platform-browser/package.json)" = "$effect_version"
test "$(jq -r '.version' .repos/effect/packages/vitest/package.json)" = "$effect_version"
(cd frontend && bun add --exact "effect@$effect_version" "@effect/platform-browser@$effect_version" && \
  bun add --dev --exact "@effect/vitest@$effect_version")
(cd packages/github-app-ui && bun add --exact "effect@$effect_version" && \
  bun add --dev --exact "@effect/vitest@$effect_version")
bun install --frozen-lockfile
```

Expected: every installed `effect` and `@effect/*` package exactly matches the pinned checkout's `4.0.0-beta.102`; no unrelated Effect package is added.

- [ ] **Step 2: Write failing boundary and lifecycle tests**

Use `@effect/vitest` and test app-owned outcomes. The resource test uses the adapter's injected constructor, not a real browser EventSource:

```ts
import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Ref, Stream } from "effect";
import { eventSourceStream } from "./event-source.js";
import { EventSourceFactoryTest, EventSourceProbe } from "../testing/effect-layers.js";

it.layer(EventSourceFactoryTest)("EventSource ownership", (it) => {
  it.effect("closes the source when its owner is interrupted", () =>
    Effect.gen(function* () {
      const probe = yield* EventSourceProbe;
      const fiber = yield* Effect.forkChild(Stream.runDrain(eventSourceStream("/api/v1/events")).pipe(Effect.scoped));
      yield* probe.awaitOpened;
      yield* Fiber.interrupt(fiber);
      assert.isTrue(yield* Ref.get(probe.closed));
    }),
  );
});
```

`EventSourceFactoryTest` and `EventSourceProbe` are test-only exports from `frontend/src/lib/testing/effect-layers.ts`; the live service surface remains only `open(url)`. Add tests that classify a rejected generated fetch as `TransientTransportError`, preserve a generated problem body inside `ApiProblemError`, retry a transient idempotent read at most twice with `TestClock`, do not retry a validation problem, and release each acquired fake EventSource/reader/socket/observer on success, typed failure, and interruption. Test `OrderedCommandQueue` submission order, per-command failure acknowledgement without consumer death, capacity backpressure, and pending acknowledgement completion on shutdown. In the App harness, await `Fiber.interrupt(rootFiber)` and then assert Svelte unmounted and the supplied managed-runtime finalizers completed once before the test continues; do not join an interrupted fiber.

- [ ] **Step 3: Run the focused tests and confirm failure**

```bash
./node_modules/.bin/vp test run --config frontend/vite.config.ts --project unit \
  frontend/src/lib/api/generated-api.test.ts \
  frontend/src/lib/api/retry-policy.test.ts \
  frontend/src/lib/browser/resources.test.ts \
  frontend/src/lib/effect/ordered-command-queue.test.ts
```

Expected: FAIL because the new modules do not exist.

- [ ] **Step 4: Implement the typed runtime boundary**

Use this shape, adapting only import paths required by the installed beta:

```ts
import { ManagedRuntime } from "effect";
import { AppLiveLayer } from "./layer.js";

export const makeAppRuntime = () => makeAppRuntimeBoundary(ManagedRuntime.make(AppLiveLayer));
```

```ts
import { createContext } from "svelte";
import type { AppRuntime } from "./runtime.js";

export const [getAppRuntime, setAppRuntime] = createContext<AppRuntime>();
```

`frontend/src/main.ts` creates the runtime once and owns one root fiber. `frontend/src/lib/app/mount.ts` exposes `appProgram(target, runtime)`, which acquires the Svelte mount with `Effect.acquireRelease`, passes `props: { runtime }`, keeps the scope alive, and unmounts Svelte in its release action. `App.svelte` only calls `setAppRuntime(runtime)` during initialization; it does not dispose the runtime. `mountApplication(target)` returns the root fiber plus `dispose: Fiber.interrupt(rootFiber)` as an Effect so tests or embedding hosts can await complete teardown. The normal browser page owns that root for the lifetime of the JavaScript realm and does not add a detached unload callback. Use this entrypoint shape, adapting exact installed-beta imports only:

```ts
const runtime = makeAppRuntime();
const root = Effect.scoped(appProgram(target, runtime)).pipe(Effect.ensuring(runtime.disposeEffect));
const rootFiber = Effect.runFork(root);
```

Tests and any embedding host run the returned disposal Effect and await `Fiber.interrupt(rootFiber)`, then assert both Svelte unmount and managed-runtime finalizers have completed. No detached `void` promise or child runtime exists.

`runCommand` wraps the managed runtime's raw fiber in an `AppExecution`. It registers one observer using the supplied operation name and safe context, ignores interruption, sends expected typed failures to `onFailure`, and logs defects once before invoking the feature's generic failure presentation. `AppExecution.interrupt()` is the one synchronous host-boundary wrapper around `fiber.interruptUnsafe()`; its `await` member is `Fiber.await(fiber)`, and the managed runtime scope retains the fiber until completion. Components never call raw `runFork`, `interruptUnsafe`, or `addObserver`. Workflows must expose typed failures rather than starting their own reporting fibers.

- [ ] **Step 5: Implement shared failures and bounded retry**

```ts
import { Data, Schedule, Schema } from "effect";
import type { ProblemBody } from "./problems.js";

export class TransientTransportError extends Schema.TaggedErrorClass<TransientTransportError>()(
  "TransientTransportError",
  { operation: Schema.String, cause: Schema.Defect },
) {}

export class InvalidExternalPayload extends Schema.TaggedErrorClass<InvalidExternalPayload>()(
  "InvalidExternalPayload",
  { operation: Schema.String, cause: Schema.Defect },
) {}

export class ApiProblemError extends Data.TaggedError("ApiProblemError")<{
  readonly operation: string;
  readonly problem: ProblemBody;
}> {}

export const transientRetrySchedule = Schedule.exponential("500 millis").pipe(
  Schedule.jittered,
  Schedule.upTo({ times: 2 }),
);
```

Call idempotent operations with `Effect.retry({ schedule: transientRetrySchedule, while: isTransientFailure })`. Keep rate-limit gates and cadence schedules in separate exports.

- [ ] **Step 6: Implement generated transport and acquired browser adapters**

The generated client layer composes `csrfFetch(tracedFetch(fetch))` exactly once and exposes the generated client. Domain workflows call generated methods inside `Effect.tryPromise`, convert non-OK generated errors to `ApiProblemError`, and keep generated response types.

Use `Effect.acquireRelease` for resources and `Stream.fromQueue` or `Stream.fromReadableStream` for callbacks/readers. Reuse `@effect/platform-browser` `Clipboard.layer`, `BrowserSocket.layerWebSocketConstructor`, `BrowserStream.fromEventListenerWindow`, and `BrowserStream.fromEventListenerDocument` where they preserve the existing contract. Keep custom tags only where local/session storage or EventSource need distinct identities.

- [ ] **Step 7: Pass focused and full foundation checks**

```bash
./node_modules/.bin/vp test run --config frontend/vite.config.ts --project unit \
  frontend/src/lib/api/generated-api.test.ts \
  frontend/src/lib/api/retry-policy.test.ts \
  frontend/src/lib/browser/resources.test.ts \
  frontend/src/lib/effect/ordered-command-queue.test.ts
bun run --cwd frontend test
bun run check
```

- [ ] **Step 8: Run Svelte analysis and commit**

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/App.svelte --svelte-version 5
```

Use the shared commit gate and commit skill with subject:

```text
refactor: give frontend async work one owned runtime
```

### Task 4: Migrate the GitHub App Setup Fetch and Countdown

**Files:**

- Create: `packages/github-app-ui/src/setup-program.ts`, `packages/github-app-ui/src/setup-program.test.ts`, `packages/github-app-ui/src/setup-program.test-layer.ts`.
- Modify: `packages/github-app-ui/src/App.svelte`, `packages/github-app-ui/src/main.ts`, `frontend/vite.config.ts`, `vite.config.ts`.
- Preserve: `packages/github-app-ui/tests/setup-page.spec.ts`.

**Interfaces:**

- Produces: `SetupController`, whose scoped `program` decodes `SetupFlow` from `flow.json` and whose synchronous `continue()` publishes a submit command into the program's Effect queue, plus an explicitly provided `SetupEnvironmentLive` layer.
- Svelte receives callbacks `onFlow`, `onSecondsLeft`, `onFailure`, and `onSubmit`, plus the controller's synchronous `continue`; it owns only displayed state.

```ts
interface SetupController {
  readonly program: Effect.Effect<void, SetupFlowError, SetupEnvironment | Scope.Scope>;
  readonly continue: () => void;
}
```

- [ ] **Step 1: Write the TestClock regression first**

```ts
import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { makeSetupController } from "./setup-program.js";
import { SetupBrowserTest, SetupProbe } from "./setup-program.test-layer.js";

it.layer(SetupBrowserTest)("automatic setup submission", (it) => {
  it.effect("submits once after the visible countdown", () =>
    Effect.gen(function* () {
      const probe = yield* SetupProbe;
      const controller = makeSetupController({ onSecondsLeft: probe.recordSeconds });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* TestClock.adjust("2500 millis");
      yield* Effect.yieldNow;
      assert.strictEqual(yield* Ref.get(probe.submits), 1);
      yield* Fiber.interrupt(fiber);
      yield* TestClock.adjust("10 seconds");
      assert.strictEqual(yield* Ref.get(probe.submits), 1);
    }),
  );
});

it.layer(SetupBrowserTest)("manual setup submission", (it) => {
  it.effect("routes repeated Continue clicks through the same submit guard", () =>
    Effect.gen(function* () {
      const probe = yield* SetupProbe;
      const controller = makeSetupController({ onSecondsLeft: probe.recordSeconds });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      controller.continue();
      controller.continue();
      yield* Effect.yieldNow;
      assert.strictEqual(yield* Ref.get(probe.submits), 1);
      yield* TestClock.adjust("2500 millis");
      assert.strictEqual(yield* Ref.get(probe.submits), 1);
      yield* Fiber.interrupt(fiber);
    }),
  );
});

it.layer(SetupBrowserTest)("interrupted setup countdown", (it) => {
  it.effect("does not submit after interruption before the deadline", () =>
    Effect.gen(function* () {
      const probe = yield* SetupProbe;
      const controller = makeSetupController({ onSecondsLeft: probe.recordSeconds });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* TestClock.adjust("1 second");
      yield* Fiber.interrupt(fiber);
      yield* TestClock.adjust("10 seconds");
      assert.strictEqual(yield* Ref.get(probe.submits), 0);
    }),
  );
});
```

Create `packages/github-app-ui/src/setup-program.test-layer.ts` with the stateful `SetupBrowserTest` and `SetupProbe` services. Use a separate one-test `it.layer(...)` block for every test so fetch, submit count, and recorded seconds are rebuilt each time.

- [ ] **Step 2: Confirm the test fails**

```bash
./node_modules/.bin/vp test run --config frontend/vite.config.ts --project unit \
  packages/github-app-ui/src/setup-program.test.ts
```

- [ ] **Step 3: Implement one scoped setup program**

Decode `flow.json` and the nested manifest permissions with Effect `Schema.Class` definitions; do not use assertions. Build `SetupEnvironmentLive` from the browser fetch and form-submit services, and provide it explicitly at `main.ts`. Use `Effect.sleep("250 millis")` plus a `Schedule`/`Stream` countdown based on Effect `Clock`. The countdown and manual Continue callback publish the same submit command to one Effect queue; one consumer uses a `Ref<boolean>` guard so either source submits the form at most once. The synchronous `continue()` does no browser or Promise work beyond publishing to that queue. Form construction is a synchronous browser boundary inside `Effect.sync`. `main.ts` forks one `Effect.scoped(controller.program.pipe(Effect.provide(SetupEnvironmentLive)))` and requests interruption during teardown/pagehide; its root observer retains and reports the completion outcome.

- [ ] **Step 4: Verify behavior and Svelte output**

```bash
./node_modules/.bin/vp test run --config frontend/vite.config.ts --project unit \
  packages/github-app-ui/src/setup-program.test.ts
bun run --cwd packages/github-app-ui test:e2e
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer packages/github-app-ui/src/App.svelte --svelte-version 5
bun run check
```

- [ ] **Step 5: Commit the setup cutover**

Use the shared commit gate and commit skill with subject:

```text
refactor: scope GitHub App setup timing and fetch work
```

### Task 5: Verify and Close PR 1 Work

**Files:**

- No new production files unless verification exposes a defect.

- [ ] **Step 1: Run the complete layer verification**

```bash
bun run effect:prepare
make api-generate
git diff --exit-code -- frontend/openapi/openapi.yaml frontend/src/lib/api/generated internal/apiclient
bun run --cwd frontend test
bun run --cwd frontend test:browser
bun run check
bun run --cwd frontend build
bun run --cwd packages/github-app-ui test:e2e
```

- [ ] **Step 2: Capture visible GitHub App setup evidence**

Use the `capture-playwright` skill against the existing setup-page Playwright fixture and upload the resulting image with `gh image`. Keep the returned URL for the PR body; do not commit the image.

- [ ] **Step 3: Close only the first Kata child**

```bash
pr1_search="$(kata search --workspace /tmp --project forge \
  'Effect frontend foundation and source consolidation' --json)"
pr1_matches="$(printf '%s' "$pr1_search" | jq -c \
  '[.results[].issue | select(.title == "Effect frontend foundation and source consolidation" and .status == "open")]')"
test "$(printf '%s' "$pr1_matches" | jq 'length')" -eq 1
pr1_ref="$(printf '%s' "$pr1_matches" | jq -er \
  '.[0].short_id | select(type == "string" and length > 0)')"
kata close --workspace /tmp --project forge "$pr1_ref" --done \
  --message 'Consolidated the frontend source, added the Effect runtime boundaries, and verified generated clients, Vitest, browser tests, the production build, and GitHub App setup.' \
  --commit "$(git rev-parse HEAD)" \
  --test 'bun run --cwd frontend test' --agent
```

- [ ] **Step 4: Create the next branch explicitly**

```bash
gh stack add effect-provider-workflows
test "$(git branch --show-current)" = "effect-provider-workflows"
```

## PR 2: Shared Application and Provider Workflows

### Task 6: Migrate Startup, Readiness, Settings, and Persistence

**Files:**

- Modify: `frontend/src/lib/utils/appStartup.ts`, `frontend/src/lib/utils/backendReadiness.ts`, `frontend/src/lib/api/settings.ts`, `frontend/src/lib/stores/settings.svelte.ts`, `frontend/src/lib/stores/settings-hydration.ts`, `frontend/src/lib/stores/terminal-settings-persistence.ts`, `frontend/src/lib/components/terminal/WorkspaceEmbedShell.svelte`, `frontend/src/App.svelte`.
- Create: `frontend/src/lib/app/startup-workflow.ts`, `frontend/src/lib/stores/settings-workflow.ts`.
- Test: `frontend/src/lib/app/startup-workflow.test.ts`, `frontend/src/lib/stores/settings-workflow.test.ts`; adapt existing startup/readiness/settings tests.

**Interfaces:**

- Produces: `StartupWorkflow.start: Effect<StartupSnapshot, StartupError, GeneratedApi>`, shared through a capacity-one `Cache` with infinite TTL and explicit invalidation; `SettingsWorkflow.enqueue(command): Effect<void, SettingsError>` backed by the shared acknowledged `OrderedCommandQueue`.
- Preserves: settings hydration payload aliases to generated `SettingsResponse` members.

- [ ] **Step 1: Add failing policy tests**

Test that concurrent startup demand performs one generated `/settings` request, readiness polls sequentially, transient readiness failures retry, interruption does not display a startup error, and settings writes persist in submission order. Assert observed request order and projected state, not Cache/Queue mechanics.

- [ ] **Step 2: Run focused tests and confirm failure**

```bash
./node_modules/.bin/vp test run --config frontend/vite.config.ts --project unit \
  frontend/src/lib/app/startup-workflow.test.ts \
  frontend/src/lib/stores/settings-workflow.test.ts
```

- [ ] **Step 3: Implement startup and ordered settings workflows**

Use generated aliases:

```ts
import type { components } from "../api/generated/schema.js";

export type StartupSnapshot = components["schemas"]["SettingsResponse"];

export interface SettingsWorkflow {
  readonly enqueue: (command: SettingsCommand) => Effect.Effect<void, SettingsError>;
  readonly invalidateStartup: Effect.Effect<void>;
}
```

Allocate the startup `Cache` and settings queue in scoped workflow layers. The startup cache has capacity one and infinite TTL because it is startup-scoped; settings invalidation removes that one entry explicitly. Submit each settings write through `OrderedCommandQueue.enqueue`, await its per-command acknowledgement, and project that exact failure without terminating the queue consumer. Delete readiness `setTimeout`, startup AbortController, Promise sharing, and terminal settings debounce flags when callers switch.

- [ ] **Step 4: Verify, analyze Svelte, and commit**

```bash
./node_modules/.bin/vp test run --config frontend/vite.config.ts --project unit \
  frontend/src/lib/app/startup-workflow.test.ts frontend/src/lib/stores/settings-workflow.test.ts
bun run --cwd frontend test
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/App.svelte --svelte-version 5
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/terminal/WorkspaceEmbedShell.svelte --svelte-version 5
```

Use the shared commit gate and commit skill with subject `refactor: make startup and settings ordering explicit`.

### Task 7: Migrate Provider Reads, Sync, and Activity

**Files:**

- Modify: `frontend/src/lib/stores/pulls.svelte.ts`, `issues.svelte.ts`, `activity.svelte.ts`, `sync.svelte.ts`, `frontend/src/lib/components/sidebar/PullList.svelte`, `IssueList.svelte`, `frontend/src/lib/components/ActivityFeed.svelte`, `frontend/src/lib/views/PRListView.svelte`, `IssueListView.svelte`, `ActivityFeedView.svelte`, `MobileActivityView.svelte`, `FocusListView.svelte`, `frontend/src/App.svelte`.
- Create: `frontend/src/lib/stores/provider-key.ts`, `provider-key.test.ts`, `pulls-workflow.ts`, `issues-workflow.ts`, `activity-workflow.ts`, `sync-workflow.ts`.
- Adapt tests: existing pulls, issues, activity, sync, list, and view suites.

**Interfaces:**

- Produces: full `ProviderItemKey`, latest-per-query reads, bounded independent refresh, explicit sync cadence, and rune projection stores.

- [ ] **Step 1: Add failing identity, latest-wins, and dedup tests**

```ts
export interface ProviderItemKey {
  readonly provider: string;
  readonly platformHost: string;
  readonly owner: string;
  readonly name: string;
  readonly number: number;
}

export const providerItemKey = (ref: ProviderItemKey): string =>
  [ref.provider, ref.platformHost, ref.owner, ref.name, String(ref.number)].map(encodeURIComponent).join("\u0000");
```

Tests must prove two self-hosted repos with the same owner/name/number do not share work; a late old query cannot replace a newer list; duplicate refresh demand shares one request; and polling never overlaps.

- [ ] **Step 2: Run focused store tests and confirm failure**

```bash
./node_modules/.bin/vp test run --config frontend/vite.config.ts --project unit \
  frontend/src/lib/stores/provider-key.test.ts \
  frontend/src/lib/stores/pulls.svelte.test.ts \
  frontend/src/lib/stores/issues.svelte.test.ts \
  frontend/src/lib/stores/activity.svelte.test.ts \
  frontend/src/lib/stores/sync.svelte.test.ts
```

- [ ] **Step 3: Implement owned concurrency policies**

Use one scoped `FiberHandle` per latest query/selection and `FiberMap<string>` for provider-keyed detail demand. Use the capacity-64, two-second shared-read cache only for concurrent duplicate demand and invalidate the exact full provider key after mutations. Use a semaphore for bulk reads. Express sequential polling as `Stream.fromEffectSchedule(syncOnce, Schedule.spaced(syncInterval))`; the schedule starts the next run only after the current effect finishes. Keep rate-limit gate sleeps driven by the generated problem `retryAfter`; do not pass them through transient retry.

- [ ] **Step 4: Switch components to synchronous command launchers**

Component handlers call store methods that synchronously launch an Effect fiber and return `void`; component state remains runes. Remove `void store.load...()` patterns and Promise-typed action handlers as each caller changes.

- [ ] **Step 5: Verify and commit**

Run the focused tests, complete unit suite, and Svelte autofixer on every modified Svelte file. Use the shared commit gate and subject `refactor: define provider read and sync concurrency with Effect`.

### Task 8: Migrate Detail, Diff, Events, and Ordered Optimistic Mutations

**Files:**

- Modify: `frontend/src/lib/stores/detail.svelte.ts`, `diff.svelte.ts`, `diff-review-draft.svelte.ts`, `events.svelte.ts`, `frontend/src/lib/components/detail/ApproveButton.svelte`, `ApproveWorkflowsButton.svelte`, `CommentBox.svelte`, `CommentEditor.svelte`, `IssueCommentBox.svelte`, `IssueDetail.svelte`, `LabelPicker.svelte`, `MergeModal.svelte`, `PullDetail.svelte`, `ReadyForReviewButton.svelte`, `UserListEditor.svelte`, `frontend/src/lib/components/diff/DiffInlineCommentComposer.svelte`, `DiffReviewDraftInlineComment.svelte`, `DiffReviewDraftTray.svelte`, `DiffReviewThreadInlineComment.svelte`, `DiffReviewThreadSnippet.svelte`, `DiffView.svelte`, `frontend/src/lib/components/detail/keyboard-actions.ts`, `labelCatalogRefresh.ts`, `frontend/src/lib/stores/keyboard/pr-detail-actions.ts`.
- Create: `frontend/src/lib/stores/detail-workflow.ts`, `diff-workflow.ts`, `provider-events-workflow.ts`, `ordered-mutations.ts`.
- Adapt tests: existing detail, diff, draft, event, comment, label, merge, review, and component suites.

**Interfaces:**

- Produces: latest selected detail/diff, scoped provider SSE, ordered command queues, and version-guarded optimistic rollback.

- [ ] **Step 1: Migrate latest detail and diff reads, verify, and commit**

Add failing tests that a stale detail result cannot commit, changing diff scope interrupts the old read, and interruption is silent. Implement `detail-workflow.ts` and `diff-workflow.ts` with a `FiberHandle` per active selection, generated wire aliases, and the capacity-64 two-second shared-read cache keyed by the complete provider identity. Switch the detail and diff rune stores/components, run their unit/browser/component suites and Svelte autofixers, and commit with subject `refactor: make detail and diff selection latest-wins`.

- [ ] **Step 2: Migrate provider events, verify, and commit**

Add failing tests for checkpoint resume, visible reconnecting state, non-transient disconnected state, and owner interruption during both an open source and reconnect delay. Build provider events from the acquired EventSource adapter, decode frames, preserve the checkpoint in a `Ref`, and use the unbounded capped reconnect schedule from the global policy. Switch `events.svelte.ts` and callers, run event/detail integration tests and autofixers, and commit with subject `refactor: scope provider events to their owner`.

- [ ] **Step 3: Implement acknowledged version-owned optimistic mutations**

```ts
export interface VersionedCommand<A> {
  readonly version: number;
  readonly optimistic: A;
  readonly commit: Effect.Effect<void, ProviderMutationError>;
  readonly rollback: (failedVersion: number) => Effect.Effect<void>;
}
```

Add failing tests that an older failed label/comment mutation cannot roll back a newer success, commands reach the transport in submission order, one failed command reports to its own enqueuer while the next command still runs, a `stale_state` problem refreshes and requires review without replay, and queue shutdown completes pending callers. Allocate the sequence `Ref` and shared `OrderedCommandQueue` in a scoped layer. On failure, run rollback only when `failedVersion` still equals the current presentation version, then complete that command's acknowledgement with its typed error while the consumer continues. Branch on `ProblemCodes` and typed details. Never retry these mutations automatically.

- [ ] **Step 4: Switch mutation families, verify, and commit**

Switch comments/labels first, then reviews/ready/approve/merge, running each family's focused tests before moving to the next. After all families use the acknowledged queue, run affected unit/browser suites, all component tests, and autofixer on modified Svelte files. Commit with subject `refactor: serialize provider mutations with owned acknowledgements`.

### Task 9: Remove Provider, ForgeClient, and the Old App Wiring

**Files:**

- Delete: `frontend/src/lib/Provider.svelte`, `frontend/src/lib/context.ts` provider keys, and the `ForgeClient` type from `frontend/src/lib/types.ts` once no consumer remains.
- Modify: `frontend/src/App.svelte`, `frontend/src/lib/types.ts`, `frontend/src/lib/stores/keyboard/actions.ts`, `frontend/src/lib/stores/keyboard/types.ts`, `frontend/src/lib/components/layout/StatusBar.svelte`, `AppHeader.svelte`, `frontend/src/lib/components/terminal/WorkspaceEmbedShell.svelte`, `frontend/src/test/appHarness.ts` or the current app harness files, and Provider-oriented test hosts.
- Delete or rewrite: `frontend/src/Provider.test.ts`, `frontend/src/lib/testing/AppProviderMock.svelte`, and Provider-only host components.
- Test: add `frontend/src/lib/app/app-stores.test.ts`; keep real App harness routing tests.

**Interfaces:**

- Produces: `AppStores`, built from Effect workflow services and feature-local rune controllers; no injected openapi-fetch client.

- [ ] **Step 1: Write a failing app composition test**

Mount the real App with a controlled `AppRuntime` layer. Assert startup produces provider lists and teardown releases provider events/sync without calling manual `disconnect()` or `stopPolling()` methods.

- [ ] **Step 2: Compose stores directly at the app boundary**

Replace Provider initialization with `createAppStores(getAppRuntime(), hostState)` in the frontend. Store options consume Effect workflow services or runtime command functions, never a `ForgeClient`. Remove package-context symbols and manual cleanup list.

- [ ] **Step 3: Prove removal without an absence test**

```bash
rg -n 'Provider\.svelte|ForgeClient|API_CLIENT_KEY|STORES_KEY|stopPolling\(|events\.disconnect\(' frontend/src
```

Expected: no obsolete orchestration references. This command is review evidence only.

- [ ] **Step 4: Verify and commit**

Run full unit/browser suites, app harness tests, all modified Svelte autofixers, and `bun run check`. Use subject `refactor: let the app own provider workflows directly`.

### Task 10: Finish PR 2 Application-Level Async Callers

**Files:**

- Modify async callers in `frontend/src/lib/api/fleet-snapshot.ts`, `project-intake.ts`, `frontend/src/lib/components/RepoTypeahead.svelte`, `keyboard/Palette.svelte`, layout components, settings components, repository summary, recurrence dialogs, shared pickers, `frontend/src/lib/stores/container.svelte.ts`, `embed-config.svelte.ts`, `item-workspace-claim.svelte.ts`, `keyboard/mode-palette-search.ts`, `router.svelte.ts`, `theme.svelte.ts`, `tooling-status.svelte.ts`, `frontend/src/lib/utils/itemRefHandler.ts`, `markdownImages.ts`, and `frontend/src/main.ts`.
- Test: adapt their existing focused tests; add Effect tests only for newly explicit ordering, cancellation, retry, or resource ownership.

**Interfaces:**

- Produces: Effect-owned application HTTP commands and browser listeners; synchronous presentation-only Svelte events remain ordinary handlers.

- [ ] **Step 1: Classify every listed occurrence**

For each `async`, Promise, timer, fetch, listener, or observer, record one of: domain Effect command, acquired browser resource, code-split dynamic import, synchronous Svelte boundary, or presentation-only `tick`. Migrate the first two; leave only documented exceptions.

- [ ] **Step 2: Add focused failing tests only where Kenn Forge policy changes ownership**

Examples: RepoTypeahead latest search wins; settings modals serialize saves; StatusBar polling is sequential; document/window listeners release on App teardown. Do not test `addEventListener` or `ResizeObserver` themselves.

- [ ] **Step 3: Migrate application HTTP and ordering work, verify, and commit**

Migrate fleet/project intake, typeahead/search, settings saves, summaries, recurrence commands, pickers, and workspace claims. Run their focused tests, the complete unit/browser suites, autofixer for every modified Svelte file, and `bun run check`. Commit with subject `refactor: route application requests through Effect`.

- [ ] **Step 4: Migrate browser listeners and host cleanup, verify, and commit**

Move document/window listeners, theme/router/tooling host work, markdown image coalescing, and main-entrypoint resources into acquired browser adapters or scoped programs. Run focused teardown tests, the complete unit/browser suites, autofixer for every modified Svelte file, and `bun run check`. Commit with subject `refactor: scope application browser resources`.

### Task 11: Verify and Close PR 2 Work

- [ ] **Step 1: Run layer verification**

```bash
bun run --cwd frontend test
bun run --cwd frontend test:browser
bun run check
bun run --cwd frontend build
bun run --cwd frontend test:e2e:mock
make test-e2e
```

- [ ] **Step 2: Capture a provider workflow screenshot**

Use `capture-playwright` on a seeded provider detail state showing the unchanged visible UI. Upload through `gh image`; keep the URL for the PR description.

- [ ] **Step 3: Close the second child with commit and test evidence, then add the next branch**

```bash
pr2_search="$(kata search --workspace /tmp --project forge \
  'Effect provider and application workflows' --json)"
pr2_matches="$(printf '%s' "$pr2_search" | jq -c \
  '[.results[].issue | select(.title == "Effect provider and application workflows" and .status == "open")]')"
test "$(printf '%s' "$pr2_matches" | jq 'length')" -eq 1
pr2_ref="$(printf '%s' "$pr2_matches" | jq -er \
  '.[0].short_id | select(type == "string" and length > 0)')"
kata close --workspace /tmp --project forge "$pr2_ref" --done \
  --message 'Migrated startup, settings, provider reads, events, diffs, and ordered mutations; removed the provider wrapper and verified frontend plus real-backend browser behavior.' \
  --commit "$(git rev-parse HEAD)" --test 'make test-e2e' --agent
gh stack add effect-feature-streams
```

## PR 3: Kata, Roborev, Docs, and Repository Browser

### Task 12: Migrate Kata Snapshot Authority, SSE, and Mutations

**Files:**

- Modify: `frontend/src/lib/api/kata/daemons.ts`, `eventStream.ts`, `snapshot.ts`, `taskClient.ts`, `workspaces.ts`, `frontend/src/lib/features/kata/KataWorkspace.svelte`, `kataAuxiliaryAuthority.svelte.ts`, `kataEventStreamController.ts`, `kataMutationRevalidation.ts`, `kataWorkspaceAuthorityController.svelte.ts`, and Kata async components under `frontend/src/lib/components/kata/`.
- Create: `frontend/src/lib/features/kata/kata-workflow.ts`, `kata-workflow.test.ts`, `frontend/src/lib/api/kata/schemas.ts`.
- Adapt: existing Kata authority, stream, mutation, graph, and component tests.

**Interfaces:**

- Produces: one scoped `KataWorkflow` per active daemon intent, compact decoded invalidation stream, checkpointed reconnect, atomic snapshot acceptance, and ordered mutations followed by revalidation.

- [ ] **Step 1: Add failing stream/authority tests**

Prove reconnect sends the last accepted event ID, a stale daemon/epoch frame cannot replace authority, one invalidation reloads the exact current snapshot intent, mutation revalidation never overtakes an earlier ordered mutation, and feature interruption aborts pre-header fetch plus active reader.

- [ ] **Step 2: Implement decoded streams and authority**

Use `Schema.Class` for non-OpenAPI Kata SSE envelopes, `Stream.fromReadableStream` for response bodies, `Stream.decodeText()` and frame parsing, `Ref` for checkpoint and accepted generation, `FiberHandle` for exact-intent reload, and the shared acknowledged command queue for mutations. Keep raw invalidations separate from rendered authority. Keep generated Kenn Forge types for workspace and proxy HTTP responses.

- [ ] **Step 3: Switch rune controllers and components**

Controllers accept workflow events and update `$state.raw` snapshots. Component interactions synchronously launch Effect commands. Delete AbortControllers, generation counters replaced by `Ref`, reconnect timers, and Promise cleanup flags.

- [ ] **Step 4: Verify and commit**

Run all Kata unit/browser tests, relevant mock/full-stack Playwright Kata projects, autofixer on modified Svelte files, and commit with subject `refactor: make Kata snapshot and stream ownership explicit`.

### Task 13: Migrate Roborev NDJSON, Polling, Logs, and Mutations

**Files:**

- Modify: `frontend/src/lib/api/roborev/client.ts`, `frontend/src/lib/stores/roborev/daemon.svelte.ts`, `jobs.svelte.ts`, `review.svelte.ts`, `log.svelte.ts`, and async Roborev components under `frontend/src/lib/components/roborev/`.
- Create: `frontend/src/lib/stores/roborev/roborev-workflow.ts`, `roborev-workflow.test.ts`, `frontend/src/lib/api/roborev/schemas.ts`.
- Adapt: daemon/jobs/review/log/component tests and Roborev browser/full-stack tests.

**Interfaces:**

- Produces: sequential daemon poll stream, abortable NDJSON event/log streams, checkpointed reconnect, latest-per-job loads, and non-retried ordered mutations.

- [ ] **Step 1: Add failing Roborev policy tests**

Prove idle pre-header stream interruption closes fetch, active reader interruption cancels the body, reconnect resumes the job checkpoint, daemon polls do not overlap, selecting another job cancels stale review/log loads, and cancel/rerun/close commands are not automatically replayed.

- [ ] **Step 2: Implement the workflow**

Decode each NDJSON line with a schema and preserve generated Roborev HTTP types from `api/roborev/generated/schema.ts`. Use `FiberMap<number>` for per-job work, `Stream` plus the global reconnect schedule for poll/reconnect, acquired readers for logs/events, and the shared acknowledged command queue for cancel/rerun/close. Keep daemon unavailable and available cadences distinct.

- [ ] **Step 3: Verify and commit**

Run all Roborev unit/component tests and the configured Roborev Playwright project without stopping or reconfiguring any user-owned daemon. Use repository test fixtures only. Run autofixer and commit with subject `refactor: scope Roborev polling and streams with Effect`.

### Task 14: Migrate Docs and Repository Browser Workflows

**Files:**

- Modify: `frontend/src/lib/api/docs/api.ts`, `markdown.ts`, all async components under `frontend/src/lib/components/docs/`, `frontend/src/lib/features/docs/DocsFeature.svelte`, `frontend/src/lib/stores/repo-browser.svelte.ts`, `frontend/src/lib/features/repo-browser/RepoBrowserFeature.svelte`, `PierreFileContents.svelte`.
- Create: `frontend/src/lib/features/docs/docs-workflow.ts`, `docs-workflow.test.ts`, `frontend/src/lib/features/repo-browser/repo-browser-workflow.ts`, `repo-browser-workflow.test.ts`.
- Adapt: existing Docs and repository browser tests.

**Interfaces:**

- Produces: latest route/search reads, ordered document mutations/publish, full-key repository reads, explicit cache invalidation, and bounded preview work.

- [ ] **Step 1: Add failing latest/ordered tests**

Prove stale Docs search/read and repo tree/blob responses cannot commit; saves/publish execute in submission order; a failed older save cannot roll back newer editor state; repository keys include provider host; and explicit mutation invalidates the relevant cache entry.

- [ ] **Step 2: Implement and switch callers**

Use `FiberHandle` for Docs route/search, `FiberMap` for repository ref/path keys, and the shared acknowledged queue for writes/publish. Repository previews use a capacity-128 cache with a five-minute TTL, exact ref/path keys, and explicit invalidation after mutations; all other reads use the global in-flight cache policy. Use a semaphore for bounded blob/preview loads. Preserve Docs error codes and local identity; do not force Docs through provider abstractions.

- [ ] **Step 3: Verify and commit**

Run all Docs/repository unit/browser tests, affected mock/full-stack Playwright projects, autofixer, and commit with subject `refactor: own Docs and repository browser work with Effect`.

### Task 15: Verify and Close PR 3 Work

- [ ] **Step 1: Run layer verification**

```bash
bun run --cwd frontend test
bun run --cwd frontend test:browser
bun run check
bun run --cwd frontend build
bun run --cwd frontend test:e2e:mock
```

Run the affected full-stack suites through their repository-owned fixtures; do not start, stop, or alter user-owned daemons:

```bash
make test-e2e
make test-e2e-roborev
```

- [ ] **Step 2: Capture one visible feature-stream screenshot**

Use `capture-playwright` on the real seeded backend for one representative migrated feature. Upload with `gh image`.

- [ ] **Step 3: Close the third child and add the final branch**

```bash
pr3_search="$(kata search --workspace /tmp --project forge 'Effect feature streams' --json)"
pr3_matches="$(printf '%s' "$pr3_search" | jq -c \
  '[.results[].issue | select(.title == "Effect feature streams" and .status == "open")]')"
test "$(printf '%s' "$pr3_matches" | jq 'length')" -eq 1
pr3_ref="$(printf '%s' "$pr3_matches" | jq -er \
  '.[0].short_id | select(type == "string" and length > 0)')"
kata close --workspace /tmp --project forge "$pr3_ref" --done \
  --message 'Migrated Kata, Roborev, Docs, and repository-browser async workflows; verified unit, browser, mock, and affected full-stack tests.' \
  --commit "$(git rev-parse HEAD)" --test 'bun run --cwd frontend test:browser' --agent
gh stack add effect-terminal-resources
```

## PR 4: Terminal Resources and Final Cleanup

### Task 16: Migrate Workspace and Session Orchestration

**Files:**

- Modify: `frontend/src/lib/api/workspace-runtime.ts`, `frontend/src/lib/stores/session-host.svelte.ts`, `workspace-host.svelte.ts`, `frontend/src/lib/components/terminal/WorkspaceHost.svelte`, `WorkspaceEmbedShell.svelte`, `WorkspaceListSidebar.svelte`, `WorkspaceProjectCard.svelte`, `PooledSessionTerminal.svelte`, `TerminalPane.svelte`, `TerminalSplitTree.svelte`, `frontend/src/lib/components/workspace/WorkspaceCreateSplitButton.svelte`, `WorkspaceRightSidebar.svelte`, `WorktreeRow.svelte`, `frontend/src/lib/instrumentation/workspaceSwitchTiming.ts`.
- Create: `frontend/src/lib/stores/workspace-workflow.ts`, `workspace-workflow.test.ts`.
- Adapt: workspace/session host and component tests.

**Interfaces:**

- Produces: latest active-workspace runtime refresh, full workspace/session keyed fibers, sequential poll/reconcile, ordered launch/stop commands, and scoped switch tracing.

- [ ] **Step 1: Add failing workspace policy tests**

Prove switching workspaces cancels stale refresh and tracing fallback, runtime polls never overlap, session exit reconciles with exactly one refresh, an exited active session returns to Home, and interruption releases every workspace child fiber without deleting durable base-tmux state.

- [ ] **Step 2: Implement workspace workflow and switch rune hosts**

Use `FiberHandle` for active workspace, `FiberMap<SessionHostKey>` for independent sessions, the shared acknowledged queue for launch/stop/refresh commands, and `Stream` for polling. Keep provider identity separate from workspace identity. Preserve existing durable base session and natural-exit rules.

- [ ] **Step 3: Verify and commit**

Run workspace/session unit and browser tests plus affected full-stack workspace tests. Run autofixer and commit with subject `refactor: scope workspace and session orchestration with Effect`.

### Task 17: Migrate xterm, WebSocket, Clipboard, Resize, and Observer Lifetimes

**Files:**

- Modify: `frontend/src/lib/components/terminal/XtermTerminalPane.svelte`, `embeddedWebSocket.ts`, `terminal-focus.ts`, `terminalClipboardFallback.ts`, `terminalClipboardWriter.ts`, `terminalZoom.ts`, `tmuxMouseDragAutoscroll.ts`, `frontend/src/lib/browser/web-socket.ts`, `observers.ts`, and terminal-related tests.
- Create: `frontend/src/lib/components/terminal/terminal-session.ts`, `terminal-session.test.ts`, `frontend/src/lib/components/terminal/terminal-attachment.ts`.

**Interfaces:**

- Produces: `terminalAttachment(runtime, options): Attachment<HTMLElement>` which starts one scoped terminal session and synchronously interrupts it on detach.

- [ ] **Step 1: Add failing resource-release tests**

Test owned outcomes: detach disposes xterm/addons once, disconnect closes WebSocket and drops unsent drag state, observer finalizers disconnect, clipboard authority revocation interrupts browser-to-loopback fallback, reconnect cancellation reaches xterm as bytes, and interruption during connect closes a pre-open socket.

- [ ] **Step 2: Implement one scoped terminal session**

```ts
import type { Attachment } from "svelte/attachments";
import { Effect } from "effect";
import type { AppRuntime } from "../../app/runtime.js";
import { openTerminalSession } from "./terminal-session.js";

export const terminalAttachment =
  (runtime: AppRuntime, options: TerminalSessionOptions): Attachment<HTMLElement> =>
  (node) => {
    const execution = runtime.runCommand(Effect.scoped(openTerminalSession(node, options)), {
      operation: "terminal.session",
      safeContext: { surface: "workspace-terminal" },
      onFailure: options.onFailure,
    });
    return execution.interrupt;
  };
```

`openTerminalSession` acquires xterm, addons, WebSocket, observers, event streams, animation-frame work, and clipboard authority through finalizers. `execution.interrupt()` synchronously requests interruption without starting another fiber; the managed runtime retains completion, and tests await `execution.await` before asserting finalizers. DOM callbacks publish typed events into a queue; they do not start Promise chains.

- [ ] **Step 3: Replace component lifecycle plumbing**

Read `const runtime = getAppRuntime()` once during `XtermTerminalPane.svelte` initialization, then use `{@attach terminalAttachment(runtime, options)}`. Delete manual arrays of disposables, socket reconnect timers, resize timeout chains, observer cleanup flags, and callback-owned Promise fallbacks. Retain presentation-only `tick` and synchronous key/pointer validation where they meet the design exceptions.

- [ ] **Step 4: Verify unique browser boundaries and commit**

Run terminal unit tests, Vitest browser terminal suites, and Playwright tests that uniquely cover canvas/xterm, WebSocket, pointer drag, geometry, clipboard, and real server navigation. Run autofixer on `XtermTerminalPane.svelte`. Commit with subject `refactor: tie terminal resources to one Effect scope`.

### Task 18: Complete the Raw-Async Audit and Update Durable Context

**Files:**

- Modify: every remaining production file reported by the audit that is not an approved exception.
- Modify: `context/ui-design-system.md`, `context/ui-interaction-contracts.md`, `context/error-handling.md`, `context/retries-and-backoffs.md`, `context/kata-mode.md`, `context/docs-mode.md`, `context/workspace-runtime-lifecycle.md`, and `CLAUDE.md` only where the context-sync grep test shows a durable path/invariant changed.
- Delete: obsolete async helpers and cleanup registries identified by reference search.

**Interfaces:**

- Produces: an audit artifact at `tmp/effect-frontend-async-audit.txt` for review only, not a committed test.

- [ ] **Step 1: Generate the complete production inventory**

```bash
mkdir -p tmp
rg -n '\bPromise\b|\basync\b|\bawait\b|fetch\(|setTimeout\(|setInterval\(|queueMicrotask\(|requestAnimationFrame\(|\bWorker\(|\bSharedWorker\(|\.terminate\(|EventSource|WebSocket|ResizeObserver|MutationObserver|IntersectionObserver|addEventListener\(|\.then\(|\.catch\(|\.finally\(|\$effect(?:\.pre)?\(|\bonMount\(|\bonDestroy\(|\bbeforeUpdate\(|\bafterUpdate\(|\.on\(|\.onData\(|\.subscribe\(|\bregister[A-Za-z0-9_]*\(' \
  frontend/src packages/github-app-ui/src \
  --glob '!frontend/src/test/**' --glob '!frontend/src/lib/testing/**' \
  --glob '!**/*TestHost.svelte' --glob '!**/*Harness.svelte' --glob '!**/*Fixture*' \
  --glob '!**/docsTestBackend.ts' --glob '!**/viewWorkspaceTestDoubles.svelte.ts' \
  --glob '!**/inlineWorkspaceTestController.svelte.ts' \
  --glob '!**/*.test.*' --glob '!**/*.spec.*' --glob '!**/*.browser.*' \
  --glob '!**/*.bench.*' --glob '!**/generated/**' \
  > tmp/effect-frontend-async-audit.txt
```

- [ ] **Step 2: Classify every line and migrate all non-exceptions**

Allowed classifications are only: generated client internals called through Effect transport; required code-split dynamic import; browser/Svelte callback forming the boundary of an owned attachment or stream; synchronous Svelte event handler; presentation-only `tick`. For every other line, move work into an Effect program or boundary adapter and delete the old orchestration.

The existing `pierre-worker-pool.ts` singleton is not an exception: replace it with a scoped Effect worker-pool service whose finalizer terminates every owned worker, and make the diff surface acquire that service through the application runtime.

- [ ] **Step 3: Search for forbidden architecture remnants**

```bash
rg -n 'ManagedRuntime\.make|Effect\.runPromise|Effect\.runFork' frontend/src packages/github-app-ui/src \
  --glob '!frontend/src/test/**' --glob '!frontend/src/lib/testing/**' \
  --glob '!**/*TestHost.svelte' --glob '!**/*Harness.svelte' --glob '!**/*Fixture*' \
  --glob '!**/docsTestBackend.ts' --glob '!**/viewWorkspaceTestDoubles.svelte.ts' \
  --glob '!**/inlineWorkspaceTestController.svelte.ts' \
  --glob '!**/*.test.*' --glob '!**/*.spec.*' --glob '!**/*.browser.*' \
  --glob '!**/*.bench.*' --glob '!**/generated/**'
rg -n 'AbortController|stopPolling|disconnect\(\)|retryFailures|reconnectDelay|cleanup[A-Z].*\[|ForgeClient|@kenn-forge/ui|packages/ui' frontend/src packages/github-app-ui/src \
  --glob '!frontend/src/test/**' --glob '!frontend/src/lib/testing/**' \
  --glob '!**/*TestHost.svelte' --glob '!**/*Harness.svelte' --glob '!**/*Fixture*' \
  --glob '!**/*.test.*' --glob '!**/*.spec.*' --glob '!**/*.browser.*' \
  --glob '!**/*.bench.*' --glob '!**/generated/**'
rg -n '\bany\b|\bnamespace\b|\bas\s+' frontend/src packages/github-app-ui/src \
  --glob '!frontend/src/test/**' --glob '!frontend/src/lib/testing/**' \
  --glob '!**/*TestHost.svelte' --glob '!**/*Harness.svelte' --glob '!**/*Fixture*' \
  --glob '!**/*.test.*' --glob '!**/*.spec.*' --glob '!**/*.browser.*' \
  --glob '!**/*.bench.*' --glob '!**/generated/**' > tmp/effect-frontend-type-safety-audit.txt
```

Expected: one main `ManagedRuntime.make`; GitHub App has one scoped entrypoint runner; other `run*` occurrences are documented host/attachment boundaries. No removed wrapper/package or bespoke retry/cleanup state remains. Review the type-safety audit line by line: module import/export aliases may remain, but no migrated production declaration may use `any`, declare a namespace, or use `as` as an assertion. Narrow external values through schemas, predicates, or typed constructors instead of suppressing audit findings.

- [ ] **Step 4: Run context sync for changed areas**

Execute `context-sync --changed --base main`, apply only clear durable updates, then run:

```bash
scripts/context-sync --check
```

- [ ] **Step 5: Verify and commit final cleanup**

Run affected focused tests plus complete unit/browser suites and autofixer for all remaining touched Svelte files. Use subject `refactor: remove the last bespoke frontend async paths`.

### Task 19: Run Top-of-Stack Verification and Close Internal Work

- [ ] **Step 1: Run the complete production and frontend lanes**

```bash
bun install --frozen-lockfile
bun run effect:prepare
make api-generate
git diff --exit-code -- frontend/openapi/openapi.yaml frontend/src/lib/api/generated internal/apiclient
bun run --cwd frontend test
bun run --cwd frontend test:browser
bun run check
make frontend
bun run --cwd frontend test:e2e:mock
make test-e2e
GO_TEST_P=4 make test
make guardrail-check
```

Expected: every command passes after the final edit. Do not poll GitHub Actions.

- [ ] **Step 2: Review the final audit manually**

Read `tmp/effect-frontend-async-audit.txt` line by line and confirm each surviving occurrence has one approved classification. Do not add a source-grep test asserting deleted code stays deleted.

- [ ] **Step 3: Capture terminal/workspace visible evidence**

Use `capture-playwright` with the real seeded backend for a workspace terminal state and upload with `gh image`.

- [ ] **Step 4: Close PR 4 child, then the parent**

```bash
pr4_search="$(kata search --workspace /tmp --project forge \
  'Effect terminal resources and final async audit' --json)"
pr4_matches="$(printf '%s' "$pr4_search" | jq -c \
  '[.results[].issue | select(.title == "Effect terminal resources and final async audit" and .status == "open")]')"
test "$(printf '%s' "$pr4_matches" | jq 'length')" -eq 1
pr4_ref="$(printf '%s' "$pr4_matches" | jq -er \
  '.[0].short_id | select(type == "string" and length > 0)')"
kata close --workspace /tmp --project forge "$pr4_ref" --done \
  --message 'Migrated workspace and terminal resources, completed the production async audit, and passed the full frontend, Playwright, Go, and guardrail suites.' \
  --commit "$(git rev-parse HEAD)" --test 'GO_TEST_P=4 make test' --agent

parent_search="$(kata search --workspace /tmp --project forge \
  'Migrate Kenn Forge frontend async orchestration to Effect' --json)"
parent_matches="$(printf '%s' "$parent_search" | jq -c \
  '[.results[].issue | select(.title == "Migrate Kenn Forge frontend async orchestration to Effect" and .status == "open")]')"
test "$(printf '%s' "$parent_matches" | jq 'length')" -eq 1
parent_ref="$(printf '%s' "$parent_matches" | jq -er \
  '.[0].short_id | select(type == "string" and length > 0)')"
kata close --workspace /tmp --project forge "$parent_ref" --done \
  --message 'Completed and verified all four Effect migration stack layers; every child issue is closed with commit and test evidence.' \
  --commit "$(git rev-parse HEAD)" --test 'make test-e2e' --agent
```

## Stack Submission and PR Descriptions

### Task 20: Prepare, Unslop, Submit, and Verify the Four Draft PRs

**Files:**

- Create transiently outside Git: `/tmp/effect-pr1-original.md` through `/tmp/effect-pr4-final.md`.
- No tracked source changes.

**Interfaces:**

- Produces: four draft PRs with bases matching stack order and short plain-language bodies.

- [ ] **Step 1: Draft bodies with visible artifact links**

Use these facts, adding the uploaded image URL to each relevant body:

```markdown
- Moved the shared UI source into the frontend and kept generated API types in the frontend tree.
- Added the Effect runtime and migrated GitHub App setup timing and fetch work.
```

```markdown
- Migrated startup, settings, provider loading, and provider mutations to Effect.
- Removed the provider wrapper and old client/store wiring.
```

```markdown
- Migrated Kata, Roborev, Docs, and repository browser workflows to Effect.
- Scoped streams, reconnects, and feature cleanup to their owners.
```

```markdown
- Migrated workspace and terminal resource lifetimes to Effect.
- Removed the remaining custom frontend async orchestration.
```

No body may contain a test plan, checklist, implementation walkthrough, marketing language, or Kata reference.

- [ ] **Step 2: Run the crisp unslop pipeline for each body**

Save the four drafts as `/tmp/effect-pr1-original.md` through `/tmp/effect-pr4-original.md`. Invoke the `unslop` skill with `--preset crisp --strict` once per draft and save its reconstructions as the matching `-final.md` files. Then run:

```bash
unslop_root="${HOME}/.agents/skills/unslop"
for pr_index in 1 2 3 4; do
  original="/tmp/effect-pr${pr_index}-original.md"
  final="/tmp/effect-pr${pr_index}-final.md"
  python3 "$unslop_root/scripts/extract_constraints.py" "$original"
  python3 "$unslop_root/scripts/banned_phrase_scan.py" < "$final"
  python3 "$unslop_root/scripts/validate_preservation.py" "$original" "$final"
  python3 "$unslop_root/scripts/readability_metrics.py" < "$final"
  python3 "$unslop_root/scripts/diff_check.py" "$original" "$final"
done
```

Score each final body against `references/rubric.md`; require at least 32/40.

- [ ] **Step 3: Run the identifier and private-data gates before publication**

```bash
set -euo pipefail
migration_titles=(
  'Migrate Kenn Forge frontend async orchestration to Effect'
  'Effect frontend foundation and source consolidation'
  'Effect provider and application workflows'
  'Effect feature streams'
  'Effect terminal resources and final async audit'
)
identifier_file="$(mktemp)"
for title in "${migration_titles[@]}"; do
  search_json="$(kata search --workspace /tmp --project forge "$title" --json)"
  matches="$(printf '%s' "$search_json" | jq -c --arg title "$title" \
    '[.results[].issue | select(.title == $title)]')"
  test "$(printf '%s' "$matches" | jq 'length')" -eq 1
  internal_id="$(printf '%s' "$matches" | jq -er \
    '.[0].short_id | select(type == "string" and length > 0)')"
  printf '%s\n' "$internal_id" >> "$identifier_file"
done
test "$(sort -u "$identifier_file" | wc -l | tr -d ' ')" -eq 5
for body in /tmp/effect-pr{1,2,3,4}-final.md; do
  while IFS= read -r internal_id; do
    if rg -F "$internal_id" "$body" >/dev/null; then
      printf 'internal identifier found in PR body\n' >&2
      exit 1
    fi
  done < "$identifier_file"
done
```

Invoke the repository's scrub-private-data skill/workflow over the diff, commit range, titles, and final bodies. Remove any unnecessary private name or infrastructure detail.

- [ ] **Step 4: Submit the stack as drafts non-interactively**

```bash
gh stack submit --auto --remote origin
gh stack view --json > /tmp/effect-stack.json
jq -e '.branches | length == 4' /tmp/effect-stack.json
```

- [ ] **Step 5: Replace generated titles and bodies immediately**

Use concise titles with no internal identifiers:

```text
Keep the frontend in one tree and add its Effect runtime
Move provider and application workflows to Effect
Move feature streams and mutations to Effect
Move terminal resources to Effect and finish the async cutover
```

Apply them from the machine-readable stack output:

```bash
jq -r '.branches[] | select(.pr != null) | [.name, (.pr.number | tostring)] | @tsv' /tmp/effect-stack.json |
while IFS=$'\t' read -r branch pr_number; do
  case "$branch" in
    effect-runtime-foundation)
      title='Keep the frontend in one tree and add its Effect runtime'
      body=/tmp/effect-pr1-final.md
      ;;
    effect-provider-workflows)
      title='Move provider and application workflows to Effect'
      body=/tmp/effect-pr2-final.md
      ;;
    effect-feature-streams)
      title='Move feature streams and mutations to Effect'
      body=/tmp/effect-pr3-final.md
      ;;
    effect-terminal-resources)
      title='Move terminal resources to Effect and finish the async cutover'
      body=/tmp/effect-pr4-final.md
      ;;
    *)
      printf 'unexpected stack branch: %s\n' "$branch" >&2
      exit 1
      ;;
  esac
  gh pr edit "$pr_number" --title "$title" --body-file "$body"
done
```

This edits PR metadata, not a review comment, so do not append the GitHub comment attribution footer.

- [ ] **Step 6: Verify the remote stack and public text**

```bash
gh stack view --json
for pr in $(jq -r '.branches[].pr.number' /tmp/effect-stack.json); do
  gh pr view "$pr" --json number,title,body,baseRefName,headRefName,isDraft
done
```

Confirm all four are drafts, each base is the layer below (bottom uses `main`), no text contains an internal identifier, and every UI-changing PR contains its uploaded visual link. Do not watch or poll Actions.

## Lower-Layer Fix Procedure

If later work reveals a lower-layer defect, use this exact non-interactive workflow:

Choose the exact target from `effect-runtime-foundation`, `effect-provider-workflows`, or `effect-feature-streams`, then run the command with that literal name. For example, a provider-layer fix uses:

```bash
gh stack checkout effect-provider-workflows
# Implement the fix, run its listed verification, complete context-sync, scan identifiers, and create a new commit.
gh stack rebase --upstack
gh stack top
gh stack push --remote origin
gh stack view --json
```

Never hide the dependency in a higher PR, amend a commit, or run an interactive `gh stack` command.
