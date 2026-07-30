# Terminal Clipboard Unicode Fidelity

## Goal

Preserve Unicode text copied from a middleman tmux terminal when browser
clipboard writes fail and the copy uses the native macOS fallback.

The observed mojibake occurs after the frontend has decoded OSC 52 correctly:
`pbcopy` uses its process locale to select the input encoding, and a
service-launched middleman process may not supply a UTF-8 locale. The fix is to
give the macOS clipboard child an explicit UTF-8 locale without changing the
existing terminal clipboard architecture.

## Architecture

Keep the current clipboard flow unchanged:

- `osc52Clipboard.ts` synchronously validates the selection, rejects reads,
  malformed base64, invalid UTF-8, and payloads above 1 MiB, then returns
  decoded Unicode text.
- `XtermTerminalPane` checks pane and WebSocket state before consuming one
  bounded, one-shot trusted-gesture authorization.
- `terminalClipboardWriter.ts` tries the deferred browser clipboard write,
  then a direct browser write, then the CSRF-protected loopback endpoint.
- The server validates that the client is local and passes the text to the
  platform clipboard command.

Firefox and Zen support through the native fallback remains required.
Reconnect sequence cancellation, pointer authorization revocation,
tmux drag-autoscroll, and once-per-pane failure reporting remain unchanged.

## Native Clipboard Encoding

On macOS, run `pbcopy` with `LC_ALL=en_US.UTF-8` while passing the clipboard
text unchanged on standard input. `LC_ALL` deliberately overrides inherited
locale variables because `pbcopy` uses them to choose its input encoding.
Without a UTF-8 locale, bytes for characters such as an em dash or
non-breaking space can be interpreted as MacRoman and stored as mojibake.

Linux, BSD, and Windows clipboard command behavior remains unchanged. Windows
continues to receive UTF-16LE input. Do not apply the macOS locale override to
`wl-copy`, `xclip`, `xsel`, or `clip.exe`.

## Considered and Rejected: `@xterm/addon-clipboard`

Stable `@xterm/addon-clipboard` 0.2.0 was evaluated as a replacement for
Middleman's 55-line parser. It does not fit the required security and runtime
contract:

- The addon catches decode failures and unconditionally calls
  `provider.writeText` with an empty string. Malformed, oversized, or
  invalid-UTF-8 input would therefore look like a legitimate empty copy,
  consume the one-shot authorization, and clear the clipboard unless
  Middleman retained a coupled rejection side channel or a pre-filter.
- If `provider.writeText` returns a promise, the addon returns that promise
  from its OSC handler and xterm pauses input parsing until it settles. The
  native fallback includes an HTTP round trip and must remain fire-and-forget
  so clipboard latency cannot freeze terminal output.
- The addon reads the clipboard and echoes an OSC 52 response to the PTY by
  default. A Middleman read-denial handler would have to be registered after
  the addon because xterm invokes the most recently registered OSC handler
  first; the security boundary would silently depend on that ordering.

After those workarounds, Middleman would still own selection policy,
read denial, malformed and size rejection, UTF-8 validity, gesture
authorization, and the asynchronous write chain. The addon would contribute
only base64 and UTF-8 decoding already provided synchronously by browser
platform primitives, while adding `js-base64` as a dependency. It is not
adopted.

Reconsider the addon if an upstream stable release supports both rejecting a
write before provider invocation and non-blocking provider writes. Clipboard
reads must also remain denyable without relying on handler registration order
or emitting a response to the PTY.

## Verification

The true failure requires real macOS `pbcopy` under a non-UTF-8 locale, so
portable tests divide at the process boundary instead of adding a macOS-only
full-stack lane:

- A Go test with a fake command runner proves that macOS `pbcopy` receives
  unchanged Unicode text and `LC_ALL=en_US.UTF-8`.
- Existing table cases prove Linux, BSD, and Windows command input and
  environment remain unchanged.
- The real-tmux Playwright tests use non-ASCII clipboard markers in Chromium
  and Firefox. Chromium proves Unicode reaches the browser clipboard; the
  Firefox fallback case proves Unicode survives OSC 52 parsing, authorization,
  the writer, JSON serialization, and the HTTP boundary up to the server.
- Existing parser tests continue to cover read denial, unsupported selections,
  malformed base64, invalid UTF-8, empty writes, and the 1 MiB limit.

Run the focused Go and frontend tests first, then the full frontend unit suite,
the affected Chromium and Firefox full-stack Playwright suite, Svelte checks,
and the relevant shuffled Go tests before completion.
