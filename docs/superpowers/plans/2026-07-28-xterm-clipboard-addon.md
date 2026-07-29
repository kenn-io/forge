# Xterm Clipboard Addon Implementation Plan

**Goal:** Replace PR #765's custom clipboard and tmux mouse pipeline with
`@xterm/addon-clipboard` on current `origin/main`.

**Architecture:** Xterm remains the only terminal renderer. Its maintained
clipboard addon owns OSC 52 while Middleman forwards xterm input to tmux
unchanged. Browser permissions remain the clipboard authority; there is no
server or operating-system fallback.

## Constraints

- Keep the renderer removal already present on `origin/main`.
- Delegate to the addon's browser provider, mapping tmux's empty OSC 52 target
  to the system clipboard target `c`.
- Do not add a custom OSC parser, gesture-token layer, native clipboard bridge,
  compatibility path, or synthetic edge-drag mouse reports.
- Preserve multiline and bracketed-paste behavior.
- Cancel incomplete terminal parser state before output crosses a WebSocket
  reconnection boundary.
- Treat Zen/Firefox denial of asynchronous clipboard writes as expected browser
  policy, not as a trigger for a fallback.

## Task 1: Replace the custom clipboard path

**Files:**

- Modify `frontend/package.json` and `bun.lock`.
- Modify `frontend/src/lib/components/terminal/XtermTerminalPane.svelte`.
- Delete the custom OSC 52, clipboard writer/fallback, and tmux drag-autoscroll
  modules.
- Remove the hidden clipboard API, native clipboard package, generated request
  types, and their tests.

Steps:

1. Add `@xterm/addon-clipboard` and load one `ClipboardAddon` with a thin target
   adapter that otherwise delegates to `BrowserClipboardProvider`.
2. Send every `term.onData(data)` value unchanged while the terminal WebSocket
   is open.
3. Write CAN (`\x18`) before exit/reconnect output can cross terminal sessions,
   preventing an incomplete OSC sequence from consuming fresh output.
4. Regenerate API artifacts after removing the hidden native clipboard route.

## Task 2: Cover the owned integration seams

**Files:**

- Modify `frontend/src/lib/components/terminal/TerminalPane.test.ts`.
- Modify `frontend/tests/e2e/workspace-sidebar.spec.ts`.
- Replace `frontend/tests/e2e-full/00-tmux-browser-clipboard.spec.ts` with one
  focused real-tmux test.

Steps:

1. Prove the terminal loads `ClipboardAddon` and sends a complete SGR mouse
   sequence unchanged.
2. Prove a partial OSC sequence is cancelled before output from a reconnected
   WebSocket is written.
3. In mock Playwright, inject OSC 52 through xterm and observe Chromium's
   browser clipboard.
4. In isolated full-stack Playwright, create a real workspace, issue tmux
   `set-buffer -w`, and observe the Chromium clipboard through the real server
   WebSocket path.

Do not add tests for the addon's own base64 or clipboard-provider internals.
Malformed payload decoding, OSC 52 reads, and browser-denial mechanics remain
dependency/browser behavior; Middleman owns only addon loading, connection
boundaries, and the real tmux-to-browser integration.

## Task 3: Record the browser support contract

Update the design and living workspace-runtime context with these decisions:

- The browser provider supports OSC 52 reads. A terminal process can receive
  browser clipboard contents only when browser clipboard-read policy allows it.
- Invalid payloads and rejected clipboard promises use the addon's behavior;
  Middleman neither retries nor substitutes a native clipboard.
- Removing synthetic edge-drag reports also removes Middleman-provided drag
  autoscroll beyond the rendered terminal bounds.
- Zen/Firefox may reject asynchronous OSC 52 writes. Manual verification should
  confirm that denial leaves the clipboard unchanged and terminal use intact,
  not require clipboard success.

## Task 4: Verify and commit

Run:

```bash
(cd frontend && ../node_modules/.bin/vp test run --project unit)
(cd frontend && ../node_modules/.bin/vp test run --project browser)
./node_modules/.bin/vp run frontend-package-check
(cd frontend && ../node_modules/.bin/vp exec -- playwright test \
  tests/e2e/workspace-sidebar.spec.ts --config=playwright.config.ts)
(cd frontend && ../node_modules/.bin/vp exec -- playwright test \
  tests/e2e-full/00-tmux-browser-clipboard.spec.ts \
  --config=playwright-e2e.config.ts --project=chromium)
go test ./internal/config ./internal/server ./internal/server/e2etest ./cmd/middleman -shuffle=on
make api-generate
git diff --check
```

Run context sync before committing. Push only after public-history inspection
and the full affected frontend/Playwright checks succeed.
