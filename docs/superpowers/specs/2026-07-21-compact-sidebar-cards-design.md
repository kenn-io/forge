# Compact 2-Line Sidebar Cards for Issues / PRs

Date: 2026-07-21
Status: Approved

## Problem

Sidebar cards in the Issues / PRs views stack up to four rows (title, label
pills, repo chip, meta) with 10px vertical padding and 4px inter-row margins.
A card with a repo chip is roughly 80px tall, so a typical sidebar shows only
a handful of items. The goal is a fixed 2-line card (~50px) that fits
50–60% more items while staying comfortable to read.

## Scope

- `packages/ui/src/components/sidebar/PullItem.svelte`
- `packages/ui/src/components/sidebar/IssueItem.svelte`
- `packages/ui/src/components/shared/LabelRow.svelte` (new `dots` variant)

`FocusListView` reuses these item components and picks up the compact layout
automatically — including on mobile routes, which mount `FocusListView`
directly. Mobile therefore adopts the new *structure* (label dots, repo chip
in the meta row) while keeping its own `.mobile-main` *sizing* overrides:
larger type, taller hit targets, and the 2-visual-line clamped title. The
mobile `.repo-row` overrides are removed along with the row itself. Out of
scope: `KanbanCard`; `WorkspacePanelPRItem`; Kata/Docs/Messages sidebars.

## Layout

On desktop, every card is exactly two lines, regardless of labels or repo
visibility. On mobile the same two rows render with mobile sizing, and the
title may wrap to two visual lines under its existing clamp.

**Line 1 — title.** State dot (PRs only, unchanged) + title (desktop:
single line, `nowrap` + ellipsis; mobile: existing 2-line clamp), followed
by inline label dots (see below). Title `margin-bottom` shrinks from 4px to
2px on desktop.

**Line 2 — meta.** One flex row:

- Left (shrinkable): repo chip first (only when `showRepo`), then
  `#number · author`. The repo chip keeps the existing hash-colored `Chip`
  (size `xs`) but is capped at `max-width: 45%` of the meta row's left
  section so it truncates before the number/author does.
- Right (fixed): the existing indicator cluster unchanged — import button,
  workspace indicator, review indicator, worktree name/badge, CI tokens,
  conflict icon, star, status/state chip, relative time.

Truncation order on narrow sidebars: repo chip first, then author; the right
cluster never shrinks (worktree name keeps its existing 80px cap).

## Labels as dots

`LabelRow` gains a `dots` variant used by both items in place of the current
`compact` pill row:

- Renders one small color dot (~7px circle, label's color) per label, capped
  at 4 dots with no numeric overflow indicator.
- Normalizes the label color the same way kit-ui's `ColorLabel` does
  internally (that logic is private to the pinned external package, so
  `LabelRow` implements the equivalent): trim, accept 3- or 6-digit hex with
  or without a leading `#`, expand and lowercase to `#rrggbb`, and fall back
  to a neutral gray for invalid input. Tests cover bare (`d73a4a`),
  prefixed (`#d73a4a`), 3-digit, and invalid colors.
- Sits inline at the end of line 1, after the title text; shrinks never
  (title truncates instead).
- Tooltip (`title`) lists all label names; a `kit-sr-only` span provides the
  same list for screen readers.
- The existing default and `compact` variants are unchanged (other callers
  keep pill rendering).

## Spacing

Item padding changes from `var(--sidebar-row-padding, 10px 12px)` to a
literal `6px 10px` in both item components. The global
`--sidebar-row-padding` variable in `frontend/src/app.css` is not touched;
it still serves `WorkspaceListSidebar` and Kata's sidebar. The removed label
and repo rows take their 4px margins with them.

## Accessibility

- Label dots are `aria-hidden` with an adjacent `kit-sr-only` list of label
  names.
- Star and import controls keep their current markup, semantics, and
  dimensions; this change does not resize those nested click targets.

## Testing

- Update `PullItem.test.ts` / `IssueItem.test.ts`: repo chip renders inside
  the meta row, label dots render on the title line with correct tooltip and
  screen-reader text, indicator cluster unchanged.
- Extend `LabelRow.test.ts` for the `dots` variant: dot count cap, color
  normalization cases, tooltip content, sr-only text, empty-labels renders
  nothing.
- Layout behavior (fixed row height, truncation order, overflow containment)
  is browser-geometry territory per repo testing guidance: extend the
  existing `frontend/tests/e2e-full/pull-list.spec.ts` /
  `issue-list.spec.ts` checks with row-height and chip-in-meta geometry
  assertions, and update `mobile-routes.spec.ts`
  (`expectReadableFocusList` and the `.repo-chip` assertions) for the new
  structure under mobile sizing.
- Visual verification via a `capture-playwright` screenshot before the PR.
