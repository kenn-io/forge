# Kata Link Filtering Design

**Date:** 2026-07-22
**Status:** Approved for implementation

## Goal

Make linked Kata tasks reflect the active task-status scope, expose relationship filters, and communicate peer task state only when the visible set mixes open and closed tasks.

## Scope

- Filter linked peers by task status: **Open**, **Closed**, or both.
- Default the link status selection from the top-level Kata status filter.
- Filter links by their displayed relationship: **Parent**, **Child**, **Blocks**, **Blocked by**, and **Related**.
- Keep link navigation and link creation behavior unchanged.
- Do not derive a separate blocked task state. `Blocks` and `Blocked by` remain explicit relationship types.
- Keep this change in the frontend. The existing Kata detail API already provides the peer task data needed to resolve status.

## Interaction

Add a compact **Filters** control beside the Links count. It opens a popover with two groups of checkboxes:

- **Task state:** Open and Closed.
- **Relationship:** Parent, Child, Blocks, Blocked by, and Related.

All relationship types are enabled by default. Task-state defaults follow the active top-level filter:

- top-level **Open** enables only Open links;
- top-level **Closed** enables only Closed links;
- top-level **All** enables both.

Changing either group updates the visible rows immediately. The local filter state remains active while navigating between selected tasks in the current Kata workspace. A later top-level status change resets the local task-state selection to that new scope. Relationship choices remain unchanged because they are independent of task status.

The section count shows the visible and total counts when filtering removes rows, for example `8 / 29`. With no active reduction, it shows the existing total count. If links exist but none match, show `No links match these filters.` The existing `No links.` message remains for tasks with no relationships.

## Link Rows

Each row keeps the existing relationship label, peer short ID, and title. Peer task hydration retains the full task summary instead of only the title so the row can be filtered by `status`.

Render an **Open** or **Closed** state chip only when both states are enabled and the visible list can contain a mixture. When the filter shows only Open or only Closed, omit the chip because the filter already communicates that state. Status remains available to assistive technology through the row's accessible name when the chip is rendered.

Relationship labels remain plain compact text. They already describe direction relative to the selected task, including the existing conversion from `parent` to `child` and from `blocks` to `blocked_by` when the selected task is on the opposite side of the edge.

## Loading and Failure Behavior

Use any matching peer already present in the current view immediately. Resolve remaining peers through the existing task-detail reads that currently hydrate off-screen link titles.

Until a peer resolves, retain its row as pending rather than guessing its status. Pending rows do not count as an Open or Closed match. The section should keep a quiet loading indication while unresolved peers remain so the user can distinguish incomplete hydration from an empty filter result.

If a peer read fails, keep the existing short ID navigation affordance and mark its state unavailable. Failed peers remain visible only when the task-state filter includes both states, since their status cannot be classified safely. A later selected-task refresh retries hydration through the existing component lifecycle.

## Accessibility

- The Filters trigger exposes its expanded state and has a clear accessible name.
- Filter options use real labeled checkboxes.
- Escape and outside interaction close the popover through the shared overlay behavior and restore focus to the trigger.
- Visible state is not communicated by color alone: mixed-state rows use text-bearing chips, and single-state views are named by the active checkbox selection.
- Link rows remain keyboard-focusable buttons with a complete accessible name.

## Testing

Add focused component tests that prove:

- Open, Closed, and All top-level scopes produce the matching default link-status selection.
- Relationship checkboxes independently hide and restore Parent, Child, Blocks, Blocked by, and Related rows.
- Mixed Open and Closed results render state chips, while single-state results do not.
- The count distinguishes visible links from the total when filters hide rows.
- A filtered-empty state differs from a task with no links.
- Off-view peer hydration supplies title and status without changing link navigation.
- Failed or pending peer hydration follows the documented visibility behavior.

This is a UI-owned behavior change. Component tests cover the filtering and presentation contract. Add or update one affected Kata browser workflow only if the popover interaction or shared overlay behavior cannot be proved reliably in jsdom; no backend or API-contract test is required.
