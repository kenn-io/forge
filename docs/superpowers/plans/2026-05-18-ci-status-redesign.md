# CI Status Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the misleading `CI: failure (26)` chip with a multi-state token cluster shared across the detail chip, dropdown, and sidebar, backed by client-side bucketing of `CIChecksJSON`.

**Architecture:** Runtime product behavior is frontend-only — no backend or schema changes; the `MergeRequest` payload already carries `CIChecksJSON`. Tests-only Go changes land alongside the relevant Playwright specs (new `__e2e/pr-ci-state/*` fixture endpoints in `cmd/e2e-server/main.go` for the redesigned states). A new pure `packages/ui/src/utils/ci-buckets.ts` exposes the parse + classify pipeline; a sibling `ci-buckets-warn.ts` owns side-effect warnings; a new `CITokenCluster.svelte` renders the cluster. `CIStatus.svelte` and `PullItem.svelte` consume them.

**Tech Stack:** TypeScript, Svelte 5 (runes mode), Vitest + @testing-library/svelte for component tests, Playwright for e2e, Lucide Svelte icons, Bun for package management.

**Spec:** `docs/superpowers/specs/2026-05-18-ci-status-redesign-design.md`

**Non-goals (compatibility assumptions):**
- The redesigned chip, dropdown, and sidebar do NOT read `pr.CIStatus` directly — `CIStatus` keeps its non-UI consumers (`stack_health.go`, sync paths) untouched, but the redesigned surfaces gate solely on parsed checks.
- Empty `CIChecksJSON` hides the chip even when `CIStatus` is non-empty (transient sync state); the chip never falls back to status-only rendering.
- The reduced-motion swap is "next-render-aware" via getter reads of `matchMedia.matches`; it does NOT subscribe to live preference changes (see the `prefersReducedMotion` rationale in Task 7). **Acceptance for visual verification:** capture the chip in both motion states by setting the OS preference *before* navigating to the page (or before clicking the chip to expand). If the user toggles the OS-level reduced-motion setting while a view is already mounted and otherwise idle, the loader-circle/static-circle swap will not re-render until something else triggers a re-render. This is intentional — a subscription-based helper is out of scope for v1.
- Provider identity is not surfaced in the CI UI — the repo chip elsewhere already conveys it.
- Required-vs-optional check distinction is not surfaced in v1 (data is available on `CICheck.required` for future work).
- Sidebar diagnostic visibility on malformed CI relies on the row button's accessible name + token `title` attribute, not on a separate focus-visible popover. The detail chip carries the popover treatment because it's the primary diagnostic surface; the sidebar is a triage surface and inherits the row button's focus path. This is intentional — the design trades sidebar-specific keyboard popover affordance for row density.
- The `warnOnUnknownConclusions` helper dedupes the `console.warn` call **globally by raw conclusion value** (truncated to 128 chars, see below), not by PR. So if 50 different PRs ship the same unknown conclusion, only the first triggers a warning, and the context passed in is just for log identification of which PR happened to be first. This is acceptable because the warning is a developer-diagnostic signal, not a per-PR audit trail.
- The conclusion string used as the dedupe key and printed in the log is truncated to a 128-char prefix (with a `…` marker). This bounds the warning helper's memory growth even if a buggy provider sends multi-KB conclusion strings, and bounds the per-log-line size. Truncation is best-effort dedupe: two distinct over-long conclusions that share the same 128-char prefix will collapse into one warning. Real CI conclusions are short identifiers, so this is a defence against pathological input, not a normal-case concern.
- **Trust assumption for the `conclusion` field.** Unlike `CIChecksJSON` (raw JSON that can carry arbitrary provider output and therefore goes through `categorizeParseError` before logging), the `CICheck.conclusion` field is treated as a non-sensitive enum identifier. Every supported provider (GitHub, GitLab, Forgejo, Gitea) normalises `conclusion` to a short identifier from a known taxonomy (`success`, `failure`, `cancelled`, `timed_out`, `action_required`, `skipped`, `neutral`, `stale`, `startup_failure`). The 128-char truncation defends against the pathological case of a provider sending oversized strings; the warning otherwise logs the raw value because it IS the diagnostic. If a future provider ever returned user-visible text as the conclusion (e.g. a free-form failure message), that would itself be a provider bug worth surfacing — the warning would still cap at 128 chars, so log noise is bounded. This is a deliberate trust call, not an oversight.
- **`console.warn` output is local-developer diagnostics, not user-visible telemetry.** middleman is a local-first SPA; nothing collects browser console output. The warnings target the dev console for backend/provider drift debugging, not analytics or telemetry pipelines. The dedupe policy spelled out above is appropriate for that audience: one warning per truncated unknown conclusion value (global, across all PRs) and one malformed-payload warning per `(repo, number, payload-hash)` tuple (per-PR-per-payload). Both keys are signal-rich for a dev investigating a single user's setup but irrelevant for a hypothetical fleet-wide pipeline. If a future deployment adds remote log collection, the unknown-conclusion logging policy should be revisited to use the same category/hash treatment that malformed-JSON warnings use.
- The module-level warning Sets (`warnedUnknown`, `warnedMalformed`) and the parser's byte-aware LRU cache do not have an explicit lifecycle reset for long-running sessions. The combined upper bound is: 1 MiB for the LRU (per Task 4's byte cap) plus, in pathological worst-case, `(number of distinct truncated conclusions × ~150 bytes)` for `warnedUnknown` plus `(number of distinct (repo, number, payload-hash) tuples × ~80 bytes)` for `warnedMalformed`. In normal operation both warning Sets stay tiny — real installs see at most a handful of unknown conclusion values across the lifetime of a session, and malformed payloads are rare and bounded by the number of tracked PRs. If long-running session memory becomes a concern in practice, v2 can swap the Sets for an LRU; v1 keeps the Set for simplicity and uses the truncation cap to defend against the pathological case.
- `--accent-purple` is verified present in `frontend/src/app.css` (light theme line 17, dark theme line 109) — Unknown-bucket purple styling reuses an existing token. The CI components live in `packages/ui` but render only inside the middleman SPA, which loads `frontend/src/app.css`; `packages/ui` is not currently consumed by any other downstream package, so a missing-token fallback in CITokenCluster is not required. If `packages/ui` ever ships standalone, a follow-up should add `color: var(--accent-purple, #8b5cf6)` fallbacks across the cluster tokens.
- **Single diagnostic-sanitisation rule across all surfaces.** The raw `parseError.message` from native `JSON.parse` (or any third-party Error) is **never** rendered or logged in production. Every surface — production console.warn, chip `title`, popover content, sr-only/aria-describedby text, chip `aria-label`, sidebar row `title`, sidebar sr-only text, dropdown panel — routes its diagnostic text through `safeDiagnosticText(parseError)` (UI helper in `ci-buckets.ts`) or `categorizeParseError(error)` (logging helper in `ci-buckets-warn.ts`). Both helpers project native parse errors to a small enum of category strings (e.g. "Malformed JSON (unexpected token)"). Locally-created shape errors (`CIChecksJSON: …`) are content-free and pass through both helpers intact. Development builds (`import.meta.env.DEV`) log the raw `error.message` plus a 64-char input preview for debugging — dev is not the privacy boundary.
- Malformed-warning production log line is `Malformed CIChecksJSON: <category> (length=N, hash=X)` — no `error.message`, no raw payload content. Dev log line adds `error.message` and a 64-char `Preview:` clause. The dedupe key is `(repo, number, payload-hash)`: distinct malformed payloads on the same PR each warn once, and the same malformed payload on different PRs warns once per PR. This intentionally differs from `warnOnUnknownConclusions`' global-per-conclusion key — for malformed JSON the per-PR signal is the actionable diagnostic (you want to know which PRs are broken), whereas unrecognised conclusions are a provider/taxonomy issue where one global warning is enough.
- The FNV-1a hash used in the malformed-warning dedupe key is **best-effort**, not cryptographic. Two distinct malformed payloads could in theory hash to the same value and share a dedupe slot — the user would see one warning instead of two for those payloads. Acceptable for diagnostic signal because (a) the input space of malformed payloads is small and (b) the warning is a hint for backend/provider drift, not an audit trail.
- Duplicate check names and missing names are not deduplicated by the parser — each entry in `CIChecksJSON` becomes one check, in source order. The bucketing helper counts each independently. This matches GitHub's check-runs semantics (a job can have multiple runs).
- Very large `CIChecksJSON` payloads (under the 64 KiB cache cap) parse on every consumer render. The LRU cache hits in the common case (same payload across re-renders), and parse cost for hundreds of small check objects is negligible in practice.
- Long check names and long app names in the dropdown rely on the existing `.ci-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }` truncation rules already in `CIStatus.svelte`. No additional handling is added; long names truncate visually but remain accessible via the row's `title` attribute and the GitHub link.
- Dropdown row counts beyond the Passed/Skipped 8-row threshold rely on the "Show N more" / "Show fewer" toggle and the dropdown's `min(340px, 50vh)` scroll cap. No additional pagination for extreme counts.
- The dropdown "Show N more" / "Show fewer" toggle is a native `<button>` with `aria-expanded`. Keyboard activation is the default `<button>` semantics: Enter and Space both invoke the click handler. Focus styling reuses the project's existing button focus-ring; no custom keyboard shortcuts beyond the default button behavior.
- Warning helpers log unconditionally in dev and production. middleman currently logs the same way across builds and these warnings are diagnostic signal for backend/provider drift — a single warn per (payload-hash, repo+number) per page-load is not noisy in normal operation. No runtime toggle (env var, settings flag, etc.) is planned for v1: the dedupe keys cap output at one log per distinct trigger, the truncation cap bounds line size, and the warnings are user-actionable signal that middleman is showing degraded CI data. If a future installer reports the warnings as noisy in practice (e.g. a long-lived provider bug producing the same warning repeatedly across page loads), v2 can add a `MIDDLEMAN_DISABLE_CI_WARNINGS` env var or a settings entry — but adding one preemptively risks hiding real bugs.
- Existing tests outside the files named in this plan that assert on the legacy `CI: <status> (<total>)` chip text or the legacy ASCII glyphs will need updates. Implementers should grep for `CI:\s*(success|failure|pending|error) \(\d+\)` and for `"✓"|"✗"|"–"|"◦"` after each user-visible commit and migrate any matches to the new chip/aria-label or new section markers.
- Failed, Pending, and Unknown dropdown sections always render every row, no "Show more" truncation. These are the actionable buckets — a user opening the dropdown needs to see everything that needs attention. The "Show more" affordance is intentionally limited to Passed and Skipped, the non-actionable buckets where truncation hides noise.
- Test-only exports (`__resetParseCIChecksCache`, `__parseCIChecksCacheStats`, `__resetCIWarnings`, `__resetPrefersReducedMotion`) ship from the runtime modules but are marked `@internal` in JSDoc and prefixed with `__` per the existing project convention in `packages/ui/src/utils/`. Tests are the only intended caller; production code must not import them. The convention is documented in the spec and enforced socially (no separate test-only build artifact).
- The malformed-chip popover anchors below the chip and may clip the viewport edge if the chip sits at the bottom-right corner. Acceptable v1 limitation — the `title` attribute and aria-describedby still expose the diagnostic when the popover is partially off-screen. Future work can add viewport-edge detection.
- Extremely large `CIChecksJSON` arrays (e.g. 500+ checks) within the 64 KiB cache cap: the LRU parses once and reuses. The dropdown renders Failed/Pending/Unknown rows in full (these are actionable) and Passed/Skipped only their first 8 + "Show N more"; the `min(340px, 50vh)` scroll cap keeps the panel bounded even when Show-N-more is expanded. The cluster rendering is linear in token count (at most 5 tokens), independent of array length. No additional pagination is planned for v1.
- Malformed CI does not directly affect merge-warning affordances. The merge-warning lines in `PullDetail.svelte` (conflict, branch protection, behind) are driven by `pr.MergeableState`, not by `CIChecksJSON`. The "CI unavailable" chip surfaces the diagnostic; merge warnings stay independent.
- Unknown `status` values (anything not in the active set and not `"completed"`) are **not** auto-routed to Pending. Following the spec's 6-step precedence, after the active-status check the classifier falls through to conclusion-based bucketing. So `status: "weird", conclusion: "success"` → Passed; `status: "weird", conclusion: ""` → Pending (step 6's fallback only when conclusion is also empty). This avoids trapping otherwise-resolved checks in Pending just because the provider's status string drifted. Unknown statuses are not warned because the warning system targets unrecognised *conclusions*, where treating an unknown failure-like value as Passed/Skipped would actually be misleading.
- **No whitespace/case normalisation on `status` or `conclusion`.** `bucketForCheck` compares against the active-status `Set` and the failed/skipped-conclusion `Set`s by strict equality. A provider sending `"Success"` (capital S) or `" success "` (with leading/trailing whitespace) won't match `"success"` and will fall through to Unknown. This is intentional for v1: all four supported providers' normalisation layers in `internal/platform/<provider>/` already emit lowercase, trimmed values; any drift surfaces as a provider-side bug we want to see via the unknown-conclusion warning. Normalising silently in the frontend would hide that signal. If a future provider's normalisation layer is found to ship whitespace/case-inconsistent values, the fix belongs in `internal/platform/<provider>/` (so all consumers benefit), not in `bucketForCheck`.
- The new `/__e2e/pr-ci-state/{mixed,malformed,status-only,dropdown-mixed}` endpoints mutate the global PR #1 row in the e2e server's DB. They are safe only under `startIsolatedE2EServer`, which the Playwright specs already use — each spec file gets a fresh SQLite DB seeded from `cmd/e2e-server`'s default fixtures, so cross-spec pollution is impossible. If a future test runner shares one DB across spec files (it doesn't today), the order-dependent shape of these endpoints would surface as flake. The existing `pr-ci-state/pending` and `pr-ci-state/success` endpoints already rely on this isolation; the new endpoints follow the same contract.

For the full spec including state taxonomy, edge cases, and accessibility contract, see the spec doc above.

## Acceptance matrix

The redesign is correct when each payload shape produces the expected output on every surface:

| Payload shape                                                                                   | Chip                                             | Sidebar token                                 | Dropdown                                          |
| ----------------------------------------------------------------------------------------------- | ------------------------------------------------ | --------------------------------------------- | ------------------------------------------------- |
| Empty (`""` or whitespace)                                                                      | Hidden                                           | Hidden                                        | Hidden                                            |
| Malformed (non-empty, parse-fails)                                                              | "CI: unavailable", focus-visible popover         | `circle-alert` token (amber/warning, see below), row a11y carries error | Hidden (chip's panel skips when `isUnavailable`)  |
| All passed (any positive count of successes)                                                    | One Passed token with the count                  | One Passed compact token with the count       | Passed section (first 8 + "Show N more" when >8); illustrative case in docs uses 26 |
| Mixed-small — 1 failed / 1 pending / 2 passed / 1 skipped (Task 10 `mixed` fixture)             | Tokens F/Pe/Pa/Sk with counts                    | Same vocab, compact size                      | All non-zero sections in fixed order              |
| Mixed-large — 1 failed / 5 pending / 12 passed / 2 skipped / 1 unknown (Task 12 `dropdown-mixed` fixture) | Tokens F/Pe/U/Pa/Sk with counts                  | Same vocab, compact size                      | Passed shows 8 rows + "Show 4 more" affordance    |
| Unknown-only (1 conclusion the frontend doesn't recognise)                                      | One Unknown token "1"                            | One Unknown compact token "1"                 | Unknown section (1 row), console.warn fires once  |
| Status-only (`CIStatus` non-empty, `CIChecksJSON` empty)                                        | Hidden                                           | Hidden                                        | Hidden                                            |
| Pending precedence (status=in_progress, conclusion=failure)                                     | One Pending token "1" (not Failed)               | Same                                          | Pending section only                              |

The two Mixed rows exist because chip and sidebar tests don't need a large payload (the chip cares about multi-bucket presence + count format; the sidebar cares about compact rendering), while the dropdown tests do (the "Show N more" affordance only fires past 8 passed checks). The Mixed-small fixture keeps chip/sidebar tests fast; Mixed-large exercises the dropdown's truncation and Unknown section together.

Each row is a positive acceptance criterion for at least one unit, component, or e2e test added in this plan.

---

## File Structure

**Create:**
- `packages/ui/src/utils/ci-buckets.ts` — pure parse + classify
- `packages/ui/src/utils/ci-buckets.test.ts` — unit tests for pure core
- `packages/ui/src/utils/ci-buckets-warn.ts` — side-effect warning helpers
- `packages/ui/src/utils/ci-buckets-warn.test.ts` — unit tests for warnings
- `packages/ui/src/utils/prefers-reduced-motion.svelte.ts` — Svelte helper backing the reduced-motion swap
- `packages/ui/src/utils/prefers-reduced-motion.svelte.test.ts` — tests for the helper
- `packages/ui/src/components/shared/CITokenCluster.svelte` — shared cluster component
- `packages/ui/src/components/shared/CITokenCluster.test.ts` — component tests
- `packages/ui/src/components/sidebar/PullItem.test.ts` — sidebar component tests
- `frontend/tests/e2e-full/pull-list-ci.spec.ts` — sidebar full-stack e2e

**Modify:**
- `packages/ui/src/components/shared/Chip.svelte` — accept and forward `ariaLabel` / `dataTestid` props (Task 9a)
- `packages/ui/src/components/detail/CIStatus.svelte` — switch to shared cluster + bucketing
- `packages/ui/src/components/detail/CIStatus.test.ts` — update existing tests, add new
- `packages/ui/src/components/detail/PullDetail.svelte` — pass new props to `<CIStatus>` (Task 9a)
- `packages/ui/src/components/sidebar/PullItem.svelte` — replace single icon with cluster
- `cmd/e2e-server/main.go` — add `/__e2e/pr-ci-state/{mixed,malformed,status-only,dropdown-mixed}` fixture endpoints
- `frontend/tests/e2e-full/ci-dropdown.spec.ts` — update existing assertions, add mixed-state/malformed/transient tests

---

## Stage 1a: ci-buckets.ts pure core

### Task 1: Define bucket types and `bucketForCheck`

**Files:**
- Create: `packages/ui/src/utils/ci-buckets.ts`
- Create: `packages/ui/src/utils/ci-buckets.test.ts`

- [ ] **Step 1: Write the failing test**

```typescript
// packages/ui/src/utils/ci-buckets.test.ts
import { describe, expect, it } from "vitest";
import { bucketForCheck } from "./ci-buckets.js";
import type { CICheck } from "../api/types.js";

const check = (partial: Partial<CICheck>): CICheck => ({
  name: "",
  status: "completed",
  conclusion: "",
  url: "",
  app: "",
  ...partial,
});

describe("bucketForCheck", () => {
  it("returns pending for active statuses", () => {
    for (const status of ["in_progress", "queued", "pending", "waiting"]) {
      expect(bucketForCheck(check({ status, conclusion: "" }))).toBe("pending");
    }
  });

  it("pending status takes precedence over conclusion", () => {
    expect(
      bucketForCheck(check({ status: "in_progress", conclusion: "failure" })),
    ).toBe("pending");
  });

  it("returns failed for known failure conclusions", () => {
    for (const conclusion of [
      "failure", "cancelled", "timed_out",
      "action_required", "stale", "startup_failure",
    ]) {
      expect(bucketForCheck(check({ conclusion }))).toBe("failed");
    }
  });

  it("returns passed for success", () => {
    expect(bucketForCheck(check({ conclusion: "success" }))).toBe("passed");
  });

  it("returns skipped for skipped/neutral", () => {
    expect(bucketForCheck(check({ conclusion: "skipped" }))).toBe("skipped");
    expect(bucketForCheck(check({ conclusion: "neutral" }))).toBe("skipped");
  });

  it("returns unknown for non-empty unrecognised conclusions", () => {
    expect(bucketForCheck(check({ conclusion: "weird_new_state" }))).toBe(
      "unknown",
    );
  });

  it("returns pending when status is non-active and conclusion is empty", () => {
    expect(bucketForCheck(check({ status: "", conclusion: "" }))).toBe("pending");
    expect(bucketForCheck(check({ status: "completed", conclusion: "" }))).toBe(
      "pending",
    );
  });

  it("trusts the conclusion when status is an unrecognised non-completed value", () => {
    // status='weird' is not 'completed' and not active. The classifier
    // falls through to conclusion-based bucketing, NOT Pending.
    expect(
      bucketForCheck(check({ status: "weird", conclusion: "success" })),
    ).toBe("passed");
    expect(
      bucketForCheck(check({ status: "weird", conclusion: "failure" })),
    ).toBe("failed");
    expect(
      bucketForCheck(check({ status: "weird", conclusion: "skipped" })),
    ).toBe("skipped");
  });

  it("returns pending for unrecognised status with empty conclusion (step 6 fallback)", () => {
    expect(bucketForCheck(check({ status: "weird", conclusion: "" }))).toBe(
      "pending",
    );
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: FAIL with "Failed to resolve module specifier './ci-buckets.js'"

- [ ] **Step 3: Write minimal implementation**

```typescript
// packages/ui/src/utils/ci-buckets.ts
import type { CICheck } from "../api/types.js";

export type CIBucket = "failed" | "pending" | "passed" | "skipped" | "unknown";

const ACTIVE_STATUSES = new Set([
  "in_progress",
  "queued",
  "pending",
  "waiting",
]);

const FAILED_CONCLUSIONS = new Set([
  "failure",
  "cancelled",
  "timed_out",
  "action_required",
  "stale",
  "startup_failure",
]);

const SKIPPED_CONCLUSIONS = new Set(["skipped", "neutral"]);

export function bucketForCheck(check: CICheck): CIBucket {
  if (ACTIVE_STATUSES.has(check.status)) return "pending";
  if (FAILED_CONCLUSIONS.has(check.conclusion)) return "failed";
  if (check.conclusion === "success") return "passed";
  if (SKIPPED_CONCLUSIONS.has(check.conclusion)) return "skipped";
  if (check.conclusion !== "") return "unknown";
  return "pending";
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: all `bucketForCheck` describe block tests pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/utils/ci-buckets.ts packages/ui/src/utils/ci-buckets.test.ts
git commit -m "feat(ui): add bucketForCheck classifier for CI checks"
```

---

### Task 2: Add `bucketCIChecks` aggregator with longest duration

**Files:**
- Modify: `packages/ui/src/utils/ci-buckets.ts`
- Modify: `packages/ui/src/utils/ci-buckets.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `ci-buckets.test.ts`:

```typescript
import { bucketCIChecks } from "./ci-buckets.js";

describe("bucketCIChecks", () => {
  it("aggregates a mixed-state set into the right counts", () => {
    const result = bucketCIChecks([
      check({ status: "completed", conclusion: "failure" }),
      check({ status: "completed", conclusion: "success" }),
      check({ status: "completed", conclusion: "success" }),
      check({ status: "in_progress", conclusion: "" }),
      check({ status: "completed", conclusion: "skipped" }),
      check({ status: "completed", conclusion: "weird_state" }),
    ]);
    expect(result.failed.length).toBe(1);
    expect(result.pending.length).toBe(1);
    expect(result.passed.length).toBe(2);
    expect(result.skipped.length).toBe(1);
    expect(result.unknown.length).toBe(1);
    expect(result.all.length).toBe(6);
  });

  it("computes longestCompletedDurationSeconds across completed checks only", () => {
    const result = bucketCIChecks([
      check({ status: "completed", conclusion: "success", duration_seconds: 30 }),
      check({ status: "completed", conclusion: "success", duration_seconds: 120 }),
      check({ status: "in_progress", duration_seconds: 9999 }),
    ]);
    expect(result.longestCompletedDurationSeconds).toBe(120);
  });

  it("returns undefined longest when no completed check has duration", () => {
    const result = bucketCIChecks([
      check({ status: "in_progress", duration_seconds: 30 }),
      check({ status: "completed", conclusion: "success" }),
    ]);
    expect(result.longestCompletedDurationSeconds).toBeUndefined();
  });

  it("handles empty input", () => {
    const result = bucketCIChecks([]);
    expect(result.all.length).toBe(0);
    expect(result.longestCompletedDurationSeconds).toBeUndefined();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: FAIL with "bucketCIChecks is not a function" or similar.

- [ ] **Step 3: Write minimal implementation**

Add to `ci-buckets.ts`:

```typescript
export interface CIBucketedChecks {
  failed: CICheck[];
  pending: CICheck[];
  passed: CICheck[];
  skipped: CICheck[];
  unknown: CICheck[];
  all: CICheck[];
  longestCompletedDurationSeconds: number | undefined;
}

export function bucketCIChecks(checks: CICheck[]): CIBucketedChecks {
  const result: CIBucketedChecks = {
    failed: [],
    pending: [],
    passed: [],
    skipped: [],
    unknown: [],
    all: checks,
    longestCompletedDurationSeconds: undefined,
  };
  let longest: number | undefined;
  for (const check of checks) {
    result[bucketForCheck(check)].push(check);
    if (
      check.status === "completed" &&
      typeof check.duration_seconds === "number" &&
      Number.isFinite(check.duration_seconds) &&
      check.duration_seconds >= 0
    ) {
      if (longest === undefined || check.duration_seconds > longest) {
        longest = check.duration_seconds;
      }
    }
  }
  result.longestCompletedDurationSeconds = longest;
  return result;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/utils/ci-buckets.ts packages/ui/src/utils/ci-buckets.test.ts
git commit -m "feat(ui): add bucketCIChecks aggregator with longest duration"
```

---

### Task 3: Add `parseCIChecks` with shape validation and duration normalization

**Files:**
- Modify: `packages/ui/src/utils/ci-buckets.ts`
- Modify: `packages/ui/src/utils/ci-buckets.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `ci-buckets.test.ts`:

```typescript
import { parseCIChecks } from "./ci-buckets.js";

describe("parseCIChecks", () => {
  it("parses well-formed JSON into typed checks", () => {
    const json = JSON.stringify([
      { name: "build", status: "completed", conclusion: "success", url: "", app: "GH" },
    ]);
    const result = parseCIChecks(json);
    expect(result.error).toBeNull();
    expect(result.checks.length).toBe(1);
    expect(result.checks[0].name).toBe("build");
  });

  it("treats empty string as success with no checks (no error)", () => {
    expect(parseCIChecks("")).toEqual({ checks: [], error: null });
    expect(parseCIChecks("   ")).toEqual({ checks: [], error: null });
  });

  it("returns error for invalid JSON", () => {
    const result = parseCIChecks("{not json");
    expect(result.error).toBeInstanceOf(Error);
    expect(result.checks.length).toBe(0);
  });

  it("returns error when top-level value is not an array", () => {
    const result = parseCIChecks(JSON.stringify({ checks: [] }));
    expect(result.error).toBeInstanceOf(Error);
    expect(result.checks.length).toBe(0);
  });

  it("returns error when any element is non-object", () => {
    const result = parseCIChecks(JSON.stringify([{ name: "ok" }, "bad"]));
    expect(result.error).toBeInstanceOf(Error);
    expect(result.checks.length).toBe(0);
  });

  it("coerces missing fields to empty strings", () => {
    const result = parseCIChecks(JSON.stringify([{}]));
    expect(result.error).toBeNull();
    expect(result.checks[0].status).toBe("");
    expect(result.checks[0].conclusion).toBe("");
  });

  it("normalizes duration_seconds — drops NaN/negative/non-finite", () => {
    const result = parseCIChecks(
      JSON.stringify([
        { duration_seconds: 30 },
        { duration_seconds: -5 },
        { duration_seconds: "not a number" },
        { duration_seconds: null },
      ]),
    );
    expect(result.error).toBeNull();
    expect(result.checks[0].duration_seconds).toBe(30);
    expect(result.checks[1].duration_seconds).toBeUndefined();
    expect(result.checks[2].duration_seconds).toBeUndefined();
    expect(result.checks[3].duration_seconds).toBeUndefined();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: FAIL with "parseCIChecks is not a function".

- [ ] **Step 3: Write minimal implementation**

Add to `ci-buckets.ts`:

```typescript
export interface ParsedCIChecks {
  checks: CICheck[];
  error: Error | null;
}

function coerceString(value: unknown): string {
  if (value == null) return "";
  return String(value);
}

function coerceDuration(value: unknown): number | undefined {
  if (value == null) return undefined;
  const n = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(n) || n < 0) return undefined;
  return n;
}

export function parseCIChecks(json: string): ParsedCIChecks {
  if (json.trim() === "") return { checks: [], error: null };
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch (err) {
    return { checks: [], error: err instanceof Error ? err : new Error(String(err)) };
  }
  if (!Array.isArray(parsed)) {
    return { checks: [], error: new Error("CIChecksJSON: payload is not an array") };
  }
  const checks: CICheck[] = [];
  for (const elem of parsed) {
    if (typeof elem !== "object" || elem === null) {
      return { checks: [], error: new Error("CIChecksJSON: payload contains a non-object element") };
    }
    const raw = elem as Record<string, unknown>;
    checks.push({
      name: coerceString(raw.name),
      status: coerceString(raw.status),
      conclusion: coerceString(raw.conclusion),
      url: coerceString(raw.url),
      app: coerceString(raw.app),
      required: typeof raw.required === "boolean" ? raw.required : undefined,
      duration_seconds: coerceDuration(raw.duration_seconds),
    });
  }
  return { checks, error: null };
}

// Render-safe projection of a parse error. Native JSON.parse messages can
// include a preview of the malformed input, so UI surfaces (title, popover,
// sr-only text) must use this helper instead of raw `error.message`.
export function safeDiagnosticText(error: Error): string {
  const msg = error.message;
  if (msg.startsWith("CIChecksJSON: ")) return msg;
  if (error instanceof SyntaxError) {
    if (/unexpected token/i.test(msg)) return "Malformed JSON (unexpected token)";
    if (/unexpected end/i.test(msg)) return "Malformed JSON (unexpected end of input)";
    if (/unterminated/i.test(msg)) return "Malformed JSON (unterminated string)";
    return "Malformed JSON (syntax error)";
  }
  return "Malformed JSON (parse error)";
}
```

Add a test verifying the safe projection doesn't leak input content:

```typescript
it("safeDiagnosticText collapses native JSON.parse errors to content-free categories", () => {
  let parseErr: Error;
  try {
    JSON.parse(`{"x":"super_secret_sentinel_xyz",`); // truncated
    throw new Error("should have failed");
  } catch (e) {
    parseErr = e as Error;
  }
  const safe = safeDiagnosticText(parseErr);
  expect(safe).not.toContain("super_secret_sentinel_xyz");
  expect(safe).toMatch(/Malformed JSON/);
});

it("safeDiagnosticText forwards locally-created CIChecksJSON shape errors intact", () => {
  expect(safeDiagnosticText(new Error("CIChecksJSON: payload is not an array")))
    .toBe("CIChecksJSON: payload is not an array");
});
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: all tests pass.

- [ ] **Step 5: Verify generated API schema still exposes `CIChecksJSON`**

Run: `grep -n "CIChecksJSON" packages/ui/src/api/generated/schema.ts | head -5`
Expected: shows `CIChecksJSON: string` on both `MergeRequest` and `MergeRequestResponse`.

- [ ] **Step 6: Commit**

```bash
git add packages/ui/src/utils/ci-buckets.ts packages/ui/src/utils/ci-buckets.test.ts
git commit -m "feat(ui): add parseCIChecks with shape validation and duration normalization"
```

---

## Stage 1b: parseCIChecks LRU cache

### Task 4: Wrap `parseCIChecks` in a byte-aware LRU cache

**Files:**
- Modify: `packages/ui/src/utils/ci-buckets.ts`
- Modify: `packages/ui/src/utils/ci-buckets.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `ci-buckets.test.ts`:

```typescript
import {
  __resetParseCIChecksCache,
  __parseCIChecksCacheStats,
} from "./ci-buckets.js";

describe("parseCIChecks LRU cache", () => {
  beforeEach(() => __resetParseCIChecksCache());

  it("returns the same reference for repeated calls with the same input", () => {
    const json = JSON.stringify([{ name: "a", status: "completed", conclusion: "success" }]);
    const r1 = parseCIChecks(json);
    const r2 = parseCIChecks(json);
    expect(r1).toBe(r2);
  });

  it("counts hits and misses", () => {
    const json = JSON.stringify([{ name: "a" }]);
    parseCIChecks(json);
    parseCIChecks(json);
    parseCIChecks(json);
    const stats = __parseCIChecksCacheStats();
    expect(stats.misses).toBe(1);
    expect(stats.hits).toBe(2);
  });

  it("does not cache payloads above the size threshold", () => {
    const big = JSON.stringify([{ name: "x".repeat(70_000) }]);
    parseCIChecks(big);
    parseCIChecks(big);
    const stats = __parseCIChecksCacheStats();
    expect(stats.size).toBe(0);
    expect(stats.hits).toBe(0);
  });

  it("freezes the returned wrapper, array, and elements", () => {
    const json = JSON.stringify([{ name: "a" }]);
    const result = parseCIChecks(json);
    expect(Object.isFrozen(result)).toBe(true);
    expect(Object.isFrozen(result.checks)).toBe(true);
    expect(Object.isFrozen(result.checks[0])).toBe(true);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: FAIL with missing `__resetParseCIChecksCache` and `__parseCIChecksCacheStats` exports.

- [ ] **Step 3: Write minimal implementation**

Refactor `parseCIChecks` in `ci-buckets.ts`:

```typescript
const PARSE_CACHE_MAX_ENTRIES = 256;
const PARSE_CACHE_MAX_BYTES = 1 << 20; // 1 MiB encoded
const PARSE_CACHE_PAYLOAD_CAP_BYTES = 64 * 1024; // skip caching payloads > 64 KiB encoded

const encoder = new TextEncoder();

function byteLengthOf(json: string): number {
  return encoder.encode(json).byteLength;
}

const parseCache = new Map<string, { result: ParsedCIChecks; bytes: number }>();
let parseCacheBytes = 0;
let parseCacheHits = 0;
let parseCacheMisses = 0;

function parseCIChecksUncached(json: string): ParsedCIChecks {
  // ...existing body of parseCIChecks moves here, then freeze on success:
  // (Keep all logic above, then before returning the success path, do:)
  // Object.freeze(checks);
  // for (const c of checks) Object.freeze(c);
  // return { checks, error: null };
}

export function parseCIChecks(json: string): ParsedCIChecks {
  const bytes = byteLengthOf(json);
  if (bytes > PARSE_CACHE_PAYLOAD_CAP_BYTES) {
    parseCacheMisses++;
    return parseCIChecksUncached(json);
  }
  const cached = parseCache.get(json);
  if (cached !== undefined) {
    parseCache.delete(json);
    parseCache.set(json, cached); // refresh LRU position
    parseCacheHits++;
    return cached.result;
  }
  parseCacheMisses++;
  const result = parseCIChecksUncached(json);
  parseCache.set(json, { result, bytes });
  parseCacheBytes += bytes;
  while (
    parseCache.size > PARSE_CACHE_MAX_ENTRIES ||
    parseCacheBytes > PARSE_CACHE_MAX_BYTES
  ) {
    const oldestEntry = parseCache.entries().next().value;
    if (oldestEntry === undefined) break;
    parseCache.delete(oldestEntry[0]);
    parseCacheBytes -= oldestEntry[1].bytes;
  }
  return result;
}

/** @internal test helper */
export function __resetParseCIChecksCache(): void {
  parseCache.clear();
  parseCacheBytes = 0;
  parseCacheHits = 0;
  parseCacheMisses = 0;
}

/** @internal test helper */
export function __parseCIChecksCacheStats(): {
  size: number;
  bytes: number;
  hits: number;
  misses: number;
} {
  return {
    size: parseCache.size,
    bytes: parseCacheBytes,
    hits: parseCacheHits,
    misses: parseCacheMisses,
  };
}
```

In `parseCIChecksUncached`, freeze every return path — both success and error, **including the `Error` object** on the error paths. Cached parse failures get returned to many consumers; if any of them mutates `error.message`, every subsequent read of the same cache entry would see the altered diagnostic. Freezing the Error closes that hole.

```typescript
// Success path:
Object.freeze(checks);
for (const c of checks) Object.freeze(c);
return Object.freeze({ checks, error: null });

// JSON-parse failure path:
const jsonErr = err instanceof Error ? err : new Error(String(err));
Object.freeze(jsonErr);
return Object.freeze({
  checks: Object.freeze([]) as CICheck[],
  error: jsonErr,
});

// Shape failure paths (not array, non-object element):
const shapeErr = new Error("...");
Object.freeze(shapeErr);
return Object.freeze({
  checks: Object.freeze([]) as CICheck[],
  error: shapeErr,
});
```

Freeze the entire returned `ParsedCIChecks` wrapper, its `checks` array, and its `error` object on every path so cached error results are equally immutable. Freezing is **always on** (not gated by dev mode) — it's O(1) per object, the API contract is strict-immutable, and a single behavior across environments is easier to reason about than environment-conditional invariants. Note that `Object.freeze` on an `Error` instance freezes the own properties (`message`, `stack`, `name`, etc.) shallowly; this is sufficient because consumers only read `error.message`.

Add a test asserting error results are frozen:

```typescript
it("freezes the wrapper, checks array, and Error for error results", () => {
  const result = parseCIChecks("{not json");
  expect(Object.isFrozen(result)).toBe(true);
  expect(Object.isFrozen(result.checks)).toBe(true);
  expect(result.error).not.toBeNull();
  expect(Object.isFrozen(result.error)).toBe(true);
});
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets.test.ts`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/utils/ci-buckets.ts packages/ui/src/utils/ci-buckets.test.ts
git commit -m "feat(ui): add byte-aware LRU cache to parseCIChecks"
```

---

## Stage 2: ci-buckets-warn.ts side-effect helpers

### Task 5: Implement `warnOnUnknownConclusions` with de-dup

**Files:**
- Create: `packages/ui/src/utils/ci-buckets-warn.ts`
- Create: `packages/ui/src/utils/ci-buckets-warn.test.ts`

- [ ] **Step 1: Write the failing test**

```typescript
// packages/ui/src/utils/ci-buckets-warn.test.ts
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  warnOnUnknownConclusions,
  __resetCIWarnings,
} from "./ci-buckets-warn.js";
import type { CICheck } from "../api/types.js";

const check = (conclusion: string): CICheck => ({
  name: "x",
  status: "completed",
  conclusion,
  url: "",
  app: "",
});

describe("warnOnUnknownConclusions", () => {
  afterEach(() => __resetCIWarnings());

  it("warns once per distinct conclusion", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnUnknownConclusions([check("foo"), check("foo"), check("bar")]);
    warnOnUnknownConclusions([check("foo"), check("bar"), check("baz")]);
    expect(spy).toHaveBeenCalledTimes(3); // foo, bar, baz
    spy.mockRestore();
  });

  it("does nothing for empty input", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnUnknownConclusions([]);
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });

  it("includes PR identifier in the log message when context is provided", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnUnknownConclusions([check("foo")], { repo: "a/b", number: 7 });
    expect(spy.mock.calls[0]?.[0] as string).toContain("a/b#7");
    spy.mockRestore();
  });

  it("truncates pathologically long conclusion values when warning and dedupes by the truncated form", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const long = "X".repeat(2000);
    warnOnUnknownConclusions([check(long)]);
    const message = spy.mock.calls[0]?.[0] as string;
    // The raw 2000-char value must not appear in the log line.
    expect(message).not.toContain("X".repeat(200));
    // The truncated form ends with the truncation marker.
    expect(message).toContain("…");
    // A second call with a string that shares the same truncated prefix
    // is treated as a duplicate (best-effort dedupe — distinct providers
    // that all overrun the cap with the same prefix get one warning).
    warnOnUnknownConclusions([check("X".repeat(2000) + "_different_tail")]);
    expect(spy).toHaveBeenCalledTimes(1);
    spy.mockRestore();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets-warn.test.ts`
Expected: module-not-found.

- [ ] **Step 3: Write minimal implementation**

```typescript
// packages/ui/src/utils/ci-buckets-warn.ts
import type { CICheck } from "../api/types.js";

// Hard cap on how much of a raw `conclusion` value we keep in memory or
// log. A misbehaving provider could in theory send a multi-KB conclusion
// string; capping at 128 chars bounds memory growth (Set entry size) and
// log line length without losing the diagnostic signal — real conclusions
// are short identifiers ("success", "failure", "timed_out", etc.).
const UNKNOWN_CONCLUSION_DISPLAY_MAX = 128;

const warnedUnknown = new Set<string>();
const warnedMalformed = new Set<string>();

function truncateConclusion(c: string): string {
  return c.length > UNKNOWN_CONCLUSION_DISPLAY_MAX
    ? `${c.slice(0, UNKNOWN_CONCLUSION_DISPLAY_MAX)}…`
    : c;
}

export function warnOnUnknownConclusions(
  unknown: CICheck[],
  context?: { repo?: string; number?: number },
): void {
  const id =
    context?.repo && context?.number !== undefined
      ? `${context.repo}#${context.number}`
      : "";
  const idPrefix = id ? `[${id}] ` : "";
  for (const c of unknown) {
    const display = truncateConclusion(c.conclusion);
    if (warnedUnknown.has(display)) continue;
    warnedUnknown.add(display);
    console.warn(`${idPrefix}Unrecognised CI conclusion: ${display}`);
  }
}

/** @internal test helper */
export function __resetCIWarnings(): void {
  warnedUnknown.clear();
  warnedMalformed.clear();
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets-warn.test.ts`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/utils/ci-buckets-warn.ts packages/ui/src/utils/ci-buckets-warn.test.ts
git commit -m "feat(ui): add unknown-conclusion warning helper"
```

---

### Task 6: Implement `warnOnMalformedCIChecksJSON` with FNV-1a hash + truncation

**Files:**
- Modify: `packages/ui/src/utils/ci-buckets-warn.ts`
- Modify: `packages/ui/src/utils/ci-buckets-warn.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `ci-buckets-warn.test.ts`:

```typescript
import { warnOnMalformedCIChecksJSON } from "./ci-buckets-warn.js";

describe("warnOnMalformedCIChecksJSON", () => {
  afterEach(() => __resetCIWarnings());

  it("warns once per (context+payload) pair", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const raw = "{not json";
    const err = new Error("parse fail");
    warnOnMalformedCIChecksJSON(raw, err, { repo: "a/b", number: 1 });
    warnOnMalformedCIChecksJSON(raw, err, { repo: "a/b", number: 1 });
    warnOnMalformedCIChecksJSON(raw, err, { repo: "a/b", number: 2 });
    expect(spy).toHaveBeenCalledTimes(2);
    spy.mockRestore();
  });

  it("logs only metadata and category in production mode — never the raw error.message or input content", () => {
    // Force production: import.meta.env.DEV === false. vi.stubEnv mutates
    // the live env object that ci-buckets-warn.ts reads at call time.
    vi.stubEnv("DEV", false);
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    // Critically: trigger a REAL JSON.parse so error.message contains a
    // preview of the malformed input. Native V8/JSC messages embed input
    // fragments like "Unexpected token X in JSON at position N" or
    // "Unexpected end of JSON input near ...sentinel...". The production
    // logger must NOT forward error.message.
    const sentinel = "supersecret_sentinel_xyz";
    const raw = `{"bad":"${sentinel}",`; // truncated, will fail JSON.parse
    let parseErr: Error;
    try {
      JSON.parse(raw);
      throw new Error("parse should have failed");
    } catch (e) {
      parseErr = e as Error;
    }
    // Sanity: confirm the sentinel actually appears somewhere in the
    // real parse error so we're testing the leak path, not a no-op.
    // (Some engines may not embed it; if so, swap the raw payload until
    //  the engine's message includes a preview.)
    // expect(parseErr.message).toContain(sentinel); // intentionally
    // commented — we don't want the test to fail on engine variation,
    // just to assert the production logger doesn't leak even when the
    // sentinel IS present.
    warnOnMalformedCIChecksJSON(raw, parseErr);
    const message = spy.mock.calls[0]?.[0] as string;
    expect(message).not.toContain(sentinel);
    expect(message).not.toContain(parseErr.message);
    expect(message).not.toContain("Preview:");
    expect(message).toMatch(/JSON:\s/); // a category string
    expect(message).toMatch(/length=\d+/);
    expect(message).toMatch(/hash=[0-9a-f]+/);
    spy.mockRestore();
    vi.unstubAllEnvs();
  });

  it("forwards locally-created shape error messages in production (no leak risk)", () => {
    // Errors created by parseCIChecks have a stable "CIChecksJSON: ..."
    // prefix — content-free and safe to log. Confirm they pass through
    // intact rather than collapsing to a generic category.
    vi.stubEnv("DEV", false);
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnMalformedCIChecksJSON("[1, 2]", new Error("CIChecksJSON: element 0 is not an object"));
    const message = spy.mock.calls[0]?.[0] as string;
    expect(message).toContain("CIChecksJSON: element 0 is not an object");
    spy.mockRestore();
    vi.unstubAllEnvs();
  });

  it("includes raw error.message and a 64-char Preview clause in dev mode and caps the preview at 64 chars", () => {
    // Force dev: import.meta.env.DEV === true.
    vi.stubEnv("DEV", true);
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const raw = "A".repeat(200); // well past the 64-char cap
    warnOnMalformedCIChecksJSON(raw, new Error("bad"));
    const message = spy.mock.calls[0]?.[0] as string;
    expect(message).toContain("bad"); // raw error.message present in dev
    expect(message).toContain("Preview: ");
    // The Preview clause shows exactly 64 'A's plus the ellipsis marker.
    expect(message).toContain(`Preview: ${"A".repeat(64)}…`);
    expect(message).not.toContain("A".repeat(65));
    // Metadata clause must still be present regardless of mode.
    expect(message).toMatch(/length=200/);
    expect(message).toMatch(/hash=[0-9a-f]+/);
    spy.mockRestore();
    vi.unstubAllEnvs();
  });

  it("includes PR identifier when context is provided", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnMalformedCIChecksJSON("{}", new Error("bad"), { repo: "x/y", number: 42 });
    expect((spy.mock.calls[0]?.[0] as string)).toContain("x/y#42");
    spy.mockRestore();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets-warn.test.ts`
Expected: FAIL with missing export.

- [ ] **Step 3: Write minimal implementation**

Add to `ci-buckets-warn.ts`:

```typescript
const DEV_PAYLOAD_PREVIEW_MAX = 64;

// Read DEV at call time, not module load time, so vi.stubEnv("DEV", ...)
// in tests can toggle production vs dev behaviour without re-importing
// the module. Production builds inline this away via Vite's compile-time
// `import.meta.env.DEV` replacement.
function isDevMode(): boolean {
  return (
    typeof import.meta !== "undefined" &&
    (import.meta as { env?: { DEV?: boolean } }).env?.DEV === true
  );
}

function fnv1a(input: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i);
    hash = (hash + ((hash << 1) + (hash << 4) + (hash << 7) + (hash << 8) + (hash << 24))) >>> 0;
  }
  return hash.toString(16);
}

// Maps a real parser/shape Error to a stable, content-free category string
// safe to log in production. Native `JSON.parse` messages typically embed a
// preview of the malformed input (`Unexpected token { in JSON at position 12`,
// `Unexpected end of JSON input near "...sentinel..."`), which would leak
// provider-controlled content into the production log even if we never log
// `raw` directly. By projecting the error to a small category enum we keep
// the diagnostic signal (what *kind* of failure) without the leak.
function categorizeParseError(err: Error): string {
  // Shape-validation errors created locally by parseCIChecks carry stable
  // prefixes set by us — those are content-free and safe to forward.
  const msg = err.message;
  if (msg.startsWith("CIChecksJSON: ")) return msg;
  // Native SyntaxError categories from JSON.parse. Match on a few stable
  // substrings; everything else collapses to a generic category.
  if (err instanceof SyntaxError) {
    if (/unexpected token/i.test(msg)) return "JSON: unexpected token";
    if (/unexpected end/i.test(msg)) return "JSON: unexpected end of input";
    if (/unterminated/i.test(msg)) return "JSON: unterminated string";
    return "JSON: syntax error";
  }
  return "JSON: parse error";
}

export function warnOnMalformedCIChecksJSON(
  raw: string,
  error: Error,
  context?: { repo?: string; number?: number },
): void {
  const id =
    context?.repo && context?.number !== undefined
      ? `${context.repo}#${context.number}`
      : "";
  const hash = fnv1a(raw);
  const key = `${id}|${hash}`;
  if (warnedMalformed.has(key)) return;
  warnedMalformed.add(key);
  const idPrefix = id ? `[${id}] ` : "";
  // Production: log only metadata (length + hash + error category). The
  // category is content-free; the raw error.message is NOT logged because
  // native JSON.parse messages typically include a preview of the
  // malformed input.
  // Dev: log the raw error.message and a 64-char input preview to help
  // local debugging. Dev builds are not the privacy boundary.
  const category = categorizeParseError(error);
  if (isDevMode()) {
    const previewClause = `\nPreview: ${raw.slice(0, DEV_PAYLOAD_PREVIEW_MAX)}${raw.length > DEV_PAYLOAD_PREVIEW_MAX ? "…" : ""}`;
    console.warn(
      `${idPrefix}Malformed CIChecksJSON: ${error.message} (length=${raw.length}, hash=${hash})${previewClause}`,
    );
  } else {
    console.warn(
      `${idPrefix}Malformed CIChecksJSON: ${category} (length=${raw.length}, hash=${hash})`,
    );
  }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/ci-buckets-warn.test.ts`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/utils/ci-buckets-warn.ts packages/ui/src/utils/ci-buckets-warn.test.ts
git commit -m "feat(ui): add malformed-JSON warning helper with FNV hash de-dup"
```

---

## Stage 3: Shared CITokenCluster + prefersReducedMotion

### Task 7: `prefersReducedMotion` Svelte helper

**Files:**
- Create: `packages/ui/src/utils/prefers-reduced-motion.svelte.ts`
- Create: `packages/ui/src/utils/prefers-reduced-motion.svelte.test.ts`

- [ ] **Step 1: Write the failing test**

```typescript
// packages/ui/src/utils/prefers-reduced-motion.svelte.test.ts
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  prefersReducedMotion,
  __resetPrefersReducedMotion,
} from "./prefers-reduced-motion.svelte.js";

function stubMatchMedia(initialMatches: boolean) {
  const mql = { matches: initialMatches } as MediaQueryList;
  vi.spyOn(window, "matchMedia").mockReturnValue(mql);
  return {
    setMatches(next: boolean) {
      (mql as { matches: boolean }).matches = next;
    },
  };
}

describe("prefersReducedMotion", () => {
  afterEach(() => {
    __resetPrefersReducedMotion();
    vi.restoreAllMocks();
  });

  it("returns initial value from matchMedia", () => {
    stubMatchMedia(true);
    const pref = prefersReducedMotion();
    expect(pref.value).toBe(true);
  });

  it("reflects the live matches value on every read", () => {
    const ctrl = stubMatchMedia(false);
    const pref = prefersReducedMotion();
    expect(pref.value).toBe(false);
    ctrl.setMatches(true);
    expect(pref.value).toBe(true);
  });

  it("returns false when matchMedia is unavailable (SSR)", () => {
    const orig = window.matchMedia;
    (window as { matchMedia: typeof window.matchMedia | undefined }).matchMedia = undefined;
    try {
      const pref = prefersReducedMotion();
      expect(pref.value).toBe(false);
    } finally {
      window.matchMedia = orig;
    }
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/prefers-reduced-motion.svelte.test.ts`
Expected: module not found.

- [ ] **Step 3: Write minimal implementation**

```typescript
// packages/ui/src/utils/prefers-reduced-motion.svelte.ts
let cachedMql: MediaQueryList | null = null;

function getMediaQueryList(): MediaQueryList | null {
  if (cachedMql !== null) return cachedMql;
  // Re-check window on every miss so post-hydration browser contexts pick up
  // matchMedia after an initial SSR or non-browser call returned null.
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return null;
  }
  cachedMql = window.matchMedia("(prefers-reduced-motion: reduce)");
  return cachedMql;
}

export function prefersReducedMotion(): { readonly value: boolean } {
  return {
    get value(): boolean {
      const mql = getMediaQueryList();
      return mql ? mql.matches : false;
    },
  };
}

/** @internal test helper — resets the cached MediaQueryList between tests. */
export function __resetPrefersReducedMotion(): void {
  cachedMql = null;
}
```

The getter reads `matchMedia.matches` live on every access. The helper does NOT subscribe to the media query — a mid-session OS-level reduced-motion toggle is only reflected on the **next** Svelte render that reads `.value` (e.g., a prop update, hover state, or any other reactive cause). This is intentional: live subscription would require integration with Svelte's reactivity system and adds complexity for a rarely-toggled preference. The chip and dropdown re-render frequently enough that the value catches up quickly in practice; if a static-during-session presentation ever causes problems, swap to a subscription-based wrapper later. No `$state`/`$effect` involvement also means the helper is callable directly from tests without a Svelte reactive context. The `.svelte.ts` extension is kept for symmetry with the spec/cluster convention but isn't strictly required here since no runes are used.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/utils/prefers-reduced-motion.svelte.test.ts`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/utils/prefers-reduced-motion.svelte.ts packages/ui/src/utils/prefers-reduced-motion.svelte.test.ts
git commit -m "feat(ui): add prefersReducedMotion Svelte helper"
```

---

### Task 8: `CITokenCluster.svelte` shared component

**Files:**
- Create: `packages/ui/src/components/shared/CITokenCluster.svelte`
- Create: `packages/ui/src/components/shared/CITokenCluster.test.ts`

- [ ] **Step 1: Write the failing test**

```typescript
// packages/ui/src/components/shared/CITokenCluster.test.ts
import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import CITokenCluster from "./CITokenCluster.svelte";
import { composeAriaLabel } from "./CITokenCluster.svelte";
import type { CIBucketedChecks } from "../../utils/ci-buckets.js";
import { __resetPrefersReducedMotion } from "../../utils/prefers-reduced-motion.svelte.js";

function bucketed(counts: Partial<Record<"failed" | "pending" | "passed" | "skipped" | "unknown", number>>): CIBucketedChecks {
  const make = (n: number) => Array.from({ length: n }, () => ({
    name: "", status: "completed", conclusion: "", url: "", app: "",
  }));
  const failed = make(counts.failed ?? 0);
  const pending = make(counts.pending ?? 0);
  const passed = make(counts.passed ?? 0);
  const skipped = make(counts.skipped ?? 0);
  const unknown = make(counts.unknown ?? 0);
  return {
    failed, pending, passed, skipped, unknown,
    all: [...failed, ...pending, ...unknown, ...passed, ...skipped],
    longestCompletedDurationSeconds: undefined,
  };
}

describe("CITokenCluster", () => {
  afterEach(() => cleanup());

  it("renders only non-zero tokens in fixed severity order", () => {
    render(CITokenCluster, {
      props: { bucketed: bucketed({ failed: 1, passed: 23, skipped: 2 }), size: "default" },
    });
    const tokens = document.querySelectorAll("[data-testid^='ci-token-']");
    expect(tokens.length).toBe(3);
    expect(tokens[0].getAttribute("data-testid")).toBe("ci-token-failed");
    expect(tokens[1].getAttribute("data-testid")).toBe("ci-token-passed");
    expect(tokens[2].getAttribute("data-testid")).toBe("ci-token-skipped");
  });

  it("emits the unknown token between pending and passed when present", () => {
    render(CITokenCluster, {
      props: { bucketed: bucketed({ failed: 1, pending: 2, unknown: 3, passed: 4, skipped: 5 }), size: "default" },
    });
    const tokens = document.querySelectorAll("[data-testid^='ci-token-']");
    const ids = Array.from(tokens).map(t => t.getAttribute("data-testid"));
    expect(ids).toEqual([
      "ci-token-failed",
      "ci-token-pending",
      "ci-token-unknown",
      "ci-token-passed",
      "ci-token-skipped",
    ]);
  });

  it("renders nothing when all buckets are empty", () => {
    render(CITokenCluster, { props: { bucketed: bucketed({}), size: "default" } });
    expect(document.querySelectorAll("[data-testid^='ci-token-']").length).toBe(0);
  });

  it("token children are aria-hidden", () => {
    render(CITokenCluster, { props: { bucketed: bucketed({ failed: 1 }), size: "default" } });
    const token = document.querySelector("[data-testid='ci-token-failed']")!;
    expect(token.getAttribute("aria-hidden")).toBe("true");
  });

  it("pending token has the spin class when prefers-reduced-motion: reduce is OFF (animated chip path)", () => {
    // matchMedia stub returning matches=false ↔ reduced-motion preference is OFF.
    const mqlOff = { matches: false, media: "(prefers-reduced-motion: reduce)",
                     addEventListener: () => {}, removeEventListener: () => {} };
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue(mqlOff));
    // Force the helper to re-read by resetting its module-scoped cache.
    __resetPrefersReducedMotion();
    render(CITokenCluster, {
      props: { bucketed: bucketed({ pending: 1 }), size: "default" },
    });
    const token = document.querySelector("[data-testid='ci-token-pending']")!;
    // The animated LoaderCircleIcon is wrapped in `.spin`. When reduced-motion
    // is OFF the cluster mounts the spinning variant.
    expect(token.querySelector(".spin")).not.toBeNull();
    vi.unstubAllGlobals();
  });

  it("pending token has no spin class when prefers-reduced-motion: reduce is ON (static chip path)", () => {
    const mqlOn = { matches: true, media: "(prefers-reduced-motion: reduce)",
                    addEventListener: () => {}, removeEventListener: () => {} };
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue(mqlOn));
    __resetPrefersReducedMotion();
    render(CITokenCluster, {
      props: { bucketed: bucketed({ pending: 1 }), size: "default" },
    });
    const token = document.querySelector("[data-testid='ci-token-pending']")!;
    // Reduced motion ON → static CircleIcon, no `.spin` wrapper.
    expect(token.querySelector(".spin")).toBeNull();
    vi.unstubAllGlobals();
  });

  it("pending token has no spin class when pendingStyle='static' (sidebar path) regardless of reduced-motion", () => {
    const mqlOff = { matches: false, media: "(prefers-reduced-motion: reduce)",
                     addEventListener: () => {}, removeEventListener: () => {} };
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue(mqlOff));
    __resetPrefersReducedMotion();
    render(CITokenCluster, {
      props: { bucketed: bucketed({ pending: 1 }), size: "compact", pendingStyle: "static" },
    });
    const token = document.querySelector("[data-testid='ci-token-pending']")!;
    expect(token.querySelector(".spin")).toBeNull();
    vi.unstubAllGlobals();
  });
});

describe("composeAriaLabel", () => {
  it("uses singular for 1, plural for others", () => {
    expect(composeAriaLabel(bucketed({ failed: 1, pending: 5 }))).toBe(
      "CI: 1 failed check, 5 pending checks",
    );
  });

  it("omits zero buckets", () => {
    expect(composeAriaLabel(bucketed({ passed: 3 }))).toBe("CI: 3 passed checks");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/shared/CITokenCluster.test.ts`
Expected: component not found.

- [ ] **Step 3: Write the component**

```svelte
<!-- packages/ui/src/components/shared/CITokenCluster.svelte -->
<script module lang="ts">
  import type { CIBucketedChecks, CIBucket } from "../../utils/ci-buckets.js";

  type LabeledBucket = { key: CIBucket; singular: string; plural: string };
  const ORDER: LabeledBucket[] = [
    { key: "failed", singular: "failed check", plural: "failed checks" },
    { key: "pending", singular: "pending check", plural: "pending checks" },
    { key: "unknown", singular: "unknown check", plural: "unknown checks" },
    { key: "passed", singular: "passed check", plural: "passed checks" },
    { key: "skipped", singular: "skipped check", plural: "skipped checks" },
  ];

  export function composeAriaLabel(bucketed: CIBucketedChecks): string {
    const parts = ORDER
      .map(({ key, singular, plural }) => {
        const n = bucketed[key].length;
        if (n === 0) return null;
        return `${n} ${n === 1 ? singular : plural}`;
      })
      .filter((p): p is string => p !== null);
    return parts.length === 0 ? "CI: no checks" : `CI: ${parts.join(", ")}`;
  }
</script>

<script lang="ts">
  import CircleXIcon from "@lucide/svelte/icons/circle-x";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import CircleMinusIcon from "@lucide/svelte/icons/circle-minus";
  import CircleHelpIcon from "@lucide/svelte/icons/circle-help";
  import CircleIcon from "@lucide/svelte/icons/circle";
  import LoaderCircleIcon from "@lucide/svelte/icons/loader-circle";
  import type { CIBucketedChecks } from "../../utils/ci-buckets.js";
  import { prefersReducedMotion } from "../../utils/prefers-reduced-motion.svelte.js";

  interface Props {
    bucketed: CIBucketedChecks;
    size: "default" | "compact";
    pendingStyle?: "animated" | "static"; // sidebar passes "static"
  }
  let { bucketed, size, pendingStyle = "animated" }: Props = $props();

  const reduced = prefersReducedMotion();
  const pendingAnimated = $derived(pendingStyle === "animated" && !reduced.value);

  const iconSize = $derived(size === "compact" ? 10 : 11);
</script>

{#if bucketed.failed.length > 0}
  <span class="tok tok-red" data-testid="ci-token-failed" aria-hidden="true">
    <CircleXIcon size={iconSize} strokeWidth={2.5} /><span class="ct">{bucketed.failed.length}</span>
  </span>
{/if}
{#if bucketed.pending.length > 0}
  <span class="tok tok-amber" data-testid="ci-token-pending" aria-hidden="true">
    {#if pendingAnimated}
      <span class="spin"><LoaderCircleIcon size={iconSize} strokeWidth={2.5} /></span>
    {:else}
      <CircleIcon size={iconSize} strokeWidth={2.5} />
    {/if}
    <span class="ct">{bucketed.pending.length}</span>
  </span>
{/if}
{#if bucketed.unknown.length > 0}
  <span class="tok tok-purple" data-testid="ci-token-unknown" aria-hidden="true">
    <CircleHelpIcon size={iconSize} strokeWidth={2.5} /><span class="ct">{bucketed.unknown.length}</span>
  </span>
{/if}
{#if bucketed.passed.length > 0}
  <span class="tok tok-green" data-testid="ci-token-passed" aria-hidden="true">
    <CircleCheckIcon size={iconSize} strokeWidth={2.5} /><span class="ct">{bucketed.passed.length}</span>
  </span>
{/if}
{#if bucketed.skipped.length > 0}
  <span class="tok tok-muted" data-testid="ci-token-skipped" aria-hidden="true">
    <CircleMinusIcon size={iconSize} strokeWidth={2.5} /><span class="ct">{bucketed.skipped.length}</span>
  </span>
{/if}

<style>
  .tok { display: inline-flex; align-items: center; gap: 2px; font-variant-numeric: tabular-nums; font-weight: 700; line-height: 1; }
  .tok .ct { font-size: 0.95em; }
  .tok-red { color: var(--accent-red); }
  .tok-amber { color: var(--accent-amber); }
  .tok-green { color: var(--accent-green); }
  .tok-muted { color: var(--text-muted); }
  .tok-purple { color: var(--accent-purple); }
  .spin { display: inline-flex; animation: spin 0.9s linear infinite; }
  @keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
</style>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/shared/CITokenCluster.test.ts`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/components/shared/CITokenCluster.svelte packages/ui/src/components/shared/CITokenCluster.test.ts
git commit -m "feat(ui): add shared CITokenCluster component"
```

---

## Stage 4a: CIStatus.svelte chip rewrite

### Task 9a: Extend Chip + introduce new CIStatus props (contract change, no visual change)

This task and Task 9b together replace the previous "rewrite the chip in one commit" approach. Splitting them keeps each commit independently reviewable: 9a is a pure prop-passing refactor with no visual change (so any regression here is a wiring mistake, easy to bisect); 9b is the rendering rewrite that depends on 9a's contract.

**Files:**
- Modify: `packages/ui/src/components/shared/Chip.svelte` — extend the prop interface to accept `ariaLabel?: string | undefined` and `dataTestid?: string | undefined`, and forward them onto the rendered element. Without this, Task 9b cannot set `data-testid="ci-chip"` or `aria-label={composeAriaLabel(bucketed)}` on the chip's outer element.
- Modify: `packages/ui/src/components/detail/CIStatus.svelte` — add four new props (`owner: string`, `name: string`, `number: number`, `prKey: string`). They are accepted into `$props()` but **not consumed** in this task. Task 9b wires `owner`/`name`/`number` into `parseCIChecks` context and the warning effects; Task 11 wires `prKey` into the dropdown's expansion-state reset. The reason to introduce them in 9a is that updating every `<CIStatus>` call site is an atomic grep-then-edit that's better done once with the full final prop set than twice with a partial set.
- Modify: `packages/ui/src/components/detail/CIStatus.test.ts` — every `render(CIStatus, { props })` in the existing tests must include the new props in the props object. The visual behaviour and assertions are unchanged; only the props object grows. Without this update, existing tests fail type-check the moment 9a introduces the props as required.
- Modify: `packages/ui/src/components/detail/PullDetail.svelte` — update both `<CIStatus … />` usage sites to pass the new props. Pass `owner={owner} name={name} number={pr.Number} prKey={pr.PlatformExternalID}`.

This task changes no rendering behaviour. Existing CIStatus tests stay green **after** their `props` objects are extended with the new required fields (see Step 4 below); no assertion changes are needed in 9a.

- [ ] **Step 1: Extend Chip.svelte**

In `Chip.svelte`, add to the prop interface:

```svelte
interface Props {
  // ... existing props
  ariaLabel?: string | undefined;
  dataTestid?: string | undefined;
}
let {
  // ... existing destructuring
  ariaLabel = undefined,
  dataTestid = undefined,
}: Props = $props();
```

And on **both** rendered chip element branches (the `<button>` branch when `interactive=true` and the `<span>` branch when interactive=false), forward them:

```svelte
{#if interactive}
  <button class="chip ..." aria-label={ariaLabel} data-testid={dataTestid} ...existing attrs...>
    {@render children?.()}
  </button>
{:else}
  <span class="chip ..." aria-label={ariaLabel} data-testid={dataTestid} ...existing attrs...>
    {@render children?.()}
  </span>
{/if}
```

Forwarding on only one branch breaks the CI chip — the normal-state CI chip is interactive (`<button>` branch, opens the dropdown), the unavailable variant is rendered via a separate `<span role="button">` element in `CIStatus.svelte` (not via `Chip.svelte` at all). The Playwright locators key on `data-testid="ci-chip"` regardless of the underlying element, and the aria-label is what makes `getByRole("button", { name: /CI: ... checks/ })` resolve. Both branches must forward, otherwise existing chip call sites silently lose the attributes too.

Svelte 5 omits the attribute when the value is `undefined`, so existing call sites that don't pass these props continue rendering without the attributes.

- [ ] **Step 2: Add unused props to CIStatus.svelte**

In `CIStatus.svelte`, add to the `Props` interface (do not consume them yet):

```svelte
interface Props {
  // ... existing props
  owner: string;
  name: string;
  number: number;
  prKey: string;
}
let { ..., owner, name, number, prKey }: Props = $props();
// 9b consumes owner/name/number (for parseCIChecks context + warning ctx)
// and 11 consumes prKey (for expandedSections reset).
void owner; void name; void number; void prKey; // silence unused-var lint
```

If the project's lint config rejects `void` no-op references, instead rename the locals via destructuring aliases so the unused-variable lint targets the alias rather than the prop name: `let { owner: _owner, name: _name, number: _number, prKey: _prKey }: Props = $props();`. The original prop names remain on the public component contract (callers still pass `owner=…`, `name=…`, etc.); only the in-component locals carry the underscore. Either form is fine — the goal is to land the props in the contract without consuming them and without a noisy disable comment. In Task 9b you'll rename them back (or drop the alias prefix) when consumption is wired up.

- [ ] **Step 3: Update every `<CIStatus>` call site**

```bash
rg -n '<CIStatus' packages/ ui/ frontend/
```

Known callers: `packages/ui/src/components/detail/PullDetail.svelte` (two usage sites). Pass `owner={owner} name={name} number={pr.Number} prKey={pr.PlatformExternalID}` at each site. The type-checker fails immediately if a caller is missed.

- [ ] **Step 4: Extend the existing CIStatus test props with the new fields**

Every `render(CIStatus, { props: { ... } })` in `CIStatus.test.ts` needs the four new fields. Define a small helper near the top of the file (if one doesn't already exist):

```typescript
const chipBaseProps = {
  status: "",
  detailLoaded: true,
  detailSyncing: false,
  owner: "acme",
  name: "widgets",
  number: 1,
  prKey: "ext-1",
};
```

Spread `...chipBaseProps` into every existing test's props object. Assertions stay unchanged — 9a does not modify rendered output, only the prop contract.

- [ ] **Step 5: Run typecheck and existing tests**

```bash
cd frontend && nix run nixpkgs#bun -- run check
cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/detail/CIStatus.test.ts
```

Expected: typecheck passes (Chip + CIStatus + PullDetail all type-check with the new props); existing CIStatus tests pass unchanged (no visual change yet).

- [ ] **Step 6: Commit**

```bash
git add packages/ui/src/components/shared/Chip.svelte packages/ui/src/components/detail/CIStatus.svelte packages/ui/src/components/detail/CIStatus.test.ts packages/ui/src/components/detail/PullDetail.svelte
git commit -m "feat(ui): extend Chip + introduce CIStatus contract props"
```

---

### Task 9b: Wire bucketing pipeline through `$derived` and rewrite the chip (normal + unavailable variants together)

This task depends on Task 9a's contract changes (Chip props + CIStatus props). It is intentionally bundled as one commit even though it touches multiple visual concerns (cluster, unavailable variant, warnings) — splitting *within* 9b risks intermediate UI states where (e.g.) the cluster renders but the malformed path falls through to old code, which the previous split attempt found to be a worse regression surface than the single-commit visual rewrite.

**Files:**
- Modify: `packages/ui/src/components/detail/CIStatus.svelte`
- Modify: `packages/ui/src/components/detail/CIStatus.test.ts`

- [ ] **Step 1: Update existing tests for the new chip text**

In `CIStatus.test.ts`, change the chip-button matcher in the "renders expanded CI checks when chip is clicked" test from `/CI:\s*success \(4\)/i` to a matcher for the new aria-label (e.g. `screen.getByRole("button", { name: /CI: 3 passed checks/i })`). Update other existing tests similarly. Run tests, expect failures (they'll fail until Step 3 lands).

- [ ] **Step 2: Add new chip tests**

Append to `CIStatus.test.ts`:

```typescript
const chipBaseProps = {
  status: "",
  detailLoaded: true,
  detailSyncing: false,
  owner: "acme",
  name: "widgets",
  number: 1,
  prKey: "ext-1",
};

it("renders the mixed-state token cluster", () => {
  const checks = [
    { name: "f", status: "completed", conclusion: "failure", url: "", app: "" },
    { name: "p1", status: "completed", conclusion: "success", url: "", app: "" },
    { name: "p2", status: "completed", conclusion: "success", url: "", app: "" },
    { name: "s", status: "completed", conclusion: "skipped", url: "", app: "" },
    { name: "pend", status: "in_progress", conclusion: "", url: "", app: "" },
  ];
  render(CIStatus, {
    props: { ...chipBaseProps, status: "failure", checksJSON: JSON.stringify(checks) },
  });
  expect(document.querySelector("[data-testid='ci-token-failed']")!.textContent).toContain("1");
  expect(document.querySelector("[data-testid='ci-token-pending']")!.textContent).toContain("1");
  expect(document.querySelector("[data-testid='ci-token-passed']")!.textContent).toContain("2");
  expect(document.querySelector("[data-testid='ci-token-skipped']")!.textContent).toContain("1");
});

it("hides the chip when there are zero checks and CIStatus is empty", () => {
  const { container } = render(CIStatus, {
    props: { ...chipBaseProps, checksJSON: "" },
  });
  expect(container.querySelector(".chip")).toBeNull();
});

it("hides the chip when CIStatus is set but CIChecksJSON is empty", () => {
  const { container } = render(CIStatus, {
    props: { ...chipBaseProps, status: "success", checksJSON: "" },
  });
  expect(container.querySelector(".chip")).toBeNull();
});

it("renders CI: unavailable chip when CIChecksJSON is malformed without leaking the raw payload to UI surfaces", () => {
  // Same sanitisation invariant as the sidebar test: a sentinel embedded
  // in malformed JSON must not appear in the chip's title, aria-describedby
  // sr-only text, or popover content. All four surfaces route through
  // safeDiagnosticText(parseError) which projects native parse errors to
  // stable categories.
  const sentinel = "supersecret_sentinel_xyz";
  render(CIStatus, {
    props: { ...chipBaseProps, checksJSON: `{"x":"${sentinel}",` },
  });
  expect(screen.getByText(/CI:\s*unavailable/i)).toBeTruthy();
  const unavail = document.querySelector("[aria-disabled='true']");
  expect(unavail).not.toBeNull();
  const title = unavail!.getAttribute("title") ?? "";
  expect(title).toMatch(/CI unavailable:/i);
  expect(title).not.toContain(sentinel);
  const popover = document.querySelector("[data-testid='ci-unavailable-popover']");
  expect(popover).not.toBeNull();
  expect(popover!.textContent ?? "").not.toContain(sentinel);
  expect(popover!.textContent ?? "").toMatch(/Malformed JSON/);
  // aria-label on the chip element must also be sanitised.
  expect(unavail!.getAttribute("aria-label") ?? "").not.toContain(sentinel);
});

it("fires malformed warning at most once per payload via console.warn", async () => {
  const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
  const { rerender } = render(CIStatus, {
    props: { ...chipBaseProps, checksJSON: "{not json" },
  });
  await rerender({ ...chipBaseProps, checksJSON: "{not json" });
  // Same payload — warning helper's internal Set keeps the actual log count at 1.
  expect(spy.mock.calls.filter(c =>
    typeof c[0] === "string" && c[0].includes("Malformed"))
  ).toHaveLength(1);
  spy.mockRestore();
});

it("fires unknown warning at most once per distinct conclusion via console.warn", async () => {
  const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
  const checks = JSON.stringify([{ status: "completed", conclusion: "weird_state" }]);
  const { rerender } = render(CIStatus, {
    props: { ...chipBaseProps, prKey: "A", checksJSON: checks },
  });
  await rerender({ ...chipBaseProps, prKey: "B", checksJSON: checks });
  expect(spy.mock.calls.filter(c =>
    typeof c[0] === "string" && c[0].includes("Unrecognised CI conclusion"))
  ).toHaveLength(1);
  spy.mockRestore();
});
```

- [ ] **Step 3: Run tests to verify all the new + updated ones fail**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/detail/CIStatus.test.ts`
Expected: many failures.

- [ ] **Step 4: Refactor `CIStatus.svelte` — chip rewrite + temporary dropdown adapter**

Replace the contents of `CIStatus.svelte` with a new version that:
1. Imports `parseCIChecks`, `bucketCIChecks`, `safeDiagnosticText` from `ci-buckets.js`.
2. Imports `warnOnUnknownConclusions`, `warnOnMalformedCIChecksJSON` from `ci-buckets-warn.js`.
3. Imports `CITokenCluster`, `composeAriaLabel` from `../shared/CITokenCluster.svelte`.
4. Replaces the existing `parseCIChecks` local helper and `checks/failedChecks/nonFailedChecks` derivations with a single `$derived` that calls `bucketCIChecks(parseCIChecks(checksJSON).checks)` plus a `parseError` derived from the same parse result.
5. Computes `hasCheckBucket = parseError === null && bucketed.all.length > 0` and `isUnavailable = parseError !== null`. The outer render gate is `shouldRender = hasCheckBucket || isUnavailable` — both the normal cluster branch and the unavailable branch ride inside this `{#if shouldRender}` block so neither is hidden.
6. Replaces the chip's children with one of two variants based on `isUnavailable`. Normal variant: `<span class="ci-label">CI</span>` + `<CITokenCluster bucketed={bucketed} size="default" />` + chevron. Unavailable variant: see below. Both carry `data-testid="ci-chip"` so e2e tests can locate the chip element without depending on aria-label strings.

   **Unavailable variant** — a `<span role="button" tabindex="0" aria-disabled="true" data-testid="ci-chip" aria-describedby="ci-unavailable-desc">` reading `CI: unavailable`, with a sibling positioning wrapper that holds a focus-visible diagnostic popover:

   ```svelte
   {#if isUnavailable}
     <span class="ci-unavailable-wrap">
       <span
         class="chip chip--muted ci-chip-unavailable"
         role="button"
         tabindex="0"
         aria-disabled="true"
         aria-describedby="ci-unavailable-desc-{instanceId}"
         data-testid="ci-chip"
         title="CI unavailable: {safeDiagnosticText(parseError)}"
       >CI: unavailable</span>
       <span
         class="sr-only"
         id="ci-unavailable-desc-{instanceId}"
       >CI unavailable: {safeDiagnosticText(parseError)}</span>
       <span
         class="ci-unavailable-popover"
         data-testid="ci-unavailable-popover"
       >CI unavailable: {safeDiagnosticText(parseError)}</span>
     </span>
   {/if}
   ```

   The popover is hidden by default and revealed via CSS when the chip is `:focus-visible` or `:hover`:

   ```css
   .ci-unavailable-wrap { position: relative; display: inline-flex; }
   .ci-unavailable-popover {
     position: absolute; top: calc(100% + 4px); left: 0;
     padding: 4px 8px; border-radius: 4px;
     background: var(--bg-surface); border: 1px solid var(--border-muted);
     color: var(--text-primary); font-size: var(--font-size-xs);
     box-shadow: var(--shadow-sm);
     max-width: 320px; white-space: normal; overflow-wrap: anywhere;
     opacity: 0; visibility: hidden;
     transition: opacity 0.12s;
     pointer-events: none;
   }
   .ci-chip-unavailable:hover + .sr-only + .ci-unavailable-popover,
   .ci-chip-unavailable:focus + .sr-only + .ci-unavailable-popover,
   .ci-chip-unavailable:focus-visible + .sr-only + .ci-unavailable-popover {
     opacity: 1; visibility: visible;
   }
   .sr-only {
     position: absolute; width: 1px; height: 1px; padding: 0;
     overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0;
   }
   ```

   `instanceId` is a unique per-component id sourced from a module-level monotonic counter (e.g., `let _ciStatusInstanceCounter = 0;` at module scope, then `const instanceId = ++_ciStatusInstanceCounter;` cached in `$state` so it stabilises per instance). This avoids `crypto.randomUUID()` — which has SSR/jsdom edge cases — and produces deterministic IDs in tests. The popover content is bounded to 320px width with wrapping so a long parse error doesn't blow out the layout.

   **Diagnostic text sanitisation.** The raw `parseError.message` can include a preview of the malformed input when the error originated from native `JSON.parse` (V8 messages like `Unexpected token X in JSON at position N` or `Unexpected end of JSON input near "..."` embed input fragments). To prevent the chip's `title`, popover content, sr-only text, and aria-describedby span from leaking provider-controlled content into UI surfaces, every display-bound interpolation uses `safeDiagnosticText(parseError)` (exported from `ci-buckets.ts` and added in Stage 1a Task 3). The category text is short, stable, and safe to render in any of the three surfaces. The original `parseError.message` is still available in dev logs via the warning helper.
7. Sets the chip's `aria-label` via `composeAriaLabel(bucketed)` when normal, or `\`CI unavailable: ${safeDiagnosticText(parseError)}\`` when unavailable. Never use raw `parseError.message` on any UI surface; the helper enforces the single sanitisation rule documented in non-goals.
8. Keeps the existing dropdown markup unchanged for now (it still reads the old `checks` / `failedChecks` derivations — wire those off `bucketed.all` and `bucketed.failed` to keep them functional).
9. Adds two `$effect` blocks. The first calls `warnOnUnknownConclusions(bucketed.unknown, ctx)` when `bucketed.unknown.length > 0`. The second calls `warnOnMalformedCIChecksJSON(checksJSON, parseError, ctx)` when `parseError !== null`. Both helpers' module-scoped Sets dedupe the actual `console.warn` calls; the effects may fire multiple times across rerenders, but the user-visible log count is bounded by the dedupe keys (raw conclusion for unknown; `repo+number+payload-hash` for malformed).

The `ctx` value is `{ repo: `${owner}/${name}`, number }` and consumes three of the four props (`owner`, `name`, `number`) introduced in Task 9a. The fourth, `prKey`, is consumed by Task 11 for the dropdown's expansion-state reset.

**prKey dependency (Task 9a → Task 11):** Task 9a introduces `prKey` as a prop on `CIStatus.svelte` and threads it from the call sites (`PullDetail.svelte` passes `prKey={pr.PlatformExternalID}`). Neither 9a nor 9b consumes `prKey` — it sits unused on the component until Task 11 wires it into the dropdown's `expandedSections` `$state` so the "Show N more" toggle resets when the user navigates between PRs. The reason 9a introduces the prop early (rather than Task 11 adding it alongside the dropdown work) is to keep the call-site grep + update step atomic: every `<CIStatus` usage is touched once in 9a, with the full final prop set, so 9b can add internal wiring without touching call sites and Task 11 only needs to add expansion-reset logic inside the component.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/detail/CIStatus.test.ts`
Expected: all new chip tests pass; existing dropdown tests still pass (adapter keeps them green).

- [ ] **Step 6: Remove now-unused legacy chip helpers**

Delete the now-orphaned `parseCIChecks` local helper (moved to `ci-buckets.ts` in Stage 1a), `chipColor`, and any chip-only color/text helpers that the rewrite supersedes. Keep `checkIcon`/`checkColor`/`isPendingCheck` for now — the dropdown adapter still uses them. Task 11 removes them when the dropdown is restructured.

- [ ] **Step 7: Drop the unused-prop silencers from Task 9a**

In Task 9a, `owner`, `name`, and `number` were introduced as unused props with a `void` (or underscore alias) silencer. 9b now consumes them via the `ctx` value and the `$effect` blocks, so the silencers are dead code — remove them. `prKey` is still unused at this point (Task 11 consumes it); leave its silencer in place.

- [ ] **Step 8: Commit**

```bash
git add packages/ui/src/components/detail/CIStatus.svelte packages/ui/src/components/detail/CIStatus.test.ts
git commit -m "feat(ui): rewrite CIStatus chip to use shared token cluster"
```

---

### Task 10: Update Playwright e2e for chip changes

**Files:**
- Modify: `cmd/e2e-server/main.go` — add `/__e2e/pr-ci-state/mixed` and `/__e2e/pr-ci-state/malformed` fixture endpoints (reused later by Task 14)
- Modify: `frontend/tests/e2e-full/ci-dropdown.spec.ts`

- [ ] **Step 1: Extract a shared `setPR1CIState` helper**

Before adding any new endpoint bodies, extract the common boilerplate from the existing `pr-ci-state/pending` and `pr-ci-state/success` handlers into a single helper. The new endpoints (`mixed`, `malformed`, `status-only`, and Task 12's `dropdown-mixed`) all do the same shape of work — repo lookup, optional payload marshal, `UpdateMRCIStatus`, `UpdateMRDetailFetchedByRepoID`, optional fixture-provider pin — and copy-pasting that block four more times is exactly how the endpoints would drift (one forgets the anti-resync stamp, another forgets the provider pin, e2e flake follows).

Place the helper at function scope inside the request handler (or as a package-private function near the `pr-ci-state/*` block):

```go
type ciFixtureOptions struct {
    statusName    string  // "failure" | "success" | "pending" | ...
    // checksJSON is the raw CIChecksJSON to seed. The empty string ""
    // writes an empty payload (the status-only fixture case). The helper
    // always writes this value to CIChecksJSON — there is no "leave it
    // alone" / "no-op" mode. If you need a transient state that doesn't
    // touch CIChecksJSON, add a new option flag rather than overloading
    // this field.
    checksJSON    string
    pinProviderTo *struct{ // nil means don't touch the fixture provider
        Status     string
        Conclusion string
    }
}

func setPR1CIState(
    w http.ResponseWriter,
    r *http.Request,
    database *db.DB,
    fc *fixturecontrol.Client,
    label string,
    opts ciFixtureOptions,
) bool {
    repo, err := database.GetRepoByOwnerName(r.Context(), "acme", "widgets")
    if err != nil || repo == nil {
        http.Error(w, "repo not found", http.StatusNotFound)
        return false
    }
    if err := database.UpdateMRCIStatus(
        r.Context(), repo.ID, 1, opts.statusName, opts.checksJSON,
    ); err != nil {
        http.Error(w, "update "+label+" CI", http.StatusInternalServerError)
        return false
    }
    // Explicit anti-resync guarantee — every fixture stamps detail_fetched_at
    // with ci_had_pending=false so the sync engine treats the seeded row as
    // fresh and doesn't refetch + overwrite it. Centralised here so no
    // future endpoint can forget it.
    if err := database.UpdateMRDetailFetchedByRepoID(
        r.Context(), repo.ID, 1, false,
    ); err != nil {
        http.Error(w, "mark "+label+" CI fetched", http.StatusInternalServerError)
        return false
    }
    if opts.pinProviderTo != nil {
        if !fc.SetPullRequestCheckRunStatus(
            "acme", "widgets", 1,
            opts.pinProviderTo.Status, opts.pinProviderTo.Conclusion,
        ) {
            http.Error(w, "update fixture check runs", http.StatusNotFound)
            return false
        }
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]string{"status": label})
    return true
}
```

The helper consumes all the divergence-prone surface area (anti-resync stamp + provider pin) and exposes per-fixture choices (status name, payload bytes, whether to pin the provider) via the options struct. Each new endpoint becomes a few lines: marshal its payload, populate `ciFixtureOptions`, call `setPR1CIState`. The existing `pending` and `success` endpoints **must also be migrated to use the helper in this same task** — leaving them on their original bespoke paths is the failure mode the helper extraction is designed to prevent (one of the seven fixtures forgets the anti-resync stamp during a future maintenance edit). Treat the migration as a hard step, not a follow-up: it's two small handler bodies and the diff stays co-located with the new endpoints so reviewers can verify all five fixtures use the same path.

The new endpoints below use the helper. The `mixed` and `dropdown-mixed` fixtures pin the provider state to a failing conclusion so a sync triggered by a route transition doesn't overwrite the seeded payload with synthesised success. `malformed` and `status-only` do **not** pin the provider state and rely entirely on the anti-resync stamp inside `setPR1CIState`: there's no provider analogue for a malformed JSON payload (a real sync would replace the seeded text with a valid array), and `status-only` keeps `CIStatus` aligned with an absent payload — pinning would be a no-op at best and could mask sync bugs. The anti-resync stamp is sufficient because both fixtures bypass freshness-driven refetches; only an explicit/forced sync against PR #1 would re-trigger the path, and tests don't issue one.

- [ ] **Step 2: Add the new endpoints using the helper**

Place the new endpoints next to the existing `pr-ci-state/pending` / `pr-ci-state/success` handlers in `cmd/e2e-server/main.go`. Each endpoint reduces to: build its payload, populate a `ciFixtureOptions`, dispatch through `setPR1CIState`. No endpoint should re-implement the repo lookup, anti-resync stamp, provider-pin error wiring, or JSON response — those live in the helper.

The `mixed`, `malformed`, and `status-only` endpoints are reused by Task 14's sidebar tests; `status-only` covers the transient state where `CIStatus` is set but `CIChecksJSON` is empty.

```go
if r.Method == http.MethodPost && r.URL.Path == "/__e2e/pr-ci-state/mixed" {
    mixedPayload, err := json.Marshal([]db.CICheck{
        {Name: "build-darwin",   Status: "completed",  Conclusion: "failure",
         URL: "https://github.com/acme/widgets/actions/runs/1/job/1",
         App: "GitHub Actions"},
        {Name: "build-linux",    Status: "completed",  Conclusion: "success",
         App: "GitHub Actions"},
        {Name: "test-linux",     Status: "completed",  Conclusion: "success",
         App: "GitHub Actions"},
        {Name: "deploy-staging", Status: "in_progress",                    Conclusion: "",
         App: "GitHub Actions"},
        {Name: "build-windows",  Status: "completed",  Conclusion: "skipped",
         App: "GitHub Actions"},
    })
    if err != nil {
        http.Error(w, "marshal mixed checks", http.StatusInternalServerError)
        return
    }
    setPR1CIState(w, r, database, fc, "mixed", ciFixtureOptions{
        statusName: "failure",
        checksJSON: string(mixedPayload),
        pinProviderTo: &struct{ Status, Conclusion string }{
            Status: "completed", Conclusion: "failure",
        },
    })
    return
}

if r.Method == http.MethodPost && r.URL.Path == "/__e2e/pr-ci-state/malformed" {
    // No fixture-provider analogue for malformed JSON exists — a real
    // sync would replace the seeded text with a valid array. The
    // anti-resync stamp inside setPR1CIState is the only defence.
    setPR1CIState(w, r, database, fc, "malformed", ciFixtureOptions{
        statusName: "failure",
        checksJSON: "{not json",
        // pinProviderTo intentionally nil
    })
    return
}

if r.Method == http.MethodPost && r.URL.Path == "/__e2e/pr-ci-state/status-only" {
    setPR1CIState(w, r, database, fc, "status-only", ciFixtureOptions{
        statusName: "success",
        checksJSON: "", // explicit empty payload — see helper field doc
        // pinProviderTo intentionally nil — CIStatus and provider stay aligned
    })
    return
}
```

- [ ] **Step 3: Migrate the existing `pending` and `success` endpoints to the helper**

Rewrite the bodies of the existing `/__e2e/pr-ci-state/pending` and `/__e2e/pr-ci-state/success` handlers in `cmd/e2e-server/main.go` so they call `setPR1CIState` instead of doing their own inline lookup-and-update. Confirm the existing semantics are preserved:

- `pending` seeded one `in_progress` check, set CIStatus to `"pending"`, stamped the anti-resync marker, and pinned the fixture provider to `("in_progress", "")`. New body:

  ```go
  if r.Method == http.MethodPost && r.URL.Path == "/__e2e/pr-ci-state/pending" {
      pendingPayload, err := json.Marshal([]db.CICheck{{
          Name: "build", Status: "in_progress", Conclusion: "",
          URL: "https://github.com/acme/widgets/actions/runs/1/job/1",
          App: "GitHub Actions",
      }})
      if err != nil {
          http.Error(w, "marshal pending checks", http.StatusInternalServerError)
          return
      }
      setPR1CIState(w, r, database, fc, "pending", ciFixtureOptions{
          statusName: "pending",
          checksJSON: string(pendingPayload),
          pinProviderTo: &struct{ Status, Conclusion string }{
              Status: "in_progress", Conclusion: "",
          },
      })
      return
  }
  ```

- `success` historically only pinned the fixture provider (no DB write). The helper always writes both `UpdateMRCIStatus` and the anti-resync stamp, so the new body must seed a **valid all-passed `CIChecksJSON` array** — not an empty payload. The redesigned UI intentionally hides the chip when `CIChecksJSON` is empty (the status-only transient case), so seeding `""` here would break every existing test that uses the success fixture to verify a positive CI chip. The new body seeds a couple of representative success rows so the chip renders the "all passed" acceptance state:

  ```go
  if r.Method == http.MethodPost && r.URL.Path == "/__e2e/pr-ci-state/success" {
      successPayload, err := json.Marshal([]db.CICheck{
          {Name: "build", Status: "completed", Conclusion: "success",
           URL: "https://github.com/acme/widgets/actions/runs/1/job/1",
           App: "GitHub Actions"},
          {Name: "test",  Status: "completed", Conclusion: "success",
           App: "GitHub Actions"},
      })
      if err != nil {
          http.Error(w, "marshal success checks", http.StatusInternalServerError)
          return
      }
      setPR1CIState(w, r, database, fc, "success", ciFixtureOptions{
          statusName: "success",
          checksJSON: string(successPayload),
          pinProviderTo: &struct{ Status, Conclusion string }{
              Status: "completed", Conclusion: "success",
          },
      })
      return
  }
  ```

  Two checks is enough — the success fixture's existing consumers just need the chip to render a Passed token, not specific counts. The acceptance-matrix "All passed" row's illustrative 26-check count is just that, illustrative; a Passed-only chip rendering 2 vs 26 exercises the same code path (one bucket token, count derived from `bucketed.passed.length`). If a downstream spec ever needs a high-count Passed case to verify the dropdown's "Show N more" affordance, the existing `dropdown-mixed` fixture already provides 12 passed checks for that purpose. Don't bloat the `success` fixture to 26 checks — the test cost grows linearly with no signal gain.

After migrating, run the existing e2e specs that exercise these endpoints (`pull-detail-ci.spec.ts` or whichever specs grep matches `pr-ci-state/pending` / `pr-ci-state/success`) and confirm they still pass. The migration is intentionally in this task — not a follow-up — to guarantee all five fixtures stay on one path before any new test depends on the new helper.

- [ ] **Step 4: Update existing aria-label/text matchers**

Find each `getByRole("button", { name: /CI:\s*(success|pending|failure) \(N\)/i })` in `ci-dropdown.spec.ts` and replace with the new aria-label format, e.g. `/CI: \d+ (passed|pending|failed|skipped) checks?/i`.

- [ ] **Step 5: Add mixed-state, malformed, transient, unknown-only chip tests**

Append to `ci-dropdown.spec.ts`:

```typescript
test("mixed-state chip renders all bucket tokens", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const seed = await page.request.post(
      `${server.info.base_url}/__e2e/pr-ci-state/mixed`,
    );
    expect(seed.ok()).toBe(true);

    await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

    const chip = page.locator(".pull-detail [data-testid='ci-chip']");
    await expect(chip.locator("[data-testid='ci-token-failed']")).toHaveText(/1/);
    await expect(chip.locator("[data-testid='ci-token-pending']")).toHaveText(/1/);
    await expect(chip.locator("[data-testid='ci-token-passed']")).toHaveText(/2/);
    await expect(chip.locator("[data-testid='ci-token-skipped']")).toHaveText(/1/);
  } finally {
    await server.stop();
  }
});

test("malformed CIChecksJSON renders the unavailable chip with focus-visible popover", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const seed = await page.request.post(
      `${server.info.base_url}/__e2e/pr-ci-state/malformed`,
    );
    expect(seed.ok()).toBe(true);

    await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

    const chip = page.locator(".pull-detail [data-testid='ci-chip']");
    await expect(chip).toContainText(/CI:\s*unavailable/i);
    await expect(chip).toHaveAttribute("aria-disabled", "true");
    await expect(chip).toHaveAttribute("title", /CI unavailable:/i);

    const popover = page.locator(".pull-detail [data-testid='ci-unavailable-popover']");
    // Popover is in the DOM but hidden until the chip is focused.
    await expect(popover).toHaveCSS("visibility", "hidden");
    await chip.focus();
    await expect(chip).toBeFocused();
    await expect(popover).toHaveCSS("visibility", "visible");
    await expect(popover).toContainText(/CI unavailable:/i);
  } finally {
    await server.stop();
  }
});

test("CIStatus set but CIChecksJSON empty hides the chip (transient sync state)", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const seed = await page.request.post(
      `${server.info.base_url}/__e2e/pr-ci-state/status-only`,
    );
    expect(seed.ok()).toBe(true);

    await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

    await expect(page.locator(".pull-detail [data-testid='ci-chip']")).toHaveCount(0);
  } finally {
    await server.stop();
  }
});
```

Also add component-level coverage for the unknown-only chip directly in `CIStatus.test.ts` (no separate e2e endpoint — the unknown-only state is straightforward to seed via `JSON.stringify` in component tests). Append to `CIStatus.test.ts`:

```typescript
it("renders a single Unknown token when all checks have unrecognised conclusions", async () => {
  const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
  render(CIStatus, {
    props: {
      ...chipBaseProps,
      checksJSON: JSON.stringify([
        { name: "weird-only", status: "completed", conclusion: "mystery_state", url: "", app: "" },
      ]),
    },
  });
  // Exactly one Unknown token, no other bucket tokens.
  expect(document.querySelector("[data-testid='ci-token-unknown']")).not.toBeNull();
  expect(document.querySelector("[data-testid='ci-token-failed']")).toBeNull();
  expect(document.querySelector("[data-testid='ci-token-pending']")).toBeNull();
  expect(document.querySelector("[data-testid='ci-token-passed']")).toBeNull();
  expect(document.querySelector("[data-testid='ci-token-skipped']")).toBeNull();
  // The unknown warning fires once.
  expect(spy.mock.calls.filter(c =>
    typeof c[0] === "string" && c[0].includes("Unrecognised CI conclusion"))
  ).toHaveLength(1);
  spy.mockRestore();
});
```

The matching sidebar unknown-only test lives in Task 13 (where `PullItem.test.ts` and the cluster rendering land together). This Task only adds the chip-side coverage so the unknown-only acceptance row gets at least one test in each surface as soon as that surface exists.

- [ ] **Step 6: Run the new tests locally**

Two test runs — the Playwright specs added in Step 5 AND the unknown-only chip component test that was appended to `CIStatus.test.ts` above:

```bash
cd frontend && nix run nixpkgs#bun -- run test:e2e -- \
  --grep "mixed-state chip|unavailable chip|transient sync state"
cd frontend && nix run nixpkgs#bun -- run test -- --run \
  ../packages/ui/src/components/detail/CIStatus.test.ts
```

Expected: both pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/e2e-server/main.go \
        frontend/tests/e2e-full/ci-dropdown.spec.ts \
        packages/ui/src/components/detail/CIStatus.test.ts
git commit -m "test(ui): add e2e + unknown-only chip coverage for new CI rendering"
```

---

## Stage 4b: Dropdown restructure

### Task 11: Summary header + five-section dropdown + show-more toggle

**Files:**
- Modify: `packages/ui/src/components/detail/CIStatus.svelte`
- Modify: `packages/ui/src/components/detail/CIStatus.test.ts`

- [ ] **Step 1: Write the failing tests for the new dropdown structure**

Append to `CIStatus.test.ts`:

```typescript
function mkCheck(partial: Partial<{
  name: string; status: string; conclusion: string;
  duration_seconds: number; url: string; app: string;
}> = {}) {
  return {
    name: "x", status: "completed", conclusion: "success",
    url: "", app: "", ...partial,
  };
}

const baseProps = {
  status: "",
  detailLoaded: true,
  detailSyncing: false,
  expanded: true,
  owner: "o",
  name: "n",
  number: 1,
  prKey: "ext-1",
};

it("dropdown shows summary header with longest duration", () => {
  render(CIStatus, {
    props: {
      ...baseProps,
      checksJSON: JSON.stringify([
        mkCheck({ duration_seconds: 30 }),
        mkCheck({ duration_seconds: 90 }),
      ]),
    },
  });
  expect(document.querySelector(".ci-summary")!.textContent).toMatch(
    /2 checks · longest 1m 30s/,
  );
});

it("dropdown renders five sections in fixed order when all non-zero", () => {
  const c = (conclusion: string, status = "completed") =>
    mkCheck({ status, conclusion });
  render(CIStatus, {
    props: {
      ...baseProps,
      checksJSON: JSON.stringify([
        c("failure"),
        c("", "in_progress"),
        c("weird_new_state"),
        c("success"),
        c("skipped"),
      ]),
    },
  });
  const headings = Array.from(document.querySelectorAll(".ci-section-heading"))
    .map((h) => h.textContent?.trim() ?? "");
  expect(headings).toEqual([
    "Failed (1)", "Pending (1)", "Unknown (1)", "Passed (1)", "Skipped (1)",
  ]);
});

it("Passed section shows first 8 + Show 1 more toggle", async () => {
  const checks = Array.from({ length: 9 }, (_, i) =>
    mkCheck({ name: `p${i}` }));
  render(CIStatus, { props: { ...baseProps, checksJSON: JSON.stringify(checks) } });
  expect(document.querySelectorAll(".ci-row").length).toBe(8);
  const toggle = screen.getByRole("button", { name: /Show 1 more passed/i });
  await fireEvent.click(toggle);
  expect(document.querySelectorAll(".ci-row").length).toBe(9);
  const collapseToggle = screen.getByRole("button", { name: /Show fewer passed/i });
  await fireEvent.click(collapseToggle);
  expect(document.querySelectorAll(".ci-row").length).toBe(8);
});

it("dropdown row uses bucket Lucide icon (not ASCII glyph)", () => {
  render(CIStatus, {
    props: {
      ...baseProps,
      checksJSON: JSON.stringify([mkCheck({ conclusion: "failure" })]),
    },
  });
  const row = document.querySelector(".ci-row")!;
  expect(row.querySelector("svg")).not.toBeNull();
  expect(row.textContent).not.toContain("✗");
});

it("expansion state resets when prKey changes", async () => {
  const checks = Array.from({ length: 9 }, (_, i) =>
    mkCheck({ name: `p${i}` }));
  const { rerender } = render(CIStatus, {
    props: { ...baseProps, prKey: "ext-A", checksJSON: JSON.stringify(checks) },
  });
  await fireEvent.click(screen.getByRole("button", { name: /Show 1 more passed/i }));
  expect(document.querySelectorAll(".ci-row").length).toBe(9);
  await rerender({ ...baseProps, prKey: "ext-B", checksJSON: JSON.stringify(checks) });
  expect(document.querySelectorAll(".ci-row").length).toBe(8);
});

it("renders static circle for pending row under prefers-reduced-motion", () => {
  vi.spyOn(window, "matchMedia").mockReturnValue(
    { matches: true } as MediaQueryList,
  );
  __resetPrefersReducedMotion();
  render(CIStatus, {
    props: {
      ...baseProps,
      checksJSON: JSON.stringify([
        mkCheck({ status: "in_progress", conclusion: "" }),
      ]),
    },
  });
  const row = document.querySelector(".ci-row")!;
  expect(row.querySelector(".spin")).toBeNull();
});
```

Update the test file's `beforeEach`/`afterEach` to reset module-scoped state between tests:

```typescript
beforeEach(() => {
  __resetCIWarnings();
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  __resetPrefersReducedMotion();
});
```

The `__resetCIWarnings` call is critical because `warnOnUnknownConclusions` / `warnOnMalformedCIChecksJSON` keep a module-scoped Set of already-warned keys. Without the reset, earlier tests that trigger a warning leave that key in the Set; later tests asserting "fires exactly once" see zero fires and fail order-dependently.

Imports for `vi`, `__resetPrefersReducedMotion`, `__resetCIWarnings`, and the existing testing-library helpers need to be present at the top of the file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/detail/CIStatus.test.ts`
Expected: new dropdown tests fail.

- [ ] **Step 3: Restructure the dropdown markup**

In `CIStatus.svelte`'s `{#if showPanel && expanded}` block:

1. Replace the existing `{#if failedChecks.length > 0} … {/if}` + `{#each nonFailedChecks}` block with:
   - A summary header div: `<div class="ci-summary">{bucketed.all.length} checks{#if bucketed.longestCompletedDurationSeconds !== undefined} · longest {formatDuration(bucketed.longestCompletedDurationSeconds)}{/if}</div>`
   - Five `{#if bucketed.<bucket>.length > 0}` blocks in the order Failed, Pending, Unknown, Passed, Skipped. Each block wraps its rows in `<div class="ci-section ci-section-<bucket>">` so the e2e and component tests can count rows per section. Each block renders a `<div class="ci-section-heading">` with the bucket color class and the count, then the first `THRESHOLD` (8) rows for Passed/Skipped (or all rows for Failed/Pending/Unknown), then a "Show N more <bucket>" / "Show fewer <bucket>" toggle button for Passed and Skipped when over the threshold.
2. Replace the per-row icon (ASCII glyph or LoaderCircle) with the bucket's Lucide icon. Use a small helper that picks the icon by bucket, **and** swaps the Pending row icon between `LoaderCircleIcon` (animated, wrapped in `.spin`) and `CircleIcon` (static) based on `prefersReducedMotion().value` — same swap rule as `CITokenCluster`. Concrete sketch:

   ```svelte
   <script lang="ts">
     import { prefersReducedMotion } from "../../utils/prefers-reduced-motion.svelte.js";
     // imports for CircleXIcon, CircleCheckIcon, CircleMinusIcon, CircleHelpIcon,
     //                CircleIcon, LoaderCircleIcon
     const reduced = prefersReducedMotion();
   </script>

   {#snippet rowIcon(bucket, check)}
     {#if bucket === "failed"}<CircleXIcon size={14} class="row-icon row-icon-red" />
     {:else if bucket === "pending"}
       {#if reduced.value}<CircleIcon size={14} class="row-icon row-icon-amber" />
       {:else}<span class="spin"><LoaderCircleIcon size={14} class="row-icon row-icon-amber" /></span>{/if}
     {:else if bucket === "passed"}<CircleCheckIcon size={14} class="row-icon row-icon-green" />
     {:else if bucket === "skipped"}<CircleMinusIcon size={14} class="row-icon row-icon-muted" />
     {:else}<CircleHelpIcon size={14} class="row-icon row-icon-purple" />{/if}
   {/snippet}
   ```
3. Wire the `prKey` prop (already added to `CIStatus.svelte` in Task 9a and threaded from `PullDetail.svelte`) into a new `expandedSections` `$state` plus a reset `$effect`:

   ```svelte
   <script lang="ts">
     // prKey was added to Props in Task 9a; this task only consumes it.
     // Do NOT re-add it to the Props interface or re-touch PullDetail.svelte.
     const expandedSections = $state<Record<"passed" | "skipped", boolean>>({
       passed: false, skipped: false,
     });
     $effect(() => {
       // Reset when key changes — referencing prKey inside the effect
       // registers the dependency.
       prKey;
       expandedSections.passed = false;
       expandedSections.skipped = false;
     });
   </script>
   ```

   Also remove the `prKey` unused-prop silencer that Task 9a installed (the `_prKey` alias or the `void prKey;` line). `prKey` is now consumed.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/detail/CIStatus.test.ts`
Expected: all dropdown tests pass (including the reduced-motion test from step 1).

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/components/detail/CIStatus.svelte packages/ui/src/components/detail/CIStatus.test.ts
git commit -m "feat(ui): restructure CI dropdown with summary header and five sections"
```

---

### Task 12: Dropdown Playwright e2e

**Files:**
- Modify: `cmd/e2e-server/main.go` — add `/__e2e/pr-ci-state/dropdown-mixed` fixture for the bigger payload this test needs
- Modify: `frontend/tests/e2e-full/ci-dropdown.spec.ts`

- [ ] **Step 1: Add the `dropdown-mixed` fixture endpoint**

This endpoint seeds a richer mixed-state payload (10+ passed checks so the "Show N more" affordance is exercised). It calls the `setPR1CIState` helper extracted in Task 10 Step 1 — the body below shows the payload + options, not the full lookup-and-stamp boilerplate (that lives in the helper). Append next to the Task 10 endpoints:

```go
if r.Method == http.MethodPost && r.URL.Path == "/__e2e/pr-ci-state/dropdown-mixed" {
    checks := []db.CICheck{
        {Name: "build-darwin", Status: "completed", Conclusion: "failure",
         App: "GitHub Actions"},
    }
    for i := 1; i <= 5; i++ {
        checks = append(checks, db.CICheck{
            Name: fmt.Sprintf("pending-%d", i), Status: "in_progress",
            App: "GitHub Actions",
        })
    }
    for i := 1; i <= 12; i++ {
        checks = append(checks, db.CICheck{
            Name:   fmt.Sprintf("passed-%d", i),
            Status: "completed", Conclusion: "success",
            App:    "GitHub Actions",
        })
    }
    checks = append(checks,
        db.CICheck{Name: "skip-1", Status: "completed", Conclusion: "skipped",
                   App: "GitHub Actions"},
        db.CICheck{Name: "skip-2", Status: "completed", Conclusion: "skipped",
                   App: "GitHub Actions"},
        db.CICheck{Name: "weird",  Status: "completed", Conclusion: "mysterious_state",
                   App: "GitHub Actions"},
    )
    dropdownPayload, err := json.Marshal(checks)
    if err != nil {
        http.Error(w, "marshal dropdown-mixed checks", http.StatusInternalServerError)
        return
    }
    setPR1CIState(w, r, database, fc, "dropdown-mixed", ciFixtureOptions{
        statusName: "failure",
        checksJSON: string(dropdownPayload),
        pinProviderTo: &struct{ Status, Conclusion string }{
            Status: "completed", Conclusion: "failure",
        },
    })
    return
}
```

(Add `"fmt"` to the file imports if not already present.)

- [ ] **Step 2: Add the dropdown e2e**

Append to `ci-dropdown.spec.ts`:

```typescript
test("dropdown shows summary header, five sections, show-N-more toggle", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    const seed = await page.request.post(
      `${server.info.base_url}/__e2e/pr-ci-state/dropdown-mixed`,
    );
    expect(seed.ok()).toBe(true);

    await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

    const chip = page.locator(".pull-detail [data-testid='ci-chip']");
    await chip.click();

    const panel = page.locator(".pull-detail .ci-checks");
    await expect(panel).toBeVisible();

    await expect(panel.locator(".ci-summary")).toContainText(/\d+ checks/);

    const headings = panel.locator(".ci-section-heading");
    await expect(headings).toHaveCount(5);
    await expect(headings.nth(0)).toContainText(/Failed \(1\)/);
    await expect(headings.nth(1)).toContainText(/Pending \(5\)/);
    await expect(headings.nth(2)).toContainText(/Unknown \(1\)/);
    await expect(headings.nth(3)).toContainText(/Passed \(12\)/);
    await expect(headings.nth(4)).toContainText(/Skipped \(2\)/);

    const showMore = panel.getByRole("button", { name: /Show 4 more passed/i });
    await expect(showMore).toBeVisible();
    await showMore.click();

    const passedRowsAfter = panel.locator(".ci-section-passed .ci-row");
    await expect(passedRowsAfter).toHaveCount(12);

    const showFewer = panel.getByRole("button", { name: /Show fewer passed/i });
    await showFewer.click();
    await expect(panel.locator(".ci-section-passed .ci-row")).toHaveCount(8);
  } finally {
    await server.stop();
  }
});
```

Note: the impl in Task 11 should add `.ci-section-passed` / `.ci-section-skipped` class hooks (or `data-testid` equivalents) so the per-section row count is queryable. Update Task 11's restructure markup step to include those class hooks.

- [ ] **Step 3: Run the spec**

Run: `cd frontend && nix run nixpkgs#bun -- run test:e2e -- --grep "dropdown shows summary"`
Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/e2e-server/main.go frontend/tests/e2e-full/ci-dropdown.spec.ts
git commit -m "test(ui): add e2e for dropdown summary, sections, show-N-more"
```

---

## Stage 5: PullItem.svelte sidebar cluster

### Task 13: Replace the single sidebar CI icon with the shared cluster

**Files:**
- Modify: `packages/ui/src/components/sidebar/PullItem.svelte`
- Create: `packages/ui/src/components/sidebar/PullItem.test.ts`

- [ ] **Step 1: Write the failing tests**

```typescript
// packages/ui/src/components/sidebar/PullItem.test.ts
import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import PullItem from "./PullItem.svelte";

import type { PullRequest } from "../../api/types.js";

const mkPR = (overrides: Record<string, unknown>): PullRequest =>
  ({
    Number: 1,
    Title: "title",
    Author: "x",
    State: "open",
    IsDraft: false,
    KanbanStatus: "new",
    CIStatus: "",
    CIChecksJSON: "",
    MergeableState: "clean",
    LastActivityAt: new Date().toISOString(),
    PlatformExternalID: "ext-1",
    repo_owner: "o",
    repo_name: "n",
    worktree_links: [],
    Starred: false,
    ...overrides,
  }) as unknown as PullRequest;

import { __resetCIWarnings } from "../../utils/ci-buckets-warn.js";

describe("PullItem CI cluster", () => {
  beforeEach(() => {
    __resetCIWarnings();
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders compact tokens for a mixed-state PR", () => {
    const checks = [
      { status: "completed", conclusion: "failure", name: "f", url: "", app: "" },
      { status: "completed", conclusion: "success", name: "p1", url: "", app: "" },
      { status: "in_progress", conclusion: "", name: "pe", url: "", app: "" },
    ];
    render(PullItem, {
      props: {
        pr: mkPR({ CIChecksJSON: JSON.stringify(checks) }),
        selected: false,
        showRepo: false,
        onclick: () => {},
      },
    });
    expect(document.querySelector("[data-testid='ci-token-failed']")).not.toBeNull();
    expect(document.querySelector("[data-testid='ci-token-pending']")).not.toBeNull();
    expect(document.querySelector("[data-testid='ci-token-passed']")).not.toBeNull();
  });

  it("Pending token is static (no spin animation) in sidebar", () => {
    render(PullItem, {
      props: {
        pr: mkPR({ CIChecksJSON: JSON.stringify([{ status: "in_progress", conclusion: "" }]) }),
        selected: false,
        showRepo: false,
        onclick: () => {},
      },
    });
    const pendingTok = document.querySelector("[data-testid='ci-token-pending']")!;
    expect(pendingTok.querySelector(".spin")).toBeNull();
  });

  it("hides cluster when PR has no CI", () => {
    render(PullItem, {
      props: { pr: mkPR({}), selected: false, showRepo: false, onclick: () => {} },
    });
    expect(document.querySelector("[data-testid^='ci-token-']")).toBeNull();
  });

  it("hides cluster when CIStatus is set but CIChecksJSON is empty", () => {
    render(PullItem, {
      props: {
        pr: mkPR({ CIStatus: "success", CIChecksJSON: "" }),
        selected: false,
        showRepo: false,
        onclick: () => {},
      },
    });
    expect(document.querySelector("[data-testid^='ci-token-']")).toBeNull();
  });

  it("renders unavailable token when CIChecksJSON is malformed without leaking the raw payload via title or accessible name", () => {
    // Embed a sentinel in the malformed JSON. Native JSON.parse may
    // include input fragments in the SyntaxError message. The sidebar
    // diagnostic surfaces (title attr, sr-only span feeding the row
    // button's accessible name) MUST route through safeDiagnosticText
    // and never expose the sentinel.
    const sentinel = "supersecret_sentinel_xyz";
    render(PullItem, {
      props: {
        pr: mkPR({ CIChecksJSON: `{"x":"${sentinel}",`, Title: "Sample PR" }),
        selected: false,
        showRepo: false,
        onclick: () => {},
      },
    });
    expect(document.querySelector("[data-testid='ci-token-unavailable']")).not.toBeNull();
    const titleAttr = document.querySelector("[data-testid='ci-token-unavailable']")
      ?.getAttribute("title") ?? "";
    expect(titleAttr).not.toContain(sentinel);
    expect(titleAttr).toMatch(/CI unavailable:/i);
    const button = screen.getByRole("button", { name: /Sample PR/i });
    // The button's full accessible name must include the diagnostic
    // category but must NOT include the sentinel.
    const ciNameMatch = screen.queryByRole("button", { name: new RegExp(sentinel) });
    expect(ciNameMatch).toBeNull();
    expect(screen.getByRole("button", { name: /CI unavailable:/i })).toBe(button);
  });

  it("row button exposes 'CI unavailable:' through its accessible name for malformed CI", () => {
    render(PullItem, {
      props: {
        pr: mkPR({ CIChecksJSON: "{not json", Title: "Sample PR" }),
        selected: false,
        showRepo: false,
        onclick: () => {},
      },
    });
    // Rely on @testing-library's getByRole name matcher rather than
    // hand-rolling textContent+aria-label concatenation. The matcher
    // resolves the accessible name via the same algorithm browsers and
    // screen readers use (computeAccessibleName from dom-accessibility-api,
    // pulled in transitively by @testing-library/dom). If a descendant
    // aria-label is present in the DOM but the browser AccName rules
    // wouldn't surface it, this query will fail — which is exactly the
    // assertion we want for the diagnostic.
    const titleMatch = screen.getByRole("button", { name: /Sample PR/i });
    const ciMatch = screen.getByRole("button", { name: /CI unavailable:/i });
    expect(ciMatch).toBe(titleMatch);
  });

  it("row button exposes the CI cluster summary through its accessible name for normal CI", () => {
    const checks = [
      { status: "completed", conclusion: "failure", name: "f", url: "", app: "" },
      { status: "completed", conclusion: "success", name: "p1", url: "", app: "" },
      { status: "completed", conclusion: "success", name: "p2", url: "", app: "" },
      { status: "in_progress", conclusion: "", name: "pe", url: "", app: "" },
    ];
    render(PullItem, {
      props: {
        pr: mkPR({ CIChecksJSON: JSON.stringify(checks), Title: "Sample PR" }),
        selected: false,
        showRepo: false,
        onclick: () => {},
      },
    });
    // Each substring is asserted via its own getByRole({ name }) lookup.
    // All three must resolve to the same row button — that proves the
    // accessible name includes every fragment and that the row is the
    // sole accessible-name carrier (no ambiguous duplicate buttons).
    const titleMatch = screen.getByRole("button", { name: /Sample PR/i });
    expect(screen.getByRole("button", { name: /1 failed/i })).toBe(titleMatch);
    expect(screen.getByRole("button", { name: /1 pending/i })).toBe(titleMatch);
    expect(screen.getByRole("button", { name: /2 passed/i })).toBe(titleMatch);
  });

  it("dedupes console.warn across many rows with identical malformed payloads", async () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const props = {
      pr: mkPR({ CIChecksJSON: "{not json", Number: 1 }),
      selected: false,
      showRepo: false,
      onclick: () => {},
    };
    render(PullItem, { props });
    cleanup();
    render(PullItem, { props: { ...props, pr: mkPR({ CIChecksJSON: "{not json", Number: 1 }) } });
    cleanup();
    render(PullItem, { props: { ...props, pr: mkPR({ CIChecksJSON: "{not json", Number: 1 }) } });
    expect(spy.mock.calls.filter(c =>
      typeof c[0] === "string" && c[0].includes("Malformed"))
    ).toHaveLength(1);
    spy.mockRestore();
  });

  it("renders a single Unknown token for an unknown-only payload (acceptance-matrix Unknown-only row)", () => {
    render(PullItem, {
      props: {
        pr: mkPR({ CIChecksJSON: JSON.stringify([
          { status: "completed", conclusion: "mystery_state", name: "", url: "", app: "" },
        ]) }),
        selected: false,
        showRepo: false,
        onclick: () => {},
      },
    });
    expect(document.querySelector("[data-testid='ci-token-unknown']")).not.toBeNull();
    expect(document.querySelector("[data-testid='ci-token-failed']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-pending']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-passed']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-skipped']")).toBeNull();
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/sidebar/PullItem.test.ts`
Expected: token elements not found.

- [ ] **Step 3: Refactor `PullItem.svelte`**

In `PullItem.svelte`:
1. Import `parseCIChecks`, `bucketCIChecks`, `safeDiagnosticText` from `../../utils/ci-buckets.js`.
2. Import `warnOnUnknownConclusions`, `warnOnMalformedCIChecksJSON` from `../../utils/ci-buckets-warn.js`.
3. Import `CITokenCluster`, `composeAriaLabel` from `../shared/CITokenCluster.svelte`.
4. Import `CircleAlertIcon` from `@lucide/svelte/icons/circle-alert`.
5. Add `const parsed = $derived(parseCIChecks(pr.CIChecksJSON));` and `const bucketed = $derived(bucketCIChecks(parsed.checks));`.
6. Add `$effect`s that fire the warning helpers with `{ repo: \`${pr.repo_owner}/${pr.repo_name}\`, number: pr.Number }`.
7. Remove the existing `{#if pr.CIStatus === "success"} … {/if}` block.
8. Replace with the following markup. The CI summary text lives in a real `<span class="sr-only">` *sibling* of the visual cluster, both inside the row button. The visible cluster is `aria-hidden` (its tokens carry no semantic content for AT); the sr-only span is the sole carrier of the accessible name fragment. This avoids relying on AccName's "wrap an element with aria-label and its descendants get included" behaviour for a generic `<span>` — visually-hidden text inside the button is the most robust pattern across browsers and screen readers.

   ```svelte
   {#if parsed.error !== null}
     <span class="ci ci-unavailable" data-testid="ci-token-unavailable"
           title={`CI unavailable: ${safeDiagnosticText(parsed.error)}`} aria-hidden="true">
       <CircleAlertIcon size={10} strokeWidth={2.5} />
     </span>
     <span class="sr-only">CI unavailable: {safeDiagnosticText(parsed.error)}</span>
   {:else if bucketed.all.length > 0}
     <span class="ci" aria-hidden="true">
       <CITokenCluster {bucketed} size="compact" pendingStyle="static" />
     </span>
     <span class="sr-only">{composeAriaLabel(bucketed)}</span>
   {/if}
   ```

   The `.sr-only` rule (from Task 9b's CSS, or already present elsewhere — verify with a grep, and add it locally to PullItem.svelte if missing):

   ```css
   .sr-only {
     position: absolute; width: 1px; height: 1px; padding: 0;
     overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0;
   }
   ```

   **`.ci-unavailable` token style.** Distinct from the failed/pending tokens so a sighted user can tell at a glance that this is a data-shape problem, not a CI failure or in-progress state:

   ```css
   .ci-unavailable {
     color: var(--state-warn, var(--accent-amber, #c08a2a));
     opacity: 0.85;
   }
   ```

   The amber/warning hue reuses the project's existing `--state-warn` token if present (with `--accent-amber` and a literal hex as fallbacks). It's intentionally **not** the failed-bucket red (which would suggest a CI failure) and **not** the pending-bucket amber-with-spin (no spinner here — the data is broken, not in flight). The slight opacity drop signals "data not available" without making the token blend with the row background. The icon is `CircleAlertIcon` rather than `CircleXIcon` for the same reason: the alert glyph conveys "something needs your attention about this PR's CI data," not "a CI job failed."

9. The row's CI accessibility comes entirely from the sr-only span inside the row button — visually hidden but exposed to AT via plain text content (no `aria-label`, no `aria-hidden`). Screen readers hear "CI: 1 failed check, 5 pending checks, …" once because the visible cluster tree is fully `aria-hidden`, and the sr-only sibling provides the text content as part of the button's accessible name via standard name-from-content. The `title` on the visible cluster is the mouse-hover diagnostic only.

   Don't add an explicit `aria-label` to the row button — that would override the visible text and lose the title-first reading. The token itself doesn't need to be focusable because the row button is — this is intentional and keeps sidebar density. **Verification:** the PullItem test asserts that `screen.getByRole("button", { name: /CI unavailable:/i })` returns the same row as `getByRole("button", { name: /<PR title>/i })`. If the sr-only span is ever dropped, the test fails because the name matcher uses the W3C accessible-name algorithm (via `dom-accessibility-api`), which sees the visible cluster as aria-hidden and so the only CI text contributing to the button's accessible name is the sr-only sibling.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && nix run nixpkgs#bun -- run test -- --run ../packages/ui/src/components/sidebar/PullItem.test.ts`
Expected: pass.

- [ ] **Step 5: Add mobile gap tightening CSS**

In `PullItem.svelte`'s `<style>` block, append:

```css
.pull-item .ci {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  gap: 5px;
}
:global(.mobile-main) .pull-item .ci {
  gap: 3px;
}
```

`gap` only takes effect on flex (or grid) containers — `.ci` is a `<span>` by default, which renders inline. Setting `display: inline-flex; align-items: center` turns it into a flex line that respects `gap`, keeps the cluster vertically aligned with the rest of the row, and prevents the cluster from wrapping into the row's `flex-shrink` cascade (the `flex-shrink: 0` reserves cluster space when the sidebar narrows). Base rule first, mobile override after, so the mobile-only `gap` adjustment cascades without depending on selector specificity.

- [ ] **Step 6: Commit**

```bash
git add packages/ui/src/components/sidebar/PullItem.svelte packages/ui/src/components/sidebar/PullItem.test.ts
git commit -m "feat(ui): replace sidebar CI icon with compact token cluster"
```

---

### Task 14: Playwright e2e for sidebar list payload

**Files:**
- Create: `frontend/tests/e2e-full/pull-list-ci.spec.ts`

This task reuses the `/__e2e/pr-ci-state/mixed` and `/__e2e/pr-ci-state/malformed` fixture endpoints added in Task 10.

- [ ] **Step 1: Write the e2e spec**

```typescript
// frontend/tests/e2e-full/pull-list-ci.spec.ts
import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

test.describe("pull list CI cluster", () => {
  test("renders compact tokens from the live list payload (mixed state)", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seed = await page.request.post(
        `${server.info.base_url}/__e2e/pr-ci-state/mixed`,
      );
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls`);

      const row = page.locator(".pull-item", { hasText: "#1" });
      await expect(row.locator("[data-testid='ci-token-failed']")).toHaveText(/1/);
      await expect(row.locator("[data-testid='ci-token-pending']")).toHaveText(/1/);
      await expect(row.locator("[data-testid='ci-token-passed']")).toHaveText(/2/);
      await expect(row.locator("[data-testid='ci-token-skipped']")).toHaveText(/1/);
    } finally {
      await server.stop();
    }
  });

  test("renders the unavailable token when CIChecksJSON is malformed", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seed = await page.request.post(
        `${server.info.base_url}/__e2e/pr-ci-state/malformed`,
      );
      expect(seed.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls`);

      const row = page.locator(".pull-item", { hasText: "#1" });
      await expect(
        row.locator("[data-testid='ci-token-unavailable']"),
      ).toBeVisible();
    } finally {
      await server.stop();
    }
  });
});
```

- [ ] **Step 2: Run the spec**

Run: `cd frontend && nix run nixpkgs#bun -- run test:e2e -- --grep "pull list CI cluster"`
Expected: both tests pass.

- [ ] **Step 3: Commit**

```bash
git add frontend/tests/e2e-full/pull-list-ci.spec.ts
git commit -m "test(ui): add e2e for sidebar CI cluster on real list payload"
```

---

## Stage 6: Final cleanup

### Task 15: Visual verification across desktop + mobile

**Files:** (no committed changes — verification only)

This task is a visual check, not a test. Implementers capture screenshots of the redesigned surfaces on desktop and mobile breakpoints, inspect them for clipping/wrapping/positioning regressions, and keep the images locally for review-cycle audit. Use the explicit Playwright commands below as the primary path; the `capture-playwright` skill is a convenience wrapper around the same Playwright API and may be used interchangeably if the implementer's environment exposes it.

- [ ] **Step 1: Spin up an isolated e2e server**

The `__e2e/pr-ci-state/*` fixture endpoints live in `cmd/e2e-server/main.go`, not in the main `cmd/middleman` binary that `make dev` runs — so `make dev` alone cannot exercise these fixture states. For visual verification, spin up an isolated e2e server (the same one the Playwright specs use) and point the browser at it. The exact mechanism is left to the implementer.

- [ ] **Step 2: Capture each payload case from the acceptance matrix**

For each row in the acceptance matrix above, POST to the relevant `/__e2e/pr-ci-state/<state>` endpoint to seed the fixture state, then navigate to the detail or sidebar page and capture the chip + dropdown + sidebar surfaces.

Concrete capture path — a one-off Playwright script that consumes the Playwright API directly. Save as `frontend/scripts/visual-verify-ci.mjs` (gitignored or deleted afterward):

```javascript
import { chromium } from "playwright";

const BASE = process.env.E2E_BASE_URL ?? "http://localhost:8091";
const OUT  = process.env.CAPTURE_DIR
  ?? `${process.env.TMPDIR ?? "/tmp"}/ci-status-redesign-captures-${Date.now()}`;
const fs = await import("node:fs/promises");
await fs.mkdir(OUT, { recursive: true });

// Capture variants:
//   "detail"          — chip closed
//   "detail-open"     — chip clicked, dropdown panel visible
//   "detail-expanded" — dropdown's "Show N more passed" toggle clicked
//   "sidebar"         — sidebar list row
async function capture(name, viewport, route, variant = "detail",
                       reducedMotion = "no-preference") {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport, reducedMotion });
  const page = await ctx.newPage();
  await page.goto(`${BASE}${route}`);
  if (variant === "detail-open" || variant === "detail-expanded") {
    await page.locator("[data-testid='ci-chip']").click();
    await page.locator(".ci-checks").waitFor({ state: "visible" });
  }
  if (variant === "detail-expanded") {
    const showMore = page.getByRole("button", { name: /Show \d+ more passed/i });
    if (await showMore.count() > 0) {
      await showMore.first().click();
    }
  }
  await page.screenshot({ path: `${OUT}/${name}.png`, fullPage: false });
  await browser.close();
}

const fixtures = ["mixed", "malformed", "status-only", "dropdown-mixed", "success", "pending"];
for (const f of fixtures) {
  await fetch(`${BASE}/__e2e/pr-ci-state/${f}`, { method: "POST" });
  // Closed chip
  await capture(`${f}-desktop-detail`,        { width: 1440, height: 900 },
                "/pulls/github/acme/widgets/1", "detail");
  await capture(`${f}-mobile-detail`,         { width:  390, height: 844 },
                "/pulls/github/acme/widgets/1", "detail");
  // Open dropdown (skip for state where dropdown is hidden: malformed, status-only)
  if (f !== "malformed" && f !== "status-only") {
    await capture(`${f}-desktop-detail-open`, { width: 1440, height: 900 },
                  "/pulls/github/acme/widgets/1", "detail-open");
    await capture(`${f}-mobile-detail-open`,  { width:  390, height: 844 },
                  "/pulls/github/acme/widgets/1", "detail-open");
  }
  // Sidebar
  await capture(`${f}-desktop-sidebar`,       { width: 1440, height: 900 }, "/pulls",   "sidebar");
  await capture(`${f}-mobile-sidebar`,        { width:  390, height: 844 }, "/m/pulls", "sidebar");
}

// dropdown-mixed has >8 passed checks → exercises the "Show N more" expansion.
await fetch(`${BASE}/__e2e/pr-ci-state/dropdown-mixed`, { method: "POST" });
await capture("dropdown-mixed-desktop-detail-expanded",
              { width: 1440, height: 900 }, "/pulls/github/acme/widgets/1",
              "detail-expanded");

// Reduced-motion variants for pending (the only fixture with active checks).
await fetch(`${BASE}/__e2e/pr-ci-state/pending`, { method: "POST" });
await capture("pending-desktop-detail-reduced-motion-on",
              { width: 1440, height: 900 }, "/pulls/github/acme/widgets/1",
              "detail-open", "reduce");

console.log(`Captures saved to ${OUT}`);
```

Run from the repo root: `nix run nixpkgs#bun -- run frontend/scripts/visual-verify-ci.mjs` after the isolated e2e server from Step 1 is up. Surfaces captured per fixture:

- Desktop detail view (`/pulls/github/acme/widgets/1`) — chip + dropdown expanded.
- Mobile detail view (390×844) — same.
- Desktop sidebar (`/pulls`) — list row's CI cluster.
- Mobile sidebar (`/m/pulls`) — same.

If the implementer's environment exposes the `capture-playwright` skill, that's an equivalent wrapper around the same Playwright API and may be substituted for the script above. Either path produces the same image set in the same temp directory layout.

For the malformed case, additionally capture the focus-visible popover by tabbing focus to the unavailable chip; verify the popover positions cleanly (not clipped by the viewport edge — popover anchors below the chip with `top: calc(100% + 4px)`; if the chip sits near the bottom of the viewport, the popover may extend off-screen — note that as a known limitation for v1).

For reduced-motion verification, capture the pending chip in both motion states. Set `prefers-reduced-motion: reduce` **before** the page loads, since the helper reads `matchMedia.matches` at render time and doesn't subscribe to live changes (see the non-goals entry on reduced-motion swap):

- **Reduced motion ON** — desktop detail with pending checks: launch Chromium with `--force-prefers-reduced-motion`, or set the user agent's media-emulation via Playwright (`page.emulateMedia({ reducedMotion: "reduce" })` before `page.goto`). Confirm the chip's pending token renders the static `CircleIcon`, no `.spin` wrapper, no rotation animation. Same for the dropdown's pending rows.
- **Reduced motion OFF** — same surfaces, with `page.emulateMedia({ reducedMotion: "no-preference" })` or no flag. Confirm the pending token renders the animated `LoaderCircleIcon` inside `.spin`. Sidebar tokens stay static regardless of the OS preference because `CITokenCluster` is rendered with `pendingStyle="static"` from `PullItem.svelte` — confirm the sidebar pending token shows the static glyph in both motion states.

- [ ] **Step 3: Manually inspect and audit**

Open each screenshot. Verify:
- No clipped tokens or wrapped text breaking the chip layout.
- Mobile chip cluster fits in the row without overflowing.
- Mobile sidebar cluster's tightened gap (3px) keeps tokens on one line.
- Popover content is readable, wraps cleanly within 320px max-width.
- Long check names truncate with ellipsis (don't overflow the row).

The screenshots live in the script's output directory (default `${TMPDIR:-/tmp}/ci-status-redesign-captures-<ts>` or whatever `CAPTURE_DIR` was set to). Keep them on disk through the implementation review and PR cycle — do not delete after inspection. The temp directory is outside the working tree, so they are not committed; PR notes should record:

- The temp directory path that holds the captures.
- A short checklist of which acceptance-matrix rows × surfaces were captured (e.g. "mixed-small × desktop chip ✓, mixed-large × mobile sidebar ✓, malformed × focus-visible popover ✓").

This lets the implementation reviewer audit that visual verification actually ran without forcing image upload. Do not attach the images to the PR or commit them to git without explicit user approval (per the project's "never publish images without explicit approval" rule).

- [ ] **Step 4: No commit** (verification only)

If issues surfaced, file them as follow-up plan tasks before proceeding to Task 16.

---

### Task 16: Lint + typecheck + final unit/component/e2e cleanup

**Files:**
- Modify (if needed): existing components, tests

- [ ] **Step 1: Run packages/ui typecheck**

Run: `cd packages/ui && nix run nixpkgs#bun -- run typecheck`
Expected: pass.

- [ ] **Step 2: Run frontend typecheck**

Run: `cd frontend && nix run nixpkgs#bun -- run typecheck`
Expected: pass. (Covers .svelte via svelte-check and .ts via tsc.)

- [ ] **Step 3: Run packages/ui lint**

Run: `cd packages/ui && nix run nixpkgs#bun -- run lint`
Expected: pass.

- [ ] **Step 4: Run frontend lint**

Run: `cd frontend && nix run nixpkgs#bun -- run lint`
Expected: pass. (Covers the e2e spec changes.)

- [ ] **Step 5: Run Go lint and vet for the e2e fixture endpoint change**

Run: `make lint && make vet`
Expected: pass. (Covers `cmd/e2e-server/main.go`.)

- [ ] **Step 6: Run the full unit/component test suite**

Run: `cd frontend && nix run nixpkgs#bun -- run test`
Expected: pass. This covers both `frontend/src` tests and `packages/ui/src` tests (the vitest config in `frontend/vite.config.ts` includes both). Order-dependent failures (e.g., warning-helper Set state leaking between tests) surface here even if individual tests passed during their TDD cycle.

- [ ] **Step 7: Run the full Playwright e2e suite**

Run: `cd frontend && nix run nixpkgs#bun -- run test:e2e`
Expected: pass.

- [ ] **Step 8a: Grep for production imports of test-only helpers**

The plan ships several test-only exports from runtime modules (`__resetParseCIChecksCache`, `__parseCIChecksCacheStats`, `__resetCIWarnings`, `__resetPrefersReducedMotion`). The `__` prefix and `@internal` JSDoc tag are the social-enforcement convention; nothing prevents a production file from importing them. Catch any drift at this step:

```bash
# Find every __-prefixed import inside packages/ui/src and frontend/src.
# Anything inside *.test.{ts,svelte.ts} is fine; anything outside is a bug.
rg "import \{[^}]*\b__\w+" \
   packages/ui/src/ frontend/src/ \
   -g '!**/*.test.ts' -g '!**/*.test.svelte.ts' \
   -g '!**/*.spec.ts' -g '!**/node_modules/**'
```

Expected: no matches. If a production file imports a `__` helper, refactor it to use the public API instead (or, if it genuinely needs the helper, the helper is no longer test-only and should be renamed without the prefix).

- [ ] **Step 8b: Grep for stale legacy-CI assertions across the whole tree**

Tests outside `CIStatus.test.ts` and `PullItem.test.ts` may still assert on the legacy `CI: <status> (<total>)` chip text or the legacy ASCII glyphs. The component tests touched in this plan can pass while other specs (snapshot tests, integration specs, Playwright fixtures used by unrelated tests) still expect the old text and will fail in CI.

Run these greps and migrate every match before committing:

```bash
# 1. Legacy chip-text assertions (e.g. /CI: success (4)/) — scoped to CI files.
rg --multiline 'CI:\s*(success|failure|pending|error)\s*\(\s*\d+\s*\)' \
   frontend/ packages/ -g '!**/node_modules/**'

# 2. Legacy ASCII row glyphs — scoped to the CI-rendering files first, since
# these glyphs also appear in unrelated UI (✓ for completed todos, etc.) and
# a broad grep would surface false positives. Start with the files this plan
# already touches; only widen if migration leaves stale snapshots.
rg '"✓"|"✗"|"–"|"◦"' \
   packages/ui/src/components/detail/CIStatus.svelte \
   packages/ui/src/components/detail/CIStatus.test.ts \
   packages/ui/src/components/sidebar/PullItem.svelte \
   packages/ui/src/components/sidebar/PullItem.test.ts \
   frontend/tests/e2e-full/ci-dropdown.spec.ts \
   frontend/tests/e2e-full/pull-list-ci.spec.ts 2>/dev/null

# 3. If step 6 (full test suite run) surfaces snapshot diffs that match the
# legacy chip text, find their source files. Broad-scope only as a follow-up,
# not a primary search.
rg -l 'CI:\s*(success|failure|pending|error)' frontend/ packages/ \
   -g '!**/node_modules/**' | rg '\.snap|__snapshots__'
```

Each match either updates to the new aria-label format (`/CI: \d+ (passed|pending|failed|skipped) checks?/i`), new section/header text, or new token data-testid, or — for snapshots — gets regenerated with the new output. Re-run `bun run test` after migration to confirm no stale snapshots remain. The scoping in step 2 is intentional: an unscoped glyph grep across the whole tree surfaces false positives in unrelated UI (any todo/check list using ✓, any divider using –), wasting time. Step 6's test run is the authoritative ratchet — if every test passes including snapshot diffs, the migration is complete.

- [ ] **Step 9: Remove any leftover dead code**

Scan `CIStatus.svelte` for the legacy `checkIcon`, `checkColor`, `chipColor`, `isPendingCheck`, `parseCIChecks` (now in `ci-buckets.ts`), `failedChecks`, `nonFailedChecks` derivations. Delete unused functions/styles.

Scan `PullItem.svelte` for the legacy `.ci-icon`, `.ci-icon--success`, `.ci-icon--failure`, `.ci-icon--pending` CSS rules. Delete.

- [ ] **Step 10: Final commit (conditional)**

If `git status --porcelain` is empty, skip this step — earlier tasks already removed all dead code. Otherwise stage and commit every file that the cleanup touched:

```bash
# Inspect what cleanup actually changed, then stage all of it.
git status --porcelain
git add -u  # restage all tracked files modified by the cleanup
# If new untracked files appeared (rare for a cleanup task), name them explicitly:
# git add <path>
git commit -m "chore(ui): remove dead legacy CI rendering helpers"
```

The earlier Step 9 (dead-code removal) touches `CIStatus.svelte` and `PullItem.svelte` at minimum, but stale CSS in shared components or test helpers may also need updating. Don't hard-code the file list — let `git add -u` cover whatever the cleanup actually changed, and confirm `git status` is clean before continuing.

---

## Verification

The redesign is complete when all of the following hold:

- All unit, component, and e2e tests added in this plan pass.
- The chip on a mixed-state PR shows tokens with correct counts (no misleading total).
- The sidebar list payload feeds the compact cluster end-to-end.
- Malformed `CIChecksJSON` produces "CI unavailable" visibly with a `console.warn`.
- `prefers-reduced-motion: reduce` swaps the Pending icon to static `circle` in chip/dropdown.
- `make lint` and the typecheck pass.
- The full Playwright e2e suite passes.
