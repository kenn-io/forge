# ADR 0005: Active Pane Owns Input

## Status

Accepted

## Context

Pull request conversation and files panes can be visible simultaneously. The
files pane currently installs a window-level Page Up/Page Down handler, so it
can scroll after the user has moved to the conversation pane. Focus events alone
do not solve ownership because blank pane surfaces are not focusable.

Without a visible active state, users also cannot predict which pane will
receive a global keyboard action.

## Decision

Focus, pointer, or wheel interaction makes a pane active. Only the active pane
may consume window-level keyboard paging, while wheel scrolling remains local
to its event target. A locally targeted Page Up/Page Down event may be handled
immediately while active state settles.

The active pane is shown with a one-pixel inset border mixed from the existing
blue accent and normal pane border. The indicator introduces no layout shift,
glow, or motion, and does not replace focus-visible styling on controls.

Dedicated files routes retain global diff paging. Workspace diff panels retain
their focus-local paging.

## Consequences

- Page Up/Page Down follows the pane the user last interacted with.
- Wheel input never scrolls a different pane.
- Pane ownership is visible without competing with review and status colors.
- Pointer and wheel input participate in the same ownership model as keyboard
  focus.
