# Terminal Rendering and Zoom Design

## Problem

Codex and other TUIs can show corrupted glyphs in workspace terminals even
though the underlying tmux pane contains the correct bytes. The affected
configuration uses xterm.js with its WebGL addon. The installed
`@xterm/addon-webgl` 0.19.0 generates mipmaps for glyph-atlas textures, a path
that upstream removed after reports of live-context glyph corruption.

Terminal text size is also only editable through the full terminal settings
form. There is no quick terminal-specific zoom control, so browser zoom is the
obvious alternative even though it scales the entire middleman interface.

## Rendering Fix

Backport xterm.js pull request 5987 as a Bun dependency patch:

- remove glyph-atlas `generateMipmap`;
- set both texture filters to `LINEAR`;
- retain the WebGL renderer and its custom box-drawing glyphs;
- keep the patch explicitly tied to `@xterm/addon-webgl` 0.19.0 so it can be
  removed when middleman adopts an upstream release containing the fix.

The server, PTY, tmux snapshot, and ghostty-web paths do not change.

## Terminal Zoom

Add a compact zoom control to terminal chrome beside the existing Terminal
options button. It shows decrease, current pixel size, increase, and reset
actions. The allowed range remains the existing 8–32 px configuration range;
each step is one pixel and reset restores the 12 px default.

The same actions are available from `Cmd/Ctrl` + `+`, `-`, and `0` while focus
is inside a terminal. Outside a terminal these keys keep their browser
behavior.

Every zoom action:

1. updates the shared settings store immediately, causing all mounted xterm
   and ghostty-web panes to resize together;
2. persists the complete terminal settings object through the existing
   settings API;
3. rolls the store back and shows the existing shared flash error if the save
   fails.

Rapid changes are serialized so a slower response cannot overwrite the newest
font size. The persisted server setting remains the source of truth across
reloads and navigation.

## PTY Geometry

Changing font metrics alters the number of terminal cells that fit inside the
same pixel-sized container. A font change therefore must not rely on
`ResizeObserver`, which only reports container geometry changes.

After applying font settings, each xterm and ghostty-web pane fits its renderer
and sends the resulting columns and rows through the existing terminal
WebSocket resize message. Only an active pane may claim resize authority.
Hidden panes do not resize tmux; the existing activation refresh fits and
propagates their current geometry when they become active. If the WebSocket is
not open during a font change, that activation or connection refresh supplies
the latest size without reconnecting the terminal.

The server, PTY, and tmux resize path remains unchanged. The correction is at
the renderer-to-WebSocket boundary where the newly fitted geometry was
previously not forwarded.

## Verification

- Unit-test size clamping, reset, shared-store updates, save serialization, and
  rollback.
- Component-test the compact zoom controls and accessible labels.
- Component-test that active xterm and ghostty-web panes send their fitted
  columns and rows after font metrics change, while inactive panes do not claim
  resize authority.
- Browser-test focused keyboard shortcuts, persistence through the API, and
  synchronized sizing in both regular and inline workspace placements.
- In a real tmux workspace, record `stty size` before and after zoom and assert
  the PTY geometry follows the renderer for both terminal implementations.
- Verify the Bun patch applies under a frozen install and inspect the built
  addon for linear texture filters without `generateMipmap`.
