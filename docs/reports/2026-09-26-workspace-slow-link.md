# Workspace navigation on high-latency HTTPS

This investigation covers a browser connected to a remote Forge server over
HTTPS. It does not measure a particular airplane connection or claim an
end-to-end speedup from local test timings.

## Reproduced and changed

- **Workspace list disappears on return.** Leaving Workspaces unmounts its
  sidebar, whose list previously started empty on every mount. A test that
  withholds the next snapshot response reproduced the loading placeholder.
  Desktop and mobile lists now restore the last successful app-owned snapshot,
  including an empty list, before refreshing. Hidden sidebar polling still
  stops. The cache lasts for the application runtime, not across page reloads.
- **Home appears during a chosen agent launch.** The inline launcher respected
  pending launch intent, but the standalone Home pane did not. A deferred launch
  reproduced it. An empty workspace now displays the selected agent's launch
  progress until the session arrives; a failed launch restores Home and its
  launch controls.

The browser navigation regression visits Activity and Workspaces, holds both
refresh responses open, and checks that their cached content remains visible.
Releasing the responses updates the workspace list. The launch regression also
holds the post-launch runtime response and confirms that the acknowledged
session becomes selected without waiting for that read.

## Existing behavior verified or traced

- Terminal traffic uses WebSockets, including upstream fleet/devbox connections;
  SSH is not part of this browser transport. The shared transport enables
  `permessage-deflate` on both legs. Focused Go tests exercised local/fleet
  negotiation, heartbeat, replay ordering, and replay boundaries. The user's
  live browser connection was not inspected.
- PTY output forwards immediately per read. Ordinary-screen replay is bounded
  to 64 KiB. Compression and buffering do not remove the request dependencies
  below (`internal/terminalwebsocket/terminalwebsocket.go`,
  `internal/workspace/localruntime/manager.go`).
- Workspace metadata and runtime requests already start concurrently. Repeat
  visits restore recent metadata/runtime, and retained terminals reuse their
  existing socket. Activity already retains populated rows while refreshing.

## Remaining costs to measure

| Path | Source-backed cost | Next discriminating check |
| --- | --- | --- |
| Terminal cache miss | Fresh runtime precedes a new WebSocket attachment. Presentation retains 10 workspaces, while socket retention defaults to 10 **sessions**, configurable from 0–20. Several panes per workspace can exhaust socket retention first. | Compare retained and evicted switches using `workspace-switch:*` timing entries under controlled RTT and bandwidth. |
| Agent launch | The mutation workflow reads a runtime baseline before its launch POST, even when the view just read runtime. That baseline supports uncertain-outcome reconciliation. | Measure this read separately; assess baseline reuse or server-owned launch identity before removing it. |
| Fleet directory | The header loads the full `/snapshot?include_peers=true` for host names; the workspace list loads it independently. | Count duplicate requests and bytes when opening Workspaces and restoring browser focus. |
| Parked workspace | Runtime polling uses a three-second interval even while the persistent host is parked on another page. | Count idle requests after leaving Workspaces; verify promoted panes and accepted launches before changing ownership. |
| Expanded Activity threads | Refreshing a collapsed snapshot can trigger thread-history reads, with pages fetched sequentially. | Compare collapsed rows with individually expanded histories and record time to the last page. |

Relevant sources are `WorkspaceTerminalView.svelte`,
`workspace-runtime-workflow.ts::executeMutation`, `session-host.svelte.ts`,
`ForgeSelector.svelte::refresh`, and `activity.svelte.ts::projectActivitySnapshot`.
These mechanisms are established by code inspection; their contribution to the
reported connection has not been measured. The existing HTTP slow-link profile
does not measure terminal transport, and the terminal-switch profiler does not
currently impose a slow-link profile.

## Verification

- The workspace-list suite passed all 81 tests; the terminal-view suite passed
  all 168 tests. The browser navigation regression passed with refresh responses
  deliberately held open.
- Frontend formatting, lint, type checks, Effect checks, Go lint, and context
  structure checks passed.
- A full Vitest run with four workers passed 4,258 tests, skipped one, and timed
  out in two settings tests. Both affected files passed separately (five tests).
  An earlier unrestricted run also had timeouts, all absent on the bounded run.
  Neither full run was entirely green.
- The Workspaces and workspace-sidebar Playwright suites passed 249 cases,
  skipped one, and failed 16 Chromium cases. All 16 passed on a focused rerun
  without code changes. They also passed against the unchanged starting commit
  in a separate checkout. The initial failures remain unexplained; no clean
  full Playwright run is claimed.
