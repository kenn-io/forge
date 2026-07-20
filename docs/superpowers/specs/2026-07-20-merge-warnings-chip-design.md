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
  provider-neutral rule; the chip uses platform metadata for the label.

### Data flow

`PullDetail` computes a typed list of warning entries (it already owns
`hasWarningLines` and `requiredStatusChecksHaveNotPassed`) and passes it to
the chip as a prop. The component is presentation-only.

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

Vitest jsdom component tests:

- Chip hidden when there are no warnings.
- Conflicts state: warning tone, "Conflicts" label.
- Non-conflict warnings: neutral tone, correct count and pluralization.
- Panel toggle via click and `ontoggle` callback.
- Panel lists entries in order; conflicts line first.
- Provider link href and label (non-GitHub provider label case included).
- Stale-PR case shows only detail warnings.

No Playwright coverage: no layout, geometry, or navigation behavior beyond
what jsdom covers.

## Known tradeoff

The chip still appears when data arrives, nudging neighbors within its
wrapping row by a few pixels. True zero-shift would require permanently
reserved space. The vertical banner reflow — the disruptive shift — is
eliminated.
