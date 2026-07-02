# Keybindings, Command Palette, and Discovery Design

## Goal

Replace the ad-hoc keyboard shortcut layer in middleman with a unified system that gives keyboard-first users a fast way to navigate and act on PRs while making the same shortcuts discoverable for users who do not know them yet.

## Scope

The design covers three frontend surfaces:

- A unified command palette opened with a global shortcut. Fuzzy search across the PRs and issues currently loaded in middleman's stores, plus named commands. The palette does not fetch or page; if a PR is not in the active filter set, it does not appear in palette results — the user changes the filter to bring it into view. Optional prefixes scope the result set. Repo and workspace palette search are deferred to a follow-up because their data is currently component-local rather than in shared stores; see Out of scope.
- A read-only cheatsheet listing every keyboard shortcut active in the current view, opened with a help key.
- Inline keyboard hint badges next to global-shortcut-having buttons, tabs, and menu items.

It also defines the shared registry, dispatcher, and modal-stack semantics that all three surfaces use.

The design does not include rebinding (custom keybindings), new product behaviors beyond what the existing UI exposes, or a touch-optimized palette. Those are noted as out of scope for this version.

## User Experience

### Palette

A single keystroke opens a centered modal. The default openers are `Cmd/Ctrl+K` (primary) and `Cmd/Ctrl+P` (alias). The cheatsheet and inline kbd hints render the primary glyph (K); the alias is documented but not displayed alongside.

The `Cmd/Ctrl+P` alias is a default product decision and a deliberate browser-convention violation: the keystroke that normally opens the browser print dialog opens the middleman palette instead. v1 ships this binding for everyone — there is no per-user opt-out because customization is out of scope. The tradeoff is acceptable for a focused power-user dashboard whose users have OS-level menu access to print, and it satisfies the explicit topic owner request that motivated the design. Users who dislike it can ignore P and use K; a future customization release (separately scoped) will let them remove the alias entirely.

The modal is `~920px` wide and `~480px` tall over a dimmed backdrop. The body has two columns:

- A `~360px` results column on the left. Top: a search input with placeholder text that reflects the current scope and the loaded-only constraint (for example, "Search loaded PRs, issues, commands…"). Below: results grouped by type. Group order: Commands, Pull requests, Issues. Each row shows a type icon, the title or label, secondary text (repo for PRs and issues; scope for commands), and the kbd glyph for actions that have a default binding. When the user types a query that matches nothing in the loaded set, the empty-state row reads "No matches in loaded items" so the loaded-only behavior is visible at the moment of search.
- A `~560px` preview column on the right. The preview reflects the highlighted result. PRs show title, repo, state, last activity, and a body excerpt. Issues show the same structure. Commands show the action label, current scope ("Available when a PR is selected"), and a description of what the action will do, including which existing button or modal it routes through if applicable.

The footer is full-width and lists the navigation keys: arrows to navigate, return to run, escape to close.

Optional prefixes narrow the result set:

- `>` shows only commands.
- `pr:`, `issue:` scope the search to one type.

Prefixes for `repo:` and `ws:` are reserved for the follow-up that adds shared repo/workspace stores; they are not implemented in v1. When a user types `repo:` or `ws:` in v1, the palette treats the colon as the prefix delimiter and shows a single "Repo/workspace search is not available yet" placeholder row in the result column. The placeholder is informational only: it cannot be highlighted, cannot be selected with `Enter`, does not render a preview pane, is excluded from the recents schema, and does not count toward the per-group or combined result caps. The remainder of the typed string after the colon is ignored. This keeps the prefix surface stable so users who learn it now do not get unexpected results when the follow-up lands.

A future "assigned to me" prefix is noted under Out of scope; implementing it requires platform-credential login resolution that does not currently exist in middleman.

### Search data sources

Palette search runs entirely against state already loaded in the frontend through existing shared stores; v1 does not introduce new API endpoints, fetch paths, or shared stores. The sources per result group:

- **Pull requests:** the `pulls` store (`packages/ui/src/stores/pulls.svelte.ts`). Whatever PRs are currently materialized for the active filter set are searchable. The palette does not trigger additional sync.
- **Issues:** the `issues` store (`packages/ui/src/stores/issues.svelte.ts`).
- **Commands:** the registry — both the default catalog (`actions.ts`) and any view-scoped or detail-scoped registrations live in the same store.

Repo and workspace search are not in v1; their data is component-local today (in `RepoTypeahead` and `WorkspaceListSidebar`), and extracting shared stores belongs to a separate follow-up.

Result limits and behavior:

- Per-group result cap: top 10 by fuzzy-match score within each group.
- Total combined cap across groups: 30 visible (10 commands + 10 PRs + 10 issues). When repo/workspace search lands, the cap rises to match the new group count.
- Input debounce: 80 ms on the search input before re-filtering, to keep typing responsive on large local datasets.
- Loading state: while `pulls.isLoading()` returns `true` (the existing getter on `pulls.svelte.ts`), the Pull requests group shows a one-line skeleton; same for the Issues group while `issues.isLoading()` returns `true`. Other groups render immediately.
- Empty state for a typed query: a single "No matches" row spans the result column. Recents do not show during a non-empty query.
- Error state: errors during PR fetch are already routed to the existing flash store from `pulls.svelte.ts`; the palette does not duplicate that surface. The Issues store additionally exposes `issues.getIssuesError()`; when non-null, the Issues group shows a single "Issues unavailable — see toast" row instead of skeleton or results, and the other groups stay searchable. Implementations that want a symmetric pulls error getter should add it to `pulls.svelte.ts` rather than introduce palette-local error state.

When the input is empty, the palette shows the eight most recently selected items followed by all currently-applicable commands grouped under Commands. Recents persist in `localStorage` under the key `middleman-palette-recents` with this shape:

```ts
type RecentsState = {
  version: 1;
  items: Array<{
    kind: "pr" | "issue";
    ref: RoutedItemRef; // re-uses the existing routed-item ref shape from @middleman/ui/routes
    lastSelectedAt: string; // ISO 8601 UTC
  }>;
};

// Canonical owner: packages/ui/src/routes.ts, exported as `RoutedItemRef` from
// @middleman/ui/routes. The frontend app shell imports it; recents code lives in
// frontend/src/lib (app shell only — the @middleman/ui package never imports
// localStorage-bound state). The actual shape for v1's two kinds is:
//   {
//     itemType: "pr" | "issue";
//     provider: string;
//     platformHost?: string;       // omitted for the default GitHub host
//     owner: string;
//     name: string;                // repo name
//     repoPath: string;            // owner/name (or owner/group/name for GitLab)
//     number: number;              // PR or issue number
//   }
// Storing the full ref keeps navigation working after reload and keeps identity
// stable across hosts and providers. Dedupe uses kind + JSON.stringify(ref).
```

Behavior:

- Max 8 items. New selections push to the front; the oldest entry beyond the cap is dropped.
- Dedupe key: `kind + JSON.stringify(ref)`. Re-selecting an existing item moves it to the front and updates `lastSelectedAt` rather than adding a duplicate.
- Read path: malformed JSON, missing fields, or `version` mismatch → ignore the stored value AND immediately overwrite the key with an empty `RecentsState` so the next read does not waste a parse. Items whose `kind` is anything other than `"pr"` or `"issue"` are silently dropped on read (forward-compatible pruning so a future expansion can introduce new kinds without breaking older clients).
- Recents are intentionally per-browser (and per-profile within a browser). The recents key in `localStorage` is not synced across devices; a user logging into middleman from a different browser starts with empty recents. This matches the rest of middleman's UI preferences and is not surfaced as a setting in v1.
- Stale entries (kind/ref no longer resolvable against current stores) are filtered out at render time and pruned from `localStorage` on the next successful selection. If hydration fails after stale data was already shown, the affected rows are removed at the next render tick and the empty-state palette falls back to currently-applicable commands until hydration succeeds. The empty-state palette never shows unresolvable rows.

Selecting a content result navigates to that item. Selecting a command runs the action's handler, which routes through the existing UI flow (see Commands below).

Only commands whose `when(ctx)` currently returns `true` appear in the palette result column. Commands whose preconditions are not met (for example, "Approve PR" when no PR detail is open) are filtered out entirely rather than shown as disabled rows. The cheatsheet behaves the same way for its "On this view" category, so non-applicable commands never appear with a confusing disabled affordance.

Pressing the palette opener (`Cmd/Ctrl+K` or its alias `Cmd/Ctrl+P`) while the palette is already open closes it. `Escape` and clicking the backdrop also close.

### Cheatsheet

Pressing `?` when no editable target is focused opens a modal symmetric with the palette but read-only. Same dimensions and dim. The cheatsheet closes via `Escape` or clicking the backdrop. Unlike the palette's K/P toggle, `?` is not used to close — repurposing a printable character would prevent the user from typing `?` in the cheatsheet's filter input.

The cheatsheet has a top filter input and three categories:

- "On this view" lists shortcuts that are active because of the current page or detail context (for example, `j` and `k` on the PR list, `Esc` to close a modal).
- "Global" lists shortcuts that work everywhere (open palette, open cheatsheet, sync now, toggle theme, toggle sidebar).
- "Commands" lists palette-only commands that have no default binding.

Each row shows the action label, the kbd glyph (or "—" for palette-only commands), and an optional condition badge (for example, "PR selected").

### Inline kbd hints

A `KbdBadge` primitive renders the kbd glyph next to global-shortcut-having buttons in the existing chrome: the Sync button, the theme toggle, the sidebar toggle, and the palette opener (a small search-affordance in the header). Inline badges are visible on pointer devices and hidden on touch (`@media (pointer: coarse)`).

`KbdBadge` is a generic primitive that lives in `@middleman/ui` so consumer components can use it without importing app-specific registry code.

### Existing keystrokes

Existing global shortcuts continue to work and become entries in the registry. `j`/`k` navigate the PR or issue list; `f` toggles between the conversation and files tabs on a PR; `Escape` returns from a detail to its list; `1` opens `/pulls`; `2` opens `/pulls/board`; `Cmd/Ctrl+[` toggles the sidebar.

## Architecture

The middleman frontend has two source roots:

- `packages/ui/src/` — the `@middleman/ui` package, holding reusable components, stores, and types that the app and any embedded host consume.
- `frontend/src/lib/` — the app shell built on top of `@middleman/ui`.

This design touches both roots: a shared visual primitive lives in `@middleman/ui`, and app-specific orchestration lives in the app shell.

`@middleman/ui` adds:

- `components/keyboard/KbdBadge.svelte` — generic kbd glyph component. Picks `⌘` versus `Ctrl` based on platform via a `useKbdLabel` helper.

App-specific code follows the existing layout: components under `frontend/src/lib/components/`, stores under `frontend/src/lib/stores/`, utilities under `frontend/src/lib/utils/`.

`frontend/src/lib/components/keyboard/` adds:

- `Palette.svelte` — list+preview modal.
- `Cheatsheet.svelte` — read-only modal.

`frontend/src/lib/stores/keyboard/` adds:

- `registry.svelte.ts` — Svelte 5 rune-based store of action descriptors, indexed by owner.
- `dispatch.svelte.ts` — single window-level keydown listener with priority-aware dispatch.
- `context.svelte.ts` — derived context object built from existing stores (`getPage`, `getRoute`, `pulls.getSelectedPR`, etc.).
- `actions.ts` — the v1 default action catalog. Pure data plus handler references; no Svelte components or templates.

The existing `frontend/src/lib/utils/keyboardShortcuts.ts` `shouldIgnoreGlobalShortcutTarget` helper is reused inside `dispatch.svelte.ts`.

### Action descriptor

```ts
type ScopeTag =
  | "global"
  | "view-pulls" | "view-issues"
  | "detail-pr" | "detail-issue";

interface KeySpec {
  key: string;          // e.g. "k", "Escape", "ArrowDown"
  ctrlOrMeta?: boolean; // true matches both Ctrl on Linux/Windows and Meta on macOS
  shift?: boolean;
  alt?: boolean;
}

interface Context {
  page: ReturnType<typeof getPage>; // string union from frontend/src/lib/stores/router.svelte.ts
  route: Route;                     // from getRoute()
  selectedPR: PullSelection | null;     // from pulls.getSelectedPR()
  selectedIssue: IssueSelection | null; // from issues.getSelectedIssue()
  isDiffView: boolean;              // from isDiffView()
  detailTab: DetailTab;             // from getDetailTab()
}

interface Action {
  id: string;                 // dotted, e.g. "go.next", "pr.approve"
  label: string;
  scope: ScopeTag;
  binding: KeySpec | KeySpec[] | null;  // null = palette-only
  priority: number;           // default 0; higher wins when binding+scope tie
  when: (ctx: Context) => boolean;
  handler: (ctx: Context) => void | Promise<void>;
  preview?: (ctx: Context) => PreviewBlock;
}

interface CheatsheetEntry {
  id: string;                 // unique within owner; may match an Action.id when describing the same shortcut
  label: string;
  binding: KeySpec | KeySpec[]; // never null — display-only entries always have a visible key
  scope: ScopeTag;
  conditionBadge?: string;    // e.g. "PR selected", rendered as a chip on the cheatsheet row
}
```

Every `Action` with a non-null `binding` is automatically rendered as a cheatsheet row using its `id`/`label`/`scope`/`binding`. `registerCheatsheetEntries(ownerId, entries: CheatsheetEntry[])` is for *display-only* shortcuts whose handler stays inside an existing component (e.g. `RepoTypeahead`'s arrow-key nav, comment editor shortcuts) — those keystrokes never go through the registry's dispatch but still need to surface in the cheatsheet. Owner-based replacement/cleanup matches `registerScopedActions`. Cheatsheet-only entries do NOT participate in the dispatch conflict assertion (they have no handler), but the cheatsheet does deduplicate by `(scope, binding)` at render time. When an action-derived row and a `registerCheatsheetEntries` row collide on `(scope, binding)`, the action-derived row wins and the display-only entry is suppressed, regardless of registration order. This avoids flicker during dynamic mount/unmount and gives `Action` definitions authoritative rendering for any shortcut they cover.

### Registration

`registerScopedActions(ownerId, actions)` returns a cleanup function. The registry indexes entries by `ownerId`. Cleanup removes only that owner's entries. Re-registration with the same `ownerId` atomically replaces only that owner's entries. The default catalog uses `ownerId="app:defaults"`.

Components register their action sets from a Svelte 5 `$effect` and return the cleanup. Per-component handlers that already exist (the arrow nav in `RepoTypeahead`, modal `Escape` handlers, comment editor shortcuts) keep their internal handler logic. Those components additionally call `registerCheatsheetEntries(ownerId, descriptors)` so the cheatsheet's "On this view" category lists them. Same `$effect` cleanup discipline.

### Modal stack

`pushModalFrame(frameId, actions)` pushes a frame onto a stack and returns a `popFrame` callback. The frame contains its own input-capturing action set. Components that open modals or palette-like overlays push a frame on open and pop on close. The modal inventory splits by introduction time:

- Existing overlays, wired in stage 3: `MergeModal`, `RepoImportModal`, `RepoIssueModal`, `IssueDetail`'s confirm sub-modal, and `ShortcutHelpModal`.
- New overlays introduced by this design, wired in their introducing stage: `Palette` (stage 6), `Cheatsheet` (stage 9).

Any new modal added to middleman after this design lands must follow the same pattern. The stage 3 PR is the authoritative inventory point for existing overlays; the introducing-stage PR is authoritative for new ones.

Lightweight popovers that genuinely should pass shortcuts through to the global registry do not push a frame. They handle their own keys inline.

### Dispatch

A single `window.addEventListener("keydown", …)` consults the registry on every event:

1. If the modal stack is non-empty, walk it top-down. The first frame whose action matches the event runs and dispatch returns. If no frame's action matches, the event is still considered consumed by the topmost frame for app-level dispatch — global registry handlers do NOT fire while a modal is open.
2. When the modal stack consumes the event, `preventDefault()` is called only if (a) an action handler matched and ran, or (b) the key is on a small reserved-while-modal-open list: `Cmd/Ctrl+K` and `Cmd/Ctrl+P`. Other unmatched keys (including `?`, `Escape`, arrow keys, `Enter`, `Tab`) flow normally so text input inside modals — typing `?` or newlines in a textarea, moving the caret with arrows, cycling focus with Tab — keeps working. The reserved list exists only to suppress browser-conflicting defaults (address bar focus, print dialog) on modifier-bearing keys, where the user could not have meant to type a literal character. Default bindings deliberately avoid other common browser shortcuts (`Cmd/Ctrl+W`, `Cmd/Ctrl+T`, `Cmd/Ctrl+R`, `Cmd/Ctrl+L`, `Cmd/Ctrl+N`, `Cmd/Ctrl+S`, `Cmd/Ctrl+F`) so the reserved list does not need to grow. Modals that need Escape, Enter, arrow, or `?` behavior register an explicit action in their frame so dispatch step (a) fires.
3. If the modal stack is empty, run `shouldIgnoreGlobalShortcutTarget(event.target)`. If true, only modifier-bearing global shortcuts (`ctrlOrMeta` set on the action's `binding`, or `alt` set) can fire — every other shortcut stops here. The rationale: a modifier-bearing keystroke could not have been a literal character the user meant to type into an input, so the open-palette shortcut works from a comment editor. Bare-character shortcuts (`j`, `k`, `f`, `?`, `1`, `2`) stay suppressed inside editable targets.
4. Walk the global registry. Filter to entries whose `binding` matches the event and whose `when` returns true against the current context (and, when step 3 indicated an editable target, whose binding is modifier-bearing). Sort the surviving set by scope specificity (`detail-pr` beats `view-pulls`; `detail-issue` beats `view-issues`; view-scoped beats `global`), then by `priority` descending, then by registration order. Run the first survivor's handler and call `preventDefault()`. Modal-frame actions never participate in this walk — they are dispatched exclusively by step 1.

### Conflict assertion

The registry checks for conflicts on static fields only — never on `when` predicates, which are purely a runtime gate:

- The check runs at startup over the default catalog and again on every `registerScopedActions` replacement so dynamic registrations (PR detail commands, view-scoped actions) are validated as they mount.
- For every (binding × scope-tag × priority) tuple within the same scope tag, if more than one entry exists across all current owners, fail with an explicit error in development and test, and emit a loud warning in production. The error names both colliding actions and their owner ids.
- Cross-scope-tag overlaps are allowed and resolved by the dispatch precedence above.
- In production, dynamic conflicts log a warning and fall through to dispatch precedence (priority desc, then registration order). Dev/test is the primary defense, so production never silently changes behavior on a conflict; the warning surfaces in browser dev tools so users can report it.

The default catalog is fixed and small enough that the startup assertion catches authoring mistakes; the dynamic check catches future registrations that accidentally collide with existing entries.

## Commands

The v1 command catalog routes through existing UI flows. No command introduces new product behavior.

Global commands (active everywhere, scope `global`):

- Sync repos — calls the existing `sync.triggerSync()`.
- Toggle theme — calls the existing theme store's toggle.
- Toggle sidebar — wraps the existing sidebar toggle (already bound to `Cmd/Ctrl+[`; this just names it in the registry).
- Open settings — navigates to `/settings`.
- Open repos summary — navigates to `/repos`.
- Open reviews — navigates to `/reviews`.
- Open workspaces — navigates to `/workspaces`.
- Open design system — navigates to `/design-system`.
- Open palette — bound to `Cmd/Ctrl+K` (primary) and `Cmd/Ctrl+P` (alias).
- Open cheatsheet — bound to `?`.

View-scoped commands:

- `view-pulls` and `view-issues`: select next, select previous, escape to list, open file tab (`f` on `view-pulls`).
- The existing `1` and `2` shortcuts become `nav.pulls.list` and `nav.pulls.board`.

PR detail commands (scope `detail-pr`, `when` returns true only when a PR detail is selected):

- Approve PR — routes through the existing `ApproveButton` flow.
- Open merge dialog — routes through `MergeModal`.
- Mark ready for review — routes through `ReadyForReviewButton`.
- Approve workflows — routes through `ApproveWorkflowsButton`.

PR detail commands are NOT in the default catalog (`actions.ts`). They are registered by the PR detail view component when a PR is mounted, via `registerScopedActions("pr-detail-actions", […])`, and the cleanup returned from the `$effect` removes them on unmount.

Each command's `handler` AND its `when(ctx)` predicate are extracted from the existing button or modal so the palette command is available exactly when the corresponding button is interactive. For example: `ApproveButton.svelte` already gates its render or its disabled state on PR state (open vs. closed/merged), the viewer's permissions, draft status, and prior-approval state; the extraction yields a shared `canApprovePR(ctx)` predicate that both the button's render guard and the action's `when` use. The same applies to `MergeModal` opening (gated on mergeability, conflicts, branch protection), `ReadyForReviewButton` (only on draft PRs), and `ApproveWorkflowsButton` (only when there are pending workflows that need approval). When the button is hidden or disabled, the palette command is also absent. No availability predicate is invented for the palette: every gate is reused from the existing UI.

Each handler is a thin closure that invokes the same code path the existing button uses. Where the button currently inlines its mutation logic (for example `ApproveButton.svelte`'s `client.POST(.../approve)` call), the implementation extracts that logic into a small shared function (or method on the relevant store) that both the button and the registered action call. No mutation logic is duplicated; the existing dialog or button still drives confirmations, validation, error reporting, and state updates. Mutation guardrails are preserved.

For commands that open a modal (Open merge dialog), the handler sets the same modal-open state the button click sets. The modal then pushes its own modal frame per the Modal stack section.

## Implementation order

The work lands in stages so each layer is reviewable on its own. Earlier stages must be in place before later stages depend on them.

Each stage lands with the unit, component, and Playwright coverage that exercises the user-visible behavior the stage introduces — not deferred to a later cleanup pass.

1. **Registry + dispatch foundation.** `registry.svelte.ts`, `dispatch.svelte.ts`, `context.svelte.ts`, the `ScopeTag` enum, and the conflict assertion. Unit tests for registration, owner-based replacement/cleanup, dispatch precedence under specificity × priority × registration-order combinations, and the conflict assertion. No UI changes; no e2e in this stage because nothing user-visible changes.
2. **Migrate existing global shortcuts.** Replace `App.svelte`'s `handleKeydown` with a default catalog (`actions.ts`) of named actions. The window listener installed by `dispatch.svelte.ts` takes over. Existing behavior of `j`, `k`, `f`, `Escape`, `1`, `2`, and `Cmd/Ctrl+[` is preserved end-to-end — including the existing pre-modal-stack behavior with modals open. Playwright tests cover each migrated shortcut against the live app to prove no regression on the routes where they fire today. The intentionally-temporary modal-leakage behavior (background `j`/`k` still fire while a modal is open, same as pre-migration) is NOT covered by a durable e2e assertion in this stage, because stage 3 will replace that behavior immediately. Stage 3 introduces the modal-isolation e2e.
3. **Modal stack + integration with existing modals.** `pushModalFrame`/`popFrame`. Wire every existing modal-like overlay (`MergeModal`, `RepoImportModal`, `RepoIssueModal`, `IssueDetail`'s confirm sub-modal, `ShortcutHelpModal`) to push/pop. Lightweight popovers like `BudgetPopover` keep their existing inline keydown handlers and do not push a frame because they are not full input-capturing layers. The stage's PR description must include the resulting inventory so reviewers can verify nothing was missed. Existing inline keydown handlers stay; only the open/close lifecycle gains the frame calls. The behavior contract is covered by a single Playwright table-driven test that opens each modal in the inventory in turn and asserts background-view shortcuts (`j`, `k`, etc.) do not fire while it is open — one row per modal, sharing setup, cheaper than five separate full-page tests. The push/pop wiring on the modal-stack store itself (its own internal state) is covered by direct unit tests in the store module.
4. **Cheatsheet entries from per-component handlers.** Components like `RepoTypeahead` add `registerCheatsheetEntries` calls so their existing local shortcuts can be listed in the cheatsheet later. No user-visible change yet (cheatsheet UI is stage 8); component tests assert the entries are registered/unregistered with mount/unmount.
5. **`KbdBadge` primitive.** Land the shared component in `@middleman/ui` and use it next to the Sync, theme toggle, and sidebar toggle buttons in the existing chrome. Component tests for platform glyph (`⌘` on macOS, `Ctrl` on Linux/Windows) and screen-reader label. Playwright verifies the badge is visible next to the Sync button on a pointer device.
6. **Palette shell + open/close.** `Palette.svelte` skeleton with the list+preview layout, the `Cmd/Ctrl+K`/`Cmd/Ctrl+P` openers, the modal frame push/pop, and the toggle-close behavior. No search yet — empty list column, empty preview pane. Playwright covers open via Cmd/Ctrl+K, close via Escape and via the same opener again, focus restoration on close, focus-trap via tab cycling inside the open palette, and that the modal frame blocks background `j`/`k` navigation while open.
7. **Palette search and previews.** The fuzzy filter, prefix parsing (`>`, `pr:`, `issue:`, plus the reserved-prefix placeholder for `repo:`/`ws:`), per-group result caps, debounce, the preview pane content for each result type (PRs, issues, commands), and the empty/loading/error states. Wires up the existing `pulls` and `issues` stores as search sources. Component tests cover prefix narrowing, the reserved-prefix placeholder, and preview rendering. Playwright covers: search-and-select for a PR; search-and-select for an issue; the `>` prefix filtering to commands only and executing at least one safe global command (Open settings or Toggle theme) that proves the palette → handler path works end-to-end; the reserved `repo:` prefix shows the placeholder row; and typing inside the search input does not fire global shortcuts.
8. **Recents persistence.** `localStorage`-backed recents per the `RecentsState` schema. Empty-state palette renders recents + applicable commands. Unit tests cover the schema (dedupe, cap, malformed-JSON handling, version mismatch, stale pruning). Component tests cover the empty-state render. Playwright covers the round-trip: select a PR, close palette, reopen, see it as the top recent.
9. **Cheatsheet modal.** `Cheatsheet.svelte` opening on `?`. Reads from the registry and from `registerCheatsheetEntries` per-view registrations. Pushes a modal frame on open, pops on close. Component tests cover category grouping and filter input narrowing. Playwright covers `?` to open, focus-restore on Escape, focus-trap via tab cycling, and that the migrated shortcuts and per-component entries appear in the right categories.
10. **PR detail commands.** PR detail view registers `pr-detail-actions` (Approve PR, Open merge dialog, Mark ready for review, Approve workflows) using shared closures extracted from the existing buttons. Each action's `when(ctx)` reuses the same availability predicate the corresponding button already enforces (extracted into the shared closure file alongside the handler). Unit tests cover the extracted closures and predicates. Playwright covers running each command from the palette (success path), verifying it does NOT appear in the palette when the corresponding button is hidden/disabled (PR closed/merged, draft state mismatch, no pending workflows, etc.), and a failure-path flash for at least Approve PR. A keyboard-only flow opens the palette, runs Approve PR via Enter, and verifies focus returns to the originating element on close.
11. **Coverage harmonization (optional).** Stage 11 only lands if cross-stage gaps actually surface during stages 6–10. Each earlier stage MUST fail review if its own coverage is missing — stage 11 is not a relief valve for deferring required coverage. Distribution of accessibility and focus-restoration coverage is already explicit:
    - Focus restoration on close: stage 3 (existing modals), stage 6 (palette open/close), stage 9 (cheatsheet open/close).
    - Focus trap via tab cycling: stage 6 (palette), stage 9 (cheatsheet).
    - Dynamic-conflict assertion under mount/unmount cycles: stage 1 unit tests.
    - Keyboard-only PR detail command flow: stage 10.

   When stage 11 lands, its PR enumerates the named cross-stage integration tests it adds (and only those). If a check fits naturally earlier, it goes there instead.

Stages 6 through 11 each depend on stages 1 through 3 being complete. Stage 7 depends on stage 6. Stage 10 depends on stage 7 (palette search must exist before PR detail commands are useful). Implementers should not start a later stage before its prerequisites land in main.

## Migration

`App.svelte`'s existing `handleKeydown` is removed. Its logic becomes a set of registered actions in the default catalog: `go.next`, `go.prev`, `tab.toggle`, `escape.list`, `nav.pulls.list`, `nav.pulls.board`, `sidebar.toggle`. Each action has the same `when` predicates the original code expressed inline (`page === "pulls"`, not on board view, etc.).

Per-component keydown logic stays where it is. Components gain two additive call types:

- Every component with a global-shortcut binding adds `registerCheatsheetEntries(ownerId, descriptors)` so its keys appear in the cheatsheet's "On this view" category.
- Components that open a modal or palette-like overlay — `MergeModal`, `RepoImportModal`, and any other ad-hoc modal in the existing UI — bracket their open/close lifecycle with `pushModalFrame`/`popFrame`. Existing internal keydown handlers (e.g. `Escape` to close in `MergeModal`) keep working; promoting any of them into the frame's action set is optional and only needed when `preventDefault` matters.

## Accessibility

The palette and cheatsheet are keyboard-first and must remain usable via screen readers and keyboard-only navigation:

- Both modals trap focus while open. The previously-focused element is restored on close.
- The palette uses `role="dialog"` with `aria-modal="true"` and an `aria-label` that names it ("Command palette"). The search input uses `role="combobox"`; the result list uses `role="listbox"` with `aria-activedescendant` pointing at the highlighted row's id; each row uses `role="option"` with `aria-selected` reflecting highlight state.
- The cheatsheet uses `role="dialog"` with `aria-modal="true"` and an `aria-label` ("Keyboard shortcuts"). Categories are sectioned with `<h3>` or equivalent and the filter input is announced as such.
- `KbdBadge` includes a screen-reader-only label expanding the shortcut (for example, "Command-K") so the kbd glyph is not announced as raw symbols.
- Tab order inside each modal cycles through interactive elements without escaping to background content. Backdrop is not in the tab order.

Component tests cover focus trap, focus restore, and active-descendant behavior. End-to-end tests cover keyboard-only invocation paths.

## Error handling

- Unknown action id during dispatch: log and return; never crash.
- Synchronous action handler throws: route the error through the existing `flash` store (toast). Dispatch survives so the next keystroke works.
- Async action handler rejects: the dispatcher attaches `.catch()` to every promise returned by a handler and routes the rejection through the same flash path as a synchronous throw.
- Toast text rule: if the thrown value is an `Error` whose `.message` is non-empty, use that message verbatim. Otherwise show the literal string `"Command failed"` and `console.error` the raw value with the action id so a developer can recover the original error from the dev console. The rule applies to both synchronous throws and async rejections so behavior is identical regardless of handler shape.
- Concurrent invocations: each action descriptor has an implicit per-id in-flight flag. While a handler's promise is pending, the same action is not dispatched again — the keystroke is consumed but does nothing visible. The flag clears when the promise resolves or rejects.
- Palette command lifecycle: the palette closes immediately when an action is invoked, before the handler resolves. Any error surfaces via flash, not via reopening the palette.
- Conflict at startup in dev/test: assertion failure with a clear message naming both colliding actions and their owner ids.
- Conflict at startup in production: loud `console.warn` naming both actions; the colliding entries fall through to the same dispatch precedence as any other equal-priority pair (priority desc, then registration order). "First-registered wins" follows from that ordering rather than being a separate rule. The dev/test path is the primary defense.

## Testing

- **Unit (Vitest)**: registry registration, owner-based replacement, owner-based cleanup, modal stack push/pop ordering, dispatch precedence under combinations of scope specificity × priority × registration order, conflict assertion on duplicate (binding, scope, priority) tuples.
- **Component (Svelte tests)**: Palette empty state shows recents and applicable commands; typing filters the list and updates the preview; prefixes narrow correctly; arrow keys move highlight; return runs the highlighted action; backdrop click closes; escape closes. Cheatsheet category grouping; filter input narrows. KbdBadge platform glyph rendering.
- **E2E (Playwright)**: open the palette via `Cmd/Ctrl+K`; type "approve"; arrow to a command; press Enter; verify the existing `ApproveButton` flow runs. Open the cheatsheet via `?`; verify `j`, `k`, `f`, `Escape`, `1`, `2`, and `Cmd/Ctrl+[` appear under "On this view" or "Global" with the right glyphs. Verify the inline kbd badge renders next to the Sync button. Verify that with the palette open, typing a single character into the search input does not trigger global shortcuts (e.g. `j` types `j` in the input rather than navigating). Verify that with the palette open, `Cmd/Ctrl+P` runs the palette's own handling rather than opening the browser's print dialog. Verify that a command whose handler rejects surfaces a flash toast (failure path), not a silent crash, and that the palette has already closed before the toast appears. Verify that closing the palette restores focus to whatever element was focused before opening, and that the same applies to the cheatsheet and to `MergeModal`.

End-to-end coverage is non-negotiable per the project's e2e policy.

## Out of scope for this version

- Customization (rebinding, removing, adding default keystrokes by users). The registry exposes a stable shape so a future rebinding feature can plug in without reshaping existing actions.
- Persistence of bindings (file, SQLite, or otherwise). Defaults are hardcoded; no storage is touched.
- Repo and workspace palette search. Both data sources are component-local in middleman today; making them searchable from the palette requires extracting shared stores, which belongs to a separate follow-up. Reserved prefix labels (`repo:`, `ws:`) are documented but unwired in v1.
- New product behaviors not currently exposed in the UI (open in GitHub, mark all read, cycle theme, reopen, close, reply-to-comment, an "assigned to me" filter that would require platform-credential login resolution).
- Touch-optimized palette UX. The palette is keyboard-first; touch users continue to use existing UI surfaces.
- Workflow-specific shortcuts that depend on a comment-thread selection model that does not yet exist.

## Success criteria

The implementation is done when:

- Every default keystroke that worked before this change still works (`j`, `k`, `f`, `Escape`, `1`, `2`, `Cmd/Ctrl+[`).
- Pressing `Cmd/Ctrl+K` or `Cmd/Ctrl+P` from any view opens the palette; pressing it again closes; Escape closes; backdrop click closes.
- Pressing `?` from any non-editable focus opens the cheatsheet; Escape and backdrop click close it.
- The cheatsheet lists every default and per-component-registered shortcut active in the current view, grouped under "On this view", "Global", or "Commands".
- Inline `KbdBadge` glyphs appear next to the Sync, theme toggle, and sidebar toggle buttons on pointer devices and are hidden on touch.
- Palette search returns results from the `pulls` and `issues` stores plus commands, with the documented prefix narrowing (`>`, `pr:`, `issue:`) working.
- PR detail commands (Approve PR, Open merge dialog, Mark ready for review, Approve workflows) run from the palette and route through the existing buttons/modals.
- Modal frames isolate background shortcuts; text input inside modals continues to work.
- Async command failures surface via the existing flash store; the palette closes before the toast appears.
- All described unit, component, and Playwright tests are present and passing in CI.
- Accessibility contract (focus trap, focus restore, ARIA roles, screen-reader labels for `KbdBadge`) is met for every modal introduced.
