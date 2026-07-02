# CI Status Redesign

## Goal

Redesign the CI status surfaces in middleman so successes, failures, skips, and pendings all read tastefully and informatively at a glance. A single PR usually has multiple CI states coexisting — a typical mix is something like 1 failed, 5 pending, 18 passed, 2 skipped — and the current chip flattens that into a single dominant status with a misleading total count. The redesign keeps every state visible across the chip, the dropdown panel, and the sidebar indicator, using one shared visual vocabulary.

The motivating bug: the chip displays `CI: failure (26)` for a PR where only 1 of 26 checks failed. The number reads as "26 failed", which is wrong. Fixing only that count would leave the chip honest but uninformative — the user has to expand the dropdown to learn anything beyond "something failed". The redesign solves both: counts that tell the truth and a layout that surfaces the truth without a click.

## Current Behavior

`packages/ui/src/components/detail/CIStatus.svelte` renders one Chip with text `CI: <backend-derived status> (<total checks>)`. The Chip's color is set by the backend-derived `ciStatus` string (`success` → green, `failure`/`error` → red, `pending` → amber, else muted). The number in parentheses is always `checks.length` regardless of status, which is why a PR with 1 failed and 25 passed checks renders as `CI: failure (26)`.

The expanded dropdown groups failed checks at the top under a `Failed (N)` heading, then dumps the remaining checks in source order. Each row uses ASCII glyphs for the per-check status (`✓` / `✗` / `–` / `◦`) except for in-progress checks, which use the Lucide `loader-circle` spinner. There is no header summary; cancelled, timed-out, action-required, stale, and startup-failure conclusions fall into the `?` glyph bucket.

`packages/ui/src/components/sidebar/PullItem.svelte` shows a single tiny SVG icon at the right edge of each row — a check for `success`, an X for `failure`, a small filled dot for `pending`, hidden otherwise. The icon reflects the same backend-derived `ciStatus` string. A separate PR state dot (open/draft/closed/merged) sits at the start of the title and is unrelated to CI; the CI redesign does not touch it.

## State Taxonomy

The redesign introduces a single client-side bucketing function used by every CI surface. Each check belongs to exactly one bucket. The classification order is fixed so that ambiguous and partial data lands somewhere safe:

1. If `status` ∈ {`in_progress`, `queued`, `pending`, `waiting`}: **Pending** (covers GitHub's `in_progress`, GitLab's `in_progress`/`queued`, gitealike's `pending`).
2. Else if `conclusion` is one of the Failed values listed below: **Failed**.
3. Else if `conclusion` = `success`: **Passed**.
4. Else if `conclusion` ∈ {`skipped`, `neutral`}: **Skipped**.
5. Else if `conclusion` is a non-empty string (anything not matched above): **Unknown** — this is the new-conclusion case the warning system targets.
6. Else (empty/missing `conclusion` and a non-active `status`): **Pending** — defensive default for active-but-unclassifiable data. This is the only path that lands a check in Pending without an explicit active status, and it activates only when conclusion is also absent.

`conclusion`-based buckets:

| Bucket  | Conclusion values                                                                       |
| ------- | --------------------------------------------------------------------------------------- |
| Failed  | `failure`, `cancelled`, `timed_out`, `action_required`, `stale`, `startup_failure`      |
| Passed  | `success`                                                                               |
| Skipped | `skipped`, `neutral`                                                                    |
| Unknown | a non-empty `conclusion` value that doesn't match any of the above                      |

`Unknown` covers values the frontend has not been taught about (a new GitHub conclusion, a non-GitHub provider's value that slipped past normalization). It only fires at step 5 when there is a non-empty `conclusion` value we don't recognise — not when conclusion is simply absent (step 6 sends those to Pending). Unknown checks render in their own state token with a distinct icon and color (see below); they do **not** roll into the Skipped count, because a new failure-like conclusion incorrectly counted as Skipped would silently hide a regression. A single `console.warn` per distinct unrecognised value per page-load keeps the gap observable for diagnosis.

Each bucket has one Lucide icon, one color token, and one accessible label fragment:

| Bucket  | Lucide icon                                                                                | Color             | aria-label fragment (count=1 / count>1) |
| ------- | ------------------------------------------------------------------------------------------ | ----------------- | --------------------------------------- |
| Failed  | `circle-x`                                                                                 | `--accent-red`    | "1 failed check" / "N failed checks"    |
| Pending | `loader-circle` (animated) in the detail chip and dropdown; `circle` (static) in the sidebar | `--accent-amber` | "1 pending check" / "N pending checks" |
| Passed  | `circle-check`                                                                             | `--accent-green`  | "1 passed check" / "N passed checks"    |
| Skipped | `circle-minus`                                                                             | `--text-muted`    | "1 skipped check" / "N skipped checks"  |
| Unknown | `circle-help`                                                                              | `--accent-purple` | "1 unknown check" / "N unknown checks"  |

The animated `loader-circle` respects `prefers-reduced-motion: reduce` only on the surfaces that animate it (detail chip and dropdown). The sidebar uses the static `circle` unconditionally — `prefers-reduced-motion` is irrelevant there because there is no animation to disable. The chip/dropdown swap is implemented in Svelte, not CSS: the Pending token reads a `prefersReducedMotion` boolean from a small reactive helper backed by `window.matchMedia("(prefers-reduced-motion: reduce)")` and renders `circle` instead of `loader-circle` when the helper is true. Tests stub `matchMedia` to assert each branch renders the correct Lucide component.

## State Token Vocabulary

A state token is the shared atom across every CI surface: a Lucide icon plus a tabular-nums count, both in the bucket color. Tokens are emitted only for non-zero buckets so a uniformly-green PR shows one token, not four. The Pending token's loader spins in the chip and dropdown but stays static in the sidebar.

A small shared component `CITokenCluster.svelte` renders the cluster — both `CIStatus.svelte` and `PullItem.svelte` consume it, passing the bucketed structure and a size prop (`default` or `compact`). The cluster owns the token order, the aria-label composition, the `data-testid` markup, and the reduced-motion swap for the Pending token, so vocabulary stays single-sourced and the two consumers cannot drift.

Two semantic sizes are formalized rather than hard-coded per surface:

- Default token: chip-text sized, inheriting from `--font-size-xs` (today's chip text scale) — used inside the detail chip's count cluster.
- Compact token: smaller than the chip token by one step, used in the sidebar mini cluster.

The dropdown panel does not use the cluster vocabulary internally. Each dropdown row keeps its existing layout and uses the section's Lucide icon at the dropdown's existing icon size, so list rows stay readable without inheriting the chip's tighter sizing.

Token order in clusters is fixed: Failed, Pending, Unknown, Passed, Skipped. This order matches severity, not data order, so the eye reaches the most actionable state first. Unknown sits between Pending and Passed because it warrants investigation but isn't a confirmed failure.

Tokens carry a non-color cue (their icon shape) and a numeric count. Color reinforces meaning but is never the sole carrier. `CITokenCluster.svelte` does not own its own focusable wrapper or `aria-label` — instead it exposes a `composeAriaLabel(bucketed: CIBucketedChecks): string` helper and the consumer (the chip's `<button>`, the sidebar row's `<button>`) sets that string as its own `aria-label`. This way the interactive parent's accessible name carries the full CI breakdown ("CI: 1 failed checks, 5 pending, 18 passed, 2 skipped"); the cluster's children are marked `aria-hidden="true"` so screen readers don't read them twice. Wording uses **"N failed checks"** (not "N failing") so the count covers required and non-required failures alike without falsely implying merge-blocking. Same neutral wording for other buckets: `"5 pending checks"`, `"18 passed checks"`, `"2 skipped checks"`, `"1 unknown check"`. The cluster reads naturally for zero, one, or many counts — singular "check" is used only when the count is exactly 1.

Each token in the cluster carries a stable `data-testid` so component and e2e tests can assert it without relying on icon classes or visible text shape: `data-testid="ci-token-failed"`, `ci-token-pending`, `ci-token-unknown`, `ci-token-passed`, `ci-token-skipped`. Tests assert "is this token present?" via the test-id and "what count does it show?" via the test-id's text content (the count number is the only direct text inside the token).

**Unknown count display rule.** Unknown checks render as their own token in the cluster and their own section in the dropdown. Counts are reported as-is (`unknown.length`) — no rolling into Skipped, no implicit reclassification. The aria-label fragment "N unknown" appears in the combined cluster label when present. Per-row tooltips for unknown checks still include the raw conclusion value.

## Detail Chip

The chip stays in its current home — the horizontal chips-row alongside State, ReviewDecision, DiffSummary, Worktree, and label badges. It keeps its 22px minimum height, 10px corner radius, and chevron affordance.

The chip's shell is neutral: `--bg-inset` background, `--border-muted` border, `--text-muted` body color, `--text-secondary` for the leading `CI` label. The shell is not tinted by the worst state — colors live in the tokens. This is intentional. Tinting by the worst state collapses a 1-of-26 failure back into "everything is red", which is exactly the misleading signal the redesign exists to fix.

Inside the chip, in order: the `CI` label, the default-size state tokens for every non-zero bucket in severity order, and the existing chevron. The tokens scale with the chip's existing typography variables, so the mobile preset and desktop preset both inherit correctly. Clicking the chip toggles the dropdown's expanded state exactly as it does today; tokens are not individually clickable in v1.

On narrow widths the chip behaves like every other chip in the row: the row's `flex-wrap: wrap` already handles overflow. The chip itself does not truncate its tokens. When the row wraps, the CI chip lands on its own row beneath the State chip and the dropdown still anchors below it.

The chip renders one of three states based on parse and bucket results:

- **Normal**: `parseError` is `null` and `bucketed.all.length > 0` — render the token cluster as described.
- **Hidden**: `parseError` is `null` and `bucketed.all.length === 0` — the PR has no CI yet; nothing to show. `CIStatus` value alone never triggers a render because the redesigned chip has no status-only visual to fall back to.
- **Unavailable**: `parseError` is non-null — render a muted-shell chip reading `CI: unavailable` with the chevron hidden. The unavailable chip uses a `<span role="button" tabindex="0" aria-disabled="true" aria-describedby="ci-unavailable-desc-<n>">` element (not a native `<button disabled>` — `disabled` removes focusability, hiding the tooltip from keyboard users). A visually hidden `<span id="ci-unavailable-desc-<n>" class="sr-only">` carries the parse error summary so screen readers announce "CI unavailable: <error>" when the chip receives focus. The `sr-only` utility uses the standard 1px clip pattern (not the `hidden` attribute, which some screen readers skip even with `aria-describedby`); the project's existing visually-hidden CSS is reused if one exists, else added to the cluster component's scoped styles. Sighted users see the same text in a `title` attribute tooltip. Click and Enter/Space handlers no-op on the chip (no expand action). The unavailable case never falls back to Hidden because a sync or serialization bug must never make failing CI look like no CI.

The "Unavailable" state is shape-faithful: it uses the same chip shell so layout stays stable, but it suppresses interaction and replaces the cluster with text. The malformed-JSON warning fires from a `$effect` watching `parseError`.

## Dropdown Panel

The expanded panel keeps its current outer styling — inset surface, rounded corners, scroll-on-overflow, `min(340px, 50vh)` max-height. Three structural changes go in:

1. A summary header at the top of the panel shows the total check count and the longest single check duration: `<total> checks · longest <duration>`. A check counts as "completed" for the longest-duration calculation when its `status` is exactly `"completed"` **and** its normalized `duration_seconds` (after the parser's coerce/clamp step) is a finite non-negative number. The "longest <duration>" segment renders only when at least one such check exists; otherwise the header shows just the total. The longest-check value comes from `max(duration_seconds across completed checks)`; in-progress checks and checks with no usable duration contribute nothing. The header is not a fake wall-clock total — it is honestly framed as the longest check, which is the useful signal for triaging slow CI.

2. Checks render in up to five sections instead of one. Sections appear in fixed order: Failed, Pending, Unknown, Passed, Skipped. Each section's heading is `"<Bucket name> (N)"` in the bucket color, using the same uppercase, letter-spaced typography as the current `Failed (N)` heading. Sections render only when they contain at least one check.

3. Within each section, rows keep their current layout (icon, name, optional duration, optional app, optional GitHub link). The icon switches from ASCII glyphs to the same Lucide icon as the section's heading. Failed, Pending, and Unknown sections render every row (these are all actionable buckets). Passed and Skipped sections render their first 8 rows and append a `"Show N more <bucket>"` affordance when the section has more. The affordance is a real `<button>` with `aria-expanded` that toggles: clicking when collapsed expands the remaining rows inline and the label changes to `"Show fewer <bucket>"`; clicking again collapses back to the first 8. Keyboard activation is Enter/Space, and the focus ring matches other interactive panel elements.

The `detailLoaded` and `detailSyncing` loading affordances stay as today. The "Show N more" affordance is purely local UI state; it does not persist across reopens and does not affect the chip's counts. When the consumer's PR changes (e.g. the user navigates between PRs while the same `CIStatus.svelte` instance is mounted), the local expansion state resets — the dropdown's expansion-tracking object is keyed by `pr.PlatformExternalID` so stale "Show more" state cannot leak from one PR to another.

## Sidebar Indicator

The sidebar PR row replaces its single 10×10 SVG with a small state-token cluster sized to the existing right-edge cluster. Tokens use the compact size. The cluster appears in fixed severity order, hides empty buckets, and uses the static `circle` for the Pending token to avoid row-by-row spinner noise. Hidden entirely when the PR has no CI.

The sidebar row's existing `<button>` receives the same `composeAriaLabel` output as the chip's button, so the row's accessible name includes the CI breakdown ("Open `<title>`. CI: 1 failed check, 5 pending checks, ..."). When `parseError` is non-null, the sidebar renders a single muted `circle-alert` token and the row's aria-label appends "CI unavailable: <parse error summary>" so keyboard/screen-reader users get the same diagnostic that the chip's focusable tooltip carries. The cluster itself does not capture clicks — interaction stays at the row level.

On narrow widths the row already uses fixed-edge spacing; the cluster respects that by tightening its inter-token gap when scoped under the existing `.mobile-main` ancestor class — the same class selector that already shapes phone-first sizing across `PullItem.svelte`. No collapse, no inline summary — preserving every count is what the redesign exists to do.

The existing PR state dot (open/draft/closed/merged) at the start of the title is unchanged. It encodes information CI tokens do not.

## Data Flow

`CIStatus.svelte` and `PullItem.svelte` consume the same client-side bucketing helper. The sidebar's data contract is already in place: the generated API schema (`packages/ui/src/api/generated/schema.ts`) shows `CIChecksJSON: string` on the `MergeRequest` payload that powers the pull-list response, so `PullItem.svelte` reads the same field the detail view reads. The redesign does not require any backend changes to surface the per-check data the sidebar cluster needs.

The helper is a pure function exported from a new module under `packages/ui/src/utils/ci-buckets.ts`:

```ts
export type CIBucket = "failed" | "pending" | "passed" | "skipped" | "unknown";

export interface CIBucketedChecks {
  failed: CICheck[];
  pending: CICheck[];
  passed: CICheck[];
  skipped: CICheck[];
  unknown: CICheck[];
  all: CICheck[];
  longestCompletedDurationSeconds: number | undefined;
}

export interface ParsedCIChecks {
  checks: CICheck[];
  error: Error | null;
}

export function parseCIChecks(json: string): ParsedCIChecks;
export function bucketCIChecks(checks: CICheck[]): CIBucketedChecks;
export function bucketForCheck(check: CICheck): CIBucket;
```

`bucketCIChecks` iterates `checks` once, classifies each via `bucketForCheck`, and aggregates the longest completed duration. `bucketForCheck` is a pure function with no side effects (no logging, no module-level state). The unknown bucket is rendered as its own token and dropdown section at the visual layer — never folded into Skipped — and a separate side-effect helper fires the per-page warning.

**Side-effect warnings (unknown + malformed).** Both warning paths live in a single side-effect module `packages/ui/src/utils/ci-buckets-warn.ts`, separate from the pure `ci-buckets.ts`. The module exports two functions:

- `warnOnUnknownConclusions(unknown: CICheck[], context?: { repo?: string; number?: number })` — called from each consumer inside a `$effect` that depends only on the derived bucketed structure. Module-scoped `Set<string>` keys off the raw conclusion value so the warning fires once per distinct unknown per page-load regardless of how many surfaces render it.
- `warnOnMalformedCIChecksJSON(raw: string, error: Error, context?: { repo?: string; number?: number })` — called by the consumer when `parseCIChecks` returns a non-null `error`. The error and raw payload come directly from the parse result, so callers never re-parse. Module-scoped guard keys malformed payloads off `"<repo>#<number>|<rawHash>"` when context is supplied, falling back to `"|<rawHash>"` otherwise, where `<rawHash>` is a short FNV-1a hash of the raw payload (no large strings retained, bounded memory across long sessions). Two different PRs with identical malformed payloads each get one warning, but the same payload re-rendered for the same PR only warns once. The warning message includes the parse error and the PR identifier (when supplied) so a long sidebar list can trace to the offending row; the raw payload is logged at `console.warn` time **truncated to 512 characters** (with an `…<N more chars>` suffix when truncated) so a runaway payload cannot dump a multi-megabyte string into the console, and is not retained in the Set afterwards.

**Cross-surface warning behavior.** When the same PR renders in both the detail and the sidebar (the common case during navigation), both components fire the warning helpers, but the module-scoped Set keys mean the second call is a no-op for both unknown and malformed cases. The detail surface always supplies a `context` (`pr.repo_owner/pr.repo_name#pr.Number`); the sidebar supplies the same context where available. The Set entries dedupe regardless of which surface fired first.

Both functions expose a `__resetCIWarnings()` test helper that clears their internal sets; `afterEach` in component tests calls it so ordering stays stable. The helpers call `console.warn` unconditionally regardless of build mode — middleman's frontend currently logs the same way through `dev` and `production` builds, and these warnings are diagnostic signal that we want available wherever the app runs (a single warn-per-payload-per-session is not noisy by design).

The backend-derived `pr.CIStatus` string is no longer consumed by any redesigned UI surface — not the chip, not the sidebar cluster, and not the dropdown. Whether to render any CI affordance is decided solely by `parseError === null && bucketed.all.length > 0` (normal) or `parseError !== null` (unavailable). `CIStatus` stays in the API for existing non-CI consumers (`stack_health.go`, sync code paths, kanban rollups, and so on); the redesigned components ignore it entirely.

The legacy CI color/glyph helpers inlined in `CIStatus.svelte` (`checkIcon`, `checkColor`, `chipColor`, `isPendingCheck`) are deleted as part of the chip rewrite. They have no other callers — the chip was the sole consumer of `chipColor`, and the per-row helpers are replaced by the bucketing pipeline. The sidebar's bespoke `ci-icon--success/failure/pending` SVG markup in `PullItem.svelte` is replaced by the shared cluster, and the corresponding CSS classes are deleted with it.

`parseCIChecks` (the JSON-parse-with-fallback helper currently inlined inside `CIStatus.svelte`) moves into the `ci-buckets.ts` module and returns `{ checks, error }`. After Stage 1b adds the cache, the function is deterministic but module-stateful — repeated calls with the same input return cache-hit results. The classification core (`bucketForCheck`, `bucketCIChecks`) remains pure throughout. On parse success **and** shape validation success, `error` is `null` and `checks` is the parsed and shape-normalised array. On parse failure or shape failure, `error` is an `Error` describing the issue and `checks` is an empty array. **Empty input is treated as success**: an empty or whitespace-only `CIChecksJSON` returns `{ checks: [], error: null }` so "no CI" never produces a malformed-JSON warning. Shape validation enforces:

- The top-level parsed value must be an array. Anything else (object, scalar, null) is a malformed payload.
- Every element must be an object. **Any** non-object element fails shape validation — the parser does not silently drop partial elements, because a dropped element could be a failure that the user must see. Failing shape validation yields `{ checks: [], error }` and the consumer renders the "Unavailable" state above.
- For each element, `status` and `conclusion` are coerced via `String(value ?? "")` — non-string values become their string form, missing values become empty strings. This keeps downstream bucketing total without runtime type errors. The coercion is permissive on a per-field basis (a check missing `url` or `app` is fine), only the top-level shape is strict.
- `duration_seconds` is normalized to a finite, non-negative number or `undefined`: values that pass `Number.isFinite` and `>= 0` are kept (with non-number inputs coerced via `Number(value)` first); `NaN`, `Infinity`, negative numbers, and any value that fails the coercion become `undefined`. The longest-check aggregator ignores `undefined`, so an invalid duration never poisons the dropdown summary header.

The function itself remains pure — no logging, no module state — so consumers can inspect `error` to decide whether to fire the malformed-JSON warning without duplicating parse work.

Both `CIStatus.svelte` and `PullItem.svelte` wrap the parse-and-bucket pipeline in a Svelte `$derived` keyed by `pr.CIChecksJSON` so the JSON is parsed at most once per distinct value within one component instance, even when sibling row state (selection, hover, worktree label) changes. The derived result exposes both `bucketed` and `parseError` for downstream effects. Effects that handle side effects (unknown-warning, malformed-warning) react to the derived structure, not to `pr.CIChecksJSON` directly.

For the sidebar list at scale, `parseCIChecks` adds a module-scoped LRU memoization with a byte-aware policy: cap of 256 entries OR 1 MiB total cached payload bytes (whichever ceiling hits first), evicted least-recently-used. Payloads larger than 64 KiB are not cached at all — they parse on every call but never sit in memory. This keeps a runaway malformed payload from inflating the cache and bounds total session memory cost regardless of how many oversized rows show up.

The cache stores the `{ checks, error }` result by reference, **read-only by contract**. Returned `checks` arrays and `CICheck` elements are `Object.freeze`d in development builds so accidental mutation throws at the call site; production builds skip the freeze (the contract is documented and enforced by TypeScript `readonly` modifiers in the public type signature). The bucketing helper does not mutate its input, and consumers must not mutate either. This keeps cache safety predictable across long sessions.

Performance target: parsing cost for a sidebar of 200 PRs with cache warm is dominated by `Map.get` lookups, not `JSON.parse` calls — verifiable by component-test instrumentation that counts `JSON.parse` invocations during a list rerender. The LRU bound caps memory growth across long sessions; bucketing remains a per-call cost (cheap, all O(n) over a typically-small `checks` array), so it is not separately memoized.

The cache exposes `__resetParseCIChecksCache()` for tests; component tests reset it in `afterEach` alongside the warning-helper reset. Tests can also inspect cache state via `__parseCIChecksCacheStats() => { size, bytes, hits, misses }` for the `JSON.parse`-invocation counting and byte-cap targets above. These helpers follow the existing project convention of `__`-prefixed test-only exports (also used in the warning helper and elsewhere in `packages/ui/src/utils/`); they are not removed at production build time but are clearly marked for test use only via the prefix and JSDoc `@internal`.

## Edge Cases

- **No CI (empty payload)**: `CIChecksJSON` is empty or whitespace-only. The chip and sidebar hide; no warning fires (empty is treated as success by `parseCIChecks`).
- **Malformed CI JSON**: `CIChecksJSON` is non-empty but fails to parse. Both surfaces render a visible "CI unavailable" affordance instead of hiding silently, so a sync or serialization bug never makes a failing PR look like it has no CI. In the chip, that's a muted-shell chip reading `CI: unavailable` with the chevron disabled; in the sidebar, that's a single muted `circle-alert` token (one slot wide, no count). Both surfaces carry a tooltip with the parse error summary. The malformed-JSON warning fires once per (PR identifier + payload-hash) pair regardless.
- **Single uniform bucket**: All checks in one bucket. The cluster shows one token; the dropdown shows one section with no header summary segment changes.
- **Pending precedence**: A check with `status: "in_progress"` and `conclusion: "failure"` (a rare race) buckets as Pending, not Failed. The next sync will update it.
- **Unknown conclusion**: A conclusion the frontend doesn't recognise renders in its own Unknown token and dropdown section (color `--accent-purple`, icon `circle-help`). The per-row tooltip in the dropdown shows the raw value. A `console.warn("Unrecognised CI conclusion: <raw>")` fires once per distinct raw value per page-load via the side-effect helper.
- **Detail not yet loaded**: When `detailLoaded` is false, the dropdown shows the existing loading placeholder. Chip and sidebar still render from the available `CIChecksJSON` (which may be sparse). The bucketing helper handles an empty array.
- **Mobile**: `.mobile-main` scoping already exists for PullItem. The chip's token size follows `--font-size-xs` which already adapts. The sidebar cluster's mobile tightening is a single `:global(.mobile-main) .ci-cluster` selector override; no new media query.
- **Reduced motion**: The dropdown's Pending icon falls back from `loader-circle` to `circle` when `prefers-reduced-motion: reduce` is active. The detail chip's Pending token follows the same rule.

## Testing Strategy

End-to-end tests are non-negotiable per the project's testing policy and the existing CI dropdown spec. The redesign adds five layers of coverage:

1. **Unit tests for the bucketing helper** in `packages/ui/src/utils/ci-buckets.test.ts`. Exhaustive coverage of every documented `status`/`conclusion` combination, the precedence rule, the unknown bucket, the longest-duration aggregation, and an empty-input case.
2. **Component tests for `CIStatus.svelte`** in `packages/ui/src/components/detail/CIStatus.test.ts`. The existing tests update to the new chip text and the new dropdown structure. New tests cover:
   - Chip with a mixed-state PR shows exactly the non-zero tokens in severity order.
   - Chip with a single-bucket PR shows exactly one token.
   - Dropdown header shows `<N> checks · longest <duration>` when at least one completed check has a duration, otherwise just `<N> checks`.
   - Failed and Pending sections render every check; Passed and Skipped sections render the first 8 plus a "Show N more" affordance.
   - The "Show N more" affordance, when clicked, reveals the remaining rows.
   - Unknown conclusions render in their own Unknown section with a tooltip carrying the raw value; the Unknown token's count is `unknown.length` and the Skipped token's count is `skipped.length` (no merging between the two).
   - Chip hides when there are zero checks and `CIStatus` is empty.
   - Chip hides when `CIStatus` is non-empty but `CIChecksJSON` is empty (transient state — `CIStatus` must not gate the chip on its own).
   - Chip renders "CI: unavailable" (not hidden) when `CIChecksJSON` is malformed, with chevron disabled and tooltip carrying the parse error summary. `console.warn` for parse failure fires exactly once across multiple re-renders with the same payload.
   - Reduced-motion (`prefers-reduced-motion: reduce`): Pending-token icon renders as a static `circle` rather than animated `loader-circle`. This can be a CSS-only test (asserting the rendered icon's static markup under a matching media query stub) or a snapshot.
3. **Component test for sidebar mini cluster** in `packages/ui/src/components/sidebar/PullItem.test.ts` (new file). Covers: cluster shows the compact tokens for a mixed-state PR, the static `circle` for Pending, is hidden when the PR has no CI, is hidden when `CIStatus` is non-empty but `CIChecksJSON` is empty (the transient case — the redesigned sidebar must not gate on `CIStatus`), and renders a single muted `circle-alert` "unavailable" token when `CIChecksJSON` is malformed.
4. **Playwright e2e** in `frontend/tests/e2e-full/ci-dropdown.spec.ts`. Update existing tests for the new chip text. Add new tests that:
   - Seed a PR with mixed CI states (failure, in_progress, success, skipped, and one unknown conclusion); assert the chip's token cluster matches the bucket counts; open the dropdown; assert the summary header text and the five section headings in fixed order with correct counts; assert that Passed and Skipped sections show "Show N more" when over 8 rows and reveal the rest on click.
   - Seed a PR whose `CIChecksJSON` is malformed (invalid JSON, or valid JSON of the wrong shape); assert the chip renders `CI: unavailable`, is keyboard-focusable, and its tooltip/aria-describedby carries the parse error summary.
   - Seed a PR with non-empty `CIStatus` but empty `CIChecksJSON` (the transient sync state); assert the chip is hidden.
5. **Playwright e2e for the sidebar cluster** in `frontend/tests/e2e-full/pull-list-ci.spec.ts` (new file). Hits the real HTTP API and SQLite-backed pull list with seeded data and asserts:
   - Each row's compact cluster renders from the live list payload for a mixed-state PR.
   - A row's cluster is hidden when its PR has no CI.
   - A row's cluster shows the unavailable token when its `CIChecksJSON` is malformed.
   - A row's cluster is hidden when its `CIStatus` is non-empty but `CIChecksJSON` is empty.

   This is the surface where the "list payload contains `CIChecksJSON`" assumption is load-bearing; component tests alone won't catch a regression there.

## Implementation Staging

The implementation plan (separate document, written next) splits the work into reviewable stages roughly in this order:

1a. Pure `ci-buckets.ts` core (`bucketForCheck`, `bucketCIChecks`, shape-validating + duration-normalizing `parseCIChecks`, types) and its unit tests. No cache, no module state — the helpers in this stage are deterministic and stateless. As part of this stage, verify that the generated API client (`packages/ui/src/api/generated/schema.ts`) still exposes `CIChecksJSON` on the `MergeRequest` payload and that the e2e test fixture builders include it; fix any drift here before later stages depend on it.
1b. Add the LRU memoization layer on top of `parseCIChecks` (byte-aware eviction, frozen returned objects in dev, `__resetParseCIChecksCache()` / `__parseCIChecksCacheStats()` test helpers) and its tests. From this stage forward, `parseCIChecks` is deterministic but module-stateful (cached); `bucketForCheck` and `bucketCIChecks` remain pure. Splits the perf feature from the correctness core so 1a can be reviewed strictly for classification rules.
2. Side-effect `ci-buckets-warn.ts` helper with its own unit tests asserting per-payload and per-context de-duping — Stage 2 lands review-complete instead of waiting on a consumer to exercise it.
3. Shared `CITokenCluster.svelte` (token vocabulary, size variants, the `composeAriaLabel` exported helper, `data-testid` markup, reduced-motion swap) and its component tests, so both downstream consumers share one cluster implementation. This stage scopes only the **normal token cluster** vocabulary; the "unavailable" presentation is rendered by each consumer in its own component using the same shared icon/color primitives, since the chip's "CI unavailable" affordance and the sidebar's single muted token differ enough that a generic shared variant would absorb surface-specific behavior and complicate later changes. This stage also adds a small `prefersReducedMotion()` Svelte helper backing the swap — wraps `window.matchMedia("(prefers-reduced-motion: reduce)")` with SSR/window guards and a `change` listener that cleans up on component teardown — with its own tests covering initial value, media-query change events, and listener cleanup.
4a. `CIStatus.svelte` `$derived` wiring and chip rewrite — wire the parse/bucket pipeline through `$derived`, swap the chip to render the shared cluster (or unavailable variant), and add the unknown/malformed warning effects. The existing dropdown remains visually unchanged in this commit; it continues to read from the new derived data via a small temporary adapter so the chip and dropdown never disagree about what's in the PR. The adapter's mappings: Unknown checks render with the old `?` glyph (the dropdown's current fallback for unrecognised conclusions) and stay sorted in their bucketed order; malformed payloads keep the dropdown in its current "no checks" state (matching the chip's "unavailable" by rendering nothing visible in the dropdown); pending checks already render in the dropdown today, so no change there. The adapter is removed in 4b. Component tests cover chip rendering. **The Playwright tests for the new chip text, mixed-state chip cluster, malformed-CI "unavailable" affordance, and transient-CIStatus hidden case land in this stage**, in `frontend/tests/e2e-full/ci-dropdown.spec.ts`, so the user-visible chip change ships with end-to-end coverage in the same commit.
4b. `CIStatus.svelte` dropdown restructure — replace the temporary adapter with the new summary header + five sections + show-N-more toggle + row icon change + expansion reset on PR change. Component tests for the dropdown land here. **The Playwright tests for the new dropdown sections, summary header, and show-N-more toggle land in this stage**, in the same spec file as Stage 4a, so dropdown structural changes ship with their own e2e coverage in the same commit.
5. `PullItem.svelte` sidebar cluster — combines the sidebar component (using the shared cluster at `compact` size, `$derived` wiring, mobile gap tightening, unavailable handling) and the Playwright list-payload e2e (`frontend/tests/e2e-full/pull-list-ci.spec.ts`) in one commit. The component and its e2e ship together so the load-bearing list-payload assumption is verified end-to-end at stage exit.
6. Final cross-surface e2e cleanup in `frontend/tests/e2e-full/ci-dropdown.spec.ts` — only what didn't land in 4a/4b, such as multi-PR navigation/expansion-reset tests that need both chip + full dropdown structure to exist. The bulk of dropdown e2e coverage lives with the UI it tests.

Each stage lands as its own commit. The 4a/4b split avoids an oversized "rewrite everything" commit by using a temporary adapter to keep chip and dropdown in sync during 4a, removed in 4b. Each of 4a, 4b, and 5 ships its own Playwright e2e in the same commit so user-visible CI behavior never lands without full-stack coverage.

## Success Criteria

The redesign is done when:

- The detail chip never shows a misleading total — a PR with 1 failed of 26 checks displays a Failed token with count 1 (not 26).
- Each CI surface (chip, dropdown, sidebar) renders all non-zero buckets simultaneously for a mixed-state PR.
- No redesigned surface reads `pr.CIStatus` directly; render gating is `parseError === null && bucketed.all.length > 0` for normal display, `parseError !== null` for "CI unavailable" display.
- The sidebar cluster renders from the existing list payload's `CIChecksJSON` with no backend changes.
- Unknown conclusions get their own token/section and a single `console.warn`, not silent reclassification.
- Malformed JSON renders a visible "CI unavailable" affordance (not hidden) and produces a single targeted `console.warn` per (repo+number, payload-hash) pair; empty `CIChecksJSON` does not warn.
- `prefers-reduced-motion: reduce` swaps the Pending icon from animated `loader-circle` to static `circle` in the detail chip and dropdown (the surfaces that animate it). The sidebar already uses the static `circle` unconditionally.
- Playwright e2e seeds mixed CI states on the real HTTP/SQLite stack and asserts both the detail dropdown and the sidebar cluster.

## Out of Scope

- Per-token click affordances on the chip (e.g., clicking a count token jumps to that section in the dropdown). The chip toggles the dropdown as a whole in v1; finer interactions can come later.
- Required vs optional check distinction in the dropdown row. The data is on `CICheck.required` but the redesign does not surface it visually; this is deliberate to keep the design focused.
- Backend changes. The bucketing happens entirely client-side; `CIStatus` derivation stays as today.
- Provider-specific visual treatments. The bucket taxonomy handles all current providers natively — GitLab and gitealike normalize their conclusions into a subset of the same vocabulary already used by GitHub, and the Unknown bucket is the permanent forgiving fallback for anything new. The redesign does not surface provider identity in the CI display (no per-provider icons, badges, or section labels); that remains the repo chip's job elsewhere.
- The kanban board's CI representation. Sidebar and detail are in scope; kanban cards keep their current treatment.
