# Workspace Detail Width Restoration Design

## Problem

The Workspaces right detail pane uses one `sidebarWidth` value as both the
user's preferred width and the width currently permitted by the layout. When
the terminal/detail row narrows, the layout clamps that value to preserve 300
pixels for the terminal. The persistence effect then records the constrained
value as the preference. Widening the row does not restore the old preference,
so the pane can remain only a few pixels wide even though it is still open.

## Design

Keep the persisted preferred width separate from the rendered width. Pointer
and keyboard resizing update the preferred width. Container reconciliation
derives the rendered width by constraining that preference to the current
maximum, without overwriting or persisting the temporary result. When space
returns, the rendered width therefore returns to the preference automatically.

The existing 280-pixel minimum remains the floor whenever the container can
support it. If the container cannot support both that floor and the 300-pixel
terminal minimum, the rendered pane may temporarily become narrower. This
preserves the existing terminal usability contract without converting a
transient constraint into durable state.

## Verification

Add a real-browser regression that opens the Workspaces detail pane with a
saved preferred width, narrows the available row until the rendered pane is
below its minimum, and then widens it again. Assert that the rendered pane
returns to the saved preference and that local storage never changes during
the temporary constraint.

Retain the existing pointer-resize persistence and left-sidebar reclamping
coverage. No API, database, provider, or terminal runtime behavior changes.
