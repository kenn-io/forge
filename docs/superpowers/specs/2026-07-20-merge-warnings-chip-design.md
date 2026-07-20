# Merge Warnings Chip

Replace the merge-warnings banner in the PR detail header with a compact,
clickable chip that follows the existing expandable-chip pattern.

## Problem

`PullDetail.svelte` renders merge warnings (conflicts, branch protection,
behind base, required checks, server-provided detail warnings) as a
full-width banner below the kanban select. The banner appears and disappears
as `MergeableState` and detail data arrive, pushing the timeline and actions
below it up and down. Visually it is the only non-chip element in a header
otherwise composed of kit-ui chips.

## Design

### Remove

- The `merge-warnings` / `merge-warning-line` block and its styles in
  `PullDetail.svelte`.

### Add: `MergeWarningsChip.svelte`

New component in `packages/ui/src/components/detail/`, built on kit-ui
`Chip`, following the CIStatus/StackStatus interaction pattern: interactive
chip, trailing chevron, expanded panel, `expanded`/`ontoggle` props.

Chip states:

- Conflicts (`MergeableState === "dirty"`, open PR): `tone="warning"`,
  git-merge icon, label "Conflicts".
- No conflicts but other warnings present: `tone="neutral"`, label
  "N merge warnings" ("1 merge warning" singular).
- No warnings: component renders nothing.

Placement: in the chips row adjacent to the CI chip (both are
merge-readiness signals).

### Panel

- Wired into the existing single-open `expandedPanel` state in `PullDetail`
  as `"merge"`.
- Lists each warning as its own line, reusing the current warning texts.
  The conflicts line renders first, in amber.
- Includes a provider-aware "View on {provider label}" link to the PR URL.
  The current banner hardcodes "View on GitHub", which violates the
  provider-neutral rule.

### Provider label source

No display label exists in TypeScript today: `RepoRefResponse` exposes only
the `provider` key string, Go labels live in `internal/platform/metadata.go`,
and the frontend's `repoImportProviders.ts` table is not importable from
`@middleman/ui`. Add a shared label helper in `packages/ui` (e.g.
`providerDisplayLabel(provider: string): string` beside
`provider-routes.ts`) mapping known provider keys to display labels, falling
back to the raw key for unknown providers. `repoImportProviders.ts` reads its
labels from this helper so the two tables cannot drift.

### Data flow

`PullDetail` computes a typed list of warning entries (it already owns
`hasWarningLines` and `requiredStatusChecksHaveNotPassed`) and passes props
to the chip: `warnings`, `pullURL`, and `providerLabel`. The component is
presentation-only. `providerLabel` derives from `detail.repo?.provider ??
provider` — the rendered detail can temporarily belong to the previous route
during navigation, so the route's `provider` prop alone is wrong (same
fallback pattern PullDetail already uses for `supportsLocked`).

Warning entries, in order:

1. Conflicts — open PR with `MergeableState === "dirty"`.
2. Branch protection — open PR with `MergeableState === "blocked"`.
3. Behind base — open PR with `MergeableState === "behind"`.
4. Required checks not passed — open PR where
   `requiredStatusChecksHaveNotPassed(pr.CIChecksJSON)` is true.
5. Server-provided `detail.warnings`, one entry each.

Stale-PR behavior is unchanged: when `stalePR` is true, only
`detail.warnings` entries are shown.

### Accessibility

`aria-expanded` on the chip (kit-ui `Chip` handles this via `expanded`),
`ariaLabel` summarizing the warning count and severity.

## Error handling

No new failure modes: the chip renders purely from already-loaded PR/detail
state. Absent or empty warning data means the chip does not render.

## Testing

Vitest jsdom tests, split by responsibility:

`MergeWarningsChip.test.ts` (component, presentation-only):

- Chip hidden when there are no warnings.
- Conflicts state: warning tone, "Conflicts" label.
- Non-conflict warnings: neutral tone, correct count and pluralization.
- Panel toggle via click and `ontoggle` callback.
- Panel lists entries in order; conflicts line first.
- Provider link href and label (non-GitHub provider label case included).

`PullDetail.test.ts` (integration behavior PullDetail retains):

- Warning-entry derivation from PR state, mergeable state, and checks.
- Stale-PR filtering: only `detail.warnings` entries reach the chip.
- Single-open mutual exclusion across the merge, CI, and stack panels
  (extending the existing CI/stack shared-slot test).

### Existing tests that must change

Two tests currently assert warning text visible without expanding anything
and will fail once the banner becomes a collapsed chip:

- `frontend/tests/e2e/detail-stale-actions.spec.ts` (warning-line cases):
  rewrite the visibility assertions against the chip, or drop cases that
  duplicate the jsdom coverage above per the repo's no-duplicate-e2e rule.
- `frontend/src/App.stack-status.browser.svelte.ts`: its readiness signal
  awaits the conflicts banner text; switch it to await the conflicts chip.

No new Playwright coverage beyond migrating those assertions: no layout,
geometry, or navigation behavior beyond what jsdom covers.

## Known tradeoff

The chip still appears when data arrives, nudging neighbors within its
wrapping row by a few pixels. True zero-shift would require permanently
reserved space. The vertical banner reflow — the disruptive shift — is
eliminated.
