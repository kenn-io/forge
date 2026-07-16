# Complete the middleman kit-ui migration

## Objective

Turn PR #669 from checker-suppression accounting into the completion change for Kata epic `kqyv`. Perform the remaining shared-component migrations, revisit exceptions invalidated by newer kit-ui APIs, and leave only durable, behavior-based application boundaries.

The migration is complete only when the manual inventory and semantic audit agree with the checker. Renaming markup to evade a heuristic or labeling ordinary debt as an exception does not count as migration.

## Scope

This PR completes the work represented by open children `fn3y`, `2df7`, and `wa1f`, plus post-upgrade gaps discovered while auditing the epic:

- stale Typeahead and MentionTextarea exceptions whose upstream blockers are resolved;
- the detached Reviews daemon indicator now supported by `TopBarTab.indicator`;
- `Timeline`, `TimelineItem`, and `CommentCard`, which were added after the original inventory;
- the local single-date picker's fit against `Calendar` and `DateRangePicker`;
- semantic status indicators hidden from the checker by domain-specific class names.

Already completed stage-4 migrations remain intact. This work does not rewrite application data ownership, provider logic, stores, or domain workflows merely to use a shared component.

## Migration principles

1. Prefer kit-ui when its contract preserves the existing behavior and semantics.
2. Keep application composition and domain logic local; migrate reusable chrome and interaction primitives.
3. An exception must state the concrete mismatch, not that migration is pending or that an old upstream issue once existed.
4. Checker output is supporting evidence, not the inventory. Audit semantic equivalents that use different names.
5. Delete replaced local markup, CSS, helpers, and components in the same slice.
6. Do not introduce compatibility shims. Adapt call sites to the canonical shared API.
7. If the checker reports application-owned UI that kit-ui does not support, fix the checker rule rather than reshaping production markup or adding a suppression solely to reach zero findings. This PR pins `@kenn-io/kit-ui` at `4d90fdf8a81424f53e92d45dc38fc24340894ee0` and adopts the axis-aware `SplitResizeHandle` and `BottomDock` contracts from that revision; unsupported middleman-specific gaps remain local.

## Accepted interaction changes

- Kata project reassignment lives under `More actions`; the menu preserves keyboard focus, stays open on a failed move, and exposes the current project in the move dialog.
- The compact PR label picker is a non-modal popover: Escape, a repeated trigger click, or outside click dismisses it without a backdrop, and background content remains available.
- File-jump Escape and keyboard selection return focus to the trigger; outside-click dismissal leaves focus with the clicked target, and Escape clears a non-empty query before closing.
- Compact empty and whitespace-only comments remain editable even when they have no expandable body. Entering edit mode mounts the editor and focuses its end once without scrolling; later value synchronization does not reclaim focus.

## Preserved layout and route contracts

- A persisted pane size is the last rendered size, not an unconstrained preference. Restore, pointer/keyboard resize, and every container-width change clamp it to `[minListSize, maxListSize]` and write the clamped value back; when the detail minimum consumes the available width, `minListSize === maxListSize`, resizing is a no-op, and later widening does not resurrect the discarded pre-clamp value.
- Repository loads are keyed by provider-qualified repository identity plus ref type, ref name, and resolved SHA. Path and anchor changes reuse the loaded tree. After a successful unresolved-ref load, its resolved-SHA route is an alias only for that load; a different repository/ref or a moved branch SHA clears the alias and reloads, while generation checks discard overlapping stale completions.
- User-selected ref routes enter history only after the new tree loads. A failed switch keeps the prior route, selected ref, last usable tree/path content, picker query, and active branch/tag tab so the same option can be retried; browser Back reloads the preceding ref rather than treating a stale alias as current.

## Stage-4 completion audit

Preserve the completed adoption of Modal, SelectDropdown, FilterDropdown, DetailDrawer exceptions, flash storage and FlashBanner, CollapsibleSidebar, SidebarToggle, SplitResizeHandle, StatusBar, SettingsLayout, SettingsSection, and TopBar.

### Typeahead family

- Replace the local `TypeaheadTrigger` with kit-ui `Typeahead`. Adapt null/clear state, option shape, selection veto, custom values, top placement, metadata, prefixes, and trigger accessible naming at its callers.
- Replace `TaskReferenceTextarea` with kit-ui `MentionTextarea`. Preserve stale-response protection, keyboard selection, Escape, Tab, modifier-Enter, and provider/project-qualified Kata reference insertion.
- Migrate the repository branch/tag ref picker to kit-ui Typeahead using its header, loading, error, and empty-state contracts unless implementation reveals a concrete mismatch.
- Reassess the Docs folder menu. Migrate its selection surface only if per-folder actions can remain valid and accessible; otherwise retain it as an actionable-menu exception with that reason.
- Retain `RepoTypeahead` as an application-specific control under the current API. Kit Typeahead supports hierarchy but remains single-select; RepoTypeahead owns a checkable hierarchical multi-select tree, tri-state subtree selection, an all-repositories state, Space toggling, and provider-qualified repository identity. Rewrite its stale "flat Typeahead" rationale to this actual mismatch.
- Retain command-palette file navigation, issue-search results, and the directory browser when their interaction model is not a form Typeahead. Their reasons must describe the distinct workflow.

### TopBar indicator

Move Reviews daemon status into the Reviews `TopBarTab.indicator`. Remove the detached right-region indicator, obsolete comments, and local indicator CSS. The status and accessible label must remain present in expanded tabs, the collapsed trigger, and collapsed navigation options.

### Remaining stage-4 acceptance

Add or retain focused coverage for:

- dismissing one of two flashes without dismissing the other;
- popovers escaping overflow-hidden ancestors;
- narrow sidebars overlaying rather than reflowing their host;
- Modal adapter focus and scroll-lock behavior where middleman-specific wiring changes the upstream path;
- migrated Typeahead and MentionTextarea behavior;
- TopBar indicator behavior in expanded and collapsed layouts.

Close `fn3y` only after obsolete exceptions and workarounds are removed or rewritten around current behavior.

## Semantic StatusDot adoption

Audit every status indicator by meaning, including dots whose classes were renamed during checker burn-down. Do not use the historical 18-finding count as the inventory.

Map compatible application states onto `StatusDot`'s vocabulary:

- `working`: active work that should draw attention;
- `waiting`: explicit user-input or blocked-on-user state;
- `idle`: recently active but not working;
- `stale`: delayed, pending, or degraded state where amber is correct;
- `unclean`: error or action-required state where red is correct;
- `quiet`: intentionally no visible status.

Preserve domain labels through the component's `label` prop rather than exposing the shared vocabulary to users where it would be inaccurate.

Retain an application-owned indicator only when a finite StatusDot mapping would lose information, such as continuous budget-health color interpolation, review-job colors with a separate domain token system, or connection/session states whose distinctions do not fit the shared model. Each retained indicator gets a concise semantic rationale. Renaming `status-dot` to evade the checker is not sufficient.

Tests should prefer accessible status labels and state outcomes over private CSS class names. Close `2df7` after the semantic inventory is complete and targeted and stock checker runs are clean.

## Card migration

Classify all 28 `wa1f` Card findings by element semantics rather than blindly applying Card.

### Genuine Cards

Use kit-ui hierarchy deliberately:

- `raised`: repository summary/state/metric panels and mobile activity cards;
- `default`: timeline events, editor boundaries, draft review items, suggestion batches, and draggable Kanban cards when the root contract permits it;
- `inset`: issue and pull descriptions inside detail surfaces and review suggestions nested inside default timeline events.

Do not nest same-level Cards. Keep Cards static when they contain nested buttons, links, or editors. Place click behavior on Card only when the whole Card is the single interactive control.

Where Card's body wrapper prevents existing grid, flex, height, or overflow layout, move application layout to one explicit inner wrapper instead of styling Card internals. Retain semantic wrappers such as `article` when Card cannot provide that root without invalid markup.

Potential Card compositions include:

- `RepoSummaryCard`, `RepoPageState`, and `RepoMetricGrid`;
- `DocMarkdownEditor` and `DiffReviewDraftTrayItem`;
- EventTimeline event and suggestion-batch surfaces;
- `ReviewSuggestionBlock` as an inset Card inside a default event Card;
- issue and pull Markdown descriptions as inset Cards while preserving the Markdown body as the delegated event target;
- mobile activity and Kanban cards, provided semantic, drag, and nested-interaction contracts remain valid.

### Non-Card findings

A Card signature on control chrome is not a Card migration. Move compatible sites to the appropriate primitive:

- text and search fields to `TextInput` or a TextInput composition;
- ordinary actions to `Button`;
- compact icon actions to `IconButton`;
- linked metadata tokens to interactive `Chip`;
- date-picker and typeahead triggers to shared controls only if their popup ARIA and keyboard contracts are preserved.

Keep precise exceptions for generated Markdown code fences and a plain review textarea while kit-ui has no compatible component. These exceptions explain the rendered-HTML or missing-primitive constraint; they do not reference pending Card migration.

## Checkbox migration

Replace standard native checkbox controls with kit-ui `Checkbox`:

- recurrence options;
- terminal, agent, fleet, and mode-visibility settings;
- repository preview filters and compatible row selection;
- add-folder hidden-file selection;
- Kata checklist items;
- Roborev filters.

Use `onchange(checked)` or `bind:checked` rather than inverting potentially stale state. Remove outer labels because Checkbox renders its own label. Use its children snippet for rich labels and retain only app-owned row layout.

Preserve specialized native controls when the installed Checkbox API cannot express their interaction:

- `TreeCheckbox` remains a controlled composite-listbox control because it depends on mousedown selection, native-toggle cancellation, delegated focus, negative tabindex, decorative aria-hidden state, and pointer pass-through.
- Repository import range selection remains native if Checkbox cannot expose the shift-click event without losing keyboard behavior. Do not replace it with modifier bookkeeping that can become stale.
- Generated Markdown task-list checkboxes and their CSS remain native because they are sanitized HTML strings with delegated source editing, task indices, drag/reorder behavior, and blockquote disabling.

Rewrite all retained suppressions with those concrete reasons. Do not leave any `wa1f` "migration pending" marker.

## Toggle migration

Replace all five hand-rolled DiffToolbar switches with kit-ui `Toggle`:

- file list visibility;
- hide whitespace;
- side-by-side view;
- word wrap;
- rich preview.

Use controlled `checked` and `onchange` mappings to existing store and callback contracts. Preserve the expanded accessible names for hide-whitespace and side-by-side controls, disabled state, preference persistence, conditional rendering, and the existing full-row hit target.

Delete the local track, knob, focus, animation, and theme CSS. Update tests from explicit `aria-checked` attributes to native checkbox state while retaining role-based queries.

## Timeline and CommentCard audit

Audit `EventTimeline` against `Timeline`, `TimelineItem`, and `CommentCard`.

Application-specific filtering, provider data, threading, mutation, review suggestions, and event rendering stay local. Adopt shared timeline rail/item structure and comment-card anatomy only where they compose without flattening those behaviors.

If a branch cannot use the shared primitive, record the exact mismatch. The audit must not end with "large component" or "checker clean" as its rationale. Structural Card migration and Timeline adoption should be designed together so the result does not duplicate surface chrome or create same-level nested Cards.

## DatePicker fit decision

Compare the local single-date `DatePicker` with kit-ui `Calendar` and `DateRangePicker`.

Do not force a single-date workflow through range semantics. Adopt `Calendar` or a supported single-date composition if it preserves:

- one-date selection;
- current popup and trigger ownership;
- disabled state;
- Escape behavior and event propagation;
- accessible expanded and popup relationships.

Otherwise retain DatePicker with a documented single-date versus range-picker distinction, while migrating its trigger and clear-button chrome only where shared controls preserve the composite widget contract.

## Error handling and state ownership

The migration does not add new data stores or error channels. Existing stores and parent components remain authoritative.

Shared component callbacks receive the new value and delegate to existing state owners. Async typeahead and mention searches keep stale-response protection. Existing flash behavior remains the user-visible error surface, and per-flash dismiss handlers always pass the flash ID.

A shared-component limitation is never handled by silently dropping an interaction, accessibility relation, provider identity field, keyboard path, or persisted preference.

## Testing strategy

Work in independently validated slices so failures remain attributable:

1. Stage-4 stale-exception cleanup and TopBar indicator.
2. Semantic StatusDot adoption.
3. Toggle migration.
4. Checkbox migrations and documented exceptions.
5. Non-Card control migrations.
6. Pin the kit-ui upgrade and verify the new `SplitResizeHandle` and `BottomDock` public contracts without changing application consumers.
7. Migrate leading horizontal split consumers: PR/diff rails, repository browser, and Messages.
8. Migrate vertical-recursive and trailing-pane split consumers: Kata and terminal/panel trees.
9. Normalize restored, resized, and viewport-reconciled pane persistence across the migrated split consumers.
10. Migrate the terminal inline panel to `BottomDock`, retaining application-owned session state and content.
11. Card hierarchy and Timeline/CommentCard adoption as one coordinated slice.
12. DatePicker fit and any supported control migration.
13. Preserve migration-adjacent regressions: file-jump focus/scroll, compact empty-body editing and one-time autofocus, and repository ref-route equivalence, failure rollback, retry, and browser history.
14. Run cross-surface geometry and real-workflow verification after all independently reviewed consumer slices land.

For each slice:

- update or add focused Vitest component/store tests;
- use Vitest browser for native focus, modifier keys, collapsed TopBar content, and non-geometric interaction behavior when no external server is needed;
- use Playwright for popup geometry and overflow, pointer drag, or full application workflows that browser components cannot prove;
- cover repository ref changes with request-counted Playwright workflows for resolved-route deduplication, moved refs, failed same-ref retry, and browser Back; cover compact comment edit entry/exit in a seeded detail workflow while component tests pin empty/whitespace rendering and autofocus;
- run targeted kit-ui-check rules and confirm temporary markers decrease.

After the final frontend/test edit, run:

- the complete `vp test` suite from `frontend/`;
- the full affected Vitest browser project;
- affected Playwright suites where specs or shared fixtures changed;
- Svelte checking with the repository's heap-safe invocation;
- format and lint checks;
- frontend production build;
- stock kit-ui-check and targeted Card/Checkbox/Toggle/StatusDot/stage-4 rule runs.

The final source search must find no temporary `wa1f` markers and no obsolete exception citing a resolved kit-ui gap.

## Completion inventory and evidence

The completed audit uses the following decision matrix. It is the manual inventory behind the checker result; a clean heuristic scan alone is not completion evidence.

| Surface family | Audited files/components | Decision | Required evidence |
| --- | --- | --- | --- |
| Repository summaries | `RepoSummaryCard`, `RepoPageState`, `RepoMetricGrid` | Raised/default Cards with equal-height composition; nested links and buttons remain local | Repository summary component tests and `repo-summaries.spec.ts`, including phone issue-dialog geometry |
| Detail activity | `EventTimeline`, threaded replies, suggestion batches, `ReviewSuggestionBlock` | Shared Timeline/TimelineItem and CommentCard/Card anatomy; per-reply actions and mutation ownership remain local | EventTimeline component coverage for root, reply, compact edit, copy/link/delete plus real detail workflows |
| Editors and review trays | `DocMarkdownEditor`, `DiffReviewDraftTray` | Default Card frames; application flex/height and upward-shadow behavior stay local | Docs and diff component tests plus affected full-stack specs |
| Mobile and board cards | `MobileActivityView`, `KanbanCard` | Shared Card hierarchy; drag payload and nested interaction remain application-owned | Browser drag/cursor coverage and server-backed workflow-state coverage |
| Detail descriptions | Pull and issue Markdown description surfaces | Inset Card only where it does not duplicate timeline chrome or break delegated Markdown interaction | Pull/issue component tests and focus/detail full-stack workflows |
| Control chrome misreported as Card | File jump, repository/ref pickers, generated Markdown, review textarea | Use Button/IconButton/SearchInput/Typeahead where contracts fit; retain only behavior-based exceptions for command navigation, sanitized generated HTML, or missing editor primitives | Kit checker suppressions state the exact mismatch and focused keyboard/focus tests pin the retained behavior |
| Pane dividers | PR/diff rails, repository browser, Messages, Kata, and recursive terminal/panel trees | Shared axis-aware `SplitResizeHandle`; callers retain bounds, leading/trailing interpretation, ratios, and persistence | Component/browser interaction tests plus repository, Kata, design-system, and Messages geometry workflows cover horizontal, vertical, trailing, undersized, and restored-value cases |
| Inline bottom dock | Workspace terminal panel | Shared `BottomDock`; the workspace retains open state, session selection, header actions, and terminal content | Dock component coverage and workspace terminal Playwright geometry/session workflows |

### Styling escape-hatch audit

| Selector family | Owners and retained reason | Browser evidence |
| --- | --- | --- |
| `.kit-card__body` | Approve popover, docs editor, and repository typeahead require host-owned flex/padding geometry not exposed as a public Card property | detail/focus, docs, and repository-selection suites |
| `.kit-checkbox__label` | Checklist, folder visibility, settings, and review filters apply domain completion or compact-row layout to the shared label | Kata, docs, settings, and review component/browser suites |
| `.kit-typeahead__trigger` / `__input` | Kata property/search and fleet settings embed Typeahead inside compact table or pill layouts | Kata and settings browser/full-stack suites |

Re-audit this table whenever the pinned kit-ui revision changes; a new public prop or custom property removes the corresponding internal-selector exception.

The semantic status inventory is similarly domain-based:

| Domain state | Shared status | Representative owners |
| --- | --- | --- |
| Active synchronization, refresh, log streaming, or active workspace work | `working` | `StatusBar`, pull/issue list and detail refresh, `LogViewer`, workspace busy indicators |
| Explicitly waiting for user input | `waiting` | Tab/workflow status callers that expose a user-blocked label |
| Live but not currently doing work | `idle` | Running terminal/workflow sessions and ready workspace activity |
| Delayed, not pushed, or degraded-but-not-failed | `stale` | Commit push state, stale worktree/project indicators, delayed tooling state |
| Failed or action-required | `unclean` | Workspace/tooling error mappings |
| No visible state | `quiet` | Pushed commits and callers without a meaningful indicator |

Labels remain domain-specific (`Helper running`, `Syncing pull requests`, `Has stale worktrees`) even when colors share the semantic vocabulary. Continuous health gradients, provider-specific review verdict colors, and composite workspace indicators remain application-owned because reducing them to one finite status would discard information.

The required workflow verification for this migration is explicit: Kata owner/project mutation and task-reference insertion, date metadata selection/clearing, repository import Shift-range selection, settings persistence, command-palette and file-jump keyboard/focus behavior, threaded comment actions, source/diff rail pointer resizing, and modal/popup geometry at narrow and phone sizes. Component or browser tests cover local interaction state; Playwright covers geometry and real backend/daemon boundaries.

The dependency contract for this completion is `@kenn-io/kit-ui` revision `4d90fdf8a81424f53e92d45dc38fc24340894ee0`, pinned in the workspace package manifests and Bun lockfile. A dependency update must rerun the manual inventory, browser interactions, affected Playwright suites, and the stock checker before it can be treated as equivalent.

## Kata and PR completion

As each slice lands, add concise completion evidence to the relevant Kata child. Create or update Kata records only for genuine migration work. Application-owned behavior that the checker misclassifies is resolved in later checker work, not converted into an upstream component enhancement. The splitter and bottom-dock adoption originally tracked by `jvxt` and `ba3w` is part of stages 6-10 in this completion PR; those tasks close only after their consumer slices and required geometry workflows pass, without moving application state or policy into kit-ui.

Close `fn3y`, `2df7`, and `wa1f` only after their current acceptance criteria are satisfied. Close `kqyv` only after:

- all children and newly discovered migration decisions are represented;
- the manual component inventory is complete;
- temporary migration suppressions are gone;
- every remaining ignore states a legitimate current reason;
- application suites and the enforced checker pass.

Update PR #669's title and description to summarize the performed migration rather than the removed suppression mechanism. Any GitHub text authored by the agent must retain the repository-required attribution footer.
