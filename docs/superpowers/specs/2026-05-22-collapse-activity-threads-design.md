# Collapse / Expand Activity Threads Design

## Goal

Let a maintainer scan active PRs and issues at a glance in the Activity view's
Threaded mode, then drill into one item's events or expand everything. Today the
Threaded view always renders every item's events inline, so a busy time range is
a long scroll with no way to see just the items.

Add:

- A per-item chevron caret that expands/collapses that item's events inline.
- A global Collapse-all / Expand-all control in the activity controls bar.

Collapsed shows only the PR/issue header rows (the at-a-glance list). Expanding
reveals each item's events, which is today's behavior. The change is confined to
the Threaded view; the Flat event-table mode is untouched.

## Current Behavior

The Activity view (`packages/ui/src/components/ActivityFeed.svelte`) has two
modes selected by the store's `viewMode` (`packages/ui/src/stores/activity.svelte.ts`):

- `flat`: a table (or compact list in the side pane) where each row is one event
  (comment, review, commit, opened).
- `threaded`: `packages/ui/src/components/ActivityThreaded.svelte` groups events
  under each PR/issue header (`item-row` followed by nested `event-row`s),
  optionally bucketed by repo.

When a PR/issue detail opens, `packages/ui/src/views/ActivityFeedView.svelte`
puts the same `ActivityFeed` instance into a narrow (~360px) `compact` side pane
beside the detail. The feed is not remounted on selection; the pane only resizes.

Activity display settings persist with this shape: `view_mode`, `time_range`,
`hide_closed`, and `hide_bots` live in the `config.Activity` struct
(`internal/config/config.go:475`), are serialized directly by the settings
handler (`internal/server/settings_handlers.go`), are set as durable defaults
from the Settings page (`frontend/src/lib/components/settings/ActivitySettings.svelte`),
and are mirrored into the URL as live session state by the feed via the store's
`syncToURL` / `syncFromURL`.

## Decisions

1. The collapse control lives in the Threaded view only. The Flat event-table
   mode is unchanged.
2. Threaded defaults to expanded (today's behavior), and the collapse choice is
   remembered.
3. The caret toggles an item's events inline. Clicking the rest of the row still
   opens the detail pane — unchanged.
4. One shared collapse state across the full-width view and the narrow side pane.
5. Persistence has full parity with `view_mode`: a server config field plus a URL
   param plus the regenerated client/schema. The live Collapse-all / Expand-all
   control is URL/session state; the Settings page sets the server default.

## State Model

Collapse state lives in the activity store, the single source shared by the
controls-bar button (rendered by `ActivityFeed`) and the per-item carets
(rendered by `ActivityThreaded`). Putting it in the store reuses the existing
persistence plumbing, and the session override set survives the full-to-side-pane
transition for free because that component instance is not remounted on selection.

New store internals:

- `collapseThreads` — reactive `$state(false)`. The live global state. `false`
  means expanded.
- `collapseThreadsDefault` — plain (non-reactive) field holding the most recently
  hydrated server default. Used to decide bidirectional URL writes.
- `expandOverrides` — reactive `$state(new Set<string>())`. Items the user has
  individually flipped away from the global default since the last
  collapse-all/expand-all. Session only; never persisted.

`expandOverrides` is mutated by reassignment, not in-place `add`/`delete`/`clear`,
matching the store's existing `enabledEvents` pattern (`activity.svelte.ts:323`,
`:331`). `svelte/reactivity` (`SvelteSet`) is not used anywhere in the codebase
and is not introduced here.

New store API:

- `getCollapseThreads(): boolean` — drives the controls-bar button label/icon.
- `collapseAllThreads(): void` — sets `collapseThreads = true`, resets
  `expandOverrides = new Set()`, calls `syncToURL()`.
- `expandAllThreads(): void` — sets `collapseThreads = false`, resets
  `expandOverrides = new Set()`, calls `syncToURL()`.
- `isThreadItemExpanded(key: string): boolean` — returns
  `expandOverrides.has(key) ? collapseThreads : !collapseThreads`. (When the
  global state is expanded, an override means that one item is collapsed, and
  vice-versa.)
- `toggleThreadItem(key: string): void` — flips `key`'s membership by building a
  new `Set` and reassigning.

The controls-bar button calls `getCollapseThreads() ? expandAllThreads() :
collapseAllThreads()`.

## URL Persistence (bidirectional)

The existing `syncToURL` compares each value against a hardcoded default and only
writes non-defaults (`activity.svelte.ts:363`). That is unsafe for a field whose
default is server-configurable: if the Settings page sets `collapse_threads =
true`, then "Expand all" (live `false`) would equal the hardcoded default, drop
the param, and a reload would re-hydrate back to collapsed — silently losing the
user's choice.

The collapse param is therefore tri-state and compared against the hydrated
server default, not a constant:

- `hydrateDefaults(activity)` sets `collapseThreadsDefault = activity.collapse_threads`
  and `collapseThreads = activity.collapse_threads`.
- `syncFromURL()` reads `collapsed`: `"1"` sets `collapseThreads = true`, `"0"`
  sets `false`, absent leaves the hydrated value.
- `syncToURL()` writes `collapsed = collapseThreads ? "1" : "0"` when
  `collapseThreads !== collapseThreadsDefault`, and deletes the param otherwise.

This keeps fresh navigation server-driven (no param means render the server
default) while making a live override reload-safe regardless of which default the
user has chosen. The pre-existing one-sided sync for `view` and `range` is left
unchanged; this change is scoped to the new field.

## Shared Item Key (identity-correct)

The current grouping key (`ActivityThreaded.svelte:78`) and keyed `{#each}`
(`:227`) are built inline as
`${platformHost}|${owner}/${name}:${itemType}:${itemNumber}` and omit `provider`.
That contradicts the project identity invariant `(platform, platform_host, owner,
name)` and lets overrides collide across providers that share a host.

Extract one helper and use it for grouping, the keyed `{#each}`, and the override
key:

```ts
// packages/ui/src/components/activityRows.ts
export interface ActivityItemKeyRef {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
  itemType: string;
  itemNumber: number;
}

export function activityItemKey(ref: ActivityItemKeyRef): string {
  return `${ref.provider}|${ref.platformHost}|${ref.owner}/${ref.name}` +
    `:${ref.itemType}:${ref.itemNumber}`;
}
```

The grouping pass derives `provider` from `item.repo?.provider ?? ""` (the
existing flat fields supply host/owner/name and the group's representative is
already guarded by the `first.repo` throw). The per-group call site passes the
`ItemGroup`'s `provider`, `platformHost`, `repoOwner`, `repoName`, `itemType`,
`itemNumber`, producing keys identical to the grouping pass. The same call feeds
`isThreadItemExpanded` / `toggleThreadItem`.

## Frontend Changes

- `packages/ui/src/stores/activity.svelte.ts`: add the state, reads, and writes
  above; extend `hydrateDefaults`, `syncToURL`, `syncFromURL`; export the new API.
- `packages/ui/src/components/activityRows.ts`: add `activityItemKey` and its ref
  type.
- `packages/ui/src/components/ActivityFeed.svelte`: render a Collapse-all /
  Expand-all toggle in `.controls-bar`, only when `viewMode === "threaded"`. It
  reflects `getCollapseThreads()` — `ChevronsDownUp` + "Collapse all" when
  expanded, `ChevronsUpDown` + "Expand all" when collapsed — with an `aria-label`
  and `title`. The existing compact CSS already wraps the controls bar, so it fits
  the side pane.
- `packages/ui/src/components/ActivityThreaded.svelte`: prepend a chevron caret
  `<button>` to each `item-row` (`ChevronDown` expanded / `ChevronRight`
  collapsed). Its `onclick` calls `toggleThreadItem(key)` and `stopPropagation()`
  so the row's open-detail click does not also fire. Wrap the
  `{#each itemGroup.displayEvents}` block in `{#if isThreadItemExpanded(key)}`.
  Replace both inline keys with `activityItemKey(...)`. Add compact caret sizing
  for the side pane.
- `packages/ui/src/api/types.ts`: add `collapse_threads: boolean` to the
  hand-maintained `ActivitySettings` interface.
- `packages/ui/src/Provider.svelte`: add `collapse_threads` to the field-by-field
  `ActivitySettings` rebuild in `reloadSettingsAfterConfigChange` (~`:217`), which
  would otherwise silently drop the field on config-change reloads.
  `frontend/src/lib/utils/appStartup.ts` and `ActivitySettings.svelte` pass
  `settings.activity` through wholesale and need no per-field change.
- `frontend/src/lib/components/settings/ActivitySettings.svelte`: add a "Collapse
  threads by default" toggle (same toggle pattern as Hide bots) that PUTs
  `collapse_threads` and re-hydrates. This is what makes the choice survive fresh
  navigation, exactly like `view_mode`.

## Backend Changes

- `internal/config/config.go`: add
  `CollapseThreads bool \`toml:"collapse_threads" json:"collapse_threads"\`` to the
  `Activity` struct (`:475`). The zero value `false` means expanded, so no default
  coercion and no validation are needed.
- `make api-generate`: regenerate the OpenAPI spec, the Go client
  (`internal/apiclient/generated/client.gen.go`), and the TS schema
  (`packages/ui/src/api/generated/schema.ts`). The settings handler needs no
  change — it copies the whole `config.Activity` and the bool passes through.

## Testing

Follow the wire-level discipline in `context/testing.md` and the e2e mandate.

- Go config: round-trip test that `collapse_threads` parses from TOML and
  survives load/marshal in `internal/config`.
- Go API (`internal/server/apitest`): a settings `PUT` then `GET` that asserts
  `collapse_threads` round-trips through the real handler and SQLite-backed config.
- Store unit tests (`packages/ui` vitest): `hydrateDefaults` sets the default;
  `syncToURL`/`syncFromURL` are bidirectional (server default `true` plus
  `collapsed=0` survives a reload simulation; default `false` plus `collapsed=1`
  survives; matching live and default writes no param); `isThreadItemExpanded`
  override math; `collapseAllThreads`/`expandAllThreads` clear overrides.
- `activityItemKey` unit test: two items differing only by `provider` (or host)
  produce different keys.
- Component test (`ActivityThreaded`): a caret hides/shows an item's events; a
  caret click does not invoke the row's `onSelectItem`; Collapse-all hides all
  events and clears prior single-item overrides.
- Playwright e2e: in Threaded mode, Collapse-all hides events; expanding one item
  shows only its events; reload preserves the URL-encoded state; the caret and
  control work in the side pane with a detail open.

## Out of Scope

- No keyboard shortcut for collapse-all (possible later follow-up).
- No repo-group-level collapse.
- No change to the Flat view, and no change to the pre-existing one-sided URL sync
  for `view` and `range`.
