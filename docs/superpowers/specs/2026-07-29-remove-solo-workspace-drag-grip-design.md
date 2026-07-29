# Remove the Solo Workspace Drag Grip

## Problem

When the Workspace pane is alone in a leaf, middleman suppresses its redundant
outer tab strip and floats the remaining pane actions over the Workspace header.
The floating cluster currently begins with a six-dot drag grip. It looks detached
from the Workspace controls and adds an unfamiliar affordance to an otherwise
compact action row.

## Design

Remove the drag grip from the floating action cluster. Do not replace it with an
invisible drag target or make the surrounding toolbar draggable, because either
choice would create an undiscoverable or accidental interaction around adjacent
buttons.

This removes pointer-driven movement of the whole Workspace pane only while it is
the sole tab in a strip-less leaf. Pane commands remain available, and the normal
tab drag source returns whenever Workspace shares a leaf with another pane and the
outer tab strip is visible.

The hide, Workspace-specific, delete, settings, and maximize controls keep their
current order and behavior.

## Verification

Update the focused component test for solo Workspace chrome to assert that:

- the redundant outer tab strip remains absent;
- no `Move Workspace` control is rendered;
- the remaining hide, leaf-extra, and maximize controls remain present; and
- multi-tab leaves still render their ordinary draggable tab strip.

Run the Svelte autofixer on the edited component, the focused component tests, and
the full frontend Vitest suite.
