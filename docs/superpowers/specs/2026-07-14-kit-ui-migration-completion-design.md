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
7. If the checker reports application-owned UI that kit-ui does not support, fix the checker rule rather than reshaping production markup or adding a suppression solely to reach zero findings. The only new shared contracts approved in this work are axis-aware `SplitResizeHandle` behavior and a reusable resizable inline bottom dock; other middleman-specific gaps remain local.

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
6. Genuine Card hierarchy migrations.
7. Timeline/CommentCard audit and adoption.
8. DatePicker fit and any supported control migration.

For each slice:

- update or add focused Vitest component/store tests;
- use Vitest browser for native focus, modifier keys, popup positioning, collapsed TopBar content, drag behavior, and computed layout when no external server is needed;
- use Playwright only for geometry or full application workflows that browser components cannot prove;
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

## Kata and PR completion

As each slice lands, add concise completion evidence to the relevant Kata child. Create or update Kata records only for genuine migration work. Application-owned behavior that the checker misclassifies is resolved in the checker, not converted into an upstream component enhancement; `jvxt` and `ba3w` are the approved shared-contract exceptions.

Close `fn3y`, `2df7`, and `wa1f` only after their current acceptance criteria are satisfied. Close `kqyv` only after:

- all children and newly discovered migration decisions are represented;
- the manual component inventory is complete;
- temporary migration suppressions are gone;
- every remaining ignore states a legitimate current reason;
- application suites and the enforced checker pass.

Update PR #669's title and description to summarize the performed migration rather than the removed suppression mechanism. Any GitHub text authored by the agent must retain the repository-required attribution footer.
