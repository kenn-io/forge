# UI Design System

Use this document as the intent-level guide for frontend UI work in `kenn-forge`.
Desktop and mobile remain supported application experiences. External host embeds
have no consumers and are retired; keep shared detail and workspace views available
to the application.

## Purpose

- Keep the app visually coherent.
- Reuse shared primitives by default; one-off styling is a last resort.
- Extend semantic tokens and components instead of duplicating UI geometry.
- Hand-styled controls in existing code are not precedent. Use kit controls such as
  `SegmentedControl` and `Checkbox`; a confirmation that needs choices passes them
  as `ConfirmDialog` children, with no local CSS.

## Design intent

`kenn-forge` is a dense maintainer tool, not a marketing surface.

- Layouts are compact, deliberate, and information-rich; emphasis comes from
  hierarchy and semantic color, not oversized controls or decorative effects.
- Light and dark themes express the same UI language through shared tokens.
- The quiet dark canvas and content surfaces stay dark enough for comfortable
  reading; inset, hover, and border tokens step up in small even increments so
  structure never reads as bright gray fills.
- The quiet light palette uses an off-white reading surface on light warm-gray
  chrome and soft warm-black ink (about 13:1): never pure white, near-black on
  white, dimmed mid-gray surfaces, or a blue or yellow cast.
- Muted text steps visibly below body ink. Accents are the brightest shades that
  stay AA, not darkened toward black.
- Muted and accent text stay AA on every surface step, and accents also on their own
  `color-mix` tints up to 16%; selected rows sit between surface and hover in both themes.
- Diff code text follows `--text-primary`, not the Pierre theme's foreground.
  Preserve kit's explicit high-contrast palette (`frontend/src/app.css`).
- Workspace terminals follow the app theme, with `--terminal-bg` matching; light ANSI
  hues clear 4.5:1 on the light page, and the contrast floor darkens colors a TUI chose
  for a dark background (`frontend/src/lib/components/terminal/terminal-appearance.ts`).
- PR background refresh uses an out-of-flow top progress bar at every width so narrow
  workspace panels do not reflow (`frontend/src/lib/components/detail/PullDetail.svelte::.sync-indicator`).
- Issue background refresh uses the metadata-row `Syncing` indicator.
- Manual Activity refresh reports progress only in its initiating icon button; add no
  stale-data banners or other progress surfaces. The control stays disabled while
  detail is loading, stale for the route, or already syncing.
- Inline conditional notices occupy layout only while active; never reserve invisible
  rows for them.
- Section controls live in the section header's action row, never overlaid on content;
  description copy sits left of Edit on every viewport.
- PR list rows use the workspace indicator only; no separate worktree icon or
  branch-name label.
- Use `AgentStatusIndicator` for opt-in agent labels in PR, Issue, and Activity rows,
  right-aligned to match Workspaces.
- PR size means total added + deleted lines at observed head/base revisions, never
  cumulative commit churn; missing heads stay missing. A base branch change starts a
  new series because sizes against another base are not comparable
  (`internal/db/queries_pr_size.go`).
- The PR size sparkline draws one near-uniform dot per observed revision so each
  step stays visible, and renders nothing below two revisions. In the detail header
  it sits directly after the diff `+/-` chip in a matching pill, with no visible
  label. Its tooltip is only the terse change across the plotted revisions, such
  as `+400 lines over 4 revisions`. History starts at first sync, resets on base
  changes, and is capped, so never claim a lifetime change such as "since open"
  (`frontend/src/lib/components/shared/PRSizeSparkline.svelte`).
- Commit attribution icons identify the harness before the author, and tooltips name
  only the harness. Hide recognized attribution lines only in the displayed message,
  preserving the original for copying.
- Attribution names come from the shared harness catalog, not an inventory of locally
  observed agents (`frontend/src/lib/components/detail/commit-attribution.ts`).

## Sources of truth

- Tokens: kit-ui `theme.css`, `mermaid.css`, and `markdown-images.css`, plus the
  kenn-forge tokens `frontend/src/app.css` defines on top.
- Shared primitives: `@kenn-io/kit-ui` first; app compositions in
  `frontend/src/lib/components/shared/`.
- Routed item references and URL builders: `frontend/src/lib/routes.ts`.
- SPA code and tests live in `frontend/src`; `packages/ui` is generated-schema output,
  not an `@kenn-forge/ui` application import surface.
- Svelte guidance: the `svelte-core-bestpractices` and `svelte-code-writer` skills
  under `skills/`; use them when editing Svelte components.
- Interaction contracts: [`ui-interaction-contracts.md`](./ui-interaction-contracts.md).
  Mobile: [`mobile-ux.md`](./mobile-ux.md). Effect ownership:
  [`frontend-effect.md`](./frontend-effect.md).

## kit-ui contract

- kit-ui is consumed as source pinned to one commit SHA in `frontend/package.json`,
  never a `file:` path; bun's store keys by name@version and goes stale.
- Its rune-module source cannot be prebundled: keep it in `optimizeDeps.exclude` with
  transitive deps as `"@kenn-io/kit-ui > <dep>"` includes.
- Kit source that imports assets needs kit's resolved source root in
  `server.fs.allow`; otherwise only the Vitest/Playwright transform tier fails, not
  `svelte-check` (`frontend/vite.config.ts::kitUiSourceRoot`).
- kata-ui's cached checkout has no `node_modules`, so every kit-ui specifier it
  imports needs a Vite alias to Forge's copy (`frontend/vite.config.ts::kitUiEntry`).
- Fix a kit-ui component defect in kit-ui and bump the pin; never patch it per call
  site with Forge CSS overrides.
- kit BEM classes are the sanctioned surface for parent `:global` styles and test
  selectors; a SHA bump that renames a class updates selectors in the same change.
- `kit-ui-check` gates at zero findings. If a rule mistakes app-owned UI for
  component debt, fix the rule rather than expanding kit-ui or adding an ignore;
  checker cleanliness alone never justifies new kit-ui behavior.
- `kit-ui-check` honors an ignore comment only directly above the flagged line; when
  `vp fmt` wraps a selector, move the comment, and verify with `vp run -w check`.

### Theme and tokens

- Theme tokens come from kit `theme.css` plus the kit `quiet` identity. Quiet is the
  default; preserve an explicit e-reader choice through startup and first paint
  (`frontend/src/lib/stores/theme.svelte.ts`).
- Dark mode and high contrast are `dark` / `high-contrast` classes on `<html>`. Kit's
  theme store owns dark/light/system resolution and persistence.
- E-reader modes are always light: pure white surfaces, pure black text, strong
  edges. Plain e-reader forces black ink with pale fills; color e-reader keeps inline
  ink with saturated accents of at least 4.5:1 on white (`frontend/src/e-reader.css`).
- E-reader diffs show +/- signs and take row tints from `--diff-add-mark` /
  `--diff-del-mark`, since Pierre blends at only 12%.
- Disabled native controls use `var(--opacity-disabled)`, never a literal; a
  deliberate no-dim rule uses `opacity: unset`; enabled-only hover uses `:enabled`.
- Rem tokens self-adjust on coarse pointers (`kit-type-touch` forces the touch scale);
  never pin `html { font-size }`.
- Breakpoints are px (shared steps 640/760/900); media-query `rem` resolves against
  the browser's 16px, not the app root.
- App-owned labels and section titles use `var(--label-transform, uppercase)` and
  `var(--letter-spacing-label, …)`, never literal `uppercase`, so they follow the theme.
- Spacing ladder `--space-1…8` = 2/4/6/8/12/16/24/32px. New or edited `gap`
  declarations use ladder tokens, snapping off-ladder px to the nearest step, biased
  compact; migrate untouched on-ladder px opportunistically, not as churn.
- Relative timestamps use kit `formatRelativeTime`; API timestamp parsing and local
  date labels stay app-side.

### Component contracts

- Chip: icons go in `children`, dropdown chevrons in `trailing`; no downstream
  `.kit-chip__label` overrides, since repo chips depend on its ellipsis.
- Line-change counts reuse kit `DiffStats`; commit-range menus hide them on mobile.
  Commit-range additions, removals, and times share right-aligned columns sized by
  the widest timestamp.
- Agent launch targets draw a kit `HarnessIcon` through the shared agent-key
  resolver; the glyph replaces only the generic kind icon, and the label always stays.
- Agent menus use kit `MenuItem`'s icon slot even for unrecognized profiles; empty
  icons preserve label alignment.
- Dialogs: every dialog pushes a keyboard modal-stack frame. Background Escape
  surfaces stand down when `getStackDepth() > 0`, since `defaultPrevented` arrives
  too late. kit `Modal`'s `closable` gates only the header X.
- A non-empty kit `SearchInput` claims Escape to clear itself; an empty one lets it
  bubble, and every popover hosting a `SearchInput` must close on that bubbled Escape.
- Palette and Cheatsheet keep hand-rolled focus traps that restore focus
  synchronously before the picked action runs, so the action's own focus move wins;
  kit `trapFocus` would undo it.
- Kit text fields show keyboard focus as the wrapper's accent border only; `app.css`
  drops the stacked `TextInput` outline ring.
- Hover-only controls inside a list row button take `tabindex="-1"` so Tab moves row
  to row.
- Tab strips are one tab stop: only the selected tab has `tabindex="0"`, arrow/Home/End
  move focus and selection (`shared/tablist-keyboard.ts`), and Tab enters the panel.
- In `TabbedPanelTree`, a focused tab button reports its own tab; other focus in the
  leaf reports the active tab, so focusing a tab's "Hide" tool does not activate it.
- `CollapsibleSidebar`: Forge relies on `kit-sidebar-layout*` classes,
  `data-collapsed`, `SplitResizeEvent`, and `SidebarToggle` modifiers; the narrow
  overlay is kit-owned via `overlay`, with no app-side copies of its CSS.
- `StatusBar`: Forge relies on `kit-status-bar*` classes and `--status-bar-height`;
  `overflow="visible"` lets BudgetPopover use kit's popover recipe; the app keeps bar
  text short.
- `TopBar`: the header clips its x-axis (`overflow-x: clip`) and side regions stay
  content-sized, or measurement blocks tab re-expansion. Select tabs via
  `.kit-top-bar__tabs .kit-top-bar__tab`, never the bare class.
- Provider-mode repo selector visibility must not move the tab row; non-provider modes
  reserve its footprint (`frontend/src/lib/components/layout/AppHeader.svelte`).
- `AdaptiveActionGrid` hosts the issue action row and the phone PR primary actions
  (see [`mobile-ux.md`](./mobile-ux.md)). Controls inside never pass their own `size`;
  wrappers never hardcode a width below `--focus-detail-hit-target`.
- Kit fills only its own direct controls; compound wrappers are custom items, and
  `PullDetail` fills their primary button itself.
- `WorkspaceCreateSplitButton` takes its height from its kit `size` or grid item,
  never a pinned `min-height`.
- `FitStages` owns how an action row degrades under pressure, never a media query,
  Button-internal overrides, or a wrapping stage. Probes are stateless and hidden from
  accessibility; stateful controls render exactly once outside them.
- Every `FitStages` stage exposes the same accessible names. Inside a `flex-wrap`
  parent the host needs `flex: 1 1 0` and a `min-width` at the compact stage's width.
- PR lifecycle decisions stay in the primary action row; workspace creation and
  workflow dispatch form a utility row that joins the `Actions` overflow only under
  pressure. Every control in both rows is a kit `Button` at the row's `size`.
- Flash: one shared store; kit `FlashBanner` mounts once per shell in a page-level
  fixed layer below shell chrome and above modal backdrops, never inside features.
- Commit timeline rows keep type, author, SHA, and relative time in the compact
  header; the SHA is metadata, not card action content.
- `PullItem`/`IssueItem` rows have a uniform two-line height; any icon inside must fit
  the row's line-height (`frontend/src/lib/components/shared/CITokenCluster.svelte`).

## Shared primitives

### Chip

Use `Chip` and its semantic wrappers for compact status and metadata (PR/issue state,
CI/review state, repo and count badges). The standard term is **chip**, not pill.

- Use `Chip` directly with a semantic `tone`; use `ItemKindChip` and `ItemStateChip`
  rather than repeating kind/state class maps.
- Do not create local `.badge`, `.pill`, `.tag`, or `.chip` geometry when a chip fits.
  New semantic color extends `Chip` with a named tone class; sizing, casing, and
  spacing always come from `Chip`.

### Tree cells

Tree-like rows in dense tables keep IDs in their normal column and put chevrons and
indentation inside the content cell that owns the hierarchy. No TUI connector glyphs,
branch-line borders, or ornamental strokes.

### Table

- Row-and-column data uses kit `Table`/`TableHeaderCell` with caller-owned sort, not
  grid-styled buttons. Every labeled data column sorts; short settings and status
  tables opt out with `<Table unsorted>`.
- A table whose scroller also holds controls below the rows keeps its own scroller with
  sortable `TableHeaderCell`s (`frontend/src/lib/components/roborev/JobTable.svelte`).
- Under `table-layout: fixed` with a full-width `colspan` row, collapse narrow columns
  to zero width; `display: none` leaves phantom columns.

### ActionButton

Use `ActionButton` for repeated action styling; a new repeated button treatment
extends it rather than creating another local button pattern.

### Comment composers

Every comment composer insets its submit button bottom-right inside the input and
reserves that footprint as bottom padding, never a button beside the field. Three
composers repeat this shell; extract it into a shared component instead of adding
another copy.

### Modal primitives

Use kit `Modal`, `ConfirmDialog`, and `DialogButton`.

- Dialog titles and buttons use sentence case. Dialog body copy carries only what the
  title and buttons do not already say; a body that explains the primary action is cut.
- Open a confirmation dialog only when it has a decision to offer; with nothing to
  choose, the action button acts directly (`PullDetail.svelte::enqueueWithoutDialog`).

### Sidebars

- Use kit `SidebarToggle` directly for left-rail collapse controls, with a specific
  label when `sidebar` would be ambiguous; no one-off SVG buttons or local styling.
- Hosts request the floating overlay through `CollapsibleSidebar`'s `overlay` prop
  from container width; kit's `overlayOnNarrow` is viewport-based, a different signal.
- `SidebarTitlePopover` shows the full title and repository (plus head branch for PR
  and workspace rows) on sidebar rows, opening only while a named element is truncated.
- `GroupedSidebarSection` provides collapsible list-rail groups; header controls go in
  its `actions` slot outside the collapse button.
- Wrap large always-visible vertical scroll panes in kit `ScrollBox` with a concise
  accessible label; the scrolling element is its `viewport`, not the content wrapper.
- Long lists use kit `VirtualList`; missing capabilities belong in kit-ui, not a
  Forge-only windowing helper. The PR/issue sidebar scroll budget is a stopgap until
  it supports grouped sticky headers.

### SplitResizeHandle and BottomDock

- Use kit `SplitResizeHandle` for every pane divider, including inside app-owned
  trees; the app owns topology, bounds, and persistence, the handle owns interaction
  and separator semantics. Give it a specific label.
- Kit owns handle thickness, color, and grab margin; app CSS may only place or hide
  a handle (kit-ui-check `split-handle-override`, unsuppressible).
- A pane beside a handle draws no border on that edge; the handle is the divider.
- A pane beside a handle must not set its own z-index, or it clamps every popover it
  opens under the handle.
- Use kit `BottomDock` for resizable inline bottom panels. It cannot hide its handle,
  so a mode forcing a controlled height hides it with a scoped child-combinator
  `:global(.kit-bottom-dock > .kit-split-resize-handle)` rule pinned by a browser test.

### Styling shared components

- Props, documented CSS custom properties, and the root class are the supported
  styling contract; wrap or set a public property before reaching into child markup.
- An inner kit selector is allowed only when no public hook exists, the dependency is
  pinned, and browser coverage exists; scope it below an app-owned class, never
  globally, and re-audit when the kit-ui revision moves.

### TabbedPanelTree

- Use it for VS Code-like panel workspaces (reorder, cross-group drag, split, resize).
  PR, issue, and activity detail panes are an intended home; do not hand-roll another
  splitter. Simple sidebars and drawers stay on `SplitResizeHandle`.
- Arrangement, availability, history, and terminal-hosting contracts live in
  [`ui-interaction-contracts.md`](./ui-interaction-contracts.md).
- Every selected tab keeps its top accent; the pane focus border never replaces it.
- Healthy running workspace tabs show no status dot.
- Use neutral `tabbed-panel-*` selectors; add no workflow-specific aliases.
- Omitted mutation callbacks make that interaction read-only rather than rendering a
  drop target that cannot apply.
- A leaf renders every tab and shows only the active one; gate expensive or
  side-effecting bodies on `visible`, or a lingering portal slot strands its content.
- A pane body must not size itself from its content; detail views end in a `ScrollBox`
  that needs a height-constrained flex parent.
- Zoom is transient focus state, not saved arrangement; drop it when the zoomed pane
  stops rendering and on any successful split.
- Keyboard reorder, split, and resize are not implemented; extend `TabbedPanelTree`
  first if a consumer needs them.

### SelectDropdown

Use `SelectDropdown` for every visible single-value selection; never a native
`<select>`, with no per-component exemption. Extend the primitive when it cannot express
a case (`frontend/src/no-native-select.test.ts`).

### Repository selector

- The selector shows `Global` first and named presets above the repository tree;
  search and preset regions stay fixed, only the tree scrolls, and the save footer
  stays visible.
- Custom preset rows put their destructive action at the far right. Saving opens the
  create-or-overwrite dialog; presets have no separate rename.

### KataLinksPanel

`KataLinksPanel` shows read-only linked Kata context in provider-item and workspace
detail panes; issue presentation comes from the pinned `@kenn-io/kata-ui` component.
Forge link and unlink controls stay outside the package error boundary so a Kata
rendering failure cannot hide association recovery.

### FilterDropdown menu rows

A binary view mode is one toggle row named for the non-default state ("Hide bot
activity"), checked when active; no default-state row, radio pair, or opaque label.

### Context menus

- Right-click menus use kit `Menu` with a pointer `anchor` and `returnFocus` on the
  row; group with separators, not heading rows. Destructive items use `tone="danger"`.
- Show shortcut hints only for real bindings. A row with no actions opens no menu.
- Library-rendered menus mirror kit `MenuItem` metrics.
- An item that moves focus into a field or dialog must not restore focus to the row.
  An item moving the row to another list expands that list, then focuses the new row.

### Overlays

- Dropdowns, popovers, menus, and tooltips use shared overlay primitives that float
  above panes, sidebars, drawers, handles, and scroll containers without clipping.
- An overlay opened from a modal escapes the modal's scrollable body while staying
  inside the panel, so the modal focus trap still owns it.
- Label editor popovers use kit's `--z-popover` above workspace pane focus chrome;
  verify in both workspace and PR panes and keep markers visible on uncovered edges.
- Popover chrome comes from `kit-popover-card`; never re-declare it in scoped styles.
  A `var()` of an undefined token (there is no `--bg-elevated`) silently computes to transparent.
- A popover that lowers its min-content width (`overflow-wrap: anywhere`) leaks it to
  nested surfaces; reset `overflow-wrap`/`word-break` at the nested popover's root.
- Narrow workspace headers keep identity and Launch visible; secondary controls go in
  the shared workspace controls popover.
- Expanded markdown images, Mermaid diagrams, and videos open kit `MediaViewer`; add
  no app-side lightboxes (kit-ui-check `hand-rolled-lightbox`).

### GitHubLabels

Use `GitHubLabels` for actual GitHub labels, kept distinct from generic status chips.

### Pierre diff and file tree

Changed-file UI goes through the local `PierreFileDiff.svelte` and
`PierreFileTree.svelte` wrappers, which own Pierre lifecycle, Shadow DOM styling, and
theme. Do not import `@pierre/diffs` or `@pierre/trees` in feature components; extend
a wrapper with a small prop instead. Check <https://diffs.com/> and
<https://trees.software/> before changing wrapper options.

## Tokens and semantics

Use semantic variables, not hard-coded values. Accent colors carry meaning, not
decoration:

- green: success, open, ready
- amber: pending, draft, warning
- purple: merged, waiting, workflow-secondary status
- red: failure, conflict, destructive status
- blue: focus, active controls, informational emphasis
- teal: workspace/worktree-linked state

Never render a serialized payload string as UI text. Parse it in a named util and show
labeled stats, with unabbreviated values in a `title` abbreviated by significant digits,
not fixed decimals (`frontend/src/lib/utils/roborev-usage.ts`).

## Implementation guidance

- Never add side stripes: no colored left/right border or inset edge shadow for
  selection, activity, emphasis, or quotes. Show selection with a background tint.
- An existing stripe must square both corners on that side; kit-ui-check's
  `rounded-side-stripe` rule cannot be ignored and counts radii from other selectors.
- A `$state` record reassigned whole (`x = { ...x, k: v }`) and read in the same
  reactive scope (`$effect` or `{@attach}`) loops forever; mutate the key (`x[k] = v`).
- Name reused or cross-module domain shapes near their owning module. Routed item
  identity and URLs come from `frontend/src/lib/routes.ts`; never repeat
  `{ owner; name; number; platformHost }` or hand-build `/pulls`, `/issues`, `/focus` URLs.
- When TypeScript complains, make the owning type more precise; never add
  `as unknown as`, broad `as any`, defensive `instanceof`, or normalization around
  already-typed API data.
- Assertions belong only at real boundaries (DOM targets, `JSON.parse`, incomplete
  third-party types, fixtures, browser globals). A wrong API schema is fixed in
  Go/Huma/OpenAPI and regenerated, not validated around.
- Never probe promise shape (`typeof result.then === "function"`); type the contract
  `void | Promise<void>`. Share browser feature checks in one helper.

### Markdown

- Use `renderMarkdown` for rendered descriptions and comments so fenced code is
  Shiki-highlighted. `renderMarkdownSync` and `renderMarkdownBlocks` stay independent
  of highlighter state and render plain fences.
- Shiki work is bounded per render; past the fence, language, or size budget, fences
  render as escaped plain code.
- Shiki inline styles are trusted only when generated in the current sanitization
  pass; raw markdown HTML never retains style attributes.
- Rich Markdown diff preview anchors review cards only at confidently mapped container
  boundaries; uncertain threads stay visible in fallback, never at a guessed position.
- The markdown pipelines stay app-side rather than kit `createMarkdownRenderer`,
  because they need renderer overrides and DOMPurify hooks beyond kit's extension surface.
- Both renderers import kit's fence primitives (`escapeHtml`, `codeFenceLanguage`,
  `codeHighlightPlan`, `allowedShikiStyle`); never reintroduce local copies.
- `escapeHtml`'s double-quote escaping is load-bearing for double-quoted attribute
  interpolation in both renderers.
- Mermaid is kit-owned and kit renders only its exact peer pin, so
  `frontend/package.json` pins the same `mermaid` and moves it only with kit-ui.

### Responsive layout

- Separate presentation mode from sizing mode. Compact/focus presentation removes
  sidebars and dense chrome when width is short; phone sizing applies only to
  phone-like contexts (coarse pointer, mobile UA, forced mobile).
- Never use one broad "phone viewport" predicate for both, or desktop-narrow windows
  inherit mobile typography and touch geometry.
- Shared breakpoints are defaults, not overflow overrides: max-content toolbars stack
  at the first width that contains their controls, pinned by browser geometry.

### Adding styling and shared components

- Before adding styling, check for an existing primitive and extend it with a semantic
  variant. Local CSS is fine for context-specific color or placement, never for
  repeated geometry.
- Add or promote a shared component when the same geometry or semantic control
  recurs across surfaces; never for a one-off visual detail.
- Adding a shared UI component, or materially changing one's intent, updates this
  document in the same turn: its purpose, when to use it, and the duplication it
  prevents, not implementation notes.
- Changes to shared primitives or visible interaction patterns add or update
  regression coverage, preferably at the user-visible flow.
