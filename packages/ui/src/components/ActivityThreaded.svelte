<script lang="ts">
  import type { ActivityItem } from "../api/types.js";
  import { getStores } from "../context.js";
  import {
    collapseActivityCommitRuns,
    isCollapsedActivityRow,
    activityItemKey,
    activityRepoKey,
  } from "./activityRows.js";
  import {
    localDateLabel,
    parseAPITimestamp,
  } from "../utils/time.js";
  import Chip from "./shared/Chip.svelte";
  import ItemKindChip from "./shared/ItemKindChip.svelte";
  import ItemStateChip from "./shared/ItemStateChip.svelte";

  const { grouping, activity } = getStores();
  import { repoColor } from "../utils/repo-color.js";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";

  interface Props {
    items: ActivityItem[];
    onSelectItem: ((item: ActivityItem) => void) | undefined;
    compact?: boolean;
    selectedItem?: SelectedActivityRef | null;
  }

  type SelectedActivityRef = {
    itemType: "pr" | "issue";
    owner: string;
    name: string;
    number: number;
    provider?: string | undefined;
    platformHost?: string | undefined;
    repoPath?: string | undefined;
  };

  let {
    items,
    onSelectItem,
    compact = false,
    selectedItem = null,
  }: Props = $props();

  interface ItemGroup {
    itemType: string;
    itemNumber: number;
    itemTitle: string;
    itemUrl: string;
    itemState: string;
    provider: string;
    repoOwner: string;
    repoName: string;
    repoPath: string;
    platformHost: string;
    latestTime: string;
    events: ActivityItem[];
    displayEvents: ReturnType<
      typeof collapseActivityCommitRuns
    >;
  }

  interface RepoGroup {
    key: string;
    repo: string;
    itemCount: number;
    eventCount: number;
    latestTime: string;
    items: ItemGroup[];
  }

  const grouped = $derived.by(() => {
    const byRepo = grouping.getGroupByRepo();

    // Phase 1: group events by item, using a composite key that
    // includes repo to prevent cross-repo collisions.
    const itemMap = new Map<string, ActivityItem[]>();

    for (const item of items) {
      const itemKey = activityItemKey({
        provider: item.repo?.provider ?? "",
        platformHost: item.platform_host ?? "",
        owner: item.repo_owner,
        name: item.repo_name,
        itemType: item.item_type,
        itemNumber: item.item_number,
      });

      let events = itemMap.get(itemKey);
      if (!events) {
        events = [];
        itemMap.set(itemKey, events);
      }
      events.push(item);
    }

    // Phase 2: build ItemGroup array from the map.
    const allItemGroups: ItemGroup[] = [];

    for (const [, events] of itemMap) {
      events.sort((a, b) =>
        parseAPITimestamp(b.created_at).getTime() - parseAPITimestamp(a.created_at).getTime());

      const first = events[0]!;
      if (!first.repo) {
        throw new Error("activity group missing provider repo identity");
      }
      allItemGroups.push({
        itemType: first.item_type,
        itemNumber: first.item_number,
        itemTitle: first.item_title,
        itemUrl: first.item_url,
        itemState: first.item_state,
        provider: first.repo.provider,
        repoOwner: first.repo.owner,
        repoName: first.repo.name,
        repoPath: first.repo.repo_path,
        platformHost: first.repo.platform_host,
        latestTime: first.created_at,
        events,
        displayEvents: collapseActivityCommitRuns(events),
      });
    }

    allItemGroups.sort((a, b) =>
      parseAPITimestamp(b.latestTime).getTime() - parseAPITimestamp(a.latestTime).getTime());

    if (!byRepo) {
      if (allItemGroups.length === 0) return [];
      return [{
        key: "",
        repo: "",
        itemCount: allItemGroups.length,
        eventCount: allItemGroups.reduce((n, g) => n + g.events.length, 0),
        latestTime: allItemGroups[0]?.latestTime ?? "",
        items: allItemGroups,
      }];
    }

    // Grouped: bucket ItemGroups by repo.
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

    repoGroups.sort((a, b) =>
      parseAPITimestamp(b.latestTime).getTime() - parseAPITimestamp(a.latestTime).getTime());

    return repoGroups;
  });

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

  function eventLabel(type: string): string {
    switch (type) {
      case "new_pr": case "new_issue": return "Opened";
      case "comment": return "Comment";
      case "review": return "Review";
      case "commit": return "Commit";
      case "force_push": return "Force-pushed";
      default: return type;
    }
  }

  function eventClass(type: string): string {
    switch (type) {
      case "comment": return "evt-comment";
      case "review": return "evt-review";
      case "commit": return "evt-commit";
      case "force_push": return "evt-force-push";
      default: return "";
    }
  }

  function relativeTime(iso: string): string {
    const diff = Date.now() - parseAPITimestamp(iso).getTime();
    const mins = Math.floor(diff / 60000);
    if (mins < 1) return "just now";
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    if (days < 7) return `${days}d ago`;
    return localDateLabel(iso);
  }

  function handleItemClick(group: ItemGroup): void {
    if (group.events.length > 0) {
      onSelectItem?.(group.events[0]!);
    }
  }

  function handleEventClick(event: ActivityItem): void {
    onSelectItem?.(event);
  }

  function isSelectedItemGroup(group: ItemGroup): boolean {
    return selectedItem?.itemType === group.itemType
      && selectedItem.owner === group.repoOwner
      && selectedItem.name === group.repoName
      && selectedItem.number === group.itemNumber
      && (!selectedItem.provider
        || selectedItem.provider === group.provider)
      && (!selectedItem.repoPath
        || selectedItem.repoPath === group.repoPath)
      && (!selectedItem.platformHost
        || group.platformHost === selectedItem.platformHost);
  }
</script>

<div class="threaded-view" class:threaded-view--compact={compact}>
  {#each grouped as repoGroup (repoGroup.key)}
    <div class="repo-section">
      {#if grouping.getGroupByRepo()}
        <div class="repo-header">
          <span class="repo-name">{repoGroup.repo}</span>
          <span class="repo-stats">{repoGroup.itemCount} items, {repoGroup.eventCount} events</span>
        </div>
      {/if}

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
    </div>
  {/each}

  {#if grouped.length === 0}
    <div class="empty-state">No activity found</div>
  {/if}
</div>

<style>
  .threaded-view {
    flex: 1;
    overflow-y: auto;
    padding: 0 16px;
  }

  .repo-section {
    margin-bottom: 4px;
  }

  .repo-header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 0 4px;
    position: sticky;
    top: 0;
    background: var(--bg-primary);
    z-index: 2;
    border-bottom: 1px solid var(--border-default);
  }

  .repo-name {
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
  }

  .repo-stats {
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
  }

  .item-row {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 5px 0 5px 24px;
    cursor: pointer;
    border-bottom: 1px solid var(--border-muted);
    transition: background 0.1s;
  }

  .item-row:hover {
    background: var(--bg-surface-hover);
  }

  .item-row.selected {
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    box-shadow: inset 3px 0 0 var(--accent-blue);
  }

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
    transition: color 0.1s, background 0.1s;
  }

  .thread-caret:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .thread-caret:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }

  .item-ref {
    font-size: var(--font-size-sm);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .item-title {
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
    min-width: 0;
  }

  .item-time {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    flex-shrink: 0;
    margin-left: auto;
  }

  .event-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 3px 0 3px 48px;
    cursor: pointer;
    border-bottom: 1px solid var(--border-muted);
    border-left: 2px solid var(--border-muted);
    margin-left: 24px;
    transition: background 0.1s;
  }

  .event-row:hover {
    background: var(--bg-surface-hover);
  }

  .collapsed-event {
    background: var(--bg-inset);
  }

  .event-type {
    font-size: var(--font-size-xs);
    font-weight: 500;
    flex-shrink: 0;
    color: var(--text-secondary);
  }

  .event-type.evt-comment { color: var(--accent-amber); }
  .event-type.evt-review { color: var(--accent-green); }
  .event-type.evt-commit { color: var(--accent-teal); }
  .event-type.evt-force-push { color: var(--accent-red); }

  .event-author {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  .event-time {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    margin-left: auto;
    flex-shrink: 0;
  }

  .empty-state {
    padding: 40px;
    text-align: center;
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  :global(.repo-chip) {
    flex-shrink: 1;
    max-width: 40%;
    min-width: 0;
  }

  :global(.repo-chip) .repo-chip__label {
    display: block;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .threaded-view--compact {
    padding: 0;
  }

  .threaded-view--compact .repo-header {
    padding: 6px 10px 4px;
  }

  .threaded-view--compact .item-row {
    padding: 7px 10px;
  }

  .threaded-view--compact .event-row {
    margin-left: 10px;
    padding-left: 18px;
  }
</style>
