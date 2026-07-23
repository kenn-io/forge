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

## Verification

- Unit-test size clamping, reset, shared-store updates, save serialization, and
  rollback.
- Component-test the compact zoom controls and accessible labels.
- Browser-test focused keyboard shortcuts, persistence through the API, and
  synchronized sizing in both regular and inline workspace placements.
- Verify the Bun patch applies under a frozen install and inspect the built
  addon for linear texture filters without `generateMipmap`.
