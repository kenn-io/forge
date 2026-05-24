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

- Tokens: `frontend/src/app.css`
- Shared primitives: `packages/ui/src/components/shared/`
- Diff/file-tree adapters: `packages/ui/src/components/diff/PierreFileDiff.svelte`
  and `packages/ui/src/components/diff/PierreFileTree.svelte`
- Routed item references and URL builders: `packages/ui/src/routes.ts`
- Svelte guidance: `skills/svelte-core-bestpractices/` (`svelte-core-bestpractices`) and `skills/svelte-code-writer/` (`svelte-code-writer`)
- Interaction contracts: `context/ui-interaction-contracts.md`
- Mobile UX principles: `context/mobile-ux.md`
- This guidance: `context/ui-design-system.md`

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

### ActionButton

Use `ActionButton` for repeated action styling.

Intent:

- one shared button model for tone, surface, and size
- semantic action styling instead of per-screen button CSS

If a new repeated button treatment is needed, extend `ActionButton` rather than creating another local button pattern.

### LeftSidebarToggle

Use `LeftSidebarToggle` for collapse and expand controls on left-side navigation rails.

Intent:

- one shared icon, size, hover, and accessible label contract for left-sidebar collapse affordances
- consistent expanded/collapsed direction across PR, issue, activity, and workspace sidebars
- avoid one-off SVG buttons or local `.sidebar-toggle` styling in each rail

Use it inside left sidebar headers and collapsed strips. Pass a specific label such as `Workspaces sidebar` when the generic `sidebar` label would be ambiguous.

### SplitResizeHandle

Use `SplitResizeHandle` for draggable pane dividers in split views.

Intent:

- one shared drag lifecycle, keyboard resize behavior, hit target, cursor, focus, and hover treatment
- keep pane-specific width bounds and direction math in the owning layout
- avoid one-off splitter handles in sidebars, drawers, terminal panes, and diff views

Use it between resizable panes. Pass a specific accessible label such as `Resize Activity rail` or `Resize file tree`.

### SelectDropdown

Use `SelectDropdown` for single-value selection controls in the UI.

Intent:

- one custom dropdown visual language matching header controls
- avoid mixing browser-native select styling with custom app dropdowns
- keep selection affordances consistent across detail headers, filters, and compact command surfaces

Do not add new native `<select>` controls for visible app UI unless there is a platform-specific accessibility need that cannot be met by `SelectDropdown`.

### Overlays

Use shared overlay primitives for dropdowns, popovers, menus, tooltips, and similar floating controls.

Intent:

- overlays should float above panes, sidebars, drawers, resize handles, and scroll containers
- overflow-constrained parents must not clip menus or hide available choices
- repeated positioning, collision, z-index, and outside-click behavior belongs in the shared primitive, not local screen CSS

Before placing an overlay inside a split view, compact sidebar, drawer, or scrollable region, verify that it can extend past its trigger container without being cut off.

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

Responsive layout work should separate presentation mode from sizing mode.

- Use compact/focus presentation to remove sidebars, split panes, or dense chrome when the available width is too small.
- Use phone/mobile sizing only for phone-like contexts, such as coarse pointer, mobile user agent, or explicit force-mobile test paths.
- Do not use one broad "phone viewport" predicate for both decisions. That makes desktop-narrow windows inherit oversized mobile typography, action grids, and touch-only geometry.
- When a compact canonical route reuses focus presentation, keep desktop-scale tokens unless the environment is phone-like.

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
