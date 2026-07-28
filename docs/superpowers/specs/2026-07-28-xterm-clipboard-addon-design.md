# Xterm Clipboard Addon Design

## Goal

Replace Middleman's custom tmux mouse-report filtering with xterm's official clipboard addon, and remove the alternate Ghostty renderer so terminal clipboard behavior has one implementation.

## Terminal Architecture

Workspace and runtime terminals use `XtermTerminalPane` exclusively. Remove `ghostty-web`, `GhosttyTerminalPane`, renderer selection in `TerminalPane`, and the terminal renderer setting from config, HTTP schemas, generated clients, settings stores, UI, fixtures, and tests. This is a direct removal: existing `renderer = "ghostty-web"` configuration is not translated, ignored through a compatibility layer, or retained as an inert setting.

Add `@xterm/addon-clipboard` as a frontend dependency. Create and load one `ClipboardAddon` alongside xterm's existing addons during terminal startup. The terminal owns its lifecycle, so normal xterm disposal also disposes the clipboard integration; no separate backend clipboard endpoint or operating-system clipboard bridge is introduced.

The addon uses its standard browser clipboard provider. OSC 52 read and write requests therefore remain subject to the browser's secure-context, permission, and user-activation policy. Middleman does not add gesture tokens, a custom OSC parser, fallback clipboard storage, or native clipboard commands. Terminal processes are treated like processes in a native terminal: they may request clipboard access, while the browser remains the final authority.

## Input and Clipboard Flow

Delete `tmuxMouseDragFilter` and forward every string emitted by xterm's `onData` callback to the open terminal WebSocket unchanged. Tmux receives the complete mouse sequence and owns copy-mode selection behavior. When tmux emits an OSC 52 clipboard operation, xterm's parser routes it to `ClipboardAddon`, which talks to `navigator.clipboard`.

Keep the existing multiline-paste interception unchanged. It continues sanitizing terminal control delimiters and preserving bracketed-paste framing before data reaches the WebSocket. Single-line paste remains handled by xterm.

## Error Handling

Clipboard permission denial uses the addon's normal browser behavior. Middleman does not retry through another clipboard surface or show a new application error. Terminal input and rendering continue even when a clipboard operation is rejected.

Removing the renderer field is intentional rather than a compatibility migration. Configuration containing the removed field follows the config loader's normal unknown-field behavior; no renderer-specific validation, alias, warning, or translation is added.

## Verification

- Update the terminal component test to prove `ClipboardAddon` is loaded into xterm and raw SGR mouse input reaches the WebSocket without threshold filtering.
- Remove Ghostty-only and mouse-filter-only tests, and update settings/config/API tests to prove renderer selection is absent.
- Add targeted real-browser coverage that drives an OSC 52 operation through a running xterm terminal and observes the browser clipboard result where the browser supports clipboard permission grants.
- Run Svelte autofix on every changed component, the focused terminal and settings tests, API generation checks, the full frontend unit suite, the affected browser and Playwright suites, and the relevant shuffled Go tests.
