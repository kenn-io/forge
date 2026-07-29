# Hidden terminal resize authority

## Problem

Inactive workflow tabs remain mounted under `visibility: hidden`, so their terminal regions retain measurable dimensions. Geometry alone therefore cannot distinguish a painted terminal from a hidden one: a hidden terminal can claim PTY resize authority and continue sending resize or refresh dimensions.

## Design

Resize authority requires both the pane's painted-state signal and valid measured geometry. A hidden terminal keeps its websocket and continues receiving output, but it advertises `resize_active=0`, sends `resize_active: false` when hidden, and emits no resize or refresh frames until painted again.

The painted-state signal is not focus. Every leaf in a visible terminal split receives `active=true`, including unfocused leaves, so all painted terminals continue fitting and sizing their own PTYs. Parked terminals and hidden workflow tabs receive `active=false`; valid geometry cannot override that state.

## Verification

- A hidden but measurable terminal neither claims authority nor emits resize or refresh frames.
- A visible terminal that becomes hidden revokes authority before later measurements can resize the PTY.
- Existing unmeasurable-region coverage continues protecting parked terminals.
- Split-tree coverage proves every painted leaf remains active regardless of focus.
- The real Chromium/tmux dock-resize scenario proves vertical resize events still reach the PTY.
