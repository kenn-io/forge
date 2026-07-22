# Kata Task Column Visibility Design

**Date:** 2026-07-22
**Status:** Approved for implementation

## Goal

Let maintainers hide low-value metadata columns in the Kata task list and remember that choice in the current browser. The interaction should preserve the table's compact scan line, keyboard behavior, and responsive layout.

## Scope

- Keep **ID** and **Title** permanently visible.
- Let users independently show or hide **Updated**, **Priority**, **Due**, **Owner**, and **Tags**.
- Apply one browser-local preference across every Kata task view, project scope, and daemon.
- Keep row selection, tree expansion, and task detail behavior unchanged. Sorting changes only when the active optional sort column is hidden, as defined below.
- Do not add server settings, URL state, or per-view overrides.

## Interaction

Add an always-available **Columns** button to the task-list header beside the tree controls. It opens a compact popover containing one checkbox for each optional column and a **Show all** reset action.

Checkbox changes apply immediately while the popover remains open. Escape and outside interaction close the popover using the application's existing overlay and focus conventions. The control remains available when the current task set has no expandable rows, unlike the conditional tree controls.

ID and Title do not appear as toggleable options. Keeping them fixed prevents users from hiding task identity or leaving a list that cannot be scanned meaningfully.

If the user hides the currently active optional sort column (Updated, Priority, or Owner), sorting resets immediately to Title ascending and persists through the existing sort preference. This avoids ordering the list by an invisible criterion. Hiding Due or Tags does not affect sorting because those columns are not sort controls. Showing columns again does not change the current sort.

## State and Persistence

The task-list component owns a small allowlisted set of visible optional-column keys. Its default contains all five optional columns.

Persist the set in `localStorage` under a versioned Kata-specific key. On restore:

- accept only known column keys;
- ignore duplicates and unknown keys;
- fall back to all optional columns when the stored value is missing, malformed, or not the expected shape;
- treat storage read and write failures as non-fatal so the table remains usable.

The preference is intentionally global within one browser. Navigating between Kata views, project scopes, or daemons retains the same column layout. Reloading or remounting the application restores it.

Synchronization between simultaneously open browser tabs is out of scope. Each tab reads the preference when the task list mounts and writes its own later changes.

## Layout and Responsive Behavior

Hidden columns are removed from both the header and every task row. Their grid tracks are also removed so the Title column receives the freed width instead of leaving empty gaps.

The existing container-width rules remain an additional visibility constraint. A user-enabled column may still auto-hide when the task pane is narrow; a user-disabled column never appears at any width. Expanding the pane restores only columns that are enabled in the saved preference.

Group headings, empty states, nested task indentation, row action placement, and the horizontal scroll container continue to span the resulting grid without changing their behavior.

The header actions remain on one line. At compact container widths, the Columns, Expand all, and Collapse all labels become visually hidden while their icons, accessible names, and titles remain available. This prevents the always-visible Columns control from forcing the task heading or count out of the pane.

## Accessibility

- The Columns trigger exposes its expanded state and has a clear accessible name.
- Each optional column uses a real labeled checkbox.
- Keyboard users can open the popover, change options, close it with Escape, and regain focus on the trigger.
- Visual column choices do not remove task identity or disrupt row keyboard navigation.

## Testing

Add focused component tests that prove:

- ID and Title stay visible while an optional column can be hidden;
- hiding a column removes both its header and its row cells;
- the saved preference is restored after remounting;
- Show all restores every optional column and updates storage;
- malformed or wrong-shaped stored data falls back safely, while unknown column keys are ignored;
- storage failures do not break the interaction.
- hiding the active Updated, Priority, or Owner sort resets sorting to Title ascending and persists it.

Add a Vitest browser test for keyboard opening, `aria-expanded`, Escape dismissal, and trigger focus restoration. Extend the Kata full-stack Playwright coverage for the browser-only layout contract: the picker floats above the header, header and row cells stay aligned, enabled columns auto-hide and return across representative container widths, disabled columns remain absent after widening, and the Title track receives freed width. The Playwright test exercises the real seeded Kata backend because the existing Kata full-stack fixture already owns this visible workflow.
