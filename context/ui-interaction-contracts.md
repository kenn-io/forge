# UI Interaction Contracts

Use this document for frontend behavior changes where the risk is not visual
style but stale identity, broken persistence, or surprising interaction
semantics.

## Browser Runtime Constraints

- Do not require secure-context-only browser APIs: remote daemon access may use plain HTTP
  (`frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte:workspaceRefreshOwner`).

## Identity And Route State

Interactive surfaces must agree on which item is selected.

- `platform_host` is part of PR and issue identity in route state, drawer state, and stale-detail guards.
- An omitted default host and the concrete default host are the same item; normalize before comparing
  or keying (`frontend/src/lib/api/provider-routes.ts::resolvedPlatformHost`).
- Canonicalize provider aliases (gh/gl/fj) before any identity comparison or cache key, including
  route-reset effects; an alias-only change is not a new item (`frontend/src/lib/workspace-inline.ts::identityEquals`).
- Workspace item identity includes the canonical item type; a PR and an issue can share a number, so
  repo+number alone must never key claims, overrides, or tombstones (`frontend/src/lib/workspace-inline.ts::canonicalItemType`).
- Use the shared named route/item reference types from `frontend/src/lib/stores/router.svelte.ts`
  instead of anonymous `{ owner, name, number }` shapes.
- Restore recent details only with a verified provider/host/repository ID; owner/name routes can be
  reused (`frontend/src/lib/stores/detail.svelte.ts::loadDetail`).
- Repository-scoped detail reads track identity values, not route-object references; unchanged feed
  refreshes retain loaded actions (`frontend/src/lib/components/detail/PullDetail.svelte::mergeSettingsIdentity`).
- Treat backend latency as normal: gate each action only on the data it requires, and verify cached
  navigation and local choices with responses held pending.

### Links

- Markdown cross-references (`#N`, `owner/repo#N`, provider item URLs on the rendering repository's
  host) stay in-app with the provider URL as untracked fallback; agent chat accepts any configured or
  public provider host (`frontend/src/lib/utils/item-reference.ts::parseProviderItemURL`).
- `item-ref` hrefs carry the base path so modifier clicks, new tabs, and copied links work; plain clicks
  route from data attributes, never the href, so the prefix applies once.
- Explicit link targets other than `_self` retain native browser navigation, including widget links.
- Untracked provider links retain native navigation, decided from the loaded catalog before cancelling
  the click; modifier, middle, and context-menu clicks also get the provider URL
  (`frontend/src/lib/utils/itemRefHandler.ts::initItemRefHandler`).
- A tracked link whose item 404s leaves for the provider URL in the current tab (a post-lookup new tab is
  popup-blocked); flash not-found only without a valid provider URL, and failure for other errors.
- A late link resolution acts only while its click is current: no router URL change since the click and
  the caller's selection unchanged (`frontend/src/lib/utils/itemRefHandler.ts::resolveItemReferenceEffect`).
- Modified terminal links, including provider item URLs, open in the client browser synchronously with
  the gesture, without a daemon lookup (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::openTerminalLink`).
- Link-opening gestures must not also reach the terminal program, which may launch a browser on the remote host.

### Transient And Cached State

- When a view changes from item A to item B, reset transient action state (inline edit drafts,
  merge/close/reopen dialogs, review forms) that could submit or render against the wrong item.
- Initial hydration and temporary reload gaps preserve navigation and pending action state; replacing a
  known repository ID resets transient actions (`frontend/src/lib/components/detail/PullDetail.svelte::lastResetPlatformRepoId`).
- Remembered merge settings keep controls visible across navigation but cannot enable merging before revalidation.
- Recent detail snapshots cannot authorize provider writes before revalidation; matching cached items allow
  local comment drafts. Settled mutations invalidate saved views so optimistic state cannot survive rollback.
- Workspace sidebars show cached details for the selected item during revalidation; a cache miss must
  hide the previous item's details (`frontend/src/lib/components/workspace/WorkspaceRightSidebar.svelte`).
- Workspace PR stack links select and remember the viewed PR within the workspace; keep the stack expanded.
- Merge confirmation closes on submission; app-owned pending state keeps the PR and linked workspace
  visibly merging across navigation, and failures use shared flashes (`frontend/src/lib/stores/detail.svelte.ts::mergePull`).
- Provider outcomes that arrive minutes later (a merge queue removing a PR) render from synced server
  state in list and detail until superseded; a flash alone is insufficient (maintainer decision).

### Workspace Lists And Bulk Actions

- Workspace lists retain their last successful snapshot (including empty) across view changes and restore
  it before revalidation, without keeping hidden sidebar polling alive.
- Confirmed deletions evict the matching host and ID before revalidation; same-ID rows on other hosts stay.
- Multi-selection never navigates and covers only on-screen rows. Bulk delete never forces, so dirty
  worktrees survive and are reported; deletes run in the background after the dialog closes.
- Row-menu quick actions run at once for one row and after confirmation for several; Fleet workspaces
  cannot take an agent handoff, so only local and devbox workspaces are offered.
- Stopping sessions always confirms, stops every listed runtime session, and keeps the workspace,
  worktree, and base terminal; an already-gone session counts as stopped
  (`frontend/src/lib/stores/workspace-sessions-stop.ts::stopWorkspaceSessions`).
- Bulk pull runs without confirmation because the server only fast-forwards clean, non-diverged
  worktrees; offer it regardless of cached behind counts, which go stale.
- PR and issue multi-selection follows the same rules; a bulk quick action reuses an item's existing
  workspace, uses the saved default target, runs in the background, and reports once every launch finishes.
- PR and issue row menus apply each action to eligible items only, gated like the detail views, and
  share the detail views' per-item mutation ordering (`frontend/src/lib/stores/item-actions.ts::actionEligibility`).
- Bulk assignees come only from the repository's synced history; assigning someone who never interacted
  with the repository is intentionally unsupported.
- Bulk label and assignee edits send additions and removals, applied server-side to the provider's live
  set under a per-item lock, never a full replacement list (`internal/server/pullapi/membership_handlers.go::livePull`).
- Bulk label additions wait until every repository label list is current and are dropped for repositories
  whose list failed to load or reports a refresh `sync_error`.
- Row menus offer no workflow (kanban) status; the maintainer judged it noise there.
- Bulk merge offers only methods every selected repository allows, pins each head, defers PRs with pending
  CI, and skips failed CI, which stays a per-PR detail decision (`frontend/src/lib/stores/item-actions.ts::actionRequest`).

### Repository Pickers

- Pickers share the app's successful catalog during refresh and failures; refreshed options keep the
  selected stable identity across renames or clear it, never select another repository.
- Remembered and explicitly seeded repositories resolve by stable identity; unresolved, route-only, or
  inactive ones leave the selection empty and require a choice, never a last-used or first-option fallback
  (`frontend/src/lib/components/terminal/NewWorkspaceDialog.svelte::defaultRepoSelection`).
- Repository catalogs use stored metadata; credential resolution and mutation availability belong to
  action/detail reads, not picker readiness (`internal/server/huma_routes.go::listRepos`).
- The server's `hidden_from_ui` flag is authoritative for every selector entry; consumers must not
  reimplement visibility matching client-side.
- Selection normalization drops hidden selections explicitly, matching both the configured path and
  `tracked_repo_path`, which diverge after a provider-side rename
  (`frontend/src/lib/utils/repo-filter-values.ts::normalizeInteractiveRepoFilterSelection`).

### Workspace Create, Delete, And Launch

- A server-confirmed create or delete publishes to identity-scoped global state (claims, tombstones,
  overrides, route memory) before any liveness guard; only presentation is gated on the live component.
- A pending create is identity-scoped shared state, not component-local, so remounts and route changes
  cannot re-enable a duplicate submission (`frontend/src/lib/stores/workspace-create-pending.svelte.ts`).
- A workspace-absent response clears a recorded creation only if its request started after the
  confirmation; a 404 on the workspace itself means another client deleted it.
- Tombstones are per workspace ID and suppress only that ID; a different ID is a recreation that surfaces
  immediately. Workspace IDs are never reused.
- A create response that lost the race with its own deletion must not overwrite the tombstone.
- A stale pre-confirmation envelope with a different workspace ID must not shadow or erase a confirmed
  recreation; a same-ID envelope wins for its fresher status.
- A create begun in a controller-less view (focus/mobile) must surface on an inline surface after a
  layout switch (`frontend/src/lib/stores/workspace-host.svelte.ts::effectiveRef`).
- E2e mocks of the create POST must keep the detail envelope consistent; the real server inserts the
  row before returning 202.
- Deletion invalidation must not require a live inline claim; deletion callbacks carry the
  provider-aware identity (`frontend/src/lib/stores/workspace-host.svelte.ts::notifyWorkspaceDeleted`).
- Deleting the workspace named by the active terminal route replaces that history entry with the
  Workspaces list; pushing a redirect lets Back rehost the dead workspace.
- Inline surface claims come only from live selection effects; async responses never claim a surface,
  or a late response could expose the wrong terminal (`frontend/src/lib/stores/workspace-host.svelte.ts::desiredKey`).
- `deleting`/`deletion_failed` remain workspace states; only `workspace_deleted` tombstones the row.
- Detail actions route `deleting`/`deletion_failed` workspaces to Workspaces recovery; inline hosts must
  not reopen their terminals.
- A `deletion_failed` workspace stays selectable so terminal recovery and confirmed force-delete remain
  reachable on local and Fleet rows (`frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte::openWorkspace`).
- A terminal view for a deleting or failed-deletion workspace blocks runtime actions and shows progress
  or force-delete recovery bound to the exact workspace and host.
- Create-and-launch intent suppresses automatic launcher rendering until the launch settles
  (`frontend/src/lib/stores/workspace-create-pending.svelte.ts::promoteWorkspaceCreateLaunch`).
- An accepted launch stays pending until the exact session appears or 15 seconds after acknowledgement;
  expiry clears only the intent and reports the session was not observed.
- A session appearing after expiry is authoritative and is never stopped or relaunched; pending launch
  reconciliation survives the initiating view's teardown.
- Quick actions keep the automatic session picker closed for that workspace for the browser session;
  opening it remains an explicit user action.
- Quick actions for one workspace and host queue in order and none are dropped; a mobile view presents
  the latest (`frontend/src/lib/stores/workspace-quick-actions.ts::launchWorkspaceQuickAction`).
- Completed handoffs activate in the requested pane unless the user moved the session; saved layout
  entries persist until authoritative runtime discovery prunes them.

### Launchers

- Quick-action lists sort by title, ignoring case, not settings order; launch-target lists sort agents
  by label, ignoring case, with shell targets after them.
- Every quick-action surface resolves the action's agent against both `agent` and `acp` targets.
- Workspace quick actions use one lightning icon beside Play, with no label or chevron.
- The session launcher groups ACP chat targets in their own section.
- Empty workspaces offer the launcher inside their terminal frame, with no page backdrop or focus trap;
  launching dismisses it.
- A surface-hosted terminal dock owns its visible launcher; the workspace view may be parked.
- Closing the last session tab reopens the launcher even after a dismissal; closing one of several does not.

### ACP Chat

- The composer never blocks while a turn runs; only a disconnected chat disables it. Busy input steers
  when supported, otherwise queues.
- Clicking empty pane space focuses the composer; text, media, controls, selection ends, and touch keep
  normal behavior. Decide touch from the click's pointer type, not the device's primary pointer.
- Thoughts and interleaved tool calls share one collapsed activity group; reasoning stays opt-in even
  while streaming.
- Message time and copy live in a hover/focus gutter that never shifts layout; narrow panes show them as
  one always-visible muted line under each message (`frontend/src/lib/components/acp/ChatMessageView.svelte`).
- Thought copy buttons appear only when expanded, beside the thought text; thought text matches tool-row size.
- The chat holds a transcript window; earlier messages page in without moving what the reader is viewing.
- Conversation, questions, notices, and composer share one reading column; turn status is one line of
  expandable chips.
- Elicitation choices are an always-visible radio/checkbox list, never a dropdown, with each option's
  description and the picked option's preview; a field's description is its question.
- An optional single choice clears by re-clicking or Space, never via a control that appears on selection;
  picking never moves the form. Submit stays enabled and reports problems; one answer locks the form until reconnect.
- Composer popovers are kit cards sized to content and anchored to the composer edge, never composer-wide.
- The model chip opens a flat kit `Typeahead` (agents list many models), so Enter picks the top match;
  ACP group names are searchable trailing text, not group rows.
- Agent content reaches the page only as text, sanitized markdown, or checked image/audio data URLs;
  only http(s) and mailto links are clickable (`frontend/src/lib/components/acp/chat-content.ts`).

### Routes For Other Modes

- Catalog-backed routes normalize missing selections even when the catalog is empty: first item or
  `null`, clearing dependent identity (`frontend/src/lib/components/docs/DocsWorkspace.svelte::loadFolders`).
- Repository ref routes commit only after the selected tree loads; failed switches keep the picker query,
  prior ref, and last tree, stay retryable, and never advance the URL to unloaded content.
- An unresolved ref and its resolved-SHA route are equivalent only for the load that produced the alias
  (`frontend/src/lib/features/repo-browser/RepoBrowserFeature.svelte::loadRoute`).
- Contextual Kata panes keep association selection separate from task detail; refresh replaces the
  selection only when it no longer exists, and a detail response applies only to its daemon and issue UID.
- Docs task references never route into Forge: they resolve through the folder's pinned daemon and open
  Kata with `noopener,noreferrer`; failures stay visible in Forge (`frontend/src/App.svelte::openKataReference`).
- Linking and unlinking change only Forge-owned associations, never the Kata task; Kata actions use the
  canonical daemon/issue identity, never display text or route numbers.

### Responsive Routes

- Resizing never rewrites canonical `/pulls/...`, `/issues/...`, or `/host/{platform_host}/...` routes
  into `/focus/...` or `/m/...`; responsive presentation belongs in the shell.
- Route builders follow the active route family; a compact canonical list still selects into canonical detail.
- Distinguish compact desktop from phone-like presentation in state names and tests; only phone-like
  contexts add mobile typography, touch targets, and phone action layouts.

## Persistence Scope

Persisted controls must state their scope clearly. Whenever a control persists, document and test
where it persists, whether it is global, per-view, or per-item, and what happens after navigating away
and back.

- Restored layout dimensions are clamped; temporary constraints and resizes below a valid minimum must
  not replace the saved preference.
- Browser-local `localStorage` preferences are only for intentionally per-browser behavior; server-backed
  settings are for preferences that follow the user/config.
- URL query state belongs in the route only when deep-linking or back/forward is part of the contract.
- Switching Forges is ordinary cross-origin navigation; never keep a fleet-global selected host, since each
  tab and origin owns its own state (`frontend/src/lib/components/layout/ForgeSelector.svelte`).

### Filters And Search

- `Involves me` and `Unassigned` are each three independent browser-local preferences (Pulls, Issues,
  Activity), sent as pre-limit server queries, never URL or config state.
- `Unassigned` matches only PR/issue subjects with an empty synchronized assignee set.
- `Referenced by PR` is a browser-local Issues preference, default off, exposed in every issue-filter
  presentation; it ORs with involvement and unassigned before limits, and reset clears it.
- `Referenced by PR` shows whenever any configured provider-host supplies reference edges; edges never
  cross provider-host boundaries, and the graph has no badge or detail UI.
- Repository preset definitions follow server settings; the active selection and preset affinity stay
  browser-local, and `Global` clears both (`frontend/src/lib/stores/filter.svelte.ts::setGlobalRepoPresetSelection`).
- Editing a preset-derived selection keeps its overwrite target; deleting the active preset keeps its
  repositories selected ad hoc. Exact-set matches prefer browser-local preset affinity.
- The repository selector may prune a Workspaces selection only after local, fleet-discovery, and peer
  loads all succeed; failures preserve it (`frontend/src/lib/stores/workspace-repo-catalog.svelte.ts`).
- Pulls, Issues, Activity, and Workspaces search share one grammar; the Go and TypeScript parsers must
  stay in step (`internal/db/search_query.go::ParseSearchQuery`). Those boxes use `QuerySearchInput`.
- Activity excludes parents by their own fields; only included terms may match through child events.
- Zero-padded PR searches (`0001`) match the exact number before pagination.
- Activity filter defaults live in Settings and `config.toml`; URL overrides are session-scoped, and a
  partial Activity URL inherits the last validated route's missing params.
- Explicit Activity choices survive refresh, Settings navigation, and late hydration; omit filter
  overrides only against known server defaults (`frontend/src/lib/stores/activity.svelte.ts::syncToURL`).
- Activity URLs persist `item_types` and `event_types` independently; legacy `types` still migrates both,
  exact `types=notification` means default item scope with no events, and `commit` alone sets Commits.
- Activity's Author filter follows GitHub `author:` semantics (parent author, never a child actor); its
  URL/API key stays `author`.
- Activity author candidates follow only repository and time-range scope; search, type, and the selected
  author never shrink them, and refreshes keep loaded options selectable.
- The author scope key deduplicates reads but is not freshness authority; reconciliation and refetches
  revalidate it. Rank authors from each parent's latest activity, not every event.
- A foreground Activity load replaces, rather than joins, a same-scope author read owned by reconciliation.
- Workspaces matches PR/issue authors case-insensitively; unlinked workspaces match only Anyone. Author
  choices follow repository scope and keep a selected label whose author leaves the set.
- Activity compact mode means a detail is active, not a narrow feed pane.
- Activity and Workspaces share one viewport-positioned filter panel that owns focus while open; keep
  inline-size containment for compact control sizing (`frontend/src/lib/components/shared/ItemFilters.svelte::portalToBody`).
- Arrow/Home/End radio navigation selects within the panel without the pointer-only close-on-select.

### Workspace Details And Targets

- Workspace pins are browser-local and host-aware, shown above the list without duplicates, and follow
  the repository filter and search.
- Agent status visibility uses Forge config across browsers; PR, Issue, and Activity labels sit on the right.
- The workspace details tab is keyed by host-aware workspace identity; a fallback may apply only to the
  current live workspace.
- Unlinked workspace details stay closed by default; only an explicit Diff selection opens them.
- Visible workspace PR details consume provider updates independently of diff watching and airplane mode.
- Workspace item search remembers separate PR and issue choices per workspace and host, and must never
  change workspace ownership, branch associations, or the diff merge target.
- Search selections and in-workspace links accumulate server-persisted targets; a deliberate revisit
  restores a removed target. Visits and removals apply in action order, last wins.
- Route-only target visits resolve current identity outside picker catalogs, so hidden repositories and
  reused routes stay valid (`frontend/src/lib/api/workspace-targets.ts::WorkspaceTargetMutationsLive`).
- Only the latest item-selection action may change the sidebar; superseded visits still persist and
  report save failures.
- Closed and merged targets stay saved but hidden by default; unavailable targets remain visible.
- Target dismissal must not depend on forwarded workspace events.
- The current target is marked `aria-current` by repository identity, not number alone; Kata follows the
  panel's reported selection. Remove buttons show on hover or focus and stay visible without hover;
  the open state stays in the accessible name.
- PR and Issue tabs require a linked item or a remembered choice; search stays available without either.
- Workspace item search spans all repositories, defaults to open items, opens on demand from the
  toolbar, and uses the shared preloaded list; Include closed is opt-in per popover.
- Item references opened within Workspaces stay in the workspace and show the matching panel.

### Activity Navigation And Freshness

- Activity detail uses one URL-backed slot for PRs, issues, and default-branch commits.
- Links opened from Activity stay in Activity and enable the matching item-type filter.
- Link navigation reveals the selected row once; later refreshes preserve scroll position.
- Loaded discussion stays visible across incomplete refreshes only for the same stable repository and PR,
  separately from the detail completeness CI uses.
- Refreshing unchanged discussion preserves highlighted markdown and open disclosures.
- Once visited, Activity stays live across workspace navigation with relay refreshes and a one-minute
  visible-document polling backstop; remounting must not force a full reload.
- Restored Activity selections recover verified repository identity from feed rows before loading
  details; unknown identity must not reuse a mutable route's cache.
- Activity agent badges poll workspace reads every five seconds without reloading the feed, because agent
  hooks do not emit workspace status events.
- Activity project changes may preview a cached matching snapshot, but always revalidate cursor and paging.

### Workflow Actions

- Actions is standalone-only and inert until settings hydrate; disabling it replaces `/actions` with Activity.
- Actions requires workflow catalog, run, and dispatch capabilities; unsupported repositories stay
  visible without reads.
- Actions read failures never collapse into empty or current data; retained data shows a stale alert.
- Run reads belong to the selected workflow; job reads and results belong to the run and survive
  changing the viewed workflow.
- Jobs load lazily once per expanded run; collapsing keeps them.
- A dispatched run appears from the dispatch response or first progress event; the list otherwise
  refreshes only by user action.
- Tracking expiry is not workflow failure: keep the last known run and report that tracking stopped.
- PR Actions defaults open same-repository pulls to the head branch, forks and non-open pulls to the
  target; workflows remain on merged pulls.
- PR workflow drafts must match the selected PR; navigation clears drafts without cancelling accepted dispatches.

### Settings

- ACP fonts and terminal appearance and retention live under Workspaces settings; ACP font preferences
  apply to all harnesses and persist separately from terminal preferences.
- Workspace font controls keep the selected session when the toolbar takes focus; app chrome never resizes.
- Settings controls persist on change: no Save button, dirty draft, or saving state that disables sibling
  controls; saves queue serially from the latest persisted values
  (`frontend/src/lib/components/settings/DetailSettings.svelte::persist`).
- Bounded numeric settings take limits from the generated `schema-constraints` module, never hand-copied
  numbers, and show an inline invalid state instead of sending a rejected request.
- A save control disabled by invalid input names each blocking item, including filtered ones; never
  leave a silent disabled button.
- Workspace machines are one list with one default radio; Online/Offline comes from the fleet snapshot,
  not a saved connection (`frontend/src/lib/components/settings/DevboxSettings.svelte::status`).
- An offline machine row offers Reconnect; the answering registry shows as text, with the address form
  hidden until discovery fails or the user chooses to change it.
- Concurrent controls for one settings object share a serialized mutation path and reconcile only fields
  owned by the settling generation; value equality is ABA-prone and stale full-object saves erase data.
- A control rendered in a neighboring panel need not join that panel's persistence object.
- Settings with irreversible local effects (lowering terminal retention) publish only after persistence succeeds.
- A settings form that snapshots its baseline must merge sibling mutations or mutually gate with them.
- Settings forms render and build mutations from the reactive authoritative store; idle queues and
  hydration rebase from it, excluding fields still owned by live preview or pending saves.
- Startup callers share the last successful settings snapshot; every accepted settings command
  invalidates it, and a settings failure forces fresh readiness and settings on the next start.
- Waiting for backend readiness is not part of the startup settings-request timeout
  (`frontend/src/lib/app/startup-workflow.ts::StartupWorkflowLive`).

### Timelines

- Rebasing preserves the stored event actor and time; show the original author and label a distinct
  committer. Activity's Hide bots follows the stored actor, not commit display identity.
- Timeline diff retry suppression must permit a new load after another pane clears the shared diff store.
- Detail timelines apply the server-backed entry limit after filtering and grouping and make the remainder
  explicit; a full-timeline request survives edits and resets only on item or scope change.

## Keyboard Scope Precedence

Keyboard handlers must have one clear owner for each key press.

- Focused inputs, textareas, and contenteditable elements own printable keys; modified bindings still dispatch.
- A focused terminal owns every key, modified ones included, and outranks the modal stack; only
  `Ctrl/Cmd-Shift-K` (command palette) crosses, otherwise no handler runs and nothing is prevented.
- Match terminal ownership from the terminal surface, not only xterm's hidden textarea, because focus also
  rests on the session wrapper (`frontend/src/lib/utils/keyboardShortcuts.ts::isTerminalKeyboardTarget`).
- Popovers close on Escape from their own window listeners, never the shortcut registry, which sees no
  keys while a terminal holds focus (`frontend/src/lib/stores/keyboard/dispatch.svelte.ts::dispatchKeydown`).
- xterm advertises the Kitty keyboard protocol so terminal apps can negotiate detailed key reports.
- Browser paste shortcuts stay browser-owned inside xterm; on macOS, Ctrl+V is intercepted only for
  image-only clipboard data, otherwise `\x16` is replayed to the terminal.
- Forge-owned paste matches xterm: LF and CRLF become one carriage return; a lone CR stays unchanged.
- Plain-HTTP terminals keep copy working through a gesture-authorized fallback; recheck revocation between
  async clipboard stages so a stale write cannot land.
- On insecure origins, right-button mouse events stay out of tmux so the browser context menu works.
- Modal frames outrank page shortcuts; while a modal, drawer, popover, or command surface is active, only
  actions registered for that surface run.
- Kit's expanded media viewer opens outside the modal stack; every player with `onViewerOpen` must push
  a `media-viewer` frame (`frontend/src/lib/app/mount.ts::openMediaViewerFrame`).
- Registry actions also back the command palette; remove only an accelerator with `binding: null`.
- When two surfaces expose one binding, document precedence in the registration, not registration order.
- Shortcut labels and cheatsheet entries must match the actual key contract, including modifiers.
- Async shortcut handlers report failures like pointer actions and never stay marked in-flight.

### Hidden And Parked Surfaces

- Components that stay mounted while hidden gate window listeners and geometry persistence on
  `hostVisible`; transient popovers close when it goes false.
- Panes hidden behind a maximized pane keep live command listeners; a command that opens detail UI
  un-maximizes first.
- The desktop `.app-main` clips overflow but must never become a scroll container.

### Pane Focus Ownership

- A pane is active exactly while DOM focus is inside it; only it consumes pane-scoped keys. Pointer
  changes the active pane only when the browser moves focus; wheel never does.
- The active-pane marker is a subtle inset painted in the pane below descendants, in a reserved 1px
  padding; a high-z overlay cuts through popovers in descendant stacking contexts.
- A dedicated Files route keeps global diff shortcuts only while no pane or dock has live focus.
- Focus callbacks carry the exact rendered leaf and tab ID, including an inactive focused tab; never
  infer it from the persisted tree, since narrow layouts render one synthetic leaf.
- Ownership follows a focused tab that moves between leaves; nested tab identity never bubbles outward.
- When focused content disappears without `focusout`, release its ownership and reclaim focus only for
  disconnected content or focus on `document.body`; parking never reclaims focus.
- Nested pane trees paint focus only on the deepest focused pane; workspace Workflow, Details, and
  bottom Terminal regions are siblings.
- Standalone Workspace shortcuts accept unclaimed `document.body` focus but not a focus owner outside the root.
- A focused surface-hosted dock is the live focused surface; persisted last-focused state is restoration
  memory only. Resetting pane arrangement preserves a live dock claim.
- Activity commit diffs gate global shortcuts on live pane input, not pane visibility.

### Terminal Focus

- Focus Terminal reveals, never maximizes; it reopens a closed pane beside the detail and clears another
  leaf's zoom. Maximizing is only ever an explicit user action.
- Terminal renderers autofocus only once, at creation, under a focus-intent guard; reveal or enable
  effects never focus (`frontend/src/lib/components/terminal/terminal-focus.ts`).
- Opening the dock from its header toggle is an explicit focus request for its active session.
- Active terminals focus xterm synchronously on touch or pen pointerdown; xterm's own focus path is
  mouse-only, so a delayed handoff leaves the software keyboard closed.
- A deferred session focus is cancelled on session unmount and on claim change.
- Pooled terminals must not sample `activeElement` at park time (slot teardown fires no `focusout`);
  restore focus only into unclaimed focus and after `inert` removal commits.

### Pane Layout

- History for URL-bound panes follows the arrangement: sharing a leaf pushes, split apart replaces. On
  load the URL wins over stored layout (`frontend/src/lib/views/PRListView.svelte::routePanesSplitApart`).
- Route authority over pane layout is a transition, not an invariant; re-asserting it on every change
  undoes Expand Terminal and Maximize. Wait for the conversation to be visible before a comment jump.
- Desktop PRs with a workspace keep their saved arrangement at narrow widths.
- Agent launch visibility, session tabs, and dragging must not change with viewport width.
- The stored pane tree is intent, not what is on screen; arrangement commands read the renderer's report
  (editable vs on-screen tabs) and are unavailable until measured (`frontend/src/lib/stores/paneLayout.svelte.ts::PaneRenderReport`).
- Tab drag scopes are namespaced `<kind>:<id>`; an un-namespaced scope lets unrelated trees exchange tabs.
- Drag state is cleared by a drag-end broadcast, not the element's own `dragend`, because a drop can
  destroy the dragged element; the broadcast also clears nested trees' drop previews.
- Derive pane availability at render time, not in an effect; one tick of unavailability prunes the
  pane and remounts the live terminal (`frontend/src/lib/item-workspace-claim.svelte.ts::useItemWorkspaceClaim`).
- A renderer must not clear its published report from the publishing effect's cleanup; consumers feeding
  it back would loop. Clear it from a dependency-free effect.
- A removed pane-tree node's children can read `undefined` props for the rest of the flush; guard every
  read, since a throw unmounts the whole surface (`frontend/src/lib/components/shared/TabbedPanelTree.svelte`).
- Slot keys derived from a session that can disappear are nullable; child props re-run before the `{#if}`
  tears down, and only the real-backend lane reproduces the crash.
- A pane's tab strip carries one structural control, Maximize; splits use tab drag or the palette, and
  tests split through the palette or layout store.
- The pane controls popover is portalled to `<body>`, or xterm canvas layers swallow its clicks; a
  portalled popover that opens modals sits just below the modal layer.
- Buttons sharing a row with a kit `IconButton --sm` share its 24x24 box and 13px glyph.

### Hosted Workspace Panes

- The hosted terminal is one live DOM subtree; exactly one portal slot may be mounted, so a host must
  not wrap an embedded view that owns its own workspace pane in a second slot.
- Session terminals are one subtree per session key owned by the app pool; no container renders its own
  `TerminalPane`, or one tmux session gets two sockets (`frontend/src/lib/stores/session-host.svelte.ts::isSessionClaimed`).
- Sessions claimed by a live view stay claimed behind hidden tabs; only sessions released everywhere
  enter the bounded retention cache. Pooled terminals construct immediately, even parked.
- xterm WebGL texture atlases are shared; every explicit clear must refresh sibling renderers.
- A slot's `visible` means painted, never focused; invisible slots are `inert`, so every split leaf shares
  the container's visibility (`frontend/src/lib/components/terminal/TerminalSplitTree.svelte`).
- Rendered visibility is not dock mode: revealing a workspace pane means unhide, activate its tab, and
  clear another leaf's zoom while leaving its own (`frontend/src/lib/stores/workspace-host.svelte.ts::workspacePaneVisible`).
- The inline dock mode is derived from pane layout, never stored, and covers the container plus every
  session promoted out of it (`frontend/src/lib/stores/workspace-host.svelte.ts::dockModeFor`).
- A maximized workspace must not outlive its claim; a different-identity claim or a release un-zooms it,
  while a same-identity re-assert must not.
- Expand and Focus Terminal act on the workspace's last-focused pane, recorded per workspace so it
  survives promotion, demotion, and visiting other items.
- Restoring a collapsed dock restores the whole collapsed set, tracked by a ledger rather than the
  derived dock mode (`frontend/src/lib/stores/workspace-host.svelte.ts::restoreCollapsedPanes`).
- A collapse control is reachable in every inline state, including creating and error states. Dock mode
  changes are local UI: never block them on mutation guards; only expand respects the modal stack.
- A maximized inline workspace reuses the live shell edge-to-edge without outer chrome or layout mutation.
- A promoted session is recorded once in the surface's pane tree; containers mask it at render time and
  never prune their own trees, so demotion restores the user's arrangement.
- Promoting a session requires the workspace pane to be on screen, enforced in one place
  (`frontend/src/lib/stores/paneLayout.svelte.ts::promoteSessionBesideWorkspace`).
- A promoted pane is dropped only on authoritative workspace deletion; stopped or reconnecting sessions
  keep their placement, and the API has no session-deletion signal.
- The workspace container's tab disappears once every session is promoted; the workspace stays claimed.
- An empty workflow container retires behind its bottom dock instead of resizing the tree.
- A workspace pane with exactly one session renders no chrome and takes the session's name; more or no
  sessions, a promoted sole session, or a flattened surface bring the chrome back.
- A sole workflow session in a solo-chrome leaf keeps its inner one-tab strip; a sole docked session stays bare.
- A leaf holding only the workspace pane renders no outer tab strip or drag source; its actions float
  top-right above xterm's z-index 11 overlay scrollbar, which otherwise swallows the rightmost click.
- The bottom dock stays (collapsed) in a chrome-free pane as the route to a second session, unless the
  sole session lives in the dock.
- A detail pane never shows the workspace header bar; a flattened surface keeps it. Dock modes move into
  the controls popover exactly where the header is hidden; a leaf close is not Collapse.
- A session the workflow tree is showing mounts its terminal without a click.
- Workflow presets are only a standalone Workspaces surface, never in PR or issue panes.
- Workspace metadata lives behind the info button, never a Home tab; info preserves the selected terminal.
- Workspace-wide controls sit beside the title; compact Launch controls sit at each pane's top-right and
  add a tab to the initiating pane even if focus moves.
- Every workspace pane keeps non-destructive actions such as launch; destructive actions only on the
  workspace-owner leaf.
- Delete lives in exactly one strip action on the leaf holding the workspace pane; every Delete entry
  opens the same confirmation before any request, and a 409 force prompt is a second gate.
- State handed from the embedded view to a detail pane is keyed by `(workspaceId, hostKey)`; app-wide
  terminal settings writes are not keyed per workspace.
- The workspace view renders by liveness, not cached presence, so A's ready toolbar never shows while B
  loads (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::workspaceLive`).
- Push identity is supporting metadata, not a banner; describe the last pushed commit, not the current
  login. A refresh that drops it closes the dialog in that same update and never reopens it.

### Terminal Sizing

- Terminal size and resize authority require painted state plus a valid fit measurement, never focus;
  a parked container measures nothing, which keeps it from resizing tmux to one row.
- Every pooled slot fills its painted leaf in both axes, or xterm keeps a stale height that never reaches the PTY.
- Record a size as sent only once the socket carried it; send xterm's dimensions after `fit()`
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::resizeVisibleTerminal`).
- Passive lifecycle or geometry changes never transfer resize ownership; the latest deliberate action
  wins within a priority, and a local attachment may preempt an HTTP Fleet one.
- Only trusted gestures (including mouse-tracking input, IME composition, and non-zero divider drags)
  claim resize ownership; protocol replies and passive reflow do not.
- Fleet streams close rather than forward input when tmux resize settlement fails.
- Terminal input stays immediate during reconnect replay; resize readiness never queues keystrokes.

## Modal Ownership

Any surface that blocks background interaction must own both focus and shortcuts while it is open.

- Push the modal frame before focus moves inside; closing pops only that frame.
- Close behavior is local: Escape must not also close a parent or navigate unless the child declined it.
- Unmounting a focused subtree reclaims focus for a deliberate target only when focus fell to `<body>` or
  stayed inside it (`frontend/src/lib/components/shared/DetailPaneLayout.svelte::shouldReclaimFocus`).
- Visible background actions are disabled or skipped when their `when` predicate no longer matches.
- Outside-click, focus-leave, and Escape close paths converge on the same cleanup.
- Custom focus traps cycle in rendered DOM order; sort mixed-selector results by document position.

## Palette Persistence

Command palette state is browser-local unless a feature explicitly needs a URL or server preference.

- Recent commands store stable action references, not route-specific labels.
- Stored recents tolerate malformed JSON, unknown actions, and stale references without blocking the palette.
- Palette search, highlight, preview, and enablement derive from the current route each time it opens.
- Keyboard navigation scrolls the highlighted result into view without moving focus from the search field.

## Mobile Route Constraints

Mobile layouts may redirect between list and detail surfaces, but must preserve item identity and deep links.

- Redirects keep `platform_host`, owner, repo, number, and item kind together.
- Desktop-only layout specs opt out of mobile redirects explicitly.
- Mobile detail routes reset transient action state when switching items, like desktop.
- Test mobile back/forward from direct links and in-app navigation.

## Nested Interaction Rules

- Diff file navigation and collapse/expand-all are direct header and toolbar actions; arrows follow the
  filtered order and bulk collapse affects only visible files.
- Commit diffs share one collapse key scoped by provider, host, repository path, and commit SHA.
- Activating a nested control inside a clickable row must not trigger the row.
- Escape closes the currently active drawer, split-detail panel, menu, or modal.
- Subtle controls such as tab close buttons need focus-visible states.
- Searchable dropdowns accept typing as soon as Tab focuses them.
- Clipboard controls copy provider/repository values exactly; copying is not a shell security boundary,
  so do not filter, quote, escape, or confirm.
- A component claiming menu-like behavior honors that role's keyboard contract, or uses simpler semantics.
- Kata association selection stays available while detail refreshes; background freshness must not
  block link selection or Forge-owned actions.
- Local Roborev reviews belong to the PR workspace panel: read on display or explicit action, never
  poll globally or refresh hidden panels.
- Resolve local reviews from the workspace's actual path; unresolved paths require an explicit choice, and
  remote workspaces cannot use the viewing node's Roborev authority.
- Teardown of a catalog consumer cancels only that consumer's reads, never review runs or siblings.

### Application-Owned Commands

- A lost Roborev mutation response fences its target until observed and never replays the write; a
  confirmed POST stays acknowledged when its refresh fails.
- Docs publish, repository issue creation, project registration/clone, and workspace runtime commands
  remain application-owned after acceptance; returning to them must not duplicate the command.
- Completed Docs publish success is never replayed into a later session; refreshes must not reopen
  dismissed issue-creation state.
- Obsolete Docs route reads cancel without cancelling another owner's accepted reconciliation.
- When a write may have committed before failing, recovered success requires every requested field to
  match; contradictory state keeps the failure and an inconclusive read fences resubmission.
- Uncertainty fences live for one browser application runtime; a reload clears them, so the user must
  verify fresh state before retrying (`frontend/src/lib/app/runtime.ts::makeAppRuntime`).
- Workspace runtime commands keep presentation by `(hostKey, workspaceId)`; an abandoned visit's failures
  never surface in its replacement.
- A runtime transport failure fences retry until authority proves the outcome; presets never relaunch a
  completed step. A known API or payload failure is definite; a failed refresh after acknowledged
  success is presentation degradation and cannot reopen the mutation.
- Repository-browser automatic README-first selection yields to user selection.

## Controlled Form Controls

A native form control driven from app state must not also run its own default action.

- A controlled `<input type="checkbox">` cancels its click default so it only reflects state; the repo
  selector's tri-state checkbox is the reference (`frontend/src/lib/components/TreeCheckbox.svelte`).
- Keep a real `<input>` under custom visuals: set `indeterminate` and `aria-checked="mixed"` for partial.
- A cascading tri-state parent is checked only when all leaves are, partial when some are.

## Filtering And Visibility Rules

Not every visibility control means "remove this entity entirely."

- Controls that toggle detail visibility preserve the parent row unless the feature removes that category.
- Empty states make clear when filters, not missing data, hide results.
- When two data sources race, prefer the one matching the current filter/scope over a faster stale preview.
- Workspace status counts use the complete event-refreshed open-item cache, not page lists or capped search.
- PR inclusion options (including involvement and unassigned) match any selected option; repository,
  state, search, starred, workflow, and workspace filters still narrow.
- Activity involvement and unassigned likewise match either selection; exclusions still narrow.
- Workspaces applies repository scope by full provider/host/path identity to local and Fleet rows.

### PR Stacks And Lists

- Stack trees stay inside active filters and grouping; a filtered-out root never reappears as context.
- Stack roots align with ordinary PRs, children share one shallow indent, with a stack tint and trailing
  disclosure rather than an implied parent.
- Stack expansion belongs to the stack across status-group fragments; fragment counts cover only matches.
- Collapsing a selected child keeps its detail open and anchors keyboard movement at its visible root.
- PR and issue lists mount rows through a scroll budget that always includes the selected row and
  resets only on query, filter, or grouping changes, never on polls.

### Activity Semantics

- The Commits filter controls only top-level default-branch commits; opening rows follow only events
  that can occur on that item kind (`frontend/src/lib/stores/activity.svelte.ts::buildActivityFilterTypes`).
- Parent recency comes from renderable events, visible notifications, and lifecycle transitions, never
  provider `updated_at`, which GitHub bumps for mergeability recomputes and branch deletion.
- An in-window notification dates its parent only while Notifications are visible.
- Other event visibility filters never redefine parent recency.
- "Use workspace activity for recency" is one global opt-in; disabled keeps provider sorting and hides
  workspace-only subjects.
- With Hide bots, collapsed Activity keeps a visible event direct when its parent summary is hidden.
- Hide drafts covers notifications and workspace subjects; a spoke takes draft state from the hub.
- Hide drafts' default lives in Settings; a session choice wins over later hydration.
- Server-backed visibility filters reload Activity so turning one off restores omitted rows.
- Activity `capped` reports event overflow and alone drives event reloads and the warning;
  `item_activity_capped` drives only the parent-truncation notice.
- A search whose event matches overflow the event page also reports `item_activity_capped`, since
  event-matched parents derive from that bounded page.
- Status totals deduplicate open subjects across snapshots; parent lifecycle state overrides event state.
- Authoritative parents rewrite retained event parent metadata, keyed by stable identity.

### Activity Snapshots And Paging

- Collapsed cursors come from the same snapshot as visible summaries; never derive poll authority
  from a visible row. Opaque cursors preserve timestamps outside the Unix nanosecond range.
- Empty deltas must not rebuild summaries.
- Full snapshots are latest-successful-request-wins by request start; incremental polls never claim
  snapshot authority, and failed reads never claim or suppress others.
- Projection is part of snapshot scope; the `events` projection is delta-only.
- In collapsed threaded mode every fourth scheduled poll is an authoritative replacement, so events
  hidden behind the forward cursor self-heal. Every authoritative snapshot clears an error banner.
- Single-thread expansion uses stable identity, a frozen upper bound, and active filters; global
  expansion pages the bulk read instead of fanning out per thread.
- Progressive mounting keeps its visible row window across routine updates.
- Thread and bulk pages belong to the scope that started them; late pages are discarded and publish only
  after complete success.
- Parent recency or child-ledger revision (which advances for edits, deletes, and backfills)
  invalidates thread pages; changed parents discard cached children.
- After scope or filter invalidation, stale children never cross scopes; uncapped snapshots evict
  absent parents' children.

### Detail And Sync Freshness

- Successful detail reads and sync convergence directly reconcile visible PR, issue, and Activity lists,
  without delaying the detail (`frontend/src/lib/stores/detail.svelte.ts::reconcileListsAfterDetailSync`).
- Provider data-change events refresh mounted issue details from the local cache, preserving edits and
  starting no provider sync.
- Background detail reads skip while an on-demand provider sync is active.
- After a sync trigger is accepted, keep running state through the pre-trigger idle snapshot; accept
  completion only after running or a newer `last_run_at` (`frontend/src/lib/stores/sync.svelte.ts::applySyncStatus`).

## Threaded Comments

- In reverse-chronological timelines, a thread sits where its newest visible event would appear.
- Inside a thread, the root comment renders first, then replies newest-first.
- Do not flatten same-`thread_id` comments into separate top-level items where conversations are shown;
  inline diff thread summaries follow the same root-plus-newest-first shape.

## Inline Review Drafts

Inline diff review draft comments are local staged review state until publish.

- Direct detail-form review actions leave staged drafts untouched and never publish them.
- Direct approve and request-changes share one submission contract; neither gets stronger verification
  than the other (`internal/server/pullapi/routes.go::approvalReviewHeadSHA`).
- PR head mutations do not share an in-flight lock; only durable head-conflict state blocks the others.
- Once a provider mutation succeeds, close and clear its form before the follow-up refresh; a refresh
  failure must not leave it open for a duplicate submission.
- Editing a saved draft comment changes only its body and keeps the original diff range.
- An open saved-draft editor is pending state: publish and discard stay unavailable until every editor
  is saved or cancelled (`frontend/src/lib/stores/diff-review-draft.svelte.ts::hasPendingCommentEdits`).
- Draft authoring and the publish tray are gated by `review_draft`, not `submit_review`, and show that
  operation's reason when unavailable.

## Optional Metadata Controls

- Optional metadata reserves no empty rows or placeholders; edit controls sit beside the normal display.
- Async detail mutations compare full provider route identity before opening UI or applying responses,
  and discard stale responses.
- An acknowledged comment POST clears its draft; reconciliation failure is reported separately and never
  offers to replay the POST.
- Unsent top-level comments persist only in browser storage, keyed by item kind, provider/host,
  repository ID, and number; restoring never submits.
- Onboarding owns its initial sync: a rejected trigger returns to a retryable repository step, and an
  accepted one advances only after the sync settles.
- Project registration and cloning continue to Activity, not the workspace list.
- Removing external embeds needs no replacement project/worktree screen; keep the standalone flows.

## Testing Expectations

Test behavior contracts where the user would notice the breakage.

- Component tests for local state, event propagation, and identity helpers; store tests for
  persistence scope and normalization; e2e for navigation, Escape, nested buttons, and multi-surface flows.
- Assert controlled native form controls with a real `fireEvent.click`; mousedown-only tests skip the
  native default action and pass while a real click desyncs the control.
- Keyboard e2e covers conflicting scopes, modal ownership, async failure, scroll-into-view, and mobile redirects.

Related docs:

- [`docs/adr/0003-provider-ready-first-run.md`](../docs/adr/0003-provider-ready-first-run.md)
  for first-run trigger, dismissal, identity, and completion policy.
- [`context/ui-design-system.md`](./ui-design-system.md) for visual primitives and styling guidance.
- [`context/notifications-in-activity.md`](./notifications-in-activity.md) for notification feed rows,
  state, and sync behavior.
- [`context/workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md) for runtime-specific
  workspace tab and shell behavior.
