# Kata Link Filtering Design

**Date:** 2026-07-22
**Status:** Approved for implementation

## Goal

Make linked Kata tasks reflect the active task-status scope, expose relationship filters, and communicate peer task state whenever both Open and Closed are enabled.

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

Changing either group updates the visible rows immediately. The local filter state remains active while navigating between selected tasks on the same Kata daemon. A later top-level status change resets the local task-state selection to that new scope while retaining relationship choices. Switching daemons starts a new link-filter scope: task states follow the target daemon's top-level filter and all relationship types return to visible.

If both task-state checkboxes are disabled, no resolved, pending, or failed rows are visible. If every relationship is disabled, the section shows the filtered-empty state immediately and does not show a resolving indicator for hidden relationships.

The section count shows the visible and total counts when filtering removes rows, for example `8 / 29`. With no active reduction, it shows the existing total count. If links exist but none match, show `No links match these filters.` The existing `No links.` message remains for tasks with no relationships.

## Link Rows

Each row keeps the existing relationship label, peer short ID, and title. Peer task hydration retains the full task summary instead of only the title so the row can be filtered by `status`.

Render an **Open** or **Closed** state chip whenever both states are enabled, even if the current result happens to contain only one state. The filter no longer makes any individual row's state apparent in that mode. When the filter shows only Open or only Closed, omit the chip because the filter already communicates that state. Status remains available to assistive technology through the row's accessible name when the chip is rendered.

Relationship labels remain plain compact text. They already describe direction relative to the selected task, including the existing conversion from `parent` to `child` and from `blocks` to `blocked_by` when the selected task is on the opposite side of the edge.

## Loading and Failure Behavior

Use any matching peer already present in the current view immediately. Resolve remaining peers through the existing task-detail reads that currently hydrate off-screen link titles.

Until a peer resolves, retain its row as pending rather than guessing its status. Pending rows appear only when both task states are enabled. With a single active state they remain hidden, but the section keeps a quiet loading indication when their relationship is enabled and hydration could change the visible result. Pending peers behind disabled relationships, or with both task states disabled, do not contribute to loading state.

If a peer read fails, keep the existing short ID navigation affordance visible under any non-empty task-state selection and mark its state unavailable. This prevents transient hydration failures from silently concealing relationships in the default Open view. When the selected detail is refreshed, even if its UID, revision, and link signature are unchanged, clear only failed peer entries and retry them. Successful peer summaries remain cached so a refresh does not restart every link request. Current-view peers are used directly and never receive redundant detail requests; remaining peers retain the existing bounded one-request-per-peer behavior within the selected-detail lifecycle.

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
- Both-state filters render state chips, while Open-only and Closed-only filters do not.
- An All filter containing only Open peers still renders Open state chips.
- The count distinguishes visible links from the total when filters hide rows.
- A filtered-empty state differs from a task with no links.
- Off-view peer hydration supplies title and status without changing link navigation.
- Failed or pending peer hydration follows the documented visibility behavior.
- Disabled relationships do not keep the section in a resolving state, and disabling both task states produces the filtered-empty state.
- A transient peer-read failure recovers after a same-task selected-detail refresh.
- Relationship selections survive task-to-task navigation on one daemon, a later top-level status change resets only the Open/Closed choices, and a daemon switch resets the complete local link-filter scope.

This is a UI-owned behavior change. Component and workspace tests cover filtering, hydration, and state lifetime. Add one focused Kata Playwright workflow for the browser-only popover placement and clipping contract; use the real seeded Kata backend rather than a handwritten frontend fixture. No API-contract test is required.
