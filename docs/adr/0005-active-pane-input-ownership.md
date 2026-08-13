# ADR 0005: Active Pane Follows DOM Focus

## Status

Accepted

## Context

Pull request, Issue, Activity, and Workspaces workflow panes can be visible
simultaneously. A pull request files pane can install a window-level Page
Up/Page Down handler and scroll after DOM focus has moved elsewhere.

Without a visible active state, users also cannot predict which pane will
receive a global keyboard action.

## Decision

The active pane is the pane containing actual DOM focus. Only that pane may
consume pane-scoped window-level keyboard paging. Pointer interaction changes
the active pane only when the browser moves focus as a normal consequence of
that interaction. Wheel scrolling remains local to its event target and never
changes focus or the active pane.

Programmatic `focus()` is still focus. In particular, deliberate terminal focus
restoration when switching items with a workspace updates the active pane
because it moves DOM focus. Persisted last-focused state remains useful for
restoration and command targeting, but never stands in for current focus.

The active pane is shown with a one-pixel inset border mixed from the existing
blue accent and normal pane border. The indicator introduces no layout shift,
glow, or motion, and does not replace focus-visible styling on controls.

Dedicated files routes retain global diff paging. Workspace diff panels retain
their focus-local paging.

The contract applies to every shared detail-pane tree and to the standalone
Workspaces workflow tree.

## Consequences

- Page Up/Page Down follows the pane containing DOM focus.
- Wheel input never scrolls a different pane.
- Pane focus is visible without competing with review and status colors.
- Pointer and wheel input do not create a second ownership model beside browser
  focus.
