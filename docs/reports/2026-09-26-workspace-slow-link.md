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
- **Recently visited workspace shows setup again.** The workspace presentation
  cache held only ten entries. A regression visits twelve workspaces, then holds
  both refresh responses on return to the first: it displayed "Setting up
  workspace" even though the first terminal's socket remained connected. Keeping
  100 workspace presentations restores the existing pane and socket immediately.
  Diff snapshots retain their existing limit. Terminal
  retention now defaults to 50 sessions and accepts 0–100 in Settings; saved
  explicit values remain unchanged. Each agent or terminal pane counts separately,
  and retained connections keep receiving output as well as consuming memory.
- **Terminal preferences separated from workspace preferences.** Terminal
  appearance and retention now live under Workspaces in Settings. Search keywords
  select that category, and switching categories preserves terminal drafts.
- **New-workspace repository choices wait on every open.** The dialog discarded
  its options on open and source changes; the global repository picker fetched
  the same list independently. Both now share an app-owned successful catalog
  and pending reads. The dialog restores choices while refreshing, retains them
  on refresh failure, and preserves an in-flight user choice or clears it if
  removed or replaced by a different provider repository. Creation carries the
  selected stable repository ID through backend admission. A real-browser
  regression first loads the global picker, stalls the dialog's refresh, and
  shows cached choices before releasing the read.
- **PR/issue details evicted within a normal working session.** Each detail
  cache now retains 100 items instead of ten. The existing revisit regressions
  now visit twenty other items before returning with the fresh read held open.
  Both failed at ten and pass at 100; the original cached detail appears
  immediately, retains its lifecycle tick, and updates when the read completes.
  This changes retention, not the existing requirement to revalidate before
  provider mutations.

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

## Follow-up usability fixes

The audit follows each action until it becomes usable, not just until content
appears. Held-response regressions cover the dependencies removed below.

1. **Repository choices no longer wait for write credentials.** `/repos` now
   returns stored catalog data without calculating mutation capabilities or
   operation availability. It preserves tracked/hidden filtering, stable provider
   identity, sync health, and merge metadata. PR/issue detail responses still
   provide mutation authority. A real-handler regression holds the credential
   source open while catalog reads complete across all four providers.
2. **Known machine choices remain usable during discovery.** The workspace
   dialog restores the last successful host directory and keeps the selected
   destination independent of it. An unresolved preferred devbox submits to that
   exact devbox; it never falls back to local. Fresh removal, known maintenance,
   and provider restrictions still block creation, and the backend admits every
   request. Discovery failures show a status-unavailable hint. Settings
   separates discovery from mutation busy state so saved and local choices
   remain selectable.
3. **Matching cached details allow local comment drafting.** PR and issue
   editors accept drafts while the matching cached detail refreshes. Submission
   still waits for fresh detail and operation permission. A different item's
   detail cannot enable the editor or submit controls.
4. **Queued launches use one fresh runtime admission read.** Desktop and mobile
   wait for matching ready workspace metadata, then reuse that read as the
   shared workflow's lost-response reconciliation baseline. Arbitrary cached
   runtime cannot authorize launch. Existing polling retries a failed admission
   read, and normal launches retain workflow-owned baseline reads.
5. **Readiness events avoid another polling interval.** Desktop and mobile can
   apply a local creating-to-ready event while detail refresh is pending.
   Remote events and deleting workspaces cannot take that shortcut. Mobile
   retains polling as a fallback and reads ordinary detail/runtime presentation
   concurrently.
6. **Independent fleet batches run together.** Member and devbox requests now
   start concurrently. A regression holds the member response until the devbox
   request starts and verifies both reachable hosts. Tool probing invokes
   `tmux -V` once per probe instead of twice; other tool probes remain serial.

Relevant sources are `internal/server/huma_routes.go::listRepos`,
`internal/server/helpers.go::repoCatalog`, `NewWorkspaceDialog.svelte`,
`DevboxSettings.svelte`, `PullDetail.svelte`, `IssueDetail.svelte`,
`workspace-runtime-workflow.ts`, `WorkspaceTerminalView.svelte`,
`MobileWorkspaceTerminal.svelte`,
`internal/server/fleetapi/fleet_hub.go::fetchPeerResults`, and
`internal/fleet/probe.go::Probe`.

Two suspected waits were already independent: project registration can proceed
while its host snapshot is pending, and an already mounted Forge selector keeps
its links usable during refresh. Fleet sign-in and devbox source-context
preparation perform real admission work and remain required.

Held-response tests establish dependency, not elapsed airplane latency. No
production memory or network timing claim follows from these results.

## Remaining costs to measure

| Path                      | Source-backed cost                                                                                                                                                                                                                                                                          | Next discriminating check                                                                                           |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| Terminal cache miss       | Fresh runtime precedes a new WebSocket attachment. Presentation retains 100 workspaces, while socket retention defaults to 50 **sessions**, configurable from 0–100. Several panes per workspace can exhaust socket retention first; disconnected sockets still require a fresh attachment. | Compare retained and evicted switches using `workspace-switch:*` timing entries under controlled RTT and bandwidth. |
| PR/issue detail memory    | Presentation now retains 100 items of each kind. Content size varies; the twenty-item held-response regressions establish useful retention, not memory consumption.                                                                                                                         | Measure retained heap with representative long discussions if memory becomes a concern.                             |
| Agent launch              | Fresh post-readiness admission still precedes a queued POST. Devbox launch also refreshes source context before forwarding it.                                                                                                                                                              | Measure browser and server legs separately; preserve lost-response reconciliation and source-context authority.     |
| Fleet directory           | Cold discovery still returns full inventories and probes tools. Cached host choices and exact saved-target routing now work during refresh; member/devbox batches overlap.                                                                                                                  | Measure payload bytes and batch durations independently of rendering before adding a separate directory endpoint.   |
| Expanded Activity threads | Refreshing a collapsed snapshot can trigger thread-history reads, with pages fetched sequentially.                                                                                                                                                                                          | Compare collapsed rows with individually expanded histories and record time to the last page.                       |

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

## Working-set and settings verification

- The held-response workspace revisit regression now covers twelve workspaces;
  it failed with the ten-entry cache and passes with the larger cache, retaining
  the original terminal subtree and socket. The session pool regression keeps
  50 connected sessions and evicts the oldest when a 51st is released.
- The navigation browser suite passes all ten cases, including Settings to
  Workspaces with a blocked refresh: cached rows stay visible without a loading
  placeholder and update when the response arrives.
- All 142 full-stack browser cases in the affected settings, workspace,
  federation, actions, and autocomplete suites pass in Chromium and Firefox.
  All 12 settings viewport cases also pass. The first full-stack run exposed an
  ambiguous category selector, which was corrected before this complete rerun.
- The full config package and focused settings API tests pass. The API regression
  verifies that saving 50 retained sessions survives configuration reload.
  Frontend bounds are generated from the server schema; invalid drafts remain
  local until corrected.
- The final full Vitest run passed 4,265 tests and skipped one. Six test-server
  lifecycle cases timed out; their full file then passed separately (26 tests)
  without code changes. No entirely green full Vitest run is claimed. Frontend
  formatting, lint, type and Effect checks, Go lint, formatting, and nilaway pass.

## Repository picker verification

- The reopen regression first failed with "Loading repositories" while the
  response was held. It now restores options and preserves a new selection when
  that response arrives. Cached options also remain selectable after a failed
  refresh.
- All eleven navigation browser cases pass, including reuse of the global
  picker's catalog in the new-workspace dialog and clearing a removed choice.
  The new case initially left the dialog open across tests; closing it through
  Cancel before teardown resolved the suite stall.
- All twenty full-stack workspace creation and launch cases pass in Chromium
  and Firefox. Frontend formatting, lint, type, Svelte, and Effect checks pass.
- The final full Vitest run passes 4,273 tests and skips one, with browser files
  run serially and at most two workers.

## Detail retention and usability audit verification

- Both twenty-item revisit regressions failed with the ten-entry caches. With
  100 entries, both complete store files pass all 103 tests. Independent review
  found no issues; these regressions exercise retention beyond ten, not the
  exact 100/101 eviction boundary.
- The final full Vitest run passes 4,273 tests and skips one across 393 passing
  files and one skipped file. Frontend formatting, lint, type and Effect checks,
  Svelte analysis, and context structure checks pass.
- The audit passed five temporary frontend probes that held host or discovery
  responses pending, plus the isolated real-handler credential probe. Existing
  repository-picker, machine-selection, launch-reconciliation, and fleet tests
  also passed. Temporary probes did not change repository files or live state.

## Usability follow-up verification

- Held-response regressions cover catalog reads with credential discovery blocked,
  cached machine choices with discovery pending, exact destination capture,
  editable cached drafts with submission disabled, and one fresh queued-launch
  admission read. The four launch/event suites pass all 215 tests.
- Mobile Open notifications can arrive synchronously or after mount. Both now
  wait for initial loading and refresh only pending workspace enrichment;
  stale reconnects still reload workspace and runtime. The asynchronous case
  reproduced the phone launch failure in both browsers before the correction.
  Setup polling reads runtime only once the workspace is ready.
- The full API test package, fleet packages, focused race checks, Go lint,
  formatting, and nilaway pass. The full server package timed out in
  `TestFederationEventEndpointReplaysFilteredEventsAndSignalsStale`. Twenty
  focused repetitions reproduced the same response-body cleanup hang both on
  this branch and with all pending Go changes restored to the starting commit
  through an overlay. A single focused run passed. No clean full server run is
  claimed.
- After the final frontend edit, the full Vitest suite passed 4,304 tests and
  skipped one across 393 passing files and one skipped file. Frontend formatting,
  lint, Svelte/type checks, and Effect diagnostics pass.
- All 102 full-stack cases in workspace creation/launch, workspace launcher,
  inline continuity, comment editor, and terminal settings pass in Chromium and
  Firefox. An isolated seeded-app capture also confirms the 50-session default
  under Workspaces settings.
- The broader Go run exposed an old catalog-capability assertion. The catalog
  intentionally no longer authorizes mutations; its existing capability test
  now covers only enriched responses. The focused test passes after removing
  that obsolete block, and independent review found no issue with the change.
- The final full server-package run passes with only the independently reproduced
  federation cleanup hang excluded. The API package and focused capability
  contract test also pass; the excluded test is the remaining verification limit.

## Review follow-up verification

- Repository route-reuse regressions cover cached selection, local and
  host-qualified creation, branch reuse, manager creation, and devbox creation.
  The workspace API, workspace integration, and manager Go suites pass.
- Provider label-capability tests now read the enriched PR response used by the
  UI. The GitLab and Gitea/Forgejo cases pass normally and with the race detector.
- The workspace sidebar and workspace view browser suites pass 265 tests, with
  one skipped, across Chromium and Firefox using normal timeouts. Two Chromium
  terminal-picker cases failed in an earlier run; both passed three focused
  repetitions each, a concurrent group, and the final full run with failure traces
  enabled. The initial failures lacked traces and their cause remains unproven.
- The real-backend workspace creation and launch suite passes all 20 cases
  across both browsers after the production changes. Go lint, formatting,
  nilaway, and frontend checks also pass.
- Cached unavailable machines remain selectable and usable during pending or
  failed discovery; fresh unavailable results and provider restrictions still
  block creation. Component regressions reproduce the old gates and verify the
  exact saved destination. The full frontend suite passes with one worker;
  an earlier parallel run passed its assertions but exited with file-watcher
  `EMFILE` errors. No assertions or host limits were changed.
