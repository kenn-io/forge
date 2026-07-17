# UI Design System

Use this document as the intent-level guide for frontend UI work in `middleman`. It should stay short, stable, and useful in model context.

## Purpose

- Keep the app visually coherent.
- Reuse shared primitives by default; one-off styling is a last resort.
- Extend semantic tokens and components instead of duplicating UI geometry.

## Design intent

`middleman` is a dense maintainer tool, not a marketing surface.

- Layouts should feel compact, deliberate, and information-rich.
- Visual emphasis should come from hierarchy and semantic color, not oversized controls or decorative effects.
- Light and dark themes should express the same UI language through shared tokens.

## Sources of truth

- Tokens: `@kenn-io/kit-ui/theme.css` and `@kenn-io/kit-ui/mermaid.css`
  (the `--mermaid-*`/`--viewer-scrim` tokens and diagram viewer chrome;
  both imported at the top of `frontend/src/app.css`) plus the
  middleman-specific tokens `app.css` defines on top (chrome, budget,
  kanban, review, verdict, diff, viewer glass controls)
- Shared primitives: `@kenn-io/kit-ui` first; app-specific compositions live
  in `packages/ui/src/components/shared/` and
  `frontend/src/lib/components/shared/`
- Diff/file-tree adapters: `packages/ui/src/components/diff/PierreFileDiff.svelte`
  and `packages/ui/src/components/diff/PierreFileTree.svelte`
- Routed item references and URL builders: `packages/ui/src/routes.ts`
- Svelte guidance: `skills/svelte-core-bestpractices/` (`svelte-core-bestpractices`) and `skills/svelte-code-writer/` (`svelte-code-writer`)
- Interaction contracts: `context/ui-interaction-contracts.md`
- Mobile UX principles: `context/mobile-ux.md`
- This guidance: `context/ui-design-system.md`

## kit-ui contract

middleman consumes `@kenn-io/kit-ui` as source, pinned to one commit SHA in
both `frontend/package.json` and `packages/ui/package.json` (bump both
together; never a `file:` path — bun's store keys by name@version and goes
stale). Its runtime deps are peers and its rune-module source cannot be
prebundled: keep it in vite `optimizeDeps.exclude` with transitive deps as
`"@kenn-io/kit-ui > <dep>"` includes. See kit-ui's `docs/migration.md` and
`docs/theming.md`. Invariants middleman relies on:

- Theme tokens come from kit `theme.css`; theming is `dark` /
  `high-contrast` classes on `<html>`. `@middleman/ui` additionally
  consumes app tokens from `frontend/src/app.css` and has no standalone
  theme — style-asserting harnesses must load `app.css` like
  `browserAppHarness.ts`.
- Type scale: rem tokens self-adjust on coarse pointers; `kit-type-touch`
  on `<html>` forces the touch scale. Never pin `html { font-size }`.
- Breakpoints are written in px (shared steps 640/760/900) — media-query
  `rem` resolves against the browser's 16px, not the app root.
- Spacing: `--space-1…8` = 2/4/6/8/12/16/24/32px. New or edited `gap`
  declarations use ladder tokens (both axes of shorthands); off-ladder px
  snaps to the nearest step, biased compact. On-ladder raw px in untouched
  code migrates opportunistically, not as churn.
- kit BEM classes are the sanctioned surface for parent `:global` styles
  and test selectors; a SHA bump that renames a class updates selectors in
  the same change.
- Chip: icons go in `children` (kit centers them), dropdown chevrons in
  `trailing`; no downstream `.kit-chip__label` overrides — repo chips
  depend on its ellipsis.
- Theme resolution: kit's theme store owns dark/light/system resolution
  and persistence (`middleman-theme` key); `theme.svelte.ts` adapts it. A
  host-forced mode applies classes directly and never persists via
  `setThemeMode`; an explicit user toggle persists even under a forced
  mode. Relative timestamps use kit `formatRelativeTime`;
  `parseAPITimestamp`/`localDate*Label` stay app-side.
- Dialogs: every dialog pushes a keyboard modal-stack frame. Background
  Escape surfaces cannot detect dialogs via `defaultPrevented` (kit's
  window listener registers late); they stand down when
  `getStackDepth() > 0`. kit `Modal`'s `closable` gates only the header X.
- Escape in overlay-hosted search: a non-empty kit `SearchInput` claims
  Escape to clear itself (stops propagation); an empty field lets it
  bubble so the hosting popover closes — every `SearchInput`-hosting
  popover must handle that bubbled Escape (`UserListEditor.test.ts` pins
  the flow).
- Palette, Cheatsheet, and the image lightbox keep hand-rolled focus
  traps and own their focus restore (the state stores' close functions;
  the lightbox's `restoreFocusTo`): close restores focus synchronously
  before the picked action runs, so an action's own focus move wins. kit
  `trapFocus` restores at unmount teardown, which would undo it.
- jsdom lacks `offsetParent` / `scrollIntoView` / `ResizeObserver`:
  `test/setup.ts` stubs the latter two, focus-trap tests install
  `stubOffsetParent.ts`, and synthetic Tab only exercises kit's trap at
  wrap boundaries.
- `CollapsibleSidebar`: middleman relies on the `kit-sidebar-layout*` BEM
  classes, `data-collapsed`, `SplitResizeEvent`, and `SidebarToggle`
  modifiers; the narrow floating overlay is kit-owned via the `overlay`
  prop — no app-side copies of its CSS.
- `StatusBar`: relies on `kit-status-bar*` classes and
  `--status-bar-height`; `overflow="visible"` lets BudgetPopover use kit's
  popover recipe; the app owns keeping bar text short.
- `TopBar`: renders the app header as `.app-top-bar`; tabs collapse by
  measurement. The header must clip its x-axis (`overflow-x: clip` — kit's
  hidden probe row otherwise inflates scrollWidth) and side regions must
  stay content-sized (a flex-stretched region poisons the frozen
  `expandUsed` footprint and blocks re-expansion). Select tabs via
  `.kit-top-bar__tabs .kit-top-bar__tab`, never the bare class.
  Provider-mode repo selector visibility must not move the tab row; non-provider
  modes reserve its footprint unless embed config hides it
  (`frontend/src/lib/components/layout/AppHeader.svelte::reserveProviderRepoSelectorSlot`).
- Flash: one shared store (`@middleman/ui/stores/flash`); kit `FlashBanner`
  mounts once per shell in a page-level fixed layer below measured shell chrome
  and above modal backdrops, never inside feature containers; headerless shells
  use the viewport edge (`frontend/src/App.svelte:968`).
- Commit timeline rows keep type, author, SHA, and relative time together in the
  compact header; the SHA is metadata, not card action content
  (`packages/ui/src/components/detail/EventTimeline.svelte`).

`kit-ui-check` gates at zero findings in both `make frontend-check` and the
Vite+ `frontend-check` task behind CI's `vp run -w check`. If a rule mistakes
application-owned UI for component debt, fix the rule rather than expanding
kit-ui or adding an ignore solely to silence the checker. New kit-ui behavior
requires an independently justified, reusable contract; checker cleanliness
alone is never justification.

## Shared primitives

### Chip

Use `Chip` and its semantic chip wrappers for compact status and metadata UI.

Intent:

- one shared geometry for small labeled UI
- consistent vertical alignment, spacing, casing, and density
- reusable across detail views, sidebars, and compact status surfaces
- semantic tone, dot, kind, and state semantics at the call site

Use it for:

- PR/issue state
- CI/review state
- repo and count badges
- other compact metadata markers

Use `Chip` directly when the caller already knows the semantic `tone`
(`success`, `warning`, `danger`, `info`, `merged`, `workspace`, `muted`,
or `neutral`) or needs the shared dotted status treatment. Use
`ItemKindChip` for PR/issue kind and `ItemStateChip` for PR/issue state
rather than repeating kind/state class maps in feature components.

Do not create new local `.badge`, `.pill`, `.tag`, or `.chip` geometry when
`Chip`, `ItemKindChip`, or `ItemStateChip` fits.

In this repo, the standard term is **chip**, not pill.

When a screen needs semantic chip color, extend `Chip` with a named tone class such as `chip--blue`, `chip--green`, or `chip--red` instead of redefining local badge geometry. Screens may keep legacy class names for test selectors during migration, but sizing, casing, and spacing should come from `Chip`.

### Tree Cells

Tree-like rows inside dense tables should preserve the table's scan line.
Keep IDs and primary row numbers in their normal column, and put disclosure
chevrons plus child indentation inside the content cell that owns the
hierarchy, such as a repo/ref or file-name cell. Do not use terminal/TUI
connector glyphs, branch-line borders, or extra ornamental strokes to draw the
tree. Indentation and a standard chevron are the affordance.

### ActionButton

Use `ActionButton` for repeated action styling.

Intent:

- one shared button model for tone, surface, and size
- semantic action styling instead of per-screen button CSS

If a new repeated button treatment is needed, extend `ActionButton` rather than creating another local button pattern.

### Modal primitives

- `Modal`
- `ConfirmDialog`
- `DialogButton`

### SidebarToggle

Use kit-ui's `SidebarToggle` (re-exported from `@middleman/ui`) for collapse and expand controls on left-side navigation rails.

Intent:

- one shared icon, size, hover, and accessible label contract for left-sidebar collapse affordances
- consistent expanded/collapsed direction across PR, issue, activity, and workspace sidebars
- avoid one-off SVG buttons or local `.sidebar-toggle` styling in each rail

Use it inside left sidebar headers and collapsed strips. Pass a specific label such as `Workspaces sidebar` when the generic `sidebar` label would be ambiguous. The resizable sidebar layout itself is kit-ui's `CollapsibleSidebar`; the container-width-driven floating overlay is requested through its `overlay` prop (hosts pass the container store's `isNarrow()` — kit's `overlayOnNarrow` media query is viewport-based, which is not the same signal).

### GroupedSidebarSection

Use `GroupedSidebarSection` for collapsible groups in PR, issue, and workspace list rails. Keep group chrome and the `--sidebar-*` surface/row-state tokens shared; domain-specific row content stays with its owner. Wrap large always-visible vertical scroll panes (list rails, diff area, pull/issue detail, activity views) in `ScrollBox` for consistent flex sizing, native vertical scrolling, and a labelled focusable region; bind `viewport` when a host needs imperative scroll logic, and note the scrolling element is the viewport, not the host's content wrapper class. Give each scroll area a concise accessible label so keyboard users can identify and scroll the region. (`packages/ui/src/components/shared/GroupedSidebarSection.svelte`, `ScrollBox` from `@kenn-io/kit-ui` — see kit-ui's `docs/components/scroll-box.md`, `frontend/src/app.css:39`)

### SplitResizeHandle and BottomDock

Use kit-ui `SplitResizeHandle` for horizontal and vertical pane dividers,
including handles inside application-owned recursive trees. The app retains
tree topology, ratio/size bounds, state, and persistence; the shared handle owns
pointer/keyboard interaction and separator semantics. Pass a specific label
such as `Resize Activity rail`.

Use kit-ui `BottomDock` for resizable inline bottom panels. The app owns whether
the dock is open plus its domain header/body/footer content; the shared dock
owns shell geometry, top-edge resizing, bounds, close control, and body
scrolling.

### Styling shared components

Treat component props, documented CSS custom properties, and the shared root
class as the supported styling contract. Prefer wrapping a component with an
application layout element or setting a public custom property over reaching
into child markup.

An inner selector such as `.kit-typeahead__trigger` or
`.kit-checkbox__label` is allowed only when the installed component has no
public hook for a required application layout, the dependency is pinned, and
the affected computed layout or interaction has browser coverage. Keep such
selectors scoped below an application-owned class; never use an unscoped
global override. Re-audit these selectors whenever the kit-ui revision moves.

### TabbedPanelTree

Use `TabbedPanelTree` for VS Code-like panel workspaces: tab groups that can
reorder tabs, drag tabs into another group, split a group horizontally or
vertically, and resize split panes.

Intent:

- one shared interaction model for draggable, tabbed, splittable panel groups
- let callers provide arbitrary panel content, tab icons, and tab action buttons
- keep dedicated sidebar resizing on `SplitResizeHandle` instead of forcing
  every two-pane layout into a tabbed workspace model

Use it when a surface needs multiple interchangeable panels or future panes
inside a draggable workspace. Do not use it for simple fixed sidebars,
single-purpose drawers, or file-tree/content splits where `SplitResizeHandle`
or a narrower layout primitive is enough.

Use neutral `tabbed-panel-*` DOM classes/selectors for tests and consumers.
Do not add workflow-specific aliases or compatibility selectors when moving
this primitive into new surfaces.

Pass the mutation callbacks that match the interactions you expose:
`onMoveTabBefore`/`onAppendTabToLeaf` for tab sorting and cross-group moves,
`onSplitTab` for edge drops, and `onRatioChange` for divider resizing. Omitted
callbacks make that interaction read-only instead of rendering a visual drop
target that cannot apply.

The current accessibility scope is labeled tab groups, focusable tabs and tab
actions, and labeled pointer resize handles. Keyboard tab reordering, keyboard
splitting, and keyboard resizing are not implemented here; extend
`TabbedPanelTree` first if a consumer needs those interactions.

### SelectDropdown

Use `SelectDropdown` for single-value selection controls in the UI.

Intent:

- one custom dropdown visual language matching header controls
- avoid mixing browser-native select styling with custom app dropdowns
- keep selection affordances consistent across detail headers, filters, and compact command surfaces

Do not add native `<select>` controls for visible app UI; use `SelectDropdown` instead. This is enforced by `frontend/src/no-native-select.test.ts`, which scans the component source trees and fails when a native `<select>` element is reintroduced. There is no allowlist or per-component exemption: if `SelectDropdown` cannot express a case, extend the primitive rather than reaching for a native `<select>`.

### Overlays

Use shared overlay primitives for dropdowns, popovers, menus, tooltips, and similar floating controls.

Intent:

- overlays should float above panes, sidebars, drawers, resize handles, and scroll containers
- overflow-constrained parents must not clip menus or hide available choices
- repeated positioning, collision, z-index, and outside-click behavior belongs in the shared primitive, not local screen CSS

Before placing an overlay inside a split view, compact sidebar, drawer, or scrollable region, verify that it can extend past its trigger container without being cut off.

Popover surface chrome (background, border, radius, shadow) comes from `kit-popover-card`; do not re-declare it in component-scoped styles. Scoped rules outrank the kit class, and a `var()` referencing an undefined token (there is no `--bg-elevated`) computes to transparent with no build-time error.

### GitHubLabels

Use `GitHubLabels` for actual GitHub labels.

Intent:

- keep repository labels distinct from generic status chips
- preserve GitHub-label semantics without collapsing them into a generic badge system

### Pierre Diff And File Tree

Use the local Pierre wrappers for changed-file UI:

- `PierreFileDiff.svelte` wraps `@pierre/diffs`
- `PierreFileTree.svelte` wraps `@pierre/trees`

Intent:

- keep Pierre lifecycle, Shadow DOM styling, theme selection, and selection/context
  behavior in one place
- let consumers pass app-level data such as `DiffFile[]`, selected path,
  word-wrap state, and demand-loaded file text callbacks
- avoid reimplementing direct `FileDiff` or `FileTree` setup in each files view

Reference the upstream docs before changing wrapper options or behavior:

- `@pierre/diffs`: <https://diffs.com/>
- `@pierre/trees`: <https://trees.software/>

Do not import `@pierre/diffs` or `@pierre/trees` directly in feature
components unless the existing wrappers cannot express the use case. Prefer
extending the wrappers with a small app-level prop over copying Pierre setup,
theme overrides, or Shadow DOM CSS into another component.

## Tokens and semantics

Use semantic variables instead of hard-coded values whenever possible.

- Surfaces and borders come from the app token set in `frontend/src/app.css`
- Text uses the shared primary / secondary / muted hierarchy
- Accent colors carry meaning, not decoration

Default color intent:

- green: success, open, ready
- amber: pending, draft, warning
- purple: merged, waiting, workflow-secondary status
- red: failure, conflict, destructive status
- blue: focus, active controls, informational emphasis
- teal: workspace/worktree-linked state

## Implementation guidance

When editing Svelte components, use the Svelte skills `skills/svelte-core-bestpractices/` (`svelte-core-bestpractices`) and `skills/svelte-code-writer/` (`svelte-code-writer`) alongside this document.

For TypeScript/Svelte state and routing contracts, avoid anonymous object type literals when the shape represents a domain concept that is reused or exposed across modules. Name shared item identity shapes, route payloads, embed callbacks, and API view models near the module that owns the concept, then import those types at call sites. PR/issue/file/focus route identity and URL construction belongs in the shared route item module at `packages/ui/src/routes.ts`; the frontend router remains the browser-location adapter over those builders. New routed item callers should use those named refs and builders instead of repeating `{ owner; name; number; platformHost }` shapes or hand-building `/pulls`, `/issues`, or `/focus` URLs.

When TypeScript complains, prefer making the owning type more precise over adding call-site assertions. Generated OpenAPI types, named domain unions, and shared option arrays should carry their real values so components can consume them directly. Good cleanups look like `handleCommandResult(result: void | Promise<void>, ...)`, `KanbanColumn` receiving `id: KanbanStatus`, or a typed dropdown option returning `TimeRange`; they remove runtime probing and casts by tightening the contract. Bad cleanups add `as unknown as`, broad `as any`, defensive `instanceof` branches, or response-normalization functions around data that is already typed by the API schema.

Use assertions only at real boundaries: DOM event targets, `JSON.parse`, third-party libraries with incomplete types, test fixtures, and browser globals. Keep those assertions local and obvious. Do not turn a simple input handler into a defensive branch when the markup already owns the element type; likewise, do not add runtime validation around a generated API response unless the schema is wrong. If the schema is wrong, fix the Go/Huma/OpenAPI source and regenerate clients.

For repeated async or event patterns, prefer a small typed helper over repeated structural checks. Never check promise shape with `typeof result.then === "function"`, `then?: unknown`, or similar maybe-thenable probes. If the value may be async, make the contract `void | Promise<void>` and use the promise methods through that type; if the value is a browser API promise such as `document.fonts.ready`, use the typed API directly. Do not duplicate browser API feature checks across components if a shared helper can express the actual browser boundary.

Shared markdown rendering has an async highlighted path and plain synchronous fallbacks. Use `renderMarkdown` for normal rendered descriptions and comments so fenced code blocks can be highlighted by Shiki with the declared fence language. Keep `renderMarkdownSync` and `renderMarkdownBlocks` independent of highlighter state; they intentionally render plain code fences for pending UI and rich-preview slicing. Shiki work is bounded per render; once the fence, language, or code-size budget is exceeded, additional fences render as escaped plain code. Shiki inline styles are trusted only when generated by the renderer during the current sanitization pass. Raw markdown HTML, even if it uses Shiki class names, must not retain style attributes.

The markdown pipelines deliberately stay app-side rather than moving to kit-ui `createMarkdownRenderer`: interactive task lists and docs link/image rewriting need marked renderer overrides, docs external-image blocking needs an element-level DOMPurify hook, and the drag handle needs the non-data `draggable` attribute — all beyond kit's extensions/codeFence/data-\* hook surface. This applies to the two renderers (`packages/ui/src/utils/markdown.ts` and the docs renderer) plus the markdown DOM-diff surface (`markdown-diff.ts`), which diffs already-rendered HTML and owns no render or escaping invariants of its own. The fence primitives that do fit (`escapeHtml`, `codeFenceLanguage`, `codeHighlightPlan` and its budgets, `shikiStyleIsAllowed`) are imported from `@kenn-io/kit-ui/utils/markdown` in both renderers so highlight budgets and escaping stay in parity by construction; do not reintroduce local copies. Mermaid is fully kit-owned: both renderers route fences through `mermaidCodeFence`, and `frontend/src/main.ts` wires kit's `initMarkdownMermaidRendering` (from `@kenn-io/kit-ui/utils/markdown-mermaid`, viewer classes `kit-mermaid-*`) into the app modal stack via `onLightboxOpen`; the diff image panel is kit's `ImagePreview`. New deps reached through the excluded kit-ui source barrel (mermaid, new lucide icons) must be added to `optimizeDeps.include` in `frontend/vite.config.ts`, or the cold optimizer re-bundles mid-run and breaks the browser test tier. `escapeHtml`'s double-quote escaping is a load-bearing contract for double-quoted attribute interpolation in both renderers, pinned by the docs suite's attribute-escaping tests. The invariants the boundary protects — task index stability, style stripping, external-image blocking, mermaid bypass, highlight budgets — are covered by `packages/ui/src/utils/markdown.test.ts`, `frontend/src/lib/utils/markdownTaskListStyle.test.ts`, and the docs markdown suite.

Responsive layout work should separate presentation mode from sizing mode.

- Use compact/focus presentation to remove sidebars, split panes, or dense chrome when the available width is too small.
- Use phone/mobile sizing only for phone-like contexts, such as coarse pointer, mobile user agent, or explicit force-mobile test paths.
- Do not use one broad "phone viewport" predicate for both decisions. That makes desktop-narrow windows inherit oversized mobile typography, action grids, and touch-only geometry.
- When a compact canonical route reuses focus presentation, keep desktop-scale tokens unless the environment is phone-like.
- Shared breakpoints are defaults, not overflow overrides: max-content toolbars
  must stack at the first width that contains their controls, with the boundary
  pinned by browser geometry (`frontend/src/lib/components/repositories/RepoSummaryPage.svelte::.repo-page__toolbar`).

Before adding UI styling:

1. Check whether an existing shared primitive already expresses the pattern.
2. If yes, extend that primitive with a semantic variant rather than duplicating layout CSS.
3. If no, add a shared component only when the pattern is clearly reusable.

Local CSS is acceptable for context-specific color or placement. Local CSS should not re-define repeated geometry that belongs in a shared primitive.

## When to add a shared component

Add or promote a shared component when:

- the same UI geometry appears in multiple places
- the same semantic control exists in both list and detail surfaces
- future work would otherwise copy and paste the same styling

Do not create a shared primitive for a one-off visual detail.

## Maintenance rule

If you add a new shared UI component, or materially change the intent of an existing one, you must update `context/ui-design-system.md` in the same turn.

The document should describe:

- what the component is for
- when to use it
- what UI duplication it is meant to prevent

It should not turn into implementation notes or a style dump.

## Testing expectation

When UI work changes shared primitives or visible interaction patterns, add or update regression coverage, preferably at the user-visible flow where the duplication or inconsistency previously appeared.
