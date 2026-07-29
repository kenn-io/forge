# Terminal-safe command palette chord

## Problem

A focused terminal must receive ordinary `Ctrl/Meta+K`, because terminal applications use that chord. The current terminal ownership rule correctly keeps the chord inside xterm, but the pane-move end-to-end test still expects it to open middleman's command palette.

## Design

Keep `Ctrl/Meta+K` terminal-owned. Add `Ctrl/Meta+Shift+K` as an explicit cross-platform command palette chord that middleman may reserve even when the keyboard event originates inside a focused terminal.

The exception is limited to this exact chord. Escape, function keys, unshifted `Ctrl/Meta+K`, and every other terminal keystroke continue to bypass the global keyboard registry. When the palette is open, the same shifted chord closes it, matching the existing palette-toggle bindings.

## Verification

- Unit tests prove unshifted `Ctrl/Meta+K` stays terminal-owned while shifted `Ctrl/Meta+Shift+K` invokes the palette action.
- Palette tests prove the shifted chord is registered for both opening and closing.
- The real tmux-backed pane-move test uses the shifted chord and proves that palette teardown restores focus to the same live terminal after it is reparented.
- The affected Chromium and Firefox Playwright lanes run before the fix is pushed.
