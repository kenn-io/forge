# Xterm Clipboard Addon with Native Fallback

## Goal

Replace Middleman's custom OSC 52 parsing and base64 decoding with the
maintained `@xterm/addon-clipboard` integration while preserving clipboard copy
in browsers such as Firefox and Zen that reject asynchronous browser clipboard
writes.

The change must also preserve Middleman's existing write-only security policy,
trusted-gesture authorization, payload bound, reconnect behavior, and
CSRF-protected loopback fallback.

## Architecture

`XtermTerminalPane` continues to own the terminal, WebSocket, gesture events,
and connection-state checks. It loads one `ClipboardAddon` during terminal
startup. The addon owns OSC 52 write parsing and UTF-8/base64 decoding.

Middleman supplies two narrow policy adapters around the addon:

- A read-denial OSC handler consumes OSC 52 clipboard queries before the addon
  can access or return local clipboard contents.
- A bounded base64 adapter delegates valid decoding to the addon's maintained
  implementation while rejecting malformed or decoded clipboard text above
  Middleman's existing 1 MiB limit.

The addon writes through a Middleman `IClipboardProvider`. The provider accepts
only the system clipboard selection, including tmux's empty selection alias,
and delegates accepted text to the existing one-shot terminal clipboard
writer. It does not contain OSC 52 parsing or base64 logic.

The existing native clipboard HTTP route remains. It is a required fallback,
not a compatibility shim: Firefox and Zen can reject the browser clipboard
operations used for asynchronous OSC 52 writes.

## Clipboard Flow

1. A trusted keyboard or primary-pointer gesture arms one bounded, one-shot
   clipboard authorization.
2. Tmux receives the user's copy interaction and emits an OSC 52 sequence.
3. A read query is consumed without reading the browser clipboard or sending
   clipboard content to the terminal process.
4. A write falls through to `ClipboardAddon`. The bounded adapter rejects
   malformed or oversized data; otherwise the addon's implementation decodes
   the text as UTF-8.
5. The provider ignores unsupported selections and checks that the pane is
   enabled, connected, and not disposed before consuming the authorization.
6. The existing clipboard writer tries the deferred browser clipboard write,
   then a direct browser write, then the CSRF-protected loopback endpoint.
7. If every write path fails, Middleman reports the existing clipboard failure
   once for that pane.

Authorization remains revocable on pointer cancellation, focus loss,
visibility loss, disconnection, and disposal. Existing reconnect sequence
cancellation and tmux drag-autoscroll behavior remain unchanged.

## Native Clipboard Encoding

The server continues to pass Unicode text unchanged to the platform clipboard
command.

On macOS, the `pbcopy` child process receives an explicit
`LC_ALL=en_US.UTF-8` environment override. This is required because `pbcopy`
uses locale variables to choose its input encoding; service launchers may not
provide a UTF-8 locale, causing UTF-8 bytes such as an em dash or non-breaking
space to be interpreted as MacRoman.

Linux, BSD, and Windows clipboard command behavior remains unchanged. Windows
continues to receive UTF-16LE input.

## Error and Security Behavior

- OSC 52 reads never access or disclose the local clipboard.
- Writes without a recent trusted gesture do not change either the browser or
  native clipboard.
- Unsupported clipboard selections are ignored.
- Malformed and oversized payloads do not clear or replace the clipboard.
- Writes arriving after disconnect, disablement, or disposal are ignored.
- Browser clipboard rejection falls back only through the local,
  CSRF-protected endpoint and its existing client-locality checks.
- Native clipboard failure retains the existing once-per-pane user-visible
  error.

No new settings, compatibility adapters, clipboard-read opt-ins, or remote
clipboard bridges are introduced.

## Verification

Tests will prove the behavior at the narrowest owning boundary:

- Component tests verify that `ClipboardAddon` is loaded with Middleman's
  bounded write-only adapters and that authorization and pane-state checks
  still gate writes.
- Adapter tests verify tmux's empty selection alias, denied reads, malformed
  input, the 1 MiB limit, and exact Unicode text delivery.
- Existing real-tmux full-stack Playwright coverage remains. Its Chromium and
  Firefox fallback cases will use non-ASCII clipboard markers so the real
  parser, addon, provider, and JSON fallback path must preserve Unicode.
- The reconnect test continues to prove that an incomplete terminal sequence
  cannot consume output from the next connection.
- Go tests verify that macOS `pbcopy` receives unchanged UTF-8 input and the
  explicit UTF-8 locale while other platform commands retain their existing
  inputs and environment.
- Relevant frontend tests, the full frontend unit suite, affected Playwright
  tests, Svelte checks, shuffled Go tests, and API-generation cleanliness run
  before completion.
