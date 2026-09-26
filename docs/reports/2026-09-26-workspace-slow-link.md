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

The follow-up addresses additional browser work and waits:

- **Cached PR/issue details not restored from workspaces.** Workspace response
  enrichment rebuilt `repo` without its permanent `platform_repo_id`, even
  though the database summary contained it. The frontend intentionally refuses
  cached restoration without this identity. The shared enrichment helper now
  preserves it in cached, full, and tmux response paths. The regression first
  reproduced the dropped ID for PR, issue, and ad-hoc workspaces with linked PRs.
- **Duplicate snapshot reads.** The header, workspace lists, and creation
  surfaces now share an in-flight snapshot request. A regression counted two
  simultaneous requests before the change and one afterward. Completed reads
  still allow fresh requests, and closing one consumer preserves other waiters.
  Repeated browser-focus events no longer restart a slow header request.
- **Parked runtime polling.** A hidden workspace host previously issued three
  runtime reads during nine simulated idle seconds. It now issues none until
  revealed. Visible promoted panes keep polling, and accepted launch
  reconciliation remains application-owned.
- **Terminal launch acknowledgement.** Normal terminal placement waited for a
  runtime read after the launch response returned the session. It now attaches
  that session immediately and refreshes in the background, matching workflow
  and split-terminal placement. A test holds the refresh response until after
  attachment.
- **Previous PR or issue left visible.** The workspace sidebar now hides a
  different item's details on a cache miss. The existing recent-detail cache
  restores verified repository identities; real-store tests exercise
  A → B → A → B with held requests and confirm immediate cached revisits,
  read-only cached actions, and updates after revalidation. No additional
  detail cache was needed: preserving the server's identity enables the existing
  cache for workspace responses.

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
| Fleet directory | The header still receives a full `/snapshot?include_peers=true` when it needs host names. Concurrent reads are now shared. | Measure payload bytes and assess whether a smaller directory response would materially reduce transfer. |
| Expanded Activity threads | Refreshing a collapsed snapshot can trigger thread-history reads, with pages fetched sequentially. | Compare collapsed rows with individually expanded histories and record time to the last page. |

Relevant sources are `WorkspaceTerminalView.svelte`,
`workspace-runtime-workflow.ts::executeMutation`, `session-host.svelte.ts`,
`ForgeSelector.svelte::refresh`, and `activity.svelte.ts::projectActivitySnapshot`.
These mechanisms are established by code inspection; their contribution to the
reported connection has not been measured. The existing HTTP slow-link profile
does not measure terminal transport, and the terminal-switch profiler does not
currently impose a slow-link profile.

## Initial verification

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

## Follow-up verification

- Focused tests passed: 170 terminal-view tests, 10 terminal-visibility browser
  tests, 19 right-sidebar tests, 16 fleet-selector browser tests, and the shared
  snapshot request regression. Each changed behavior was first reproduced by a
  failing regression. The PR/issue revisit tests use the real application stores.
- Frontend formatting, lint, type checks, Effect diagnostics, Go lint, and context
  structure checks passed. Independent review found no actionable issues.
- The full Vitest run after the final test edit passed 4,265 tests and skipped
  one. Two settings browser tests timed out; both files passed separately
  (seven tests). Those cases also passed in the preceding full run, which exposed
  a snapshot-module test mock that was then corrected. No entirely green final
  full Vitest run is claimed.
- The full Workspaces/workspace-sidebar Playwright run passed 263 cases and
  skipped one. Two Chromium phone-terminal cases failed; these also failed in
  the initial investigation before the follow-up changes. Both passed on a
  focused rerun without code changes.
- The repository-identity regression failed before the backend fix in all 12
  combinations of workspace kind and response path. After the fix, it and the
  full workspace API Go package passed with shuffled test order.
