# Collapse / Expand Activity Threads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-item caret and a global collapse-all/expand-all control to the Activity view's Threaded mode, defaulting to expanded with the choice persisted like `view_mode`.

**Architecture:** Collapse state lives in the activity store: a persisted `collapseThreads` boolean (URL param + server config field) plus a session-only `expandOverrides` Set of per-item exceptions. `ActivityFeed` renders the global toggle (Threaded mode only); `ActivityThreaded` renders per-item carets and hides each item's events when collapsed. A new `activityRepoKey`/`activityItemKey` helper makes grouping and override keys provider-aware. The server field rides through the existing settings handler unchanged.

**Tech Stack:** Go (config + Huma settings API), TypeScript/Svelte 5 runes (`@middleman/ui`), vitest + @testing-library/svelte (unit/component), Playwright (e2e), `@lucide/svelte@1.3.0` icons, bun for all frontend tooling.

**Open decision (resolved):** On a runtime config reload while a `collapsed=0/1` URL override is live, `hydrateDefaults` updates `collapseThreadsDefault` and resets overrides, then — only when the store is already `initialized` — re-applies the `collapsed` URL param so a background reload does not snap the user's in-session choice. Startup ordering is unchanged: `hydrateDefaults` runs first (not yet initialized), then `initializeFromMount` applies the URL.

---

## File Structure

**Backend**
- `internal/config/config.go` — add `CollapseThreads` to the `Activity` struct.
- `internal/config/config_test.go` — extend Activity default/explicit/round-trip tests.
- `internal/server/e2etest/settings_test.go` — assert `collapse_threads` round-trips through the settings API.
- Regenerated: `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, `internal/apiclient/generated/client.gen.go`, `packages/ui/src/api/generated/schema.ts`.

**Frontend**
- `packages/ui/src/api/types.ts` — add `collapse_threads` to `ActivitySettings`.
- `packages/ui/src/Provider.svelte` — add `collapse_threads` to the field-by-field rebuild.
- `packages/ui/src/components/activityRows.ts` — add `activityRepoKey`, `activityItemKey`, `ActivityRepoKeyRef`.
- `packages/ui/src/components/activityRows.test.ts` — key helper tests.
- `packages/ui/src/stores/activity.svelte.ts` — collapse state, reads/writes, persistence.
- `packages/ui/src/stores/activity.svelte.test.ts` — new store test.
- `packages/ui/src/components/ActivityThreaded.svelte` — carets, provider-aware keys, event gating.
- `packages/ui/src/components/ActivityThreaded.test.ts` — caret behavior.
- `packages/ui/src/components/ActivityFeed.svelte` — global toggle + compact ordering.
- `packages/ui/src/components/ActivityFeed.test.ts` — assert toggle absent in Flat.
- `packages/ui/src/components/ActivityFeed.collapse.test.ts` — new: toggle present/wired in Threaded.
- `frontend/src/lib/components/settings/ActivitySettings.svelte` — "Collapse threads by default" toggle.
- `frontend/tests/e2e/activity-collapse.spec.ts` — new e2e.

---

## Task 1: Backend config field + regenerate API

**Files:**
- Modify: `internal/config/config.go:475-480`
- Test: `internal/config/config_test.go:395-429`, `:693-722`

- [ ] **Step 1: Extend the Activity default + explicit + round-trip tests**

In `internal/config/config_test.go`, add an assertion to `TestLoadActivityDefaults` (after line 407):

```go
	assert.False(cfg.Activity.CollapseThreads)
```

In `TestLoadActivityExplicit`, add `collapse_threads = true` to the `[activity]` TOML block (after the `hide_bots = true` line) and an assertion after line 428:

```go
	assert.True(cfg.Activity.CollapseThreads)
```

In `TestSaveRoundTrip`, add `collapse_threads = true` to the `[activity]` TOML block (after `hide_bots = true`) and an assertion after line 722:

```go
	assert.Equal(cfg.Activity.CollapseThreads, cfg2.Activity.CollapseThreads)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config -run 'TestLoadActivity|TestSaveRoundTrip' -shuffle=on`
Expected: FAIL — `cfg.Activity.CollapseThreads` undefined (compile error).

- [ ] **Step 3: Add the field to the Activity struct**

In `internal/config/config.go`, change the `Activity` struct (lines 475-480) to:

```go
type Activity struct {
	ViewMode        string `toml:"view_mode" json:"view_mode"`
	TimeRange       string `toml:"time_range" json:"time_range"`
	HideClosed      bool   `toml:"hide_closed" json:"hide_closed"`
	HideBots        bool   `toml:"hide_bots" json:"hide_bots"`
	CollapseThreads bool   `toml:"collapse_threads" json:"collapse_threads"`
}
```

No default coercion or validation: the zero value `false` means expanded.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config -run 'TestLoadActivity|TestSaveRoundTrip' -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Regenerate API artifacts**

Run: `make api-generate`
Expected: updates `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, `internal/apiclient/generated/client.gen.go`, and `packages/ui/src/api/generated/schema.ts`. Confirm each now contains `collapse_threads` / `CollapseThreads`:

Run: `rg -l "collapse_threads|CollapseThreads" internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts frontend/openapi/openapi.yaml`
Expected: all three paths listed.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go \
  frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json \
  internal/apiclient/generated/client.gen.go packages/ui/src/api/generated/schema.ts
git commit -m "feat(config): add collapse_threads activity setting"
```

---

## Task 2: Settings API round-trip (wire-level)

**Files:**
- Test: `internal/server/e2etest/settings_test.go:83-118`

- [ ] **Step 1: Extend the settings update test with collapse_threads**

In `internal/server/e2etest/settings_test.go`, in `TestSettingsAPIE2EReadUpdateAndValidation`, add `CollapseThreads: true` to the `generated.Activity` value inside the `updateResp` PUT body (after the `HideBots: true,` line, ~line 91):

```go
				CollapseThreads: true,
```

After the existing `cfgAfterUpdate` assertions (after line 110), add a config-level assertion and a re-GET that asserts the field is observable over the wire:

```go
	assert.True(cfgAfterUpdate.Activity.CollapseThreads)

	reGetResp := doServerJSON(
		t, ts.Client(), http.MethodGet,
		ts.URL+"/api/v1/settings", nil,
	)
	defer reGetResp.Body.Close()
	require.Equal(http.StatusOK, reGetResp.StatusCode)
	var reGet generated.SettingsResponse
	require.NoError(json.NewDecoder(reGetResp.Body).Decode(&reGet))
	assert.True(reGet.Activity.CollapseThreads)
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/server/e2etest -run TestSettingsAPIE2EReadUpdateAndValidation -shuffle=on`
Expected: PASS (`generated.Activity.CollapseThreads` exists from Task 1's regen).

- [ ] **Step 3: Commit**

```bash
git add internal/server/e2etest/settings_test.go
git commit -m "test(server): assert collapse_threads round-trips through settings API"
```

---

## Task 3: Frontend type + Provider hydration path

**Files:**
- Modify: `packages/ui/src/api/types.ts:64-69`
- Modify: `packages/ui/src/Provider.svelte` (the `reloadSettingsAfterConfigChange` rebuild, ~`:215-231`)

- [ ] **Step 1: Add the field to the hand-maintained ActivitySettings type**

In `packages/ui/src/api/types.ts`, change the `ActivitySettings` interface to:

```ts
export interface ActivitySettings {
  view_mode: "flat" | "threaded";
  time_range: "24h" | "7d" | "30d" | "90d";
  hide_closed: boolean;
  hide_bots: boolean;
  collapse_threads: boolean;
}
```

- [ ] **Step 2: Add the field to the Provider rebuild**

In `packages/ui/src/Provider.svelte`, inside `reloadSettingsAfterConfigChange`, the object passed to `hydrateSettings` rebuilds activity field-by-field. Add this line after `hide_bots: data.activity.hide_bots,`:

```ts
          collapse_threads: data.activity.collapse_threads,
```

- [ ] **Step 3: Typecheck**

Run: `cd packages/ui && bun run typecheck`
Expected: PASS. (`data.activity.collapse_threads` resolves against the regenerated schema; every `ActivitySettings` literal now requires the field.)

- [ ] **Step 4: Commit**

```bash
git add packages/ui/src/api/types.ts packages/ui/src/Provider.svelte
git commit -m "feat(ui): thread collapse_threads through ActivitySettings hydration"
```

---

## Task 4: Provider-aware key helpers

**Files:**
- Modify: `packages/ui/src/components/activityRows.ts`
- Test: `packages/ui/src/components/activityRows.test.ts`

- [ ] **Step 1: Write failing tests for the key helpers**

Append to `packages/ui/src/components/activityRows.test.ts`:

```ts
import { activityItemKey, activityRepoKey } from "./activityRows.js";

describe("activityRepoKey / activityItemKey", () => {
  const base = {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
  };

  it("includes provider so same owner/name on different providers differ", () => {
    const a = activityRepoKey(base);
    const b = activityRepoKey({ ...base, provider: "gitlab" });
    expect(a).not.toBe(b);
  });

  it("includes host so same identity on different hosts differs", () => {
    const a = activityRepoKey(base);
    const b = activityRepoKey({ ...base, platformHost: "ghe.example.com" });
    expect(a).not.toBe(b);
  });

  it("builds an item key as the repo key plus type and number", () => {
    const item = { ...base, itemType: "pr", itemNumber: 42 };
    expect(activityItemKey(item)).toBe(`${activityRepoKey(base)}:pr:42`);
  });

  it("separates a PR and an issue with the same number", () => {
    const pr = { ...base, itemType: "pr", itemNumber: 42 };
    const issue = { ...base, itemType: "issue", itemNumber: 42 };
    expect(activityItemKey(pr)).not.toBe(activityItemKey(issue));
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && bunx vitest run activityRows`
Expected: FAIL — `activityItemKey`/`activityRepoKey` are not exported.

- [ ] **Step 3: Implement the helpers**

Append to `packages/ui/src/components/activityRows.ts`:

```ts
export interface ActivityRepoKeyRef {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
}

export function activityRepoKey(ref: ActivityRepoKeyRef): string {
  return `${ref.provider}|${ref.platformHost}|${ref.owner}/${ref.name}`;
}

export function activityItemKey(
  ref: ActivityRepoKeyRef & { itemType: string; itemNumber: number },
): string {
  return `${activityRepoKey(ref)}:${ref.itemType}:${ref.itemNumber}`;
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd frontend && bunx vitest run activityRows`
Expected: PASS (existing `collapseActivityCommitRuns` tests still pass too).

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/components/activityRows.ts packages/ui/src/components/activityRows.test.ts
git commit -m "feat(ui): add provider-aware activity repo/item key helpers"
```

---

## Task 5: Store collapse state + persistence

**Files:**
- Modify: `packages/ui/src/stores/activity.svelte.ts`
- Test: `packages/ui/src/stores/activity.svelte.test.ts` (create)

- [ ] **Step 1: Write the failing store test**

Create `packages/ui/src/stores/activity.svelte.test.ts`:

```ts
import { beforeEach, describe, expect, it } from "vitest";
import type { ActivitySettings } from "../api/types.js";
import { createActivityStore } from "./activity.svelte.js";

const fakeClient = {
  GET: async () => ({ data: { items: [], capped: false }, error: null }),
} as unknown as Parameters<typeof createActivityStore>[0]["client"];

function settings(collapse: boolean): ActivitySettings {
  return {
    view_mode: "threaded",
    time_range: "7d",
    hide_closed: false,
    hide_bots: false,
    collapse_threads: collapse,
  };
}

function makeStore() {
  return createActivityStore({ client: fakeClient });
}

beforeEach(() => {
  window.history.replaceState(null, "", "/");
});

describe("activity store collapse state", () => {
  it("treats threads as expanded when the collapse default is false", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    expect(s.getCollapseThreads()).toBe(false);
    expect(s.isThreadItemExpanded("k1")).toBe(true);
  });

  it("collapseAllThreads collapses everything and clears overrides", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    s.toggleThreadItem("k1");
    expect(s.isThreadItemExpanded("k1")).toBe(false);
    s.collapseAllThreads();
    expect(s.getCollapseThreads()).toBe(true);
    expect(s.isThreadItemExpanded("k1")).toBe(false);
    expect(s.isThreadItemExpanded("k2")).toBe(false);
  });

  it("toggleThreadItem expands a single item when globally collapsed", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(true));
    expect(s.isThreadItemExpanded("k1")).toBe(false);
    s.toggleThreadItem("k1");
    expect(s.isThreadItemExpanded("k1")).toBe(true);
    expect(s.isThreadItemExpanded("k2")).toBe(false);
  });

  it("writes collapsed to the URL only when it differs from the server default", () => {
    const s = makeStore();
    s.hydrateDefaults(settings(false));
    s.collapseAllThreads();
    expect(new URLSearchParams(window.location.search).get("collapsed")).toBe("1");
    s.expandAllThreads();
    expect(new URLSearchParams(window.location.search).has("collapsed")).toBe(false);
  });

  it("applies collapsed=0 from the URL over a collapsed server default", () => {
    window.history.replaceState(null, "", "/?collapsed=0");
    const s = makeStore();
    s.hydrateDefaults(settings(true));
    s.initializeFromMount();
    expect(s.getCollapseThreads()).toBe(false);
  });

  it("preserves a live collapsed override when settings reload after init", () => {
    window.history.replaceState(null, "", "/?collapsed=0");
    const s = makeStore();
    s.hydrateDefaults(settings(true));
    s.initializeFromMount();
    expect(s.getCollapseThreads()).toBe(false);
    s.hydrateDefaults(settings(true));
    expect(s.getCollapseThreads()).toBe(false);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && bunx vitest run activity.svelte`
Expected: FAIL — `getCollapseThreads`, `isThreadItemExpanded`, etc. are not functions.

- [ ] **Step 3: Add the collapse state to the store**

In `packages/ui/src/stores/activity.svelte.ts`, in the `--- state ---` block (after the `viewMode` line ~56), add:

```ts
  let collapseThreads = $state(false);
  let collapseThreadsDefault = false;
  let expandOverrides = $state<Set<string>>(new Set());
```

In the `--- reads ---` block (after `getViewMode`), add:

```ts
  function getCollapseThreads(): boolean {
    return collapseThreads;
  }
  function isThreadItemExpanded(key: string): boolean {
    return expandOverrides.has(key) ? collapseThreads : !collapseThreads;
  }
```

In the `--- writes ---` block (after `setViewMode`), add:

```ts
  function collapseAllThreads(): void {
    collapseThreads = true;
    expandOverrides = new Set();
    syncToURL();
  }
  function expandAllThreads(): void {
    collapseThreads = false;
    expandOverrides = new Set();
    syncToURL();
  }
  function toggleThreadItem(key: string): void {
    const next = new Set(expandOverrides);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    expandOverrides = next;
  }
```

- [ ] **Step 4: Wire hydration and URL persistence**

Replace `hydrateDefaults` (lines ~145-152) with:

```ts
  function hydrateDefaults(
    activity: ActivitySettings,
  ): void {
    viewMode = activity.view_mode;
    timeRange = activity.time_range;
    hideClosed = activity.hide_closed;
    hideBots = activity.hide_bots;
    collapseThreadsDefault = activity.collapse_threads;
    collapseThreads = activity.collapse_threads;
    expandOverrides = new Set();
    if (initialized) applyCollapsedFromURL();
  }
```

Add this helper just above `syncFromURL` (~line 338):

```ts
  function applyCollapsedFromURL(): void {
    const sp = new URLSearchParams(window.location.search);
    if (!sp.has("collapsed")) return;
    const v = sp.get("collapsed");
    if (v === "1") collapseThreads = true;
    else if (v === "0") collapseThreads = false;
  }
```

In `syncFromURL`, add a call right before `deriveFiltersFromTypes();` (~line 360):

```ts
    applyCollapsedFromURL();
```

In `syncToURL`, add this block right after the `view` block (after the `else sp.delete("view");` line ~375):

```ts
    if (collapseThreads !== collapseThreadsDefault) {
      sp.set("collapsed", collapseThreads ? "1" : "0");
    } else {
      sp.delete("collapsed");
    }
```

In the returned object (the final `return { ... }`), add these exports alongside the other reads/writes:

```ts
    getCollapseThreads,
    isThreadItemExpanded,
    collapseAllThreads,
    expandAllThreads,
    toggleThreadItem,
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd frontend && bunx vitest run activity.svelte`
Expected: PASS (all six cases).

- [ ] **Step 6: Commit**

```bash
git add packages/ui/src/stores/activity.svelte.ts packages/ui/src/stores/activity.svelte.test.ts
git commit -m "feat(ui): add collapse state and bidirectional URL persistence to activity store"
```

---

## Task 6: ActivityThreaded carets + provider-aware keys

**Files:**
- Modify: `packages/ui/src/components/ActivityThreaded.svelte`
- Test: `packages/ui/src/components/ActivityThreaded.test.ts`

- [ ] **Step 1: Write the failing caret test**

Replace the contents of `packages/ui/src/components/ActivityThreaded.test.ts` with:

```ts
import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ActivityItem } from "../api/types.js";
import ActivityThreaded from "./ActivityThreaded.svelte";

function activityItem(
  id: string,
  overrides: Partial<ActivityItem> = {},
): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type: "comment",
    author: "alice",
    body_preview: "",
    created_at: "2026-04-27T12:00:00Z",
    item_number: 1,
    item_state: "open",
    item_title: "Add widget caching layer",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/1",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
    ...overrides,
  };
}

const expanded = vi.hoisted(() => ({ value: true }));
const toggleThreadItem = vi.hoisted(() => vi.fn());

vi.mock("../context.js", () => ({
  getStores: () => ({
    grouping: { getGroupByRepo: () => false },
    activity: {
      isThreadItemExpanded: () => expanded.value,
      toggleThreadItem,
    },
  }),
}));

describe("ActivityThreaded collapse", () => {
  afterEach(() => {
    cleanup();
    expanded.value = true;
    toggleThreadItem.mockClear();
  });

  it("shows events when the item is expanded", () => {
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    expect(container.querySelectorAll(".event-row").length).toBeGreaterThan(0);
  });

  it("hides events but keeps the item row when collapsed", () => {
    expanded.value = false;
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    expect(container.querySelectorAll(".event-row")).toHaveLength(0);
    expect(container.querySelectorAll(".item-row")).toHaveLength(1);
  });

  it("toggles the item on caret click without selecting the row", async () => {
    const onSelectItem = vi.fn();
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem },
    });
    const caret = container.querySelector(".thread-caret");
    expect(caret).not.toBeNull();
    await fireEvent.click(caret!);
    expect(toggleThreadItem).toHaveBeenCalledTimes(1);
    expect(onSelectItem).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && bunx vitest run ActivityThreaded`
Expected: FAIL — no `.thread-caret`, and events render regardless of `expanded`.

- [ ] **Step 3: Update imports and store destructure**

In `packages/ui/src/components/ActivityThreaded.svelte`, change the activityRows import to include the key helpers:

```ts
  import {
    collapseActivityCommitRuns,
    isCollapsedActivityRow,
    activityItemKey,
    activityRepoKey,
  } from "./activityRows.js";
```

Change the store destructure (line 16) to:

```ts
  const { grouping, activity } = getStores();
```

Add the chevron icon imports below the existing imports:

```ts
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
```

- [ ] **Step 4: Add the `key` field to RepoGroup and a local key helper**

In the `RepoGroup` interface (line ~61), add a `key` field as the first member:

```ts
  interface RepoGroup {
    key: string;
    repo: string;
    itemCount: number;
    eventCount: number;
    latestTime: string;
    items: ItemGroup[];
  }
```

Add this helper just after the `grouped` `$derived.by(...)` block closes (before `eventLabel`):

```ts
  function itemKeyOf(g: ItemGroup): string {
    return activityItemKey({
      provider: g.provider,
      platformHost: g.platformHost,
      owner: g.repoOwner,
      name: g.repoName,
      itemType: g.itemType,
      itemNumber: g.itemNumber,
    });
  }
```

- [ ] **Step 5: Make grouping keys provider-aware**

In the phase-1 loop inside `grouped`, replace the `host`/`itemKey` lines (lines 77-78) with:

```ts
      const itemKey = activityItemKey({
        provider: item.repo?.provider ?? "",
        platformHost: item.platform_host ?? "",
        owner: item.repo_owner,
        name: item.repo_name,
        itemType: item.item_type,
        itemNumber: item.item_number,
      });
```

In the non-grouped early return (lines ~120-127), add `key: ""` as the first field of the returned object:

```ts
      return [{
        key: "",
        repo: "",
        itemCount: allItemGroups.length,
        eventCount: allItemGroups.reduce((n, g) => n + g.events.length, 0),
        latestTime: allItemGroups[0]?.latestTime ?? "",
        items: allItemGroups,
      }];
```

Replace the repo-bucketing block (lines ~131-152, from `const repoMap` through the `repoGroups.push({...})` loop) with:

```ts
    const repoMap = new Map<string, ItemGroup[]>();
    const repoLabels = new Map<string, string>();
    for (const ig of allItemGroups) {
      const repoKey = activityRepoKey({
        provider: ig.provider,
        platformHost: ig.platformHost,
        owner: ig.repoOwner,
        name: ig.repoName,
      });
      repoLabels.set(repoKey, `${ig.repoOwner}/${ig.repoName}`);
      let bucket = repoMap.get(repoKey);
      if (!bucket) {
        bucket = [];
        repoMap.set(repoKey, bucket);
      }
      bucket.push(ig);
    }

    const repoGroups: RepoGroup[] = [];
    for (const [repoKey, itemGroups] of repoMap) {
      const allEvents = itemGroups.flatMap((g) => g.events);
      repoGroups.push({
        key: repoKey,
        repo: repoLabels.get(repoKey) ?? "",
        itemCount: itemGroups.length,
        eventCount: allEvents.length,
        latestTime: itemGroups[0]?.latestTime ?? "",
        items: itemGroups,
      });
    }
```

- [ ] **Step 6: Update the template — keys, caret, and event gating**

Change the repo `{#each}` key (line 218) to use `repoGroup.key`:

```svelte
  {#each grouped as repoGroup (repoGroup.key)}
```

Change the item `{#each}` (line 227) to key on `itemKeyOf` and bind a local `key`, then prepend the caret to the `item-row` and gate the events. Replace the whole item loop body (lines 227-274) with:

```svelte
      {#each repoGroup.items as itemGroup (itemKeyOf(itemGroup))}
        {@const key = itemKeyOf(itemGroup)}
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="item-row"
          class:selected={isSelectedItemGroup(itemGroup)}
          onclick={() => handleItemClick(itemGroup)}
        >
          <button
            class="thread-caret"
            type="button"
            aria-label={activity.isThreadItemExpanded(key)
              ? "Collapse item activity"
              : "Expand item activity"}
            aria-expanded={activity.isThreadItemExpanded(key)}
            onclick={(e) => {
              e.stopPropagation();
              activity.toggleThreadItem(key);
            }}
          >
            {#if activity.isThreadItemExpanded(key)}
              <ChevronDownIcon size="14" strokeWidth="2" aria-hidden="true" />
            {:else}
              <ChevronRightIcon size="14" strokeWidth="2" aria-hidden="true" />
            {/if}
          </button>
          <ItemKindChip
            kind={itemGroup.itemType === "pr" ? "pr" : "issue"}
          />
          {#if !grouping.getGroupByRepo()}
            <Chip
              size="xs"
              uppercase={false}
              class="repo-chip repo-tag"
              style="color: {repoColor(`${itemGroup.repoOwner}/${itemGroup.repoName}`)}; background: color-mix(in srgb, {repoColor(`${itemGroup.repoOwner}/${itemGroup.repoName}`)} 15%, transparent);"
            >
              <span class="repo-chip__label">{itemGroup.repoOwner}/{itemGroup.repoName}</span>
            </Chip>
          {/if}
          {#if itemGroup.itemState === "merged"}
            <ItemStateChip state="merged" />
          {:else if itemGroup.itemState === "closed"}
            <ItemStateChip state="closed" />
          {/if}
          <span class="item-ref">#{itemGroup.itemNumber}</span>
          <span class="item-title">{itemGroup.itemTitle}</span>
          <span class="item-time">{relativeTime(itemGroup.latestTime)}</span>
        </div>

        {#if activity.isThreadItemExpanded(key)}
          {#each itemGroup.displayEvents as row (row.id)}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            {#if isCollapsedActivityRow(row)}
              <div class="event-row collapsed-event" onclick={() => handleEventClick(row.representative)}>
                <span class="event-type evt-commit">{row.count} commits</span>
                <span class="event-author">{row.author}</span>
                <span class="event-time">{relativeTime(row.earliest)} - {relativeTime(row.latest)}</span>
              </div>
            {:else}
              <div class="event-row" onclick={() => handleEventClick(row)}>
                <span class="event-type {eventClass(row.activity_type)}">{eventLabel(row.activity_type)}</span>
                <span class="event-author">{row.author}</span>
                <span class="event-time">{relativeTime(row.created_at)}</span>
              </div>
            {/if}
          {/each}
        {/if}
      {/each}
```

- [ ] **Step 7: Add caret styles**

In the `<style>` block, add after the `.item-row.selected` rule (~line 335):

```css
  .thread-caret {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    flex-shrink: 0;
    color: var(--text-muted);
    background: none;
    border-radius: var(--radius-sm);
  }

  .thread-caret:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }
```

And in the compact section, after `.threaded-view--compact .item-row` (~line 435), add:

```css
  .threaded-view--compact .thread-caret {
    width: 22px;
    height: 22px;
  }
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `cd frontend && bunx vitest run ActivityThreaded`
Expected: PASS (all three cases).

- [ ] **Step 9: Commit**

```bash
git add packages/ui/src/components/ActivityThreaded.svelte packages/ui/src/components/ActivityThreaded.test.ts
git commit -m "feat(ui): per-item carets and provider-aware keys in threaded activity"
```

---

## Task 7: ActivityFeed collapse-all / expand-all toggle

**Files:**
- Modify: `packages/ui/src/components/ActivityFeed.svelte`
- Test: `packages/ui/src/components/ActivityFeed.collapse.test.ts` (create), `packages/ui/src/components/ActivityFeed.test.ts`

- [ ] **Step 1: Write the failing toggle test (Threaded mode)**

Create `packages/ui/src/components/ActivityFeed.collapse.test.ts`:

```ts
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ActivityFeed from "./ActivityFeed.svelte";

const collapseAllThreads = vi.hoisted(() => vi.fn());
const expandAllThreads = vi.hoisted(() => vi.fn());

vi.mock("../context.js", () => ({
  getNavigate: () => vi.fn(),
  getSidebar: () => ({ isEmbedded: () => false }),
  getStores: () => ({
    activity: {
      initializeFromMount: vi.fn(),
      loadActivity: vi.fn(async () => undefined),
      startActivityPolling: vi.fn(),
      stopActivityPolling: vi.fn(),
      getActivitySearch: () => "",
      getEnabledEvents: () => new Set(["comment", "review", "commit", "force_push"]),
      getHideClosedMerged: () => false,
      getHideBots: () => false,
      getItemFilter: () => "all",
      getActivityItems: () => [],
      getActivityError: () => null,
      getViewMode: () => "threaded",
      getTimeRange: () => "7d",
      isActivityLoading: () => false,
      isActivityCapped: () => false,
      getCollapseThreads: () => false,
      collapseAllThreads,
      expandAllThreads,
      isThreadItemExpanded: () => true,
      toggleThreadItem: vi.fn(),
      setActivityFilterTypes: vi.fn(),
      setItemFilter: vi.fn(),
      setEnabledEvents: vi.fn(),
      setHideClosedMerged: vi.fn(),
      setHideBots: vi.fn(),
      setActivitySearch: vi.fn(),
      setTimeRange: vi.fn(),
      setViewMode: vi.fn(),
      syncToURL: vi.fn(),
    },
    settings: {
      isSettingsLoaded: () => true,
      hasConfiguredRepos: () => true,
    },
    sync: { subscribeSyncComplete: vi.fn(() => () => undefined) },
    grouping: { getGroupByRepo: () => true, setGroupByRepo: vi.fn() },
  }),
}));

describe("ActivityFeed collapse-all control", () => {
  afterEach(() => {
    cleanup();
    collapseAllThreads.mockClear();
    expandAllThreads.mockClear();
  });

  it("renders a Collapse all button in threaded mode and wires it", async () => {
    render(ActivityFeed, { props: {} });
    const btn = screen.getByRole("button", { name: "Collapse all" });
    await fireEvent.click(btn);
    expect(collapseAllThreads).toHaveBeenCalledTimes(1);
    expect(expandAllThreads).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && bunx vitest run ActivityFeed.collapse`
Expected: FAIL — no button named "Collapse all".

- [ ] **Step 3: Add the toggle to ActivityFeed**

In `packages/ui/src/components/ActivityFeed.svelte`, add the chevrons imports after the existing chip imports:

```ts
  import ChevronsDownUpIcon from "@lucide/svelte/icons/chevrons-down-up";
  import ChevronsUpDownIcon from "@lucide/svelte/icons/chevrons-up-down";
```

In the `.controls-bar`, add this block immediately after the `</FilterDropdown>` close tag and before the `<input class="search-input" ...>`:

```svelte
    {#if activity.getViewMode() === "threaded"}
      <button
        class="collapse-all-btn"
        type="button"
        aria-label={activity.getCollapseThreads() ? "Expand all" : "Collapse all"}
        title={activity.getCollapseThreads() ? "Expand all" : "Collapse all"}
        onclick={() =>
          activity.getCollapseThreads()
            ? activity.expandAllThreads()
            : activity.collapseAllThreads()}
      >
        {#if activity.getCollapseThreads()}
          <ChevronsUpDownIcon size="14" strokeWidth="2" aria-hidden="true" />
        {:else}
          <ChevronsDownUpIcon size="14" strokeWidth="2" aria-hidden="true" />
        {/if}
        <span class="collapse-all-label"
          >{activity.getCollapseThreads() ? "Expand all" : "Collapse all"}</span
        >
      </button>
    {/if}
```

- [ ] **Step 4: Add the toggle styles and compact ordering**

In the `<style>` block, add after the `.search-input` rule (~line 617):

```css
  .collapse-all-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 8px;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
  }

  .collapse-all-btn:hover {
    color: var(--text-primary);
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
  }
```

In the compact section, after `.activity-feed--compact .search-input` (~line 646), add an explicit order so the toggle does not jump to the front of the wrapped bar:

```css
  .activity-feed--compact .collapse-all-btn {
    order: 4;
    flex: 0 0 auto;
  }
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd frontend && bunx vitest run ActivityFeed.collapse`
Expected: PASS.

- [ ] **Step 6: Assert the toggle is absent in Flat mode**

In the existing `packages/ui/src/components/ActivityFeed.test.ts`, add a case to the `describe("ActivityFeed compact mode", ...)` block (the existing mock returns `getViewMode: () => "flat"`):

```ts
  it("hides the collapse-all control in flat mode", () => {
    render(ActivityFeed, { props: { compact: true } });
    expect(
      screen.queryByRole("button", { name: /Collapse all|Expand all/ }),
    ).toBeNull();
  });
```

- [ ] **Step 7: Run the existing ActivityFeed tests to verify they pass**

Run: `cd frontend && bunx vitest run ActivityFeed`
Expected: PASS (compact-mode cases plus the new flat-mode absence case and the collapse case).

- [ ] **Step 8: Commit**

```bash
git add packages/ui/src/components/ActivityFeed.svelte \
  packages/ui/src/components/ActivityFeed.collapse.test.ts \
  packages/ui/src/components/ActivityFeed.test.ts
git commit -m "feat(ui): add collapse-all/expand-all control to threaded activity feed"
```

---

## Task 8: Settings page default toggle

**Files:**
- Modify: `frontend/src/lib/components/settings/ActivitySettings.svelte`

- [ ] **Step 1: Add the toggle handler**

In `frontend/src/lib/components/settings/ActivitySettings.svelte`, add after `setViewMode` (~line 40):

```ts
  function toggleCollapseThreads(): void {
    const updated = { ...activity, collapse_threads: !activity.collapse_threads };
    onUpdate(updated);
    void save(updated);
  }
```

- [ ] **Step 2: Add the toggle row after "Default view mode"**

Insert this `setting-row` immediately after the closing `</div>` of the "Default view mode" row (after line 67, before the "Default time range" row):

```svelte
<div class="setting-row">
  <span class="setting-label">Collapse threads by default</span>
  <button class="toggle-btn" class:toggle-on={activity.collapse_threads} onclick={toggleCollapseThreads} aria-label="Toggle collapse threads by default" aria-pressed={activity.collapse_threads}>
    <span class="toggle-track"><span class="toggle-thumb"></span></span>
  </button>
</div>
```

- [ ] **Step 3: Typecheck**

Run: `cd packages/ui && bun run typecheck && cd ../../frontend && bunx svelte-check --tsconfig ./tsconfig.json`
Expected: PASS. (`activity.collapse_threads` is now part of `ActivitySettings`.)

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/settings/ActivitySettings.svelte
git commit -m "feat(ui): add collapse-threads-by-default toggle to activity settings"
```

---

## Task 9: Playwright e2e

**Files:**
- Create: `frontend/tests/e2e/activity-collapse.spec.ts`

- [ ] **Step 1: Write the e2e spec**

Create `frontend/tests/e2e/activity-collapse.spec.ts`:

```ts
import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

function event(
  id: string,
  number: number,
  type: string,
  created: string,
): unknown {
  return {
    id,
    cursor: id,
    activity_type: type,
    author: "marius",
    body_preview: "",
    created_at: created,
    item_number: number,
    item_state: "open",
    item_title:
      number === 42 ? "Add browser regression coverage" : "Refactor theme system",
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${number}`,
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
      capabilities: {},
    },
  };
}

async function mockActivity(page: Page): Promise<void> {
  await mockApi(page);
  await page.route("**/api/v1/settings", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "widgets",
            repo_path: "acme/widgets",
            is_glob: false,
            matched_repo_count: 1,
          },
        ],
        activity: {
          view_mode: "threaded",
          time_range: "7d",
          hide_closed: false,
          hide_bots: false,
          collapse_threads: false,
        },
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          renderer: "xterm",
        },
        agents: [],
      }),
    });
  });
  await page.route("**/api/v1/activity**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        capped: false,
        items: [
          event("a1", 42, "comment", "2026-03-30T14:00:00Z"),
          event("a2", 42, "review", "2026-03-30T13:00:00Z"),
          event("b1", 55, "comment", "2026-03-30T12:00:00Z"),
        ],
      }),
    });
  });
}

test.describe("threaded activity collapse", () => {
  test("collapses, drills into one item, and persists across reload", async ({
    page,
  }) => {
    await mockActivity(page);
    await page.goto("/?view=threaded");

    const itemRows = page.locator(".item-row");
    const eventRows = page.locator(".event-row");
    await expect(itemRows).toHaveCount(2);
    await expect(eventRows.first()).toBeVisible();

    await page.getByRole("button", { name: "Collapse all" }).click();
    await expect(itemRows).toHaveCount(2);
    await expect(eventRows).toHaveCount(0);

    // Drill into a single item via its caret.
    await itemRows.first().locator(".thread-caret").click();
    await expect(eventRows.first()).toBeVisible();

    // Collapse-all wrote ?collapsed=1; a reload restores the collapsed state
    // and clears the session-only single-item override.
    await page.reload();
    await expect(page.locator(".item-row")).toHaveCount(2);
    await expect(page.locator(".event-row")).toHaveCount(0);
  });

  test("collapse control works while the side detail pane is open", async ({
    page,
  }) => {
    await mockActivity(page);
    await page.goto("/?view=threaded");

    // Open a detail by clicking the item row body (not the caret).
    await page.locator(".item-row").first().locator(".item-title").click();
    await expect(page.locator(".activity-detail")).toBeVisible();
    await expect(page.locator(".activity-pane")).toBeVisible();

    // The collapse control is still present in the narrow side pane.
    await page.getByRole("button", { name: "Collapse all" }).click();
    await expect(page.locator(".event-row")).toHaveCount(0);
  });
});
```

- [ ] **Step 2: Run the e2e spec**

Run: `cd frontend && bunx playwright test --config=playwright.config.ts activity-collapse`
Expected: PASS (2 tests).

- [ ] **Step 3: Commit**

```bash
git add frontend/tests/e2e/activity-collapse.spec.ts
git commit -m "test(e2e): cover threaded activity collapse, drill-in, and side pane"
```

---

## Final Verification

- [ ] **Go:** `make test-short` — config + settings e2e pass.
- [ ] **Frontend unit/component:** `cd frontend && bun run test` — store, helper, ActivityThreaded, ActivityFeed cases pass.
- [ ] **Typecheck:** `cd packages/ui && bun run typecheck` — clean.
- [ ] **Lint:** `make lint` and `cd packages/ui && bun run lint` — clean.
- [ ] **E2e (affected):** `cd frontend && bunx playwright test --config=playwright.config.ts activity-collapse mobile-activity-repos` — pass.
