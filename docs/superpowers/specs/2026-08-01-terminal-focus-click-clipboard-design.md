# Terminal Focus-Click Clipboard Authorization Design

## Problem

In Firefox-based browsers on macOS, an ordinary click used to focus a tmux
terminal can replace clipboard text copied from a pull request comment with a
single terminal character. The user does not need to drag the pointer.

The terminal currently treats every trusted primary pointer press as permission
for a terminal clipboard write. On release it leaves that permission active for
a short grace period. A focus click can therefore authorize a one-cell tmux OSC
52 copy. When the browser clipboard API rejects the write in Firefox, the local
clipboard fallback still replaces the operating-system clipboard.

The existing delayed-write regression test covers an OSC 52 write that was
already pending before the comment copy. It does not exercise the new clipboard
authorization created by clicking the terminal afterward.

## Goals

- Preserve a pull request comment in the clipboard after an ordinary terminal
  focus click, including small involuntary pointer movement.
- Preserve deliberate tmux drag-to-copy behavior.
- Preserve keyboard-initiated terminal copy behavior.
- Apply the same authorization decision to browser and local clipboard writes.
- Cover the deployed Firefox behavior through the real tmux and xterm path.

## Non-goals

- Do not infer intent from clipboard text length; copying one character can be
  legitimate.
- Do not suppress terminal focus, mouse input, or typing after the click.
- Do not change tmux mouse bindings or redesign the Firefox clipboard fallback.
- Do not make global document focus the source of clipboard authorization.

## Design

Pointer clipboard authorization becomes a two-phase transaction:

1. **Prepared:** A trusted primary pointer press prepares browser clipboard work
   while user activation is available, but cannot yet resolve a terminal
   clipboard write.
2. **Selection confirmed:** Pointer movement of at least four CSS pixels from
   the press origin confirms selection intent and authorizes the prepared write.

The four-pixel threshold tolerates the small movement commonly produced while
clicking without materially delaying an intentional terminal drag. It is based
on pointer movement, not selected text or terminal-cell geometry, so the rule is
stable across zoom levels, fonts, and tmux output.

`terminalClipboardWriter` will expose an operation that confirms selection
intent for the current pointer transaction. Beginning a pointer gesture creates
the deferred browser clipboard operation but leaves it unauthorized. Confirming
intent is idempotent and arms that operation. Ending or canceling an unconfirmed
gesture immediately rejects and clears the deferred operation, with no release
grace period.

`XtermTerminalPane` will record the primary pointer's origin and use its existing
document-level pointer-move handling to compare the current position with that
origin. Once the distance reaches the threshold, it confirms selection intent
exactly once. Pointer release then follows the existing authorized release-grace
behavior only for a confirmed drag.

Keyboard authorization remains independent and unchanged.

## Clipboard Flow

For a focus click after copying a pull request comment:

1. The comment copy revokes any older terminal clipboard authorization.
2. Terminal pointer-down prepares a pointer transaction.
3. Pointer movement stays below four CSS pixels, so selection intent is not
   confirmed.
4. Any tmux OSC 52 write produced by that click remains unauthorized.
5. Pointer-up clears the unconfirmed transaction immediately.
6. Neither the browser clipboard writer nor the Firefox local fallback runs, so
   the comment remains in the operating-system clipboard.

For a deliberate drag, movement reaches the threshold before release. The
prepared operation becomes authorized, and the existing OSC 52 browser-write or
local-fallback flow proceeds normally.

## Lifecycle and Error Handling

An OSC 52 write rejected because pointer selection was never confirmed is an
expected authorization rejection. It must not show a terminal failure state or
invoke the local clipboard fallback.

The current cancellation paths for pointer cancelation, lost capture, window
blur, document visibility changes, terminal disablement, and component disposal
remain authoritative. Each clears both prepared and confirmed pointer state so a
later OSC 52 write cannot reuse a stale gesture.

## Regression Coverage

Writer unit tests will prove that:

- beginning and ending a pointer gesture without confirmed intent rejects a
  terminal write and invokes neither clipboard destination;
- sub-threshold pointer movement does not authorize a write;
- confirming pointer selection intent permits the existing write path; and
- keyboard authorization remains unaffected.

A Firefox full-stack Playwright regression will:

1. Copy a seeded pull request comment through the real comment copy action.
2. Press, move one CSS pixel, and release over a real tmux/xterm terminal.
3. Assert that the tmux OSC 52 event was observed, proving the failure-producing
   terminal path ran.
4. Assert that no terminal clipboard fallback replaced the copied comment.

The existing deliberate multi-cell tmux drag-copy test remains the positive
control and must continue to pass. The regression should use the established
Firefox fallback instrumentation when direct clipboard reads are unavailable,
while keeping tmux and xterm behavior real.

## Risks

The threshold could reject an unusually short intentional drag. Four CSS pixels
is small enough to be crossed before a normal multi-cell selection while still
filtering click jitter. Existing drag-copy coverage provides a guard against
making the threshold too restrictive.

Prepared browser clipboard promises must always settle when a click never
becomes a drag. Immediate cleanup on release and every cancellation path avoids
leaking a pending transaction into a later gesture.

## Success Criteria

- A pull request comment remains pasteable after clicking the terminal to focus
  it in a Firefox-based browser on macOS.
- The focus click still focuses the terminal and permits immediate typing.
- Deliberate tmux drag copies and keyboard copies still reach the clipboard.
- A zero-motion or sub-threshold pointer gesture cannot invoke the local
  clipboard fallback.
