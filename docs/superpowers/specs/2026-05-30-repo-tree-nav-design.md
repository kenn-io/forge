# Repo Tree Navigation for the Repo Selector

## Summary

Replace the flat repo list inside the header repo selector with a collapsible
`host -> owner -> repo` tree. Interior rows get two distinct affordances: clicking
the name expands/collapses the node, and a tri-state checkbox selects or deselects
every repo beneath it. Leaf repo rows toggle their own selection. The selector's
shell (trigger button, open/close, blur handling, filter input, keyboard cheatsheet)
is preserved.

The global filter contract does not change. Selection is still a comma-separated set
of leaf `platformHost/repoPath` strings written through `setGlobalRepo`. Group
selection expands to its member leaves before writing. No API, store-format, or
downstream list-view changes: this is a presentation and interaction layer over data
the selector already fetches.

## Decisions (locked)

- **Placement:** tree lives inside the existing dropdown, not a new persistent rail.
- **Build:** bespoke component. `@pierre/trees` (already a dependency, used by
  `PierreFileTree.svelte`) does not model checkboxes, tri-state/indeterminate, or
  cascade selection, and hardwires directory clicks to expand. The exact interaction
  this feature needs is the one thing the library works against. Its virtualization
  strength is irrelevant for a maintainer's fixed, small repo set.
- **Row anatomy:** name click = expand/collapse; tri-state checkbox = select subtree.
- **Tree shape:** fixed three levels (`host -> owner -> repo`). GitLab nested groups
  collapse into a single owner node whose label keeps the slashes
  (e.g. `platform/frontend`). Single-child grouping levels auto-flatten.
- **Filter behavior:** prune and auto-expand. Typing hides non-matching groups and
  force-expands groups containing a match. Clearing restores prior expansion.
- **Host rows** show the real provider logo via `ProviderIcon`. Owner and repo rows
  have no leading glyph: caret, checkbox, name only.
- **No per-row count badges.** The current selector has none; adding them is out of
  scope. Tri-state is carried by the checkbox alone.

### Deferred

- **Checkbox / glyph visual styling.** The locked contract is three visually
  distinct states (off / on / partial), checkbox toggles subtree selection, name
  toggles expand. The rendering technique (native checkbox with the `indeterminate`
  DOM property vs. a custom element with pseudo-element glyphs) and exact pixels are
  deferred to a styling pass against the running app, after the functional surface
  lands non-wedged. Any later styling must keep the three distinguishable states and
  use design tokens; it must not reopen behavior.

## Current State (what exists today)

- `frontend/src/lib/components/RepoTypeahead.svelte`: the selector. A trigger button
  ("All repos" / one repo / "N repos") that opens a filter `<input>` over a flat
  `<ul role="listbox">` of checkable `platformHost/owner/name` options. Owns
  open/close, blur-to-close, arrow/space/enter/esc keys, substring filtering with
  `highlightSegments` + `<mark>`, and cheatsheet registration.
- `frontend/src/lib/stores/filter.svelte.ts`: `parseRepoFilterValue` /
  `serializeRepoFilterValue` (comma-separated set), `getGlobalRepo` / `setGlobalRepo`
  persisted to localStorage key `middleman-filter-repo`.
- `packages/ui/src/stores/collapsedRepos.svelte.ts`: the persisted collapse-state
  pattern to mirror (per-surface `Set<string>` in localStorage, defensive try/catch).
- `frontend/src/lib/components/provider/ProviderIcon.svelte`: renders a provider
  logo from `simple-icons` by canonical provider name.
- `packages/ui/src/api/provider-routes.ts`: `ProviderRouteRef`, `canonicalProvider`.
- `internal/platform/metadata.go`: `AllowNestedOwner` is true for GitLab only; the
  others are flat. Confirms owner segments can contain slashes for GitLab.

A `RepoOption` today is `{ value, owner, name }` where
`value = ${platformHost}/${repoPath}` (repoPath falling back to `owner/name`). The
architecture below extends it with `provider` and `platformHost`.

## Architecture

All changes are frontend: a pure tree-building module, an expansion-state store, and
a recursive row renderer, plus edits to the existing component (where the derived
selection logic also lives).

### `repoTree.ts` (pure module)

`frontend/src/lib/components/repoTree.ts` (or `utils/`), framework-free and
unit-testable.

Input: `RepoOption[]`, extended so each option carries `provider` (the canonical
provider string) and `platformHost`. The component already has these at the source
(`optionFromRepo` / `optionFromConfigRepo`); thread them onto `RepoOption` rather
than re-parsing the value string.

Parsing each option (use the threaded `platformHost` field as the source of truth,
not the value's first segment). Note `value` is `${platformHost}/${repoPath}` today,
where the host has no slashes and `repoPath` may have several (GitLab groups):
- split `value` on the first slash: the head is the host, the tail is `repoPath`
  (equivalently, drop the known `platformHost` prefix). Do not split on all slashes.
- repo name = last segment of `repoPath`.
- owner = everything in `repoPath` before the last segment, slashes intact (one node,
  so GitLab `group/subgroup` becomes a single owner labelled `group/subgroup`).

Dedup stays keyed on `value` exactly as `mergeOptions` does today; the tree's node
`id`s are derived for rendering/expansion, not a new dedup key.

Output types:

```ts
type RepoLeaf = { kind: "repo"; id: string; label: string; value: string };
type OwnerNode = { kind: "owner"; id: string; label: string; children: RepoLeaf[] };
type HostNode = {
  kind: "host"; id: string; label: string; provider: string;
  platformHost: string; children: OwnerNode[];
};
```

- `id` is the full path prefix (stable across renders; used as expansion key and
  Svelte `{#each}` key).
- Hosts and owners are sorted alphabetically; leaves alphabetically by name.

Auto-flatten reduces pointless single-child nesting. Two independent rules, each keyed
on a different count:
- **Single host (keyed on host count):** when only one host exists, omit the host node
  entirely (no logo row) and render its owners at the top level, however many owners
  there are. With two or more hosts, every host node is shown.
- **Single-repo owner (keyed on that owner's repo count):** an owner with exactly one
  repo renders as a single row at the owner's depth, with no separate caret to expand.
  The label may show the repo name or the `owner/repo` path; that is a display choice
  left to the styling pass.

Express this as a derived view over the built tree rather than mutating it: keep
`buildRepoTree` returning the full `HostNode[]`, and compute the flattened, visible
row list (which also accounts for expansion and filtering) separately. That keeps the
build output type stable and makes flattening, expansion, and filtering one testable
projection instead of three mutations. Cover both rules with unit tests.

Provider for the host logo comes from each repo's own provider field, not from any
host-to-provider guess: configured repos carry `provider` (snake_case), and fetched
repos carry `Repo.Platform`. Thread that value onto `RepoOption` at the merge site
and normalize it once with `canonicalProvider` so `ProviderIcon`/`providerIcon`
(which keys on lowercase `github`/`gitlab`/`forgejo`/`gitea`) resolves it. If a host
somehow mixes providers, the first canonical provider wins; document it.

### Expansion-state store

`frontend/src/lib/stores/repoTreeExpansion.svelte.ts`, mirroring
`collapsedRepos.svelte.ts` directly: a `$state<Set<string>>` of *collapsed* node ids
persisted to localStorage key `middleman:repoTreeCollapsed`, with `isCollapsed(id)`
and `toggle(id)`, wrapped in try/catch. Tracking collapsed ids (not expanded) means an
empty set on first run reads as fully expanded, so the tree looks like a tree
immediately and the persisted set only ever records the user's explicit collapses.
This is the same polarity and shape as `collapsedRepos`, minus the per-surface split
(the selector is a single surface).

### Row rendering

A recursive renderer for caret / checkbox / logo / name. Either a `RepoTreeNode.svelte`
child component or an inline recursive snippet; prefer a child component for testability
and to keep `RepoTypeahead.svelte` focused. Host rows render `ProviderIcon`; owner and
repo rows omit the logo slot.

### Selection and tri-state (derived)

Selection state is derived from the active leaf set, never stored separately.

- Active set: `parseRepoFilterValue(selected)` as a `Set`.
- For any node, collect its descendant leaf `value`s: all in set -> `checked`; some
  -> `partial`; none -> `unchecked`. Leaves are `checked` / `unchecked`.
- Checkbox click on a node: if `checked`, remove all its leaves from the set; else add
  all its leaves. Then `serializeRepoFilterValue(next)` and `onchange`.
- Leaf checkbox / name click: toggle that single leaf (today's behavior).
- "All repos" remains the `undefined` sentinel via `clearSelection`.

## Interaction

### Mouse
- Interior name or caret: toggle expand/collapse.
- Interior checkbox: toggle subtree selection (tri-state).
- Leaf name or checkbox: toggle that repo's selection.

### Keyboard (extends the existing handler)
- `ArrowUp` / `ArrowDown`: move highlight across currently *visible* rows (the
  flattened, expansion-aware, filter-aware row list).
- `ArrowRight` / `ArrowLeft`: expand / collapse the focused node; on a leaf,
  `ArrowLeft` moves focus to its parent.
- `Space`: toggle selection of the focused row (subtree for interior, single for leaf).
- `Enter`: contextual: interior -> expand/collapse; leaf -> toggle selection.
- `Escape`: close the dropdown (unchanged).
- Existing cheatsheet entries are extended with the expand/collapse and select
  bindings via `registerCheatsheetEntries`.

### Filter (prune and auto-expand)
- Substring match on leaf `value` (reuse current lowercase `includes`).
- A host/owner with at least one surviving descendant stays and force-expands so
  matches are visible; groups with no match are hidden.
- Clearing the query restores the persisted expansion state.
- Match highlighting reuses `highlightSegments` + `<mark>` on leaf labels.
- Empty result: keep the existing "No matching repos" empty state.

## Data Flow

```
/repos + configured repos
  -> RepoOption[] (value, owner, name + new: provider, platformHost)  [merge, extended]
  -> buildRepoTree(options)  -> HostNode[]                            [new, pure]
  -> filter(query) + expansion store + active-leaf set
  -> recursive rows (caret / checkbox / ProviderIcon / name)
  -> checkbox click -> add/remove subtree leaves
  -> serializeRepoFilterValue -> onchange -> setGlobalRepo        [existing contract]
  -> downstream list views read the same comma-separated set      [unchanged]
```

## Error and Edge Handling

- localStorage failures for expansion state are swallowed exactly as
  `collapsedRepos` and `filter` already do (feature still works for the session).
- Stale selected values: the existing effect that drops selected values no longer in
  `options` is preserved; it operates on leaf values, which the tree still produces.
- Empty repo set / still loading: keep current behavior (empty list, "All repos"
  available).
- Single host (one host total) or single-repo owner: auto-flatten avoids a pointless
  one-child level (see the two flatten rules above).
- Mixed providers under one host: first canonical provider wins for the logo.

## Testing

Per the repo's e2e-non-negotiable rule and `context/testing.md`.

- **`repoTree.ts` unit (table-driven):** host/owner/repo splitting; GitLab nested
  group -> single slashed owner label; single-child auto-flatten at host and owner
  levels; provider derivation; multi-host and multi-provider sets; sort order.
- **Selection unit:** tri-state computation for host/owner/leaf; subtree add/remove;
  partial -> full -> empty transitions; round-trip through
  `serialize`/`parseRepoFilterValue`.
- **Component (Vitest + Testing Library):** expand/collapse via name and caret;
  checkbox cascade and tri-state display; prune-on-filter with auto-expand and
  highlight; keyboard nav (arrows, space, enter, esc); selection still writes the
  existing comma-separated format via `onchange`; "All repos" clears.
- **E2E (frontend Playwright, `tests/e2e-full/`):** selecting an owner group filters
  the PR list to that group's repos against the real backend. The existing
  `tests/e2e-full/repo-filter-multiselect.spec.ts` already covers flat multi-select of
  the selector and is the natural place to extend; the `e2e-full` suite
  (`playwright-e2e.config.ts`) boots the real server, unlike the mocked `tests/e2e/`
  suite. The tree is client-side, so this is the vehicle, not a Go server e2e.

Checkbox/glyph *visual* assertions are intentionally minimal: test the three logical
states are distinguishable (e.g. by attribute / class / `indeterminate` property), not
exact pixels, since styling is deferred.

## Out of Scope

- Per-row PR/issue count badges.
- A persistent repo rail or any layout change outside the dropdown.
- Changes to the global filter format or any list view.
- Final checkbox visual styling (deferred to a post-functional pass).
- Adopting `@pierre/trees`.
