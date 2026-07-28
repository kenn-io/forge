# UI Interaction Contracts

Use this document for frontend behavior changes where the risk is not visual
style but stale identity, broken persistence, or surprising interaction
semantics.

## Purpose

- Make behavior-level UI contracts explicit.
- Keep route identity, persisted browser state, and keyboard/pointer semantics
  consistent across the app.
- Prevent narrow regressions that usually show up only after review or in e2e
  flows.

## Identity And Route State

Interactive surfaces must agree on which item is selected.

- Treat `platform_host` as part of PR and issue identity in route state, drawer
  state, and stale-detail guards.
- When host is omitted for a provider's default host (Activity URLs,
  provider-default routes), normalize comparisons and cache keys with
  `packages/ui/src/api/provider-routes.ts::resolvedPlatformHost` so the
  concrete default host and an omitted host do not look like different items.
- Route segments and item references may carry provider aliases (gh/gl/fj)
  while store data uses canonical names: every identity comparison or cache
  key derived from `provider` must canonicalize it first
  (`packages/ui/src/workspace-inline.ts::identityEquals`). This includes
  route-reset/generation effects that detect item changes — tracking raw
  props treats an alias-only re-expression of the same item as a new item
  and discards in-flight work.
- Workspace item identity includes the item type, canonicalized across caller
  vocabularies ("pull"/"pr"/"pull_request") by
  `packages/ui/src/workspace-inline.ts::canonicalItemType`: a PR and an issue
  can share a repo and number, so repo+number alone must never key claims,
  overrides, or deletion tombstones.
- Use shared named route/item reference types from
  `frontend/src/lib/stores/router.svelte.ts` instead of repeating anonymous
  `{ owner, name, number }`-style shapes.
- When a view changes from item A to item B, reset transient action state that
  could otherwise submit or render against the wrong item.
- A response confirming a server-side outcome (a completed delete or create)
  must publish to identity-scoped global state — claims, tombstones, creation
  overrides, route memory — before any liveness guard: neither unmount nor a
  selection that moved on (an A→B→A round-trip bumps the request generation)
  may discard it, or the replacement UI re-offers the action for a duplicate
  submission. Gate only presentation — refetches, prompts, flashes,
  navigation — on the live component and current selection
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::handleDelete`,
  `packages/ui/src/components/detail/PullDetail.svelte::createWorkspace`).
  The pending request is identity-scoped shared state too: component-local
  creating flags reset on route changes and remounts while the request is
  still in flight, re-enabling the action for a duplicate submission
  (`packages/ui/src/stores/workspace-create-pending.svelte.ts`). The same
  store records confirmed creations for detail instances WITHOUT an inline
  controller (focus/mobile views, DetailDrawer), which otherwise only see
  the detail envelope; records reconcile away when an identity-matched
  envelope carries a workspace and clear on deletion by workspace ID. A
  workspace-absent envelope clears a created record or override only when
  its request STARTED after the confirmation (shared lifecycle tick,
  `nextWorkspaceLifecycleTick`): a stale pre-create fetch must not wipe a
  creation, but a post-create fetch reporting absence or a replacement
  workspace ID — or a 404 on the workspace itself, which also drops the
  cached envelope so liveness rendering shows the error state — means
  another client deleted it and the record must drop. Detail stores apply
  envelope payload and tick atomically (last-started-wins) so a stale
  response cannot pair with a newer tick, and tombstones mask only their
  own deleted ID — a fresh-ID created record supersedes them. Deleted
  workspace IDs persist for the session (`markWorkspaceIdDeleted`) and
  creation publications for a deleted ID are refused in both stores: a
  delayed create response that lost the race with its own deletion must
  not overwrite the tombstone or republish the record (IDs are never
  reused, so fresh-ID recreations pass the guard). The shared created
  record reconciles under the same rule as the host store's positive
  override — same-ID envelope or newer-tick request only; a stale
  different-ID envelope must not erase a recreation
  (`packages/ui/src/stores/workspace-create-pending.svelte.ts::reconcileWorkspaceCreated`).
  Controller-less detail views (focus/mobile, DetailDrawer) AND the host
  store's `effectiveRef` (both its tombstone and no-override branches)
  resolve under one rule — `resolveControllerlessWorkspaceRef`, never
  bare `envelope ?? createdWorkspaceRef`: the created record wins over a
  different-ID envelope until reconciled (a stale pre-confirmation
  envelope must not shadow, or let the dock claim over, a confirmed
  recreation), a same-ID envelope wins for its fresher status, and
  session-deleted envelope IDs are masked — globally across identities
  and past tombstone reconciliation, since IDs are never reused. E2e mocks
  of the create POST must keep the detail envelope consistent (the real
  server inserts the row before returning 202). The
  host store's `effectiveRef` falls back to that record too — a create
  begun controller-less must surface on an inline surface after a layout
  switch, where no recordCreated override ever ran
  (`frontend/src/lib/stores/workspace-host.svelte.ts::effectiveRef`).
- An explicit create-and-launch intent must be published before presentation
  liveness checks and remain reactive after a workspace is already ready; layout
  churn or branch reuse must not silently discard the command
  (`packages/ui/src/components/workspace/WorkspaceCreateSplitButton.svelte::selectTarget`,
  `packages/ui/src/stores/workspace-create-pending.svelte.ts::queueWorkspaceLaunch`,
  `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::pendingWorkspaceLaunchTarget`).
- Inline surface claims come only from live selection effects (the list
  views' claim effects, which react to recorded overrides); async responses
  record overrides and tombstones but never claim a surface themselves, and
  the hosted workspace key follows the visible surface's claim
  (`frontend/src/lib/stores/workspace-host.svelte.ts::desiredKey`), falling
  back to the sticky key only while parked — otherwise a late response could
  expose the wrong terminal beneath another surface's detail.
- Deletion invalidation must not require a live inline claim: views release
  claims on unmount, so the workspace-host store keeps workspace-id → identity
  metadata past release and tombstones by remembered identity — and deletion
  callbacks carry the provider-aware identity themselves so workspaces never
  claimed inline (tab-only, sidebar deletes) still tombstone
  (`frontend/src/lib/stores/workspace-host.svelte.ts::notifyWorkspaceDeleted`).
  Tombstones carry the deleted workspace ID and suppress only envelopes still
  carrying that ID: an envelope with a different ID is a recreation that must
  surface immediately and reconcile the tombstone away — an ID-less tombstone
  would mask it forever, because the workspace-absent envelope it waits for
  never arrives once the item has a new workspace.
- Catalog-backed routes must normalize missing selections even when the catalog
  is empty: select the first available item or `null`, and clear dependent route
  identity (`frontend/src/lib/components/docs/DocsWorkspace.svelte::loadFolders`).
- Commit user-initiated repository ref routes only after the selected tree
  loads; failed switches keep the picker query, prior ref identity, and last
  usable tree/path snapshot, remain retryable, and must not advance the URL
  to unloaded content
  (`frontend/src/lib/features/repo-browser/RepoBrowserFeature.svelte::selectRefFromPicker`).
- Treat an unresolved ref and its resolved-SHA route as equivalent only for the
  successful load that produced the alias; path/anchor changes reuse that load,
  while repository, ref, or resolved-SHA changes invalidate it
  (`frontend/src/lib/features/repo-browser/RepoBrowserFeature.svelte::loadRoute`).
- Render Kata selected detail, complete selected history, mutation ETag, and workspace
  action atomically from accepted snapshot enrichment; do not merge a prior
  action target or mutation response into a newly accepted snapshot
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::acceptedSnapshot`).
- Cross-surface Kata navigation must carry daemon, project UID, and status authority;
  UID-only sources always resolve an isolated selected-detail read for routing —
  never a shared-snapshot row, which can be stale during invalidation reloads —
  and honor an explicitly pinned daemon such as a daemon-bound Docs folder
  (`frontend/src/App.svelte::openAuxiliaryKataIssue`).
- In-workspace Kata link navigation is typed as a full-identity target
  (`uid`, `status`, `project_uid`); bare-UID peer navigation must not be
  representable, and off-authority peers route to an authority that contains
  them instead of requesting a non-member selection
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::selectLinkedIssue`).
  Catalog identity here is the accepted snapshot the user is acting on — unlike
  the shared auxiliary snapshot above — and the routing load re-resolves
  against the daemon, so a peer whose identity moved since enrichment degrades
  to the containing authority view rather than a refused stale selection.

Responsive layout changes must not change route identity.

- Resizing a canonical PR or issue route must not rewrite `/pulls/...`,
  `/pulls/.../files`, `/issues/...`, or `/host/{platform_host}/...` into
  `/focus/...` or `/m/...`.
- Responsive presentation decisions belong in the shell/rendering layer. Route
  builders still follow the active route family: canonical builders for
  canonical routes, focus builders for explicit `/focus` routes, and mobile
  builders for explicit `/m` flows.
- If a canonical list route renders with the focus presentation because the
  viewport is compact, selecting an item should still navigate to a canonical
  detail route.
- Distinguish compact desktop presentation from phone-like presentation in
  state names and tests. Compact desktop may hide sidebars or use the focus
  presentation; phone-like contexts may additionally use mobile typography,
  touch hit targets, and phone-specific action layouts.

Examples of transient state that should usually reset on identity change:

- inline edit drafts
- merge/close/reopen dialogs
- approve/review forms
- embedded detail-tab selection when the parent surface owns the item

## Persistence Scope

Persisted controls must state their scope clearly.

- Browser-local preferences belong in `localStorage` only when the behavior is
  intentionally per-browser and not worth server settings.
- URL query state belongs in the route only when deep-linking or back/forward
  navigation is part of the feature contract.
- Server-backed settings belong in the API only when the preference should
  follow the user/config rather than one browser session.
- Concurrent controls for one server-backed settings object must share a
  serialized mutation path and reconcile only fields still owned by the settling
  mutation generation; value equality alone is ABA-prone, while stale full-object
  saves can erase unrelated preferences
  (`packages/ui/src/stores/terminal-settings-persistence.ts::saveTerminalSettings`).
- A settings form that snapshots its baseline must either merge sibling mutations
  or keep the form and those controls mutually gated while either save settles
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::terminalZoomSaving`).
- An idle settings queue must rebase from authoritative store values, excluding
  fields still owned by live preview; otherwise reloads are erased or drafts leak
  into unrelated saves (`packages/ui/src/stores/terminal-settings-persistence.ts::settingsWithoutPreview`).
- Settings hydration must share the mutation coordinator; a stale read must
  preserve pending or newly confirmed fields and rebase active previews while
  retaining only generation-owned drafts
  (`packages/ui/src/stores/terminal-settings-persistence.ts::hydrateTerminalSettings`).
- Settings that select a runtime must hydrate before that runtime starts, but
  the gate must abort timed-out or superseded reads and expose retry rather than strand the surface
  (`frontend/src/lib/components/terminal/WorkspaceEmbedShell.svelte::loadTerminalSettings`).

Whenever a control persists, document and test:

- where it persists
- whether it is global, per-view, or per-item
- what happens after navigating away and back
- for layout dimensions, clamping on restore and whenever container bounds
  change, with the normalized value re-persisted so stale geometry cannot return
  (`frontend/src/lib/components/kata/KataResizableSash.svelte::clampSize`)

## Keyboard Scope Precedence

Keyboard handlers must have one clear owner for each key press.

- Input fields, textareas, contenteditable elements, and terminal surfaces own
  printable keys while focused. Global shortcuts must not reinterpret those
  keystrokes.
- Modal frames outrank page-level shortcuts. When a modal, drawer, popover, or
  command surface is active, route and list navigation should run only through
  actions explicitly registered for that active surface.
- If two surfaces can expose the same binding, document the precedence in the
  action registration rather than relying on registration order.
- Shortcut labels and cheatsheet entries must match the actual key event
  contract, including required modifiers.
- Async shortcut handlers should report failures through the same user-visible
  error path as pointer-triggered actions, and must not leave the action marked
  in-flight forever.
- Components that stay mounted while hidden (anything reparented under the
  workspace host, which parks on every page) must gate window-level listeners
  and geometry persistence on `hostVisible`: a parked view must not consume
  shortcuts on unrelated pages or clamp and persist layout measured from
  `display:none` geometry
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::toggleRightSidebar`).
  Transient popovers (and any dialog nested inside them) close when
  `hostVisible` goes false; only dialog open-state designed to restore on
  reveal may persist the hidden window
  (`frontend/src/lib/components/terminal/TerminalOptionsMenu.svelte`).
- Panes hidden behind a maximized one stay mounted with live window-level
  command listeners: a command that opens detail UI must un-maximize first so it
  cannot build an invisible overlay
  (`packages/ui/src/components/detail/PullDetail.svelte::onOpenLabelPickerCommand`).
- Focus Terminal reveals, it never maximizes: a closed workspace pane reopens
  alongside the detail and a visible one keeps its arrangement. Maximizing over
  the detail is only ever an explicit user action. Reopening also has to clear a
  zoom held by any other leaf, or the revealed pane sits behind it
  (`frontend/src/lib/stores/workspace-host.svelte.ts::focusTerminal`).
- Terminal panes call `.focus()` only once, at terminal
  creation, and only when a focus-intent guard captured at mount still holds:
  `document.activeElement` must be unchanged since mount and not "sacred"
  (inside `[role="dialog"|"menu"|"listbox"]`, or a form control/
  contenteditable). This covers the async font-load window before `.focus()`
  runs, during which something else can claim focus. Re-running the active/disabled effect on a
  reveal or an enable flip must never call `.focus()` again — that would
  fight the opt-in focus contracts above (`pendingHostFocus`,
  `shouldReclaimFocus`)
  (`frontend/src/lib/components/terminal/terminal-focus.ts`,
  `frontend/src/lib/components/terminal/XtermTerminalPane.svelte::start`).
- A maximized inline workspace reuses the live hosted shell and fills the pane
  edge-to-edge; never add outer chrome or mutate the shell's workflow/terminal
  layout state (`frontend/src/lib/components/terminal/WorkspaceHost.browser.svelte.ts`).
- History semantics for a URL-bound pane follow the ARRANGEMENT, never which
  control the user touched: while the route-bound panes share a leaf a change
  pushes, and once they are split apart — both on screen — it replaces, so
  walking between them does not fill the Back stack. Keying off "click pushes,
  focus replaces" is wrong because a pane split into its own leaf still renders a
  clickable tab header. The URL wins over stored layout state on load: it
  activates the pane it names and drops a zoom held elsewhere
  (`packages/ui/src/views/PRListView.svelte::routePanesSplitApart`).
- The stored pane tree is intent, not what is on screen: below the flatten width
  one pane renders however the tree is split, hidden panes stay in the tree, and
  a zoom covers every other leaf. Anything acting on the arrangement — palette
  split/zoom/close commands, the push-vs-replace history rule — reads the
  renderer's report and is unavailable until the host has been measured. The
  report distinguishes EDITABLE tabs (rendered, a legitimate command target even
  behind a sibling tab) from ON-SCREEN tabs (one per rendered leaf); only the
  latter answers "are both route panes visible at once"
  (`packages/ui/src/stores/paneLayout.svelte.ts::PaneRenderReport`).
- Tab drag scopes are namespaced `<kind>:<id>` and matched by string equality, so
  an un-namespaced scope silently lets two unrelated trees exchange tabs. The
  primitive that moves tabs rejects one rather than trusting call sites
  (`packages/ui/src/components/shared/tabbed-panel-drag.ts::assertNamespacedDragScope`).
- Pane availability must be derived at render time, not read back from an effect's
  result: a claim made in an effect lags one tick, and one tick of an unavailable
  pane prunes it out, collapses a split into a bare leaf, and remounts the whole
  subtree — losing scroll state and reparenting the live terminal
  (`packages/ui/src/item-workspace-claim.svelte.ts::useItemWorkspaceClaim`).
  For the same reason the release-on-teardown guard reads its controller
  untracked: reactive reads re-run the effect on a mere prop reassignment and its
  cleanup then clobbers the claim just made in the same flush.
- Rendered visibility is not dock mode. A workspace pane that is neither hidden
  nor maximized still renders nothing when it is tabbed behind a sibling or
  buried under another leaf's zoom, and its portal slot is unmounted in both
  cases — so revealing it means unhide, activate its tab, AND clear a zoom held
  by another leaf, while leaving its own zoom untouched
  (`frontend/src/lib/stores/workspace-host.svelte.ts::workspacePaneVisible`).
- The hosted terminal is one live DOM subtree reparented between registered
  portal slots, so exactly one slot may be mounted at a time. A host that
  embeds a view owning its own workspace pane must not also wrap it in a
  second slot: registration order alone decides which one gets the terminal,
  and the loser renders empty
  (`frontend/src/lib/components/terminal/WorkspaceHost.svelte`).
- Session terminals are one live subtree PER SESSION KEY, owned by the app-level
  pool: every container — workflow tabs, the terminal dock, a promoted detail
  pane — renders a `SessionTerminalSlot` and none renders a `TerminalPane` of its
  own, or one tmux session gets two sockets. A container mounts a session into
  the pool only while it actually renders it, since a parked terminal keeps its
  websocket (`frontend/src/lib/stores/session-host.svelte.ts`).
- A promoted session is recorded ONCE, in the detail surface's stored pane tree.
  Containers mask it out of what they render (derived, not an effect) and never
  prune their own stored trees, so demoting restores the tab order, split, and
  group the user chose. The pane body crosses the `packages/ui` boundary as an
  `InlineWorkspaceController` snippet: views get `{paneKey, label}` and pass
  their own `visible` back, and the generation-carrying registry key stays in
  `frontend/` (`frontend/src/lib/stores/workspace-host.svelte.ts`).
- A workspace pane holding exactly ONE session renders no chrome of its own: no
  header bar, no one-tab workflow strip. The pane's own tab takes that session's name
  (supplied through `InlineWorkspaceController`, since only the frontend knows the
  sessions), and its reopen strip follows. Two sessions, none, or a promoted sole
  session bring the chrome and the "Workspace" label back. A flattened surface keeps
  the chrome: it suppresses per-leaf strips, so the toolbar is the only thing left to
  carry the controls
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::soleEmbeddedSession`).
- A leaf holding ONLY the workspace pane renders no outer tab strip at all
  (`TabbedPanelTree`'s `soloChromeTabKeys`, wired in `DetailPaneLayout`): the pane
  draws its own strip inside, and an outer row saying "Workspace" named the same
  thing twice. Its strip contents float top-right of the leaf instead - a grip (the
  tab's replacement as HTML5 drag source; without it a strip-less pane could never
  be moved), the hide X, caller extras, and Maximize. A second tab in the leaf, or a
  flattened surface, brings the strip back. The floating cluster must stack ABOVE
  xterm's internal layers (its overlay scrollbar slider is z-index 11 and hugs the
  same right edge; nothing between the leaf and xterm's internals is a stacking
  context) - below that, the scrollbar silently swallows clicks on the rightmost
  button while everything looks fine, and only Playwright's hit-target check
  ("intercepts pointer events") names the interceptor.
- The bottom dock is NOT part of that chrome and stays, collapsed to its row, in a
  chrome-free pane: it is the only route to a second session, so dropping it made a
  one-session workspace a dead end. The one exception is a sole session that lives in
  the dock itself -- the stage is already showing it, and a dock underneath would aim
  a second slot at the same terminal host.
- A detail pane NEVER shows the workspace's own header bar (name, branch, Expand and
  Collapse Terminal, Delete). The pane's tab strip already names the workspace and
  carries its controls. A flattened surface keeps the chrome, since it has no
  per-leaf strip to carry any of it
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`).
- A pane's tab strip carries ONE structural control, Maximize. Split right and Split
  down were removed: a single-tab leaf cannot split, so on the panes that most need
  it they were permanently greyed, and elsewhere they duplicated the two routes that
  remain -- dragging a tab to a pane edge, and the `pane.splitRight`/`pane.splitDown`
  palette commands. Tests split through the palette or the layout store, never a
  button.
- Deleting a workspace is a strip action, not a popover one: it is registered as
  `HostedWorkspaceControls.stripActions` and renders beside the controls trigger. It
  lives in exactly one place -- two Deletes with independent disabled and pending
  states is worse than one behind a menu.
- The dock modes (Expand Terminal / Show Details / Collapse Terminal) move into the
  pane's controls popover wherever the header bar is hidden, gated on exactly the
  complement of the header's own condition so neither state shows two copies. The
  leaf's close button is NOT a substitute for Collapse: it hides one pane, while
  collapse reaches the container and every session promoted out of it.
- A session the workflow tree is SHOWING mounts its terminal without a click: one per
  rendered leaf's active tab. Mounting only from the tab strip's select handler left
  every workspace opened with an agent already running showing an empty pane, which
  reads as broken rather than as one click away.
- Workflow presets are a standalone-Workspaces-tab surface only. A PR or issue pane
  hosts one workspace beside the thing being reviewed, so composing multi-session
  layouts there is chrome that pane was never asked for.
- Drag state is cleared by a drag-END broadcast, not only by the dragged element's
  own `dragend`: a drop that moves a tab into another leaf destroys that element
  first, so the strip it left keeps the gap and the dragging styling. The strip that
  accepted the drop adopts the dragged key to preview an insertion, so "this leaf no
  longer holds it" cannot tell a leftover from a live preview
  (`packages/ui/src/components/shared/tabbed-panel-drag.ts::onTabbedPanelDragEnd`,
  `frontend/src/lib/components/terminal/terminal-drag.ts::onTerminalDragEnd`).
- The pane controls popover is portalled to `<body>`. The leaf's action container is
  a stacking context (`position: relative; z-index: 2`), so a popover parented inside
  it is clamped under xterm's canvas layers, which compete one level up - every click
  lands on the terminal instead
  (`frontend/src/lib/components/terminal/WorkspacePaneControls.svelte::portalToBody`).
- A portalled popover whose own actions open modals sits BELOW the modal layer
  (`calc(var(--z-overlay) - 1)`), not level with it. Portalling puts it after every
  in-tree modal in document order, so an equal z-index paints it over the dialog it
  just opened.
- A renderer publishing what it shows must not clear that report from the
  publishing effect's cleanup. Cleanup runs before every re-run, so a consumer that
  feeds the report back into the renderer's inputs (the pane tab named from it) sees
  a null on each republish, changes the inputs, and the effect never settles. Clear
  it from a separate dependency-free effect, which only runs at unmount
  (`packages/ui/src/components/shared/DetailPaneLayout.svelte`).
- A slot key computed from a session that can disappear is derived, and nullable.
  Child props are their own deriveds: they re-run on the flush that clears the
  session, before the `{#if}` guarding them is torn down, and a throw there aborts
  the app's whole render - the host stayed parked and its tab came up empty. Only the
  real-backend lane reproduces it; jsdom's flush ordering does not
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::soleEmbeddedSessionHostKey`).
- One embedded workspace view serves every selection on its surface, so anything
  it hands to a detail pane - the controls snippet, a mid-save busy flag, the
  launcher overlay's open state - is keyed by `(workspaceId, hostKey)`. An unkeyed
  flag survives the switch and acts on the next workspace: a popover pinned open by
  a write that will never report done, or a launcher covering a live terminal. Keyed
  by WORKSPACE WORK only: terminal font size and terminal options write app
  settings through one single-flight controller, so those are in flight for every
  workspace at once and keying them reports a control enabled that the controller
  is still refusing. Workspace-scoped writes are tracked as a SET of owners, since
  two workspaces can have one in flight at once
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`).
- Promoting a session into a pane requires the workspace pane to be ON SCREEN, and
  that is enforced in `promoteSessionBesideWorkspace`, not per caller: holding a
  leaf in the stored tree says nothing about being visible, and the view keeps
  publishing its sessions from a parked host
  (`packages/ui/src/stores/paneLayout.svelte.ts`).
- The inline dock mode covers EVERY pane of the hosted workspace - the container
  plus the sessions promoted out of it - so a container hidden while a promoted
  terminal is on screen is "split", not "collapsed", and collapsing hides exactly
  the panes it later restores. Which pane an expand or a Focus Terminal acts on is
  the workspace's last-focused one, recorded per WORKSPACE (views forward every
  focused pane; the host keeps the ones whose key names a workspace) so it survives
  promotion, demotion, and a visit to another item
  (`frontend/src/lib/stores/workspace-host.svelte.ts`).
- A promoted session pane is dropped from a surface's stored tree only on an
  authoritative deletion of its workspace. A stopped, exited, or reconnecting
  session is absent from the runtime in exactly the same way, and keeping the
  placement is what lets a relaunch reappear where the user put it. There is no
  session-deletion signal to react to either: the API's only session mutation is
  `stop-host-runtime-session`, a DELETE that means stop.
- Getting back to a collapsed dock restores the whole collapsed set, not the
  remembered pane alone: a container masks the sessions its workspace promoted, so
  revealing it by itself hands back an empty pane while the terminal the user asked
  for stays hidden. A ledger on record is what says a workspace is put away - not
  the derived dock mode, since the container tab is shared and another workspace's
  expand unhides it (`frontend/src/lib/stores/workspace-host.svelte.ts::restoreCollapsedPanes`).
- A deferred session focus is cancelled when its session unmounts and when the
  surface's claim changes. Left armed it waits for anything to mount under that key
  and steals the keyboard for a Focus Terminal pressed in a workspace the user has
  since left (`frontend/src/lib/stores/session-host.svelte.ts`).
- A pane tree node that leaves the on-screen tree takes its children with it, and
  those children read the removed node's `first`/`second` for the rest of that
  flush - `undefined`, while still mounted, including from a `ResizeObserver`
  batch. Every read of that prop is guarded: throwing there unmounts the whole
  surface, leaving a detail with no panes and no way back
  (`packages/ui/src/components/shared/TabbedPanelTree.svelte`).
- The desktop `.app-main` clips overflow but must never become a scroll
  container; focus-driven scrolling there shifts every mode rail and creates
  matching chrome gaps (`frontend/src/App.svelte::.app-main`).
- The inline dock mode is DERIVED from the surface's pane layout — maximized is
  the workspace pane's leaf holding the zoom, collapsed is that pane hidden —
  never stored alongside it, or the two disagree when a leaf's own controls
  maximize a pane. A maximized workspace must not outlive its claim: the store
  un-zooms both when a claim is replaced by a different identity (`setClaim`)
  and synchronously on release (`clearClaim`), because a release-and-reclaim
  within one update gives the layout host no availability gap to notice and
  leaves setClaim no previous claim to compare against. Same-identity
  re-asserts (a ref status change) must NOT un-zoom
  (`frontend/src/lib/stores/workspace-host.svelte.ts::dockModeFor`).
- A collapse control must be reachable in every inline workspace state, not only
  from the ready toolbar: the creating, fetch-failure, and setup-error branches
  render their own collapse button, since the pane's own close control is the
  only other way out short of deleting the workspace
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::inlineCollapseControl`).
  Dock mode changes are pure local UI — never disable them behind mutation
  guards like `actionsBlocked`; only the modal-stack guard applies, and only
  to the expand direction.
- The workspace view renders by liveness, not cached presence: the previous
  workspace stays cached across an in-place A→B switch, and branching on
  `workspace` alone shows A's stale ready toolbar (with action guards
  engaged) while B is slow or failing. Gate the ready branch on
  `workspaceLive` so the loading/error states own the switch window
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::workspaceLive`).

## Modal Ownership

Any surface that blocks background interaction must own both focus and
shortcuts while it is open.

- Opening a modal-like surface should push a frame before focus moves inside
  the surface; closing it should pop only that frame.
- Close behavior must be local to the active surface. Escape should not also
  close a parent drawer or trigger route navigation unless the child declined
  the key.
- Unmounting a subtree that holds focus (dock close, claim release) must
  reclaim focus for a deliberate target after the DOM update — and only when
  focus fell to `<body>` or was still inside the closing subtree, so a
  transition triggered in the background (e.g. a selection change un-zooming a
  maximized pane) never steals focus from a control the user moved to
  (`packages/ui/src/components/shared/DetailPaneLayout.svelte::shouldReclaimFocus`).
- Background actions that are still visible should be disabled or skipped when
  their `when` predicate no longer matches the active modal state.
- Outside-click, focus-leave, and Escape close paths should converge on the same
  cleanup so stale frames, listeners, and highlighted rows are not left behind.
- Custom focus traps must cycle controls in rendered DOM order. If the trap
  builds the focusable list from a mixed selector list (`button, input, select,
...`), normalize the result by document position before wrapping Tab /
  Shift+Tab so selector-engine grouping cannot change keyboard order.

## Palette Persistence

Command palette state is browser-local unless a feature explicitly needs a
shareable URL or server-backed preference.

- Recent commands should store stable action references, not route-specific
  labels that become invalid after navigation.
- Stored recents must tolerate malformed JSON, unknown actions, and stale item
  references by pruning or ignoring bad entries without blocking the palette.
- Palette search, highlighted row, preview content, and command enablement
  should be derived from the current route context each time the palette opens.
- When palette content can overflow, keyboard navigation must scroll the
  highlighted result into view without moving focus out of the search field.

## Mobile Route Constraints

Mobile layouts may redirect between list and detail surfaces, but must preserve
the user's current item identity and deep link.

- Redirects should keep `platform_host`, owner, repo, number, and item kind
  together. Repo labels alone are ambiguous in multi-provider views.
- Desktop-only layout specs should opt out of mobile redirects explicitly so
  viewport changes do not make assertions pass against the wrong surface.
- Mobile detail routes should reset transient action state when switching items,
  the same way desktop split-detail routes do.
- Any mobile-specific back/forward behavior should be tested with direct links
  and with in-app navigation, not only from the default landing route.

## Nested Interaction Rules

Rows that contain buttons, links, or toggles need clear event ownership.

- Activating a nested control inside a clickable row must not also trigger the
  row's navigation or selection behavior.
- Escape should close drawers, split-detail panels, menus, or modals when that
  surface is currently active.
- Focus-visible states matter for controls that are visually subtle, such as tab
  close buttons or compact action affordances.
- If a component claims menu-like behavior, it must honor the keyboard and focus
  contract of that role. Otherwise, use simpler semantics honestly.
- Gate unavailable menu actions at the items when the menu remains safe to
  inspect; native-disabled triggers swallow clicks and make pending work look
  like broken UI (`frontend/src/lib/features/kata/KataDaemonSwitcher.svelte::choose`).
- Keep navigation and context switches available during supersedable reads.
  Kata snapshot loads are sequence- and intent-fenced; cross-authority changes
  clear prior authority instead of painting it under the new route
  (`frontend/src/lib/stores/kata-authority.svelte.ts::KataAuthorityStore.loadSnapshot`).
- Keep Kata's daemon-scoped project navigation stable across same-daemon
  authority changes; clearing scoped task authority must not clear shared chrome
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::sidebarCatalog`).
- Kata route callbacks require both an accepted snapshot and the current navigation
  generation; daemon switches restore the target daemon's persisted authority, never
  source-daemon scope or selection (`frontend/src/lib/features/kata/KataWorkspace.svelte::switchKataDaemon`).
- Reserve disabled navigation choices for exclusive transactions such as
  writes, initial ownership setup, or a context switch already in progress;
  background refresh counters are presentation state, not transaction locks
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::daemonSwitchLocked`).
- Treat Kata event streams as invalidation, not render replay. A matching newer
  compact frame reloads the exact current snapshot intent; task, detail,
  history, and graph authority remain in the accepted snapshot
  (`frontend/src/lib/features/kata/kataWorkspaceAuthorityController.svelte.ts::createKataWorkspaceAuthorityController`).

## Controlled Form Controls

A native form control driven from app state must not also run its own default
action, or the two fight and the control renders the inverse of its real state.

- A native `<input type="checkbox">` with `checked={state}` still toggles itself
  on a real click (the click default action). When app state owns the value,
  cancel that default action (`onclick` `preventDefault`) so the box only ever
  reflects `state`. The repo selector's tri-state checkbox
  (`frontend/src/lib/components/TreeCheckbox.svelte`) is the reference: a
  controlled custom control that suppresses the native toggle and drives
  selection from `onmousedown`, not click.
- Keep an underlying real `<input>` for accessibility and tests even when the
  visuals are custom: set `indeterminate` imperatively for the partial state, and
  expose `aria-checked` (`"mixed"` for partial) on the owning row.
- Tri-state selection that cascades to descendants must keep parent and child
  visuals consistent: a parent is checked only when all leaves are, partial when
  some are, unchecked when none are. A parent disagreeing with all its children
  is a desync bug.

## Filtering And Visibility Rules

Not every visibility control means "remove this entity entirely."

- Controls that toggle detail visibility should preserve the parent row unless
  the feature explicitly removes that category from the result set.
- When two data sources race, prefer the source that matches the user's current
  filter/scope rather than a stale but faster preview.
- Empty states should make it clear when filters, not missing data, are hiding
  results.

## Threaded Comments

Threaded comment rendering must preserve both timeline recency and reply
context.

- In reverse-chronological timelines, a thread is positioned where its newest
  visible event would have appeared.
- Inside that thread, render the main/root comment first, then threaded replies
  underneath in reverse-chronological order: newest reply, then the reply before
  that, and so on.
- Do not flatten same-`thread_id` comments into separate top-level timeline
  items when the surrounding UI is meant to show comment conversations.
- This contract should also guide future diff-comment UI: inline diff threads
  can anchor to a file/line position, but their compact timeline summaries
  should still use root-comment context plus newest-first replies.

## Inline Review Drafts

Inline diff review draft comments are local staged review state until publish.
Direct detail-form review actions must leave that staged state untouched; do not
load or publish saved draft comments (`internal/server/huma_routes.go::requestChangesPR`).
Direct approve and request-changes share one submission contract end to end —
the same head pin, the same provider-side binding, the same post-success refresh
handling. Do not give either action stronger client-side verification, staleness
checks, or revocation behavior than the other
(`internal/server/huma_routes.go::approvalReviewHeadSHA`).
PR head mutations must not share an in-flight lock. Approve, request-changes,
merge, and suggestion application keep local submission guards; only durable
head-conflict state blocks the other actions (`packages/ui/src/components/detail/PullDetail.svelte::headActionsBlocked`).
Once either provider mutation succeeds, close and clear its form before the
follow-up refresh. A refresh failure may show a warning, but must not leave the
successful mutation available for an accidental duplicate submission.
Editing a saved draft comment should change only the body and preserve the
original diff range, so the PATCH path must rebuild the range from the stored
comment rather than from whichever line is currently selected
(`packages/ui/src/stores/diff-review-draft.svelte.ts::editComment`).

An open saved-draft comment editor is also pending local state, even before its
body differs from the saved text. Review-level publish and discard must stay
unavailable until every draft comment editor is saved or canceled; otherwise the
provider mutation can submit the old saved body while the UI still shows an
unsaved edit. Track that state in the draft-review store and have both tray and
inline editors clear it on save, cancel, and unmount
(`packages/ui/src/stores/diff-review-draft.svelte.ts::hasPendingCommentEdits`,
`packages/ui/src/components/diff/DiffReviewDraftTray.svelte::publish`,
`packages/ui/src/components/diff/DiffReviewDraftInlineComment.svelte::reportEditState`).

Draft authoring and the sticky publish tray are gated by the repo operation
`review_draft`, not `submit_review`. `submit_review` gates submitted review
actions in the detail header, while Files-tab draft authoring must disappear
when `review_draft` is unavailable and show that operation's reason instead
(`packages/ui/src/components/diff/DiffFilesLayout.svelte:44`,
`frontend/tests/e2e/inline-review.spec.ts:655`).

## Optional Metadata Controls

Optional metadata must not reserve empty rows or placeholders when absent. Put
compact edit controls beside the metadata's normal display location, and keep
empty states for places where missing data itself is useful information.

Async detail mutations must be scoped to the currently visible item. Compare the
full provider route identity before opening transient UI or applying mutation
responses, and discard stale responses instead of patching another item.

- Separate Kata mutation transport from post-acknowledgement authority recovery:
  transport failure preserves drafts; acknowledgement fences mutation actions without clearing editors; only the matching unchanged draft resets after accepted snapshot and required recurrence replacement; Retry never repeats the mutation
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::runAuthorityMutation`).
- A recurrence 412 is not an acknowledged mutation: if revision reconciliation fails, keep the stale dialog and all mutations fenced until Retry refreshes both snapshot and recurrence data; Retry must never repeat the delete
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::beginRecurrenceConflictRecovery`).

## Testing Expectations

Behavior contracts should usually be tested where the user would notice the
breakage.

- Component tests for local state transitions, event propagation, and route/item
  identity helpers.
- Store tests for persistence scope and normalization logic.
- Playwright/e2e tests for navigation away/back, Escape behavior, nested button
  activation, and other multi-surface flows.
- Component tests for inert transactional surfaces must await an explicitly
  enabled control before firing events; jsdom does not enforce browser inertness
  (`frontend/src/lib/features/kata/KataWorkspace.svelte::workspaceActionsBlocked`).
- For controlled native form controls, assert behavior under a real
  `fireEvent.click`, not only `fireEvent.mouseDown`. A mousedown-only test skips
  the native default action (e.g. a checkbox's own toggle) and will pass while a
  real click desyncs the control. A real-browser visual check catches this class
  of bug when the suite is green.
- Keyboard e2e tests should cover conflicting scopes, modal frame ownership,
  async action failure, overflow scroll-into-view, and mobile redirect cases
  when those behaviors are part of the feature.

Related docs:

- [`context/ui-design-system.md`](./ui-design-system.md) for visual primitives
  and styling guidance.
- [`context/notifications-in-activity.md`](./notifications-in-activity.md) for
  notification feed rows, state, and sync behavior.
- [`context/workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md)
  for runtime-specific workspace tab and shell behavior.
