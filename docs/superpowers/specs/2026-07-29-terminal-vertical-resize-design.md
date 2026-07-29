# Terminal Vertical Resize Design

## Problem

The bottom terminal dock can grow vertically without growing its pooled xterm.
The dock body receives the new height, but the terminal remains at its previous
row count. Width changes still propagate because normal block layout stretches
the terminal slot horizontally.

Live inspection showed the dock leaf body at 529 pixels while the pooled xterm
remained 90 pixels tall. No resize frame was sent for that terminal, and a
command inside the PTY still reported six rows. The immediate cause is that
`.terminal-leaf-body` is a block container, so the slot's existing `flex: 1`
does not participate in vertical layout.

Escape handling is not part of this change. With the terminal focused, Chrome
sent the exact `0x1b` byte on the terminal websocket, matching Escape injected
directly into the same tmux pane. The branch's terminal keyboard ownership path
therefore needs no additional workaround for this defect.

## Design

Make `.terminal-leaf-body` a flex container. `SessionTerminalSlot` already
declares flexible growth with zero minimum dimensions, and the pooled wrapper
and terminal container already use full height. Activating that existing flex
contract at the leaf boundary lets the reparented terminal fill both axes while
preserving the split-preview overlay and pane-move lifecycle.

Do not add height declarations through each pooled wrapper and do not
absolute-position the terminal slot. Both alternatives duplicate ownership of
the leaf's geometry and create more coupling between the split tree and the
pooling implementation.

## Verification

Add a real-browser regression around a docked shell. The test will:

1. Open a workspace with a real tmux-backed terminal.
2. Record the terminal's rendered height and PTY row count.
3. Enlarge the bottom terminal dock through its resize handle.
4. Assert that the pooled xterm fills the enlarged leaf.
5. Run `stty size` inside the terminal and assert that the PTY row count grew.

The browser/PTY boundary is intentional: a component-only test could verify a
CSS value while missing the ResizeObserver, websocket, resize-authority, or tmux
application path that the user actually depends on.

## Scope

This change is UI-only; the likely regression surface is terminal geometry
after dock resizing and pooled-terminal reparenting. It does not change session
creation, resize arbitration, keyboard dispatch, provider behavior, or server
APIs.
