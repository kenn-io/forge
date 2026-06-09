<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { ActivityItem } from "../api/types.js";
  import {
    buildActivityFilterTypes,
    DEFAULT_EVENT_TYPES,
    type TimeRange,
    type ViewMode,
  } from "../stores/activity.svelte.js";
  import { getStores, getNavigate, getSidebar } from "../context.js";
  import ActivityThreaded from "./ActivityThreaded.svelte";
  import FilterDropdown from "./shared/FilterDropdown.svelte";
  import {
    isDefaultBranchActivity,
    isDefaultBranchCommitActivity,
    isDefaultBranchForcePushActivity,
    isCollapsedActivityRow,
    shortSha,
  } from "./activityRows.js";
  import {
    localDateLabel,
    parseAPITimestamp,
  } from "../utils/time.js";
  import { repoColor } from "../utils/repo-color.js";
  import Chip from "./shared/Chip.svelte";
  import ItemKindChip from "./shared/ItemKindChip.svelte";
  import ItemStateChip from "./shared/ItemStateChip.svelte";
  import WorkspaceIndicator from "./shared/WorkspaceIndicator.svelte";
  import ArrowUpRightIcon from "@lucide/svelte/icons/arrow-up-right";
  import ChevronsDownUpIcon from "@lucide/svelte/icons/chevrons-down-up";
  import ChevronsUpDownIcon from "@lucide/svelte/icons/chevrons-up-down";

  const { activity, settings, sync, grouping } = getStores();
  const navigate = getNavigate();
  const { isEmbedded } = getSidebar();

  interface Props {
    onSelectItem?: (item: ActivityItem) => void;
    onSelectBranchCommit?: (item: ActivityItem) => void;
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
    onSelectItem,
    onSelectBranchCommit,
    compact = false,
    selectedItem = null,
    selectedBranchCommit = null,
  }: Props = $props();

  let searchInput = $state("");
  let debounceTimer: ReturnType<typeof setTimeout> | null = null;

  const EVENT_TYPES = DEFAULT_EVENT_TYPES;
  type EventType = (typeof EVENT_TYPES)[number];

  const EVENT_LABELS: Record<EventType, string> = {
    comment: "Comments",
    review: "Reviews",
    commit: "Commits",
    force_push: "Force pushes",
  };

  const EVENT_COLORS: Record<EventType, string> = {
    comment: "var(--accent-amber)",
    review: "var(--accent-green)",
    commit: "var(--accent-teal)",
    force_push: "var(--accent-red)",
  };

  const BOT_SUFFIXES = ["[bot]", "-bot", "bot"];

  function isBot(author: string): boolean {
    const lower = author.toLowerCase();
    return BOT_SUFFIXES.some((s) => lower.endsWith(s));
  }

  const hiddenFilterCount = $derived(
    (EVENT_TYPES.length - activity.getEnabledEvents().size)
    + (activity.getHideClosedMerged() ? 1 : 0)
    + (activity.getHideBots() ? 1 : 0)
    + (activity.getHideDefaultBranchActivity() ? 1 : 0)
    + (grouping.getHideOrgName() ? 1 : 0),
  );

  let unsubSync: (() => void) | undefined;

  onMount(() => {
    activity.initializeFromMount();
    searchInput = activity.getActivitySearch() ?? "";
    void activity.loadActivity();
    activity.startActivityPolling();
    unsubSync = sync.subscribeSyncComplete(() => void activity.loadActivity());
  });

  onDestroy(() => {
    activity.stopActivityPolling();
    unsubSync?.();
    if (debounceTimer) clearTimeout(debounceTimer);
  });

  function applyFilters(): void {
    activity.setActivityFilterTypes(buildActivityFilterTypes(
      activity.getItemFilter(),
      activity.getEnabledEvents(),
      activity.getHideDefaultBranchActivity(),
    ));
    activity.syncToURL();
    void activity.loadActivity();
  }

  function handleItemFilterChange(f: "all" | "prs" | "issues"): void {
    activity.setItemFilter(f);
    applyFilters();
  }

  function toggleEvent(evt: EventType): void {
    const current = activity.getEnabledEvents();
    const next = new Set(current);
    if (next.has(evt)) { if (next.size > 1) next.delete(evt); }
    else next.add(evt);
    activity.setEnabledEvents(next);
    applyFilters();
  }

  function handleTimeRangeChange(range: TimeRange): void {
    activity.setTimeRange(range);
    activity.syncToURL();
    void activity.loadActivity();
  }

  function handleViewModeChange(mode: ViewMode): void {
    activity.setViewMode(mode);
    activity.syncToURL();
  }

  const TIME_RANGES: { value: TimeRange; label: string }[] = [
    { value: "24h", label: "24h" },
    { value: "7d", label: "7d" },
    { value: "30d", label: "30d" },
    { value: "90d", label: "90d" },
  ];

  function handleSearchInput(e: Event): void {
    const val = (e.target as HTMLInputElement).value;
    searchInput = val;
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      activity.setActivitySearch(val || undefined);
      activity.syncToURL();
      void activity.loadActivity();
    }, 300);
  }

  function eventLabel(item: ActivityItem): string {
    switch (item.activity_type) {
      case "new_pr": return "Opened";
      case "new_issue": return "Opened";
      case "comment": return "Comment";
      case "review": return "Review";
      case "commit": return "Commit";
      case "force_push": return "Force-pushed";
      case "default_branch_commit": return "Commit";
      case "default_branch_force_push": return "Force-pushed";
      default: return item.activity_type;
    }
  }

  function hasStateChip(item: ActivityItem): boolean {
    if (isDefaultBranchActivity(item)) return false;
    return item.item_state === "merged" || item.item_state === "closed";
  }

  function branchName(item: ActivityItem): string {
    return item.branch_name || "default branch";
  }

  function activityAuthor(item: ActivityItem): string {
    return item.author_name || item.author;
  }

  function branchActivityTitle(item: ActivityItem): string {
    if (isDefaultBranchForcePushActivity(item)) {
      const before = shortSha(item.before_sha);
      const after = shortSha(item.after_sha);
      if (before && after) return `${before} -> ${after}`;
    }
    return item.body_preview || shortSha(item.commit_sha) || "Commit";
  }

  function branchActivityDetail(item: ActivityItem): string {
    if (isDefaultBranchCommitActivity(item)) {
      return shortSha(item.commit_sha);
    }
    return branchActivityTitle(item);
  }

  function activityLink(item: ActivityItem): string {
    return item.activity_url || item.item_url;
  }

  function repoLabel(owner: string, name: string): string {
    return grouping.getHideOrgName() ? name : `${owner}/${name}`;
  }

  function handleLinkClick(e: Event, url: string): void {
    e.stopPropagation();
    if (url) window.open(url, "_blank", "noopener");
  }

  const displayItems = $derived.by(() => {
    let result = activity.getActivityItems();
    const filter = activity.getItemFilter();
    if (filter === "prs") {
      result = result.filter((it) => it.item_type === "pr");
    } else if (filter === "issues") {
      result = result.filter((it) => it.item_type === "issue");
    }
    if (activity.getHideClosedMerged()) {
      result = result.filter((it) =>
        it.item_state !== "merged" && it.item_state !== "closed");
    }
    if (activity.getHideBots()) {
      result = result.filter((it) => !isBot(it.author));
    }
    if (activity.getHideDefaultBranchActivity()) {
      result = result.filter((it) => !isDefaultBranchActivity(it));
    }
    return result;
  });

  const flatRows = $derived(displayItems);

  function resetFilters(): void {
    activity.setEnabledEvents(new Set(EVENT_TYPES));
    activity.setHideClosedMerged(false);
    activity.setHideBots(false);
    activity.setHideDefaultBranchActivity(false);
    grouping.setHideOrgName(false);
    applyFilters();
  }

  const activityFilterSections = $derived.by(() => [
    {
      title: "Event types",
      items: EVENT_TYPES.map((evt) => ({
        id: evt,
        label: EVENT_LABELS[evt],
        active: activity.getEnabledEvents().has(evt),
        color: EVENT_COLORS[evt],
        onSelect: () => toggleEvent(evt),
      })),
    },
    {
      title: "Visibility",
      items: [
        {
          id: "hide-default-branch",
          label: "Hide default-branch activity",
          active: activity.getHideDefaultBranchActivity(),
          color: "var(--accent-teal)",
          onSelect: () => {
            activity.setHideDefaultBranchActivity(
              !activity.getHideDefaultBranchActivity(),
            );
            applyFilters();
          },
        },
        {
          id: "hide-closed-merged",
          label: "Hide closed/merged",
          active: activity.getHideClosedMerged(),
          color: "var(--accent-red)",
          onSelect: () => {
            activity.setHideClosedMerged(
              !activity.getHideClosedMerged(),
            );
          },
        },
        {
          id: "hide-bots",
          label: "Hide bots",
          active: activity.getHideBots(),
          color: "var(--accent-purple)",
          onSelect: () => {
            activity.setHideBots(!activity.getHideBots());
          },
        },
        {
          id: "hide-org-name",
          label: "Hide org name",
          active: grouping.getHideOrgName(),
          color: "var(--accent-blue)",
          onSelect: () => {
            grouping.setHideOrgName(!grouping.getHideOrgName());
          },
        },
      ],
    },
  ]);

  const currentViewDetail = $derived.by(() => {
    const mode = activity.getViewMode() === "flat" ? "Flat" : "Threaded";
    return `${mode} · ${activity.getTimeRange()}`;
  });

  const collapseThreads = $derived(activity.getCollapseThreads());

  const collapseAllLabel = $derived(
    collapseThreads ? "Expand all" : "Collapse all",
  );

  const filterSections = $derived.by(() => [
    {
      title: "View",
      items: [
        {
          id: "view-flat",
          label: "Flat",
          active: activity.getViewMode() === "flat",
          onSelect: () => handleViewModeChange("flat"),
        },
        {
          id: "view-threaded",
          label: "Threaded",
          active: activity.getViewMode() === "threaded",
          onSelect: () => handleViewModeChange("threaded"),
        },
      ],
    },
    {
      title: "Time range",
      items: TIME_RANGES.map((range) => ({
        id: `range-${range.value}`,
        label: range.label,
        active: activity.getTimeRange() === range.value,
        onSelect: () => handleTimeRangeChange(range.value),
      })),
    },
    ...(activity.getViewMode() === "threaded"
      ? [
          {
            title: "Grouping",
            items: [
              {
                id: "group-by-repo",
                label: "By repo",
                active: grouping.getGroupByRepo(),
                onSelect: () => grouping.setGroupByRepo(true),
              },
              {
                id: "group-all",
                label: "All",
                active: !grouping.getGroupByRepo(),
                onSelect: () => grouping.setGroupByRepo(false),
              },
            ],
          },
        ]
      : []),
    ...activityFilterSections,
  ]);

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

  function eventChipClass(type: string): string {
    const toneClass =
      type === "comment" ? "chip--amber"
      : type === "review" ? "chip--green"
      : type === "commit" || type === "default_branch_commit" ? "chip--teal"
      : type === "force_push" || type === "default_branch_force_push" ? "chip--red"
      : "chip--muted";
    return `evt-label ${eventClass(type)} ${toneClass}`;
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

  function handleRowClick(item: ActivityItem): void {
    if (isDefaultBranchActivity(item)) {
      if (isDefaultBranchCommitActivity(item)) {
        onSelectBranchCommit?.(item);
        return;
      }
      const url = activityLink(item);
      if (url) window.open(url, "_blank", "noopener");
      return;
    }
    onSelectItem?.(item);
  }

  function isSelectedActivityItem(item: ActivityItem): boolean {
    if (isDefaultBranchActivity(item)) {
      if (!isDefaultBranchCommitActivity(item)) return false;
      const selected = selectedBranchCommit;
      if (!selected) return false;
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
    return selectedItem?.itemType === item.item_type
      && selectedItem.owner === item.repo_owner
      && selectedItem.name === item.repo_name
      && selectedItem.number === item.item_number
      && (!selectedItem.provider
        || selectedItem.provider === item.repo?.provider)
      && (!selectedItem.repoPath
        || selectedItem.repoPath === item.repo?.repo_path)
      && (!selectedItem.platformHost
        || selectedItem.platformHost === item.platform_host);
  }

</script>

<div
  class="activity-feed"
  class:activity-feed--compact={compact}
  data-selected-item={selectedItem
    ? `${selectedItem.itemType}:${selectedItem.owner}/${selectedItem.name}/${selectedItem.number}`
    : undefined}
>
  <div class="controls-bar">
    <div class="filter-group">
      <div class="segmented-control">
        <button class="seg-btn" class:active={activity.getItemFilter() === "all"} onclick={() => handleItemFilterChange("all")}>All</button>
        <button class="seg-btn" class:active={activity.getItemFilter() === "prs"} onclick={() => handleItemFilterChange("prs")}>PRs</button>
        <button class="seg-btn" class:active={activity.getItemFilter() === "issues"} onclick={() => handleItemFilterChange("issues")}>Issues</button>
      </div>
    </div>

    <FilterDropdown
      label="View"
      detail={currentViewDetail}
      active={hiddenFilterCount > 0}
      badgeCount={hiddenFilterCount}
      title="View and filter activity"
      sections={filterSections}
      minWidth="220px"
      {...hiddenFilterCount > 0
        ? {
            resetLabel: "Show hidden activity",
            onReset: resetFilters,
          }
        : {}}
    />

    {#if activity.getViewMode() === "threaded"}
      <button
        class="collapse-all-btn"
        type="button"
        aria-label={collapseAllLabel}
        title={collapseAllLabel}
        onclick={() =>
          collapseThreads
            ? activity.expandAllThreads()
            : activity.collapseAllThreads()}
      >
        {#if collapseThreads}
          <ChevronsUpDownIcon size="14" strokeWidth="2" aria-hidden="true" />
        {:else}
          <ChevronsDownUpIcon size="14" strokeWidth="2" aria-hidden="true" />
        {/if}
        <span class="collapse-all-label">{collapseAllLabel}</span>
      </button>
    {/if}

    <input
      class="search-input"
      type="text"
      placeholder="Search..."
      value={searchInput}
      oninput={handleSearchInput}
    />
  </div>

  {#if activity.getActivityError()}
    <div class="error-banner">{activity.getActivityError()}</div>
  {/if}

  {#if settings.isSettingsLoaded() && !settings.hasConfiguredRepos()}
    <div class="table-container">
      <div class="empty-state">No repositories configured.<br />
        {#if !isEmbedded()}<button class="settings-link" onclick={() => navigate("/settings")}>Add one in Settings</button>{/if}
      </div>
    </div>
  {:else if activity.getViewMode() === "threaded"}
    {#if displayItems.length === 0 && activity.isActivityLoading()}
      <div class="table-container"><div class="empty-state">Loading...</div></div>
    {:else}
      <ActivityThreaded
        items={displayItems}
        {onSelectItem}
        {onSelectBranchCommit}
        {compact}
        {selectedItem}
        {selectedBranchCommit}
      />
    {/if}
  {:else}
    <div class="table-container">
      {#if compact}
        <div class="activity-compact-list">
          {#each flatRows as row (row.id)}
            {#if isCollapsedActivityRow(row)}
              <button
                class="activity-compact-row collapsed-row"
                class:selected={isSelectedActivityItem(row.representative)}
                onclick={() => handleRowClick(row.representative)}
                type="button"
              >
                <span class="compact-row-top">
                  {#if isDefaultBranchActivity(row.representative)}
                    <Chip size="sm" uppercase={false} class="chip--muted branch-chip">Branch</Chip>
                    <span class="branch-name">{branchName(row.representative)}</span>
                  {:else}
                    <ItemKindChip kind={row.representative.item_type} />
                    <span class="item-number">#{row.representative.item_number}</span>
                    {#if row.representative.workspace}
                      <WorkspaceIndicator
                        status={row.representative.workspace.status}
                        size={12}
                      />
                    {/if}
                  {/if}
                  <span class="compact-time">{relativeTime(row.latest)}</span>
                </span>
                <span class="compact-title">
                  {#if isDefaultBranchActivity(row.representative)}
                    {row.count} commits on {branchName(row.representative)}
                  {:else}
                    {row.representative.item_title}
                  {/if}
                </span>
                <span class="compact-meta">
                  <span>{repoLabel(row.representative.repo_owner, row.representative.repo_name)}</span>
                  <Chip
                    size="sm"
                    uppercase={false}
                    class="evt-label evt-commit chip--teal"
                  >{row.count} commits</Chip>
                  <span>{row.author}</span>
                </span>
              </button>
            {:else}
              <button
                class="activity-compact-row"
                class:selected={isSelectedActivityItem(row)}
                onclick={() => handleRowClick(row)}
                type="button"
              >
                <span class="compact-row-top">
                  {#if isDefaultBranchActivity(row)}
                    <Chip size="sm" uppercase={false} class="chip--muted branch-chip">Branch</Chip>
                    <span class="branch-name">{branchName(row)}</span>
                  {:else}
                    <ItemKindChip kind={row.item_type} />
                    <span class="item-number">#{row.item_number}</span>
                    {#if row.workspace}
                      <WorkspaceIndicator status={row.workspace.status} size={12} />
                    {/if}
                    {#if hasStateChip(row)}
                      <ItemStateChip state={row.item_state} />
                    {/if}
                  {/if}
                  <span class="compact-time">{relativeTime(row.created_at)}</span>
                </span>
                <span class="compact-title">
                  {isDefaultBranchActivity(row) ? branchActivityTitle(row) : row.item_title}
                </span>
                <span class="compact-meta">
                  <span>{repoLabel(row.repo_owner, row.repo_name)}</span>
                  {#if isDefaultBranchActivity(row) && branchActivityDetail(row)}
                    <span class="sha">{branchActivityDetail(row)}</span>
                  {/if}
                  <Chip
                    size="sm"
                    uppercase={false}
                    class={eventChipClass(row.activity_type)}
                  >{eventLabel(row)}</Chip>
                  <span>{activityAuthor(row)}</span>
                </span>
              </button>
            {/if}
          {/each}
        </div>
      {:else}
        <div class="activity-table" aria-label="Activity events">
          <div class="activity-column-headers">
            <span class="cell cell--caret-spacer" aria-hidden="true"></span>
            <span class="cell cell--type">Type</span>
            <span class="cell cell--repo col-repo">Repo</span>
            <span class="cell cell--author col-author">Author</span>
            <span class="cell cell--title">Item</span>
            <span class="cell cell--time col-when">When</span>
            <span class="cell cell--link" aria-hidden="true"></span>
          </div>
          {#each flatRows as row (row.id)}
            {@const rep = isCollapsedActivityRow(row) ? row.representative : row}
            {@const repoStyle =
              `color: ${repoColor(`${rep.repo_owner}/${rep.repo_name}`)}; `
              + `background: color-mix(in srgb, `
              + `${repoColor(`${rep.repo_owner}/${rep.repo_name}`)} 15%, transparent);`}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="activity-row"
              class:collapsed-row={isCollapsedActivityRow(row)}
              onclick={() =>
                handleRowClick(isCollapsedActivityRow(row) ? row.representative : row)}
            >
              <span class="cell cell--caret-spacer"></span>
              {#if isCollapsedActivityRow(row)}
                <span class="cell cell--type">
                  {#if isDefaultBranchActivity(row.representative)}
                    <Chip size="xs" uppercase={false} class="chip--muted branch-chip">Branch</Chip>
                  {:else}
                    <ItemKindChip kind={row.representative.item_type} />
                  {/if}
                  <Chip
                    size="xs"
                    uppercase={false}
                    class="evt-label evt-commit chip--teal"
                  >{row.count} commits</Chip>
                </span>
                <span class="cell cell--repo col-repo">
                  <Chip
                    size="xs"
                    uppercase={false}
                    class="repo-chip repo-tag"
                    style={repoStyle}
                  >
                    <span class="repo-chip__label"
                      >{repoLabel(row.representative.repo_owner, row.representative.repo_name)}</span>
                  </Chip>
                </span>
                <span class="cell cell--author col-author">{row.author}</span>
                <span class="cell cell--title">
                  {#if isDefaultBranchActivity(row.representative)}
                    <span class="item-ref">{branchName(row.representative)}</span>
                    <span class="item-title">{row.count} commits</span>
                  {:else}
                    <span class="item-ref">#{row.representative.item_number}</span>
                    {#if row.representative.workspace}
                      <WorkspaceIndicator
                        status={row.representative.workspace.status}
                        size={12}
                      />
                    {/if}
                    <span class="item-title">{row.representative.item_title}</span>
                  {/if}
                </span>
                <span class="cell cell--time col-when"
                  >{relativeTime(row.earliest)} – {relativeTime(row.latest)}</span>
                <span class="cell cell--link">
                  {#if activityLink(row.representative)}
                    <button
                      class="link-btn"
                      type="button"
                      aria-label="Open activity in provider"
                      title="Open activity"
                      onclick={(e) => handleLinkClick(e, activityLink(row.representative))}
                    >
                      <ArrowUpRightIcon size="14" strokeWidth="2" aria-hidden="true" />
                    </button>
                  {/if}
                </span>
              {:else}
                <span class="cell cell--type">
                  {#if isDefaultBranchActivity(row)}
                    <Chip size="xs" uppercase={false} class="chip--muted branch-chip">Branch</Chip>
                  {:else}
                    <ItemKindChip kind={row.item_type} />
                  {/if}
                  <Chip
                    size="xs"
                    uppercase={false}
                    class={eventChipClass(row.activity_type)}
                  >{eventLabel(row)}</Chip>
                </span>
                <span class="cell cell--repo col-repo">
                  <Chip
                    size="xs"
                    uppercase={false}
                    class="repo-chip repo-tag"
                    style={repoStyle}
                  >
                    <span class="repo-chip__label"
                      >{repoLabel(row.repo_owner, row.repo_name)}</span>
                  </Chip>
                </span>
                <span class="cell cell--author col-author">{activityAuthor(row)}</span>
                <span class="cell cell--title">
                  {#if isDefaultBranchActivity(row)}
                    <span class="item-ref">{branchName(row)}</span>
                    {#if branchActivityDetail(row)}
                      <span class="sha">{branchActivityDetail(row)}</span>
                    {/if}
                    <span class="item-title">{branchActivityTitle(row)}</span>
                  {:else}
                    {#if hasStateChip(row)}
                      <ItemStateChip state={row.item_state} />
                    {/if}
                    <span class="item-ref">#{row.item_number}</span>
                    {#if row.workspace}
                      <WorkspaceIndicator status={row.workspace.status} size={12} />
                    {/if}
                    <span class="item-title">{row.item_title}</span>
                  {/if}
                </span>
                <span class="cell cell--time col-when">{relativeTime(row.created_at)}</span>
                <span class="cell cell--link">
                  {#if activityLink(row)}
                    <button
                      class="link-btn"
                      type="button"
                      aria-label="Open activity in provider"
                      title="Open activity"
                      onclick={(e) => handleLinkClick(e, activityLink(row))}
                    >
                      <ArrowUpRightIcon size="14" strokeWidth="2" aria-hidden="true" />
                    </button>
                  {/if}
                </span>
              {/if}
            </div>
          {/each}
        </div>
      {/if}

      {#if flatRows.length === 0 && !activity.isActivityLoading()}
        <div class="empty-state">No activity found</div>
      {/if}
    </div>
  {/if}

  {#if activity.isActivityCapped()}
    <div class="capped-notice">
      Showing most recent 5,000 events. Narrow the time range or use filters to see more.
    </div>
  {/if}

</div>

<style>
  .activity-feed {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
  }

  .controls-bar {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    flex-shrink: 0;
  }

  .filter-group {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .segmented-control {
    display: flex;
    align-items: center;
    gap: 1px;
    background: var(--bg-inset);
    border-radius: var(--radius-sm);
    padding: 2px;
  }

  .seg-btn {
    padding: 3px 10px;
    font-size: var(--font-size-xs);
    font-weight: 500;
    color: var(--text-muted);
    border-radius: calc(var(--radius-sm) - 1px);
    transition: background 0.12s, color 0.12s;
  }

  .seg-btn.active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
  }

  .seg-btn:hover:not(.active) {
    color: var(--text-secondary);
  }

  .search-input {
    margin-left: auto;
    width: 180px;
    font-size: var(--font-size-sm);
    padding: 4px 8px;
  }

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
    cursor: pointer;
  }

  .collapse-all-btn:hover {
    color: var(--text-primary);
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
  }

  .collapse-all-btn:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }

  .activity-feed--compact .controls-bar {
    align-items: stretch;
    flex-wrap: wrap;
    gap: 8px;
    padding: 8px;
  }

  .activity-feed--compact .filter-group {
    order: 2;
    flex: 1 1 auto;
    min-width: 0;
  }

  .activity-feed--compact .segmented-control {
    width: 100%;
  }

  .activity-feed--compact .seg-btn {
    flex: 1;
    padding-inline: 6px;
  }

  .activity-feed--compact .search-input {
    order: 1;
    flex: 1 0 100%;
    width: 100%;
    margin-left: 0;
  }

  .activity-feed--compact .collapse-all-btn {
    order: 4;
    flex: 0 0 auto;
  }

  /* In the narrow side pane the labeled button wraps to its own row and
     stacks awkwardly, so collapse to an icon-only control there. The
     aria-label/title keep the accessible name intact. */
  .activity-feed--compact .collapse-all-label {
    display: none;
  }

  .activity-feed--compact :global(.filter-wrap) {
    order: 3;
    flex-shrink: 0;
  }

  .table-container {
    flex: 1;
    overflow-y: auto;
    padding: 0 16px;
  }

  .activity-feed--compact .table-container {
    padding: 0;
  }

  .activity-compact-list {
    display: flex;
    flex-direction: column;
  }

  .activity-compact-row {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 3px;
    width: 100%;
    min-height: 62px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-muted);
    text-align: left;
    color: inherit;
    background: transparent;
  }

  .activity-compact-row:hover {
    background: var(--bg-surface-hover);
  }

  .activity-compact-row.selected {
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    box-shadow: inset 3px 0 0 var(--accent-blue);
  }

  .compact-row-top,
  .compact-meta {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .compact-title {
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .compact-time {
    margin-left: auto;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    flex-shrink: 0;
  }

  .compact-meta {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .compact-meta > span {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* The flat view shares the threaded view's grid layout so toggling between
   * modes doesn't shift the columns. The first column is an 18px spacer that
   * lines up with the threaded view's chevron caret so the type chip starts
   * at the same x-coordinate in both layouts. Widths come from the same CSS
   * custom properties so the column caps stay in lockstep. */
  .activity-table {
    display: grid;
    grid-template-columns:
      18px
      fit-content(140px)
      fit-content(var(--threaded-col-repo-max, 220px))
      fit-content(var(--threaded-col-author-max, 140px))
      minmax(0, 1fr)
      auto
      24px;
    column-gap: 6px;
  }

  .cell--caret-spacer {
    width: 18px;
  }

  .activity-column-headers {
    display: grid;
    grid-template-columns: subgrid;
    grid-column: 1 / -1;
    align-items: center;
    padding: 6px 0 4px;
    font-size: var(--font-size-2xs);
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
    border-bottom: 1px solid var(--border-default);
    position: sticky;
    top: 0;
    background: var(--bg-primary);
    z-index: 1;
  }

  .activity-row {
    display: grid;
    grid-template-columns: subgrid;
    grid-column: 1 / -1;
    align-items: center;
    padding: 5px 0;
    cursor: pointer;
    border-bottom: 1px solid var(--border-muted);
    transition: background 0.1s;
  }

  .activity-row:hover {
    background: var(--bg-surface-hover);
  }

  .collapsed-row {
    background: var(--bg-inset);
  }

  .cell {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .cell--type {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    overflow: visible;
  }

  .cell--repo {
    display: inline-flex;
    align-items: center;
    min-width: 0;
    font-size: var(--font-size-sm);
  }

  .cell--repo :global(.repo-chip) {
    min-width: 0;
    max-width: 100%;
  }

  .cell--repo :global(.repo-chip .repo-chip__label) {
    display: block;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .cell--author {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  .cell--title {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
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
    min-width: 0;
  }

  .cell--time {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    text-align: right;
  }

  .cell--link {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    overflow: visible;
  }

  .link-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
    background: none;
    border: 0;
    padding: 2px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: color 0.1s, background 0.1s;
  }

  .link-btn:hover {
    color: var(--accent-blue);
    background: var(--bg-surface-hover);
  }

  .link-btn:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }

  :global(.evt-label) {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  :global(.evt-label.evt-comment) { color: var(--accent-amber); }
  :global(.evt-label.evt-review) { color: var(--accent-green); }
  :global(.evt-label.evt-commit) { color: var(--accent-teal); }
  :global(.evt-label.evt-force-push) { color: var(--accent-red); }

  .sha {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  /* Compact-list-only labels (rendered by the sidebar card layout). The
   * table layout uses .item-ref instead. */
  .branch-name,
  .item-number {
    color: var(--text-muted);
    margin-right: 4px;
  }

  :global(.branch-chip) {
    flex-shrink: 0;
  }

  .empty-state {
    padding: 40px;
    text-align: center;
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .settings-link {
    color: var(--accent-blue);
    cursor: pointer;
    font-size: var(--font-size-md);
    margin-top: 4px;
    display: inline-block;
  }

  .settings-link:hover {
    text-decoration: underline;
  }

  .error-banner {
    padding: 8px 16px;
    background: color-mix(in srgb, var(--accent-red) 10%, transparent);
    color: var(--accent-red);
    font-size: var(--font-size-sm);
    border-bottom: 1px solid var(--border-default);
  }

  .capped-notice {
    padding: 6px 16px;
    font-size: var(--font-size-xs);
    color: var(--accent-amber);
    background: color-mix(in srgb, var(--accent-amber) 8%, transparent);
    border-top: 1px solid var(--border-default);
    text-align: center;
    flex-shrink: 0;
  }

</style>
