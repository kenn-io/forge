<script lang="ts">
  import type { ActivityItem } from "../api/types.js";
  import { getStores } from "../context.js";
  import {
    collapseActivityCommitRuns,
    isCollapsedActivityRow,
    activityItemKey,
    activityRepoKey,
    isDefaultBranchActivity,
    isDefaultBranchCommitActivity,
    isDefaultBranchForcePushActivity,
    shortSha,
    type ActivityRow,
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
    onSelectBranchCommit?: ((item: ActivityItem) => void) | undefined;
    compact?: boolean;
    selectedItem?: SelectedActivityRef | null;
    selectedBranchCommit?: SelectedBranchCommitRef | null;
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

  type SelectedBranchCommitRef = {
    owner: string;
    name: string;
    commitSha: string;
    provider?: string | undefined;
    platformHost?: string | undefined;
    repoPath?: string | undefined;
  };

  let {
    items,
    onSelectItem,
    onSelectBranchCommit,
    compact = false,
    selectedItem = null,
    selectedBranchCommit = null,
  }: Props = $props();

  interface ItemGroup {
    kind: "item";
    itemType: string;
    itemNumber: number;
    itemTitle: string;
    itemUrl: string;
    itemState: string;
    branchName: string;
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

  interface ItemEntry {
    kind: "item";
    group: ItemGroup;
  }

  interface BranchEntry {
    kind: "branch";
    row: ActivityRow;
    provider: string;
    repoOwner: string;
    repoName: string;
    repoPath: string;
    platformHost: string;
    latestTime: string;
    eventCount: number;
  }

  type ThreadedEntry = ItemEntry | BranchEntry;

  interface RepoGroup {
    key: string;
    repo: string;
    itemCount: number;
    eventCount: number;
    latestTime: string;
    items: ThreadedEntry[];
  }

  const grouped = $derived.by(() => {
    const byRepo = grouping.getGroupByRepo();

    // Phase 1: group events by item, using a composite key that
    // includes repo to prevent cross-repo collisions.
    const itemMap = new Map<string, ActivityItem[]>();
    const branchItems: ActivityItem[] = [];

    for (const item of items) {
      if (isDefaultBranchActivity(item)) {
        branchItems.push(item);
        continue;
      }

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

    // Phase 2: build threaded entries from item groups and branch rows.
    const threadedEntries: ThreadedEntry[] = [];

    for (const [, events] of itemMap) {
      events.sort((a, b) =>
        parseAPITimestamp(b.created_at).getTime() - parseAPITimestamp(a.created_at).getTime());

      const first = events[0]!;
      if (!first.repo) {
        throw new Error("activity group missing provider repo identity");
      }
      const branch = first.branch_name || "default branch";
      threadedEntries.push({
        kind: "item",
        group: {
          kind: "item",
          itemType: first.item_type,
          itemNumber: first.item_number,
          itemTitle: first.item_title,
          itemUrl: first.activity_url || first.item_url,
          itemState: first.item_state,
          branchName: branch,
          provider: first.repo.provider,
          repoOwner: first.repo.owner,
          repoName: first.repo.name,
          repoPath: first.repo.repo_path,
          platformHost: first.repo.platform_host,
          latestTime: first.created_at,
          events,
          displayEvents: collapseActivityCommitRuns(events),
        },
      });
    }

    for (const item of branchItems) {
      threadedEntries.push(branchEntryFromRow(item));
    }

    threadedEntries.sort((a, b) =>
      parseAPITimestamp(entryLatestTime(b)).getTime() - parseAPITimestamp(entryLatestTime(a)).getTime());

    const allEntries = collapseTopLevelBranchRuns(threadedEntries);

    if (!byRepo) {
      if (allEntries.length === 0) return [];
      return [{
        key: "",
        repo: "",
        itemCount: allEntries.length,
        eventCount: allEntries.reduce((n, entry) => n + entryEventCount(entry), 0),
        latestTime: entryLatestTime(allEntries[0]!),
        items: allEntries,
      }];
    }

    // Grouped: bucket threaded entries by repo.
    const repoMap = new Map<string, ThreadedEntry[]>();
    const repoLabels = new Map<string, string>();
    for (const entry of allEntries) {
      const repoKey = activityRepoKey({
        provider: entryProvider(entry),
        platformHost: entryPlatformHost(entry),
        owner: entryRepoOwner(entry),
        name: entryRepoName(entry),
      });
      repoLabels.set(repoKey, `${entryRepoOwner(entry)}/${entryRepoName(entry)}`);
      let bucket = repoMap.get(repoKey);
      if (!bucket) {
        bucket = [];
        repoMap.set(repoKey, bucket);
      }
      bucket.push(entry);
    }

    const repoGroups: RepoGroup[] = [];
    for (const [repoKey, entries] of repoMap) {
      repoGroups.push({
        key: repoKey,
        repo: repoLabels.get(repoKey) ?? "",
        itemCount: entries.length,
        eventCount: entries.reduce((n, entry) => n + entryEventCount(entry), 0),
        latestTime: entryLatestTime(entries[0]!),
        items: entries,
      });
    }

    repoGroups.sort((a, b) =>
      parseAPITimestamp(b.latestTime).getTime() - parseAPITimestamp(a.latestTime).getTime());

    return repoGroups;
  });

  function branchEntryFromRow(row: ActivityRow): BranchEntry {
    const item = branchRowRepresentative(row);
    if (!item.repo) {
      throw new Error("branch activity row missing provider repo identity");
    }
    return {
      kind: "branch",
      row,
      provider: item.repo.provider,
      repoOwner: item.repo.owner,
      repoName: item.repo.name,
      repoPath: item.repo.repo_path,
      platformHost: item.repo.platform_host,
      latestTime: isCollapsedActivityRow(row) ? row.latest : row.created_at,
      eventCount: isCollapsedActivityRow(row) ? row.count : 1,
    };
  }

  function collapseTopLevelBranchRuns(
    entries: ThreadedEntry[],
  ): ThreadedEntry[] {
    const result: ThreadedEntry[] = [];
    let i = 0;
    while (i < entries.length) {
      const entry = entries[i]!;
      if (entry.kind !== "branch") {
        result.push(entry);
        i++;
        continue;
      }

      const branchItems: ActivityItem[] = [];
      let j = i;
      while (j < entries.length) {
        const branchEntry = entries[j]!;
        if (branchEntry.kind !== "branch") break;
        branchItems.push(branchRowRepresentative(branchEntry.row));
        j++;
      }
      for (const row of collapseActivityCommitRuns(branchItems)) {
        result.push(branchEntryFromRow(row));
      }
      i = j;
    }
    return result;
  }

  function entryLatestTime(entry: ThreadedEntry): string {
    return entry.kind === "item" ? entry.group.latestTime : entry.latestTime;
  }

  function entryEventCount(entry: ThreadedEntry): number {
    return entry.kind === "item" ? entry.group.events.length : entry.eventCount;
  }

  function entryProvider(entry: ThreadedEntry): string {
    return entry.kind === "item" ? entry.group.provider : entry.provider;
  }

  function entryPlatformHost(entry: ThreadedEntry): string {
    return entry.kind === "item" ? entry.group.platformHost : entry.platformHost;
  }

  function entryRepoOwner(entry: ThreadedEntry): string {
    return entry.kind === "item" ? entry.group.repoOwner : entry.repoOwner;
  }

  function entryRepoName(entry: ThreadedEntry): string {
    return entry.kind === "item" ? entry.group.repoName : entry.repoName;
  }

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

  function entryKeyOf(entry: ThreadedEntry): string {
    if (entry.kind === "item") return itemKeyOf(entry.group);
    if (isCollapsedActivityRow(entry.row)) return entry.row.id;
    return `${activityRepoKey({
      provider: entry.provider,
      platformHost: entry.platformHost,
      owner: entry.repoOwner,
      name: entry.repoName,
    })}:branch-activity:${entry.row.id}`;
  }

  function eventLabel(type: string): string {
    switch (type) {
      case "new_pr": case "new_issue": return "Opened";
      case "comment": return "Comment";
      case "review": return "Review";
      case "commit": return "Commit";
      case "force_push": return "Force-pushed";
      case "default_branch_commit": return "Commit";
      case "default_branch_force_push": return "Force-pushed";
      default: return type;
    }
  }

  function eventClass(type: string): string {
    switch (type) {
      case "comment": return "evt-comment";
      case "review": return "evt-review";
      case "commit": return "evt-commit";
      case "default_branch_commit": return "evt-commit";
      case "force_push": return "evt-force-push";
      case "default_branch_force_push": return "evt-force-push";
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

  function handleBranchRowClick(row: ActivityRow): void {
    const item = branchRowRepresentative(row);
    if (isDefaultBranchCommitActivity(item)) {
      onSelectBranchCommit?.(item);
      return;
    }
    handleEventClick(item);
  }

  function handleEventClick(event: ActivityItem): void {
    if (isDefaultBranchActivity(event)) {
      if (isDefaultBranchCommitActivity(event)) {
        onSelectBranchCommit?.(event);
      } else if (event.activity_url) {
        window.open(event.activity_url, "_blank", "noopener");
      }
      return;
    }
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

  function isSelectedBranchRow(row: ActivityRow): boolean {
    const selected = selectedBranchCommit;
    if (!selected) return false;
    const item = branchRowRepresentative(row);
    if (!isDefaultBranchCommitActivity(item)) return false;
    return selected.commitSha === item.commit_sha
      && selected.owner === item.repo_owner
      && selected.name === item.repo_name
      && (!selected.provider
        || selected.provider === item.repo?.provider)
      && (!selected.repoPath
        || selected.repoPath === item.repo?.repo_path)
      && (!selected.platformHost
        || selected.platformHost === item.platform_host);
  }

  function eventAuthor(event: ActivityItem): string {
    return event.author_name || event.author;
  }

  function eventSummary(event: ActivityItem): string {
    if (!isDefaultBranchActivity(event)) return "";
    if (isDefaultBranchForcePushActivity(event)) {
      const before = shortSha(event.before_sha);
      const after = shortSha(event.after_sha);
      return before && after ? `${before} -> ${after}` : event.body_preview;
    }
    return event.body_preview || shortSha(event.commit_sha);
  }

  function branchName(item: ActivityItem): string {
    return item.branch_name || "default branch";
  }

  function branchRowRepresentative(row: ActivityRow): ActivityItem {
    return isCollapsedActivityRow(row) ? row.representative : row;
  }

  function branchRowTitle(row: ActivityRow): string {
    if (isCollapsedActivityRow(row)) return `${row.count} commits`;
    return eventSummary(row) || eventLabel(row.activity_type);
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

      {#each repoGroup.items as entry (entryKeyOf(entry))}
        {#if entry.kind === "branch"}
          {@const row = entry.row}
          {@const item = branchRowRepresentative(row)}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="item-row branch-activity-row"
            class:selected={isSelectedBranchRow(row)}
            onclick={() => handleBranchRowClick(row)}
          >
            <span class="thread-caret-spacer" aria-hidden="true"></span>
            <span class="branch-event-type {eventClass(item.activity_type)}">
              {isDefaultBranchCommitActivity(item) ? "Commit" : eventLabel(item.activity_type)}
            </span>
            {#if !grouping.getGroupByRepo()}
              <Chip
                size="xs"
                uppercase={false}
                class="repo-chip repo-tag"
                style="color: {repoColor(`${entry.repoOwner}/${entry.repoName}`)}; background: color-mix(in srgb, {repoColor(`${entry.repoOwner}/${entry.repoName}`)} 15%, transparent);"
              >
                <span class="repo-chip__label">{entry.repoOwner}/{entry.repoName}</span>
              </Chip>
            {/if}
            <span class="item-ref">{branchName(item)}</span>
            <span class="item-title">{branchRowTitle(row)}</span>
            <span class="item-time">{relativeTime(entry.latestTime)}</span>
          </div>
        {:else}
          {@const itemGroup = entry.group}
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
                {#if eventSummary(row)}
                  <span class="event-summary">{eventSummary(row)}</span>
                {/if}
                <span class="event-author">{eventAuthor(row)}</span>
                <span class="event-time">{relativeTime(row.created_at)}</span>
              </div>
            {/if}
          {/each}
        {/if}
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

  .thread-caret-spacer {
    width: 18px;
    height: 18px;
    flex-shrink: 0;
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

  .branch-event-type {
    font-size: var(--font-size-xs);
    font-weight: 500;
    flex-shrink: 0;
    color: var(--text-secondary);
  }

  .branch-event-type.evt-commit { color: var(--accent-teal); }
  .branch-event-type.evt-force-push { color: var(--accent-red); }

  .event-author {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  .event-summary {
    min-width: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
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
