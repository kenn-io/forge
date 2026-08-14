<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, onMount } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { getAppRuntime } from "../app/runtime-context.js";
  import type { AppExecution } from "../app/runtime.js";
  import type { ActivityItem, WorkspaceActivitySubject } from "../api/types.js";
  import { getStores } from "../context.js";
  import {
    buildActivityFilterTypes,
    isActivityItemTypeEnabled,
    type ActivityItemType,
    type TimeRange,
  } from "../stores/activity.svelte.js";
  import {
    Card,
    Chip,
    ScrollBox,
    SearchInput,
    Timeline,
    TimelineItem,
    Toggle,
    Typeahead,
    type TypeaheadOption,
    type TimelineTone,
  } from "@kenn-io/kit-ui";
  import { parseAPITimestamp } from "../utils/time.js";
  import ItemKindChip from "../components/shared/ItemKindChip.svelte";
  import ItemStateChip from "../components/shared/ItemStateChip.svelte";
  import { SelectDropdown } from "@kenn-io/kit-ui";
  import WorkspaceIndicator from "../components/shared/WorkspaceIndicator.svelte";
  import CheckIcon from "@lucide/svelte/icons/check";
  import {
    activityBranchKey,
    activityItemKey,
    isClosedOrMergedActivity,
    isDefaultBranchActivity,
    isDefaultBranchForcePushActivity,
    notificationReasonLabel,
    shortSha,
  } from "../components/activityRows.js";
  import {
    buildMobileActivityRepoOptions,
  } from "./mobileActivityRepoOptions.js";
  import {
    createRepoLabelFormatter,
    type RepoLabelIdentity,
  } from "../utils/repo-label.js";

  const { activity, settings, sync, grouping } = getStores();
  const runtime = getAppRuntime();

  interface Props {
    selectedRepo?: string | undefined;
    onRepoChange?: ((repo: string | undefined) => void) | undefined;
    onSelectItem?: ((item: ActivityItem) => void) | undefined;
  }

  let { selectedRepo, onRepoChange, onSelectItem }: Props = $props();

  type ActivityGroup = {
    key: string;
    representative: ActivityItem;
    events: ActivityItem[];
    eventCount: number;
    latestTime: string;
    workspaceActivityAt?: string;
  };

  const BOT_SUFFIXES = ["[bot]", "-bot", "bot"];
  const timeRanges: TimeRange[] = ["24h", "7d", "30d", "90d"];
  const timeRangeOptions = timeRanges.map((range) => ({
    value: range,
    label: range,
  }));
  let searchInput = $state("");
  let filtersExpanded = $state(false);
  let searchExecution: AppExecution<void, never> | null = null;
  let unsubSync: (() => void) | undefined;

  const repoOptions = $derived.by(() =>
    [
      { value: "", label: "All repos" },
      ...buildMobileActivityRepoOptions(settings.getConfiguredRepos()),
    ],
  );
  onMount(() => {
    activity.initializeFromMount();
    searchInput = activity.getActivitySearch() ?? "";
    activity.loadActivity(true);
    activity.startActivityPolling();
    unsubSync = sync.subscribeSyncComplete(() => {
      activity.loadActivity();
      activity.loadActivityAuthors(true);
    });
  });

  onDestroy(() => {
    activity.stopActivityPolling();
    unsubSync?.();
    searchExecution?.interrupt();
  });

  function isBot(author: string): boolean {
    const lower = author.toLowerCase();
    return BOT_SUFFIXES.some((suffix) => lower.endsWith(suffix));
  }

  const displayItems = $derived.by(() => {
    let result = activity.getActivityItems().filter((item) =>
      isActivityItemTypeEnabled(item.item_type, activity.getEnabledItemTypes())
    );

    if (activity.getHideClosedMerged()) {
      result = result.filter((item) => !isClosedOrMergedActivity(item));
    }

    if (activity.getHideBots()) {
      result = result.filter((item) => !isBot(item.author));
    }

    if (activity.getHideDefaultBranchActivity()) {
      result = result.filter((item) => !isDefaultBranchActivity(item));
    }

    return result;
  });

  const visibleWorkspaceActivity = $derived.by(() => {
    let result = activity.getWorkspaceActivity().filter((subject) =>
      isActivityItemTypeEnabled(subject.item_type, activity.getEnabledItemTypes())
    );

    if (activity.getHideClosedMerged()) {
      result = result.filter((subject) =>
        subject.item_state !== "closed" && subject.item_state !== "merged"
      );
    }

    return result;
  });

  const groups = $derived.by(() => {
    const map = new SvelteMap<string, ActivityItem[]>();

    for (const item of displayItems) {
      const key = isDefaultBranchActivity(item)
        ? activityBranchKey({
            provider: item.repo.provider,
            platformHost: item.repo.platform_host,
            owner: item.repo.owner,
            name: item.repo.name,
            repoPath: item.repo.repo_path,
            branchName: item.branch_name || "default branch",
          })
        : activityItemKey({
            provider: item.repo.provider,
            platformHost: item.repo.platform_host,
            owner: item.repo.owner,
            name: item.repo.name,
            repoPath: item.repo.repo_path,
            itemType: item.item_type,
            itemNumber: item.item_number,
          });
      const bucket = map.get(key);
      if (bucket) bucket.push(item);
      else map.set(key, [item]);
    }

    const groupsByKey = new SvelteMap<string, ActivityGroup>();
    for (const [key, events] of map) {
      events.sort(
        (a, b) =>
          parseAPITimestamp(b.created_at).getTime()
          - parseAPITimestamp(a.created_at).getTime(),
      );
      const representative = events[0];
      if (!representative) continue;
      groupsByKey.set(key, {
        key,
        representative,
        events,
        eventCount: events.length,
        latestTime: representative.created_at,
      });
    }

    for (const subject of visibleWorkspaceActivity) {
      const key = activityItemKey({
        provider: subject.repo.provider,
        platformHost: subject.repo.platform_host,
        owner: subject.repo.owner,
        name: subject.repo.name,
        repoPath: subject.repo.repo_path,
        itemType: subject.item_type,
        itemNumber: subject.item_number,
      });
      const existing = groupsByKey.get(key);
      if (existing) {
        if (
          parseAPITimestamp(subject.activity_at).getTime()
          > parseAPITimestamp(existing.latestTime).getTime()
        ) {
          existing.latestTime = subject.activity_at;
        }
        existing.workspaceActivityAt = subject.activity_at;
        existing.representative = {
          ...existing.representative,
          item_title: existing.representative.item_title || subject.item_title,
          item_url: existing.representative.item_url || subject.item_url,
          item_state: existing.representative.item_state || subject.item_state,
          item_author: existing.representative.item_author || subject.item_author || "",
          ...(subject.workspace ? { workspace: subject.workspace } : {}),
        };
        continue;
      }
      groupsByKey.set(key, {
        key,
        representative: subjectSelectionItem(subject),
        events: [],
        eventCount: 0,
        latestTime: subject.activity_at,
        workspaceActivityAt: subject.activity_at,
      });
    }

    const result = [...groupsByKey.values()];

    result.sort(
      (a, b) =>
        parseAPITimestamp(b.latestTime).getTime()
        - parseAPITimestamp(a.latestTime).getTime(),
    );
    return result;
  });

  const visibleGroups = $derived(groups.slice(0, 30));

  const authorOptions = $derived.by<TypeaheadOption[]>(() =>
    activity.getActivityAuthors().map((author) => ({
      name: author,
      label: author,
    })),
  );

  const activeFilterCount = $derived(
    (activity.getActivityAuthor() ? 1 : 0)
    + (selectedRepo ? 1 : 0)
    + (activity.getEnabledItemTypes().has("pr") ? 0 : 1)
    + (activity.getEnabledItemTypes().has("issue") ? 0 : 1)
    + (activity.getHideBots() ? 1 : 0)
    + (activity.getHideDefaultBranchActivity() ? 1 : 0)
    + (grouping.getHideOrgName() ? 1 : 0)
    + (activity.getShowNotifications() ? 0 : 1)
    + (activity.getInvolvesMe() ? 1 : 0),
  );

  const repoLabelFormatter = $derived.by(() =>
    createRepoLabelFormatter(
      [
        ...displayItems.map(activityRepoIdentity),
        ...visibleWorkspaceActivity.map(workspaceRepoIdentity),
      ],
      { showOrgNames: !grouping.getHideOrgName() },
    ),
  );

  function applyFilters(): void {
    activity.setActivityFilterTypes(buildActivityFilterTypes(
      activity.getEnabledItemTypes(),
      activity.getEnabledEvents(),
      activity.getHideDefaultBranchActivity(),
      activity.getShowNotifications(),
    ));
    activity.syncToURL();
    activity.loadActivity();
  }

  function toggleItemType(itemType: ActivityItemType): void {
    const next = new SvelteSet(activity.getEnabledItemTypes());
    if (next.has(itemType)) next.delete(itemType);
    else next.add(itemType);
    activity.setEnabledItemTypes(next);
    applyFilters();
  }

  function setTimeRange(range: TimeRange): void {
    activity.setTimeRange(range);
    activity.syncToURL();
    activity.loadActivity();
  }

  function handleTimeRangeChange(value: string): void {
    setTimeRange(value as TimeRange);
  }

  function handleRepoChange(value: string): void {
    onRepoChange?.(value || undefined);
    activity.loadActivity();
  }

  function handleAuthorSelect(author: string): void {
    activity.setActivityAuthor(author || undefined);
    activity.syncToURL();
    activity.loadActivity();
  }

  function toggleHideBots(): void {
    activity.setHideBots(!activity.getHideBots());
    applyFilters();
  }

  function toggleHideNotifications(): void {
    activity.setShowNotifications(!activity.getShowNotifications());
    applyFilters();
  }

  function toggleInvolvesMe(): void {
    activity.setInvolvesMe(!activity.getInvolvesMe());
    activity.loadActivity();
  }

  function toggleHideDefaultBranchActivity(): void {
    activity.setHideDefaultBranchActivity(
      !activity.getHideDefaultBranchActivity(),
    );
    applyFilters();
  }

  function toggleHideOrgName(): void {
    grouping.setHideOrgName(!grouping.getHideOrgName());
  }

  function handleSearchInput(value: string): void {
    searchInput = value;
    searchExecution?.interrupt();
    searchExecution = runtime.runCommand(
      Effect.sleep("300 millis").pipe(
        Effect.andThen(Effect.sync(() => {
          activity.setActivitySearch(value || undefined);
          activity.syncToURL();
          activity.loadActivity();
        })),
      ),
      {
        operation: "debounce mobile activity search",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  function handleCardClick(group: ActivityGroup): void {
    if (isDefaultBranchActivity(group.representative)) {
      const url = group.representative.activity_url;
      if (url) window.open(url, "_blank", "noopener");
      return;
    }
    onSelectItem?.(group.representative);
  }

  function subjectSelectionItem(subject: WorkspaceActivitySubject): ActivityItem {
    const id = `workspace:${activityItemKey({
      provider: subject.repo.provider,
      platformHost: subject.repo.platform_host,
      owner: subject.repo.owner,
      name: subject.repo.name,
      repoPath: subject.repo.repo_path,
      itemType: subject.item_type,
      itemNumber: subject.item_number,
    })}`;
    return {
      id,
      cursor: id,
      activity_type: "workspace",
      author: subject.item_author ?? "",
      body_preview: "",
      created_at: subject.activity_at,
      item_number: subject.item_number,
      item_state: subject.item_state,
      item_title: subject.item_title,
      item_type: subject.item_type,
      item_url: subject.item_url,
      platform_host: subject.repo.platform_host,
      repo_owner: subject.repo.owner,
      repo_name: subject.repo.name,
      repo: subject.repo,
      ...(subject.workspace ? { workspace: subject.workspace } : {}),
    };
  }

  function handleEventClick(event: ActivityItem): void {
    if (isDefaultBranchActivity(event)) {
      const url = event.activity_url;
      if (url) window.open(url, "_blank", "noopener");
      return;
    }
    onSelectItem?.(event);
  }

  function isUnreadNotification(item: ActivityItem): boolean {
    return item.activity_type === "notification" && item.item_state === "unread";
  }

  function handleMarkSeen(domEvent: Event, item: ActivityItem): void {
    domEvent.stopPropagation();
    activity.markNotificationSeen(item);
  }

  function eventLabel(item: ActivityItem): string {
    switch (item.activity_type) {
      case "new_pr":
      case "new_issue":
        return "Opened";
      case "comment":
        return "Comment";
      case "review":
        return "Review";
      case "commit":
      case "default_branch_commit":
        return "Commit";
      case "force_push":
      case "default_branch_force_push":
        return "Force-pushed";
      case "notification":
        return notificationReasonLabel(item.body_preview);
      default:
        return item.activity_type;
    }
  }

  function relativeTime(iso: string): string {
    const diff = Date.now() - parseAPITimestamp(iso).getTime();
    const mins = Math.floor(diff / 60_000);
    if (mins < 1) return "just now";
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    if (days < 7) return `${days}d ago`;
    if (days < 30) return `${Math.floor(days / 7)}w ago`;
    return `${Math.floor(days / 30)}mo ago`;
  }

  function eventTone(type: string): TimelineTone {
    switch (type) {
      case "comment":
        return "warning";
      case "review":
      case "commit":
      case "default_branch_commit":
        return "success";
      case "force_push":
      case "default_branch_force_push":
        return "danger";
      default:
        return "info";
    }
  }

  function latestEvents(group: ActivityGroup): ActivityItem[] {
    return group.events.slice(0, 2);
  }

  function activityRepoIdentity(item: ActivityItem): RepoLabelIdentity {
    return {
      provider: item.repo.provider,
      platformHost: item.repo.platform_host,
      owner: item.repo.owner,
      name: item.repo.name,
      repoPath: item.repo.repo_path,
    };
  }

  function workspaceRepoIdentity(subject: WorkspaceActivitySubject): RepoLabelIdentity {
    return {
      provider: subject.repo.provider,
      platformHost: subject.repo.platform_host,
      owner: subject.repo.owner,
      name: subject.repo.name,
      repoPath: subject.repo.repo_path,
    };
  }

  function repoLabel(item: ActivityItem): string {
    return repoLabelFormatter.format(activityRepoIdentity(item));
  }

  function branchName(item: ActivityItem): string {
    return item.branch_name || "default branch";
  }

  function branchActivityTitle(item: ActivityItem): string {
    if (isDefaultBranchForcePushActivity(item)) {
      const before = shortSha(item.before_sha);
      const after = shortSha(item.after_sha);
      if (before && after) return `${before} -> ${after}`;
    }
    return item.body_preview || shortSha(item.commit_sha) || "Commit";
  }

  function eventDetail(event: ActivityItem): string {
    if (!isDefaultBranchActivity(event)) return event.author;
    if (isDefaultBranchForcePushActivity(event)) return branchActivityTitle(event);
    return [shortSha(event.commit_sha), event.author_name || event.author]
      .filter(Boolean)
      .join(" · ");
  }
</script>

<section class="mobile-activity-inbox" aria-label="Mobile activity inbox">
  <ScrollBox label="Activity inbox">
  <div class="mobile-activity-scroll">
    <header class="mobile-activity-hero">
      <p class="mobile-activity-eyebrow">
        Activity inbox · {activity.getTimeRange()}
      </p>
      <h1>What needs attention?</h1>
    </header>

    <div class="mobile-activity-search">
      <SearchInput
        bind:value={searchInput}
        block
        placeholder="Search activity"
        ariaLabel="Search activity"
        oninput={handleSearchInput}
      />
    </div>

    <div class="mobile-filter-summary">
      <button
        type="button"
        class="mobile-filter-disclosure"
        aria-label={activity.getActivityAuthor()
          ? `Filters · ${activity.getActivityAuthor()}`
          : "Filters"}
        aria-expanded={filtersExpanded}
        aria-controls="mobile-activity-filters"
        onclick={() => filtersExpanded = !filtersExpanded}
      >
        <span>Filters</span>
        {#if activity.getActivityAuthor()}
          <span class="mobile-filter-author">· {activity.getActivityAuthor()}</span>
        {/if}
        {#if activeFilterCount > 0}
          <span class="mobile-filter-count" aria-label={`${activeFilterCount} active filters`}>
            {activeFilterCount}
          </span>
        {/if}
        <span aria-hidden="true">{filtersExpanded ? "−" : "+"}</span>
      </button>

    </div>

    {#if filtersExpanded}
    <div id="mobile-activity-filters" class="mobile-activity-filter-grid" aria-label="Activity filters">
      <div class="mobile-author-filter">
        <span>Author</span>
        <Typeahead
          options={authorOptions}
          value={activity.getActivityAuthor() ?? ""}
          fallbackLabel="Anyone"
          placeholder="Filter authors"
          title="Filter by PR or issue author"
          allowClear
          clearLabel="Anyone"
          loading={activity.isActivityAuthorsLoading()}
          error={activity.getActivityAuthorsError() ?? ""}
          onselect={handleAuthorSelect}
        />
      </div>

      <div class="mobile-item-type-toggle">
        <Toggle
          checked={activity.getEnabledItemTypes().has("pr")}
          label="PRs"
          onchange={() => toggleItemType("pr")}
        />
      </div>

      <div class="mobile-item-type-toggle">
        <Toggle
          checked={activity.getEnabledItemTypes().has("issue")}
          label="Issues"
          onchange={() => toggleItemType("issue")}
        />
      </div>

      <div class="mobile-filter-select">
        <span>Range</span>
        <SelectDropdown
          class="mobile-filter-dropdown"
          title="Time range"
          value={activity.getTimeRange()}
          options={timeRangeOptions}
          onchange={handleTimeRangeChange}
        />
      </div>

      <div class="mobile-filter-select mobile-filter-select--repo">
        <span>Repo</span>
        <SelectDropdown
          class="mobile-filter-dropdown"
          title="Repository"
          value={selectedRepo ?? ""}
          options={repoOptions}
          onchange={handleRepoChange}
        />
      </div>

      <button
        type="button"
        class="mobile-filter-toggle"
        class:active={activity.getInvolvesMe()}
        aria-pressed={activity.getInvolvesMe()}
        onclick={toggleInvolvesMe}
      >Involves me</button>

      <button
        type="button"
        class="mobile-filter-toggle"
        class:active={activity.getHideBots()}
        aria-pressed={activity.getHideBots()}
        onclick={toggleHideBots}
      >Hide bots</button>

      <button
        type="button"
        class="mobile-filter-toggle"
        class:active={activity.getHideDefaultBranchActivity()}
        aria-pressed={activity.getHideDefaultBranchActivity()}
        onclick={toggleHideDefaultBranchActivity}
      >Hide branch</button>

      <button
        type="button"
        class="mobile-filter-toggle"
        class:active={grouping.getHideOrgName()}
        aria-pressed={grouping.getHideOrgName()}
        onclick={toggleHideOrgName}
      >Hide org</button>

      <button
        type="button"
        class="mobile-filter-toggle"
        class:active={!activity.getShowNotifications()}
        aria-pressed={!activity.getShowNotifications()}
        onclick={toggleHideNotifications}
      >Hide notifications</button>
    </div>
    {/if}


    {#if activity.getActivityError()}
      <div class="mobile-activity-error">{activity.getActivityError()}</div>
    {/if}

    {#if settings.isSettingsLoaded() && !settings.hasConfiguredRepos()}
      <div class="mobile-activity-empty">No repositories configured.</div>
    {:else if visibleGroups.length === 0 && activity.isActivityLoading()}
      <div class="mobile-activity-empty">Loading activity…</div>
    {:else if visibleGroups.length === 0}
      <div class="mobile-activity-empty">No activity found</div>
    {:else}
      <div class="mobile-activity-card-list">
        {#each visibleGroups as group (group.key)}
          {@const item = group.representative}
          <article>
            <Card level="raised" padding="none" class="mobile-activity-card">
              <button
                type="button"
                class="mobile-activity-card__button"
                onclick={() => handleCardClick(group)}
              >
                <span class="mobile-activity-card__top">
                  <span class="mobile-activity-card__chips">
                    {#if isDefaultBranchActivity(item)}
                      <Chip size="sm" tone="muted" uppercase={false}>Branch</Chip>
                      <span class="mobile-activity-number">{branchName(item)}</span>
                    {:else}
                      <ItemKindChip kind={item.item_type === "issue" ? "issue" : "pr"} size="sm" />
                      <span class="mobile-activity-number">#{item.item_number}</span>
                      {#if item.workspace}
                        <WorkspaceIndicator status={item.workspace.status} size={16} />
                      {/if}
                      {#if item.item_state === "merged" || item.item_state === "closed"}
                        <ItemStateChip state={item.item_state} size="sm" />
                      {/if}
                    {/if}
                  </span>
                  <time
                    title={group.workspaceActivityAt === group.latestTime
                      ? "Recent workspace activity"
                      : undefined}
                  >{relativeTime(group.latestTime)}</time>
                </span>

                <span class="mobile-activity-card__title">
                  {isDefaultBranchActivity(item) ? branchActivityTitle(item) : item.item_title}
                </span>
                <span class="mobile-activity-card__meta">
                  <span>{repoLabel(item)}</span>
                  <span>{group.eventCount} {group.eventCount === 1 ? "event" : "events"}</span>
                </span>
              </button>

              {#if group.eventCount > 0}
                <Timeline
                  class="mobile-activity-events"
                  ariaLabel={`Recent activity for ${
                    isDefaultBranchActivity(item) ? branchActivityTitle(item) : item.item_title
                  }`}
                >
                  {#each latestEvents(group) as event (event.id)}
                    <TimelineItem class="mobile-activity-event-item" tone={eventTone(event.activity_type)}>
                      <div class="mobile-activity-event-slot">
                        <button
                          type="button"
                          class="mobile-activity-event"
                          onclick={() => handleEventClick(event)}
                        >
                          <span class="mobile-activity-event__body">
                            <strong>{eventLabel(event)}</strong>
                            <span>{eventDetail(event)}</span>
                          </span>
                          <time>{relativeTime(event.created_at)}</time>
                        </button>
                        {#if isUnreadNotification(event)}
                          <button
                            type="button"
                            class="mobile-activity-event-seen"
                            aria-label="Mark notification seen"
                            title="Mark seen"
                            onclick={(domEvent) => handleMarkSeen(domEvent, event)}
                          >
                            <CheckIcon size="20" strokeWidth="2" aria-hidden="true" />
                          </button>
                        {/if}
                      </div>
                    </TimelineItem>
                  {/each}
                </Timeline>
              {/if}
            </Card>
          </article>
        {/each}
      </div>
    {/if}

    {#if activity.isActivityCapped()}
      <div class="mobile-activity-capped">
        Showing most recent 5,000 events. Narrow the range or filters to see more.
      </div>
    {/if}
  </div>
  </ScrollBox>
</section>

<style>
  .mobile-activity-inbox {
    --mobile-type-xs: var(--font-size-xs);
    --mobile-type-sm: var(--font-size-sm);
    --mobile-type-body: var(--font-size-md);
    --mobile-type-title: var(--font-size-xl);
    --mobile-type-display: var(--font-size-2xl);
    --mobile-type-metric: var(--font-size-2xl);
    --mobile-space-2xs: 4.5px;
    --mobile-space-xs: 7px;
    --mobile-space-sm: 10px;
    --mobile-space-md: 13px;
    --mobile-space-lg: 17.5px;
    --mobile-radius-sm: var(--radius-md);
    --mobile-radius-md: var(--radius-lg);
    --mobile-hit-target: 45.5px;
    container-type: inline-size;
    font-size: var(--font-size-md);
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    overflow: hidden;
    background: var(--bg-primary);
  }

  .mobile-activity-scroll {
    padding:
      var(--mobile-space-md)
      var(--mobile-space-sm)
      max(var(--mobile-space-lg), env(safe-area-inset-bottom));
    font-size: var(--font-size-md);
  }

  .mobile-activity-hero {
    margin: var(--mobile-space-2xs) var(--mobile-space-2xs) var(--mobile-space-sm);
  }

  .mobile-activity-eyebrow {
    margin: 0 0 var(--mobile-space-2xs);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 700;
    letter-spacing: 0.02em;
  }

  .mobile-activity-hero h1 {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-xl);
    line-height: 1.16;
    letter-spacing: 0;
  }

  .mobile-activity-search {
    margin-bottom: var(--mobile-space-sm);
  }

  .mobile-activity-search :global(.kit-search-input) {
    min-height: calc(var(--mobile-hit-target) + var(--mobile-space-xs));
    border-radius: var(--radius-lg);
    font-size: var(--font-size-md);
    /* The phone inbox keeps the inset field look of the original
       hand-rolled search (mobile-routes e2e pins this against
       --bg-inset). */
    background: var(--bg-inset);
  }

  .mobile-filter-summary {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--mobile-space-xs);
    margin-bottom: var(--mobile-space-sm);
  }

  .mobile-filter-disclosure {
    max-width: 100%;
    min-height: var(--mobile-hit-target);
    display: inline-flex;
    align-items: center;
    gap: var(--mobile-space-xs);
    padding: 0 var(--mobile-space-md);
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    background: var(--bg-inset);
    font: inherit;
    font-weight: 750;
  }

  .mobile-filter-author {
    min-width: 0;
    max-width: 12rem;
    overflow: hidden;
    color: var(--text-secondary);
    font-weight: var(--font-weight-medium, 500);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mobile-filter-count {
    min-width: 20px;
    padding: 1px 6px;
    border-radius: 999px;
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 14%, transparent);
    font-size: var(--font-size-xs);
    text-align: center;
  }

  .mobile-activity-filter-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--mobile-space-xs);
    margin-bottom: var(--mobile-space-sm);
  }

  .mobile-author-filter {
    grid-column: 1 / -1;
    min-width: 0;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
    gap: var(--mobile-space-xs);
    min-height: var(--mobile-hit-target);
    padding: 0 var(--mobile-space-sm);
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    background: var(--bg-inset);
  }

  .mobile-author-filter > span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 750;
  }

  .mobile-author-filter :global(.kit-typeahead) {
    width: 100%;
    min-width: 0;
  }

  .mobile-filter-select,
  .mobile-item-type-toggle {
    min-width: 0;
    min-height: var(--mobile-hit-target);
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
    gap: var(--mobile-space-xs);
    padding: 0 var(--mobile-space-sm);
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    background: var(--bg-inset);
  }

  .mobile-item-type-toggle {
    display: flex;
  }

  .mobile-item-type-toggle :global(.kit-toggle) {
    width: 100%;
    min-height: var(--mobile-hit-target);
  }

  .mobile-filter-select--repo {
    grid-column: 1 / -1;
  }

  .mobile-filter-select span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 750;
    letter-spacing: 0.01em;
  }

  .mobile-filter-select :global(.mobile-filter-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .mobile-filter-select :global(.mobile-filter-dropdown .kit-select-dropdown__trigger) {
    height: auto;
    min-height: calc(var(--mobile-hit-target) - var(--mobile-space-sm));
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 750;
  }

  .mobile-filter-select :global(.mobile-filter-dropdown .kit-select-dropdown__list) {
    left: 0;
    right: auto;
    width: min(260px, calc(100vw - (var(--mobile-space-sm) * 2)));
    max-width: calc(100vw - (var(--mobile-space-sm) * 2));
    padding: var(--mobile-space-2xs);
    border-radius: var(--radius-md);
  }

  .mobile-filter-select :global(.mobile-filter-dropdown .kit-select-dropdown__option) {
    min-height: var(--mobile-hit-target);
    padding: var(--mobile-space-xs) var(--mobile-space-sm);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    line-height: 1.2;
  }

  .mobile-filter-select :global(.mobile-filter-dropdown .kit-select-dropdown__check) {
    width: 13px;
  }

  .mobile-filter-toggle {
    min-height: var(--mobile-hit-target);
    flex: 0 0 auto;
    padding: var(--mobile-space-sm) var(--mobile-space-md);
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    background: var(--bg-inset);
    font-size: var(--font-size-sm);
    font-weight: 750;
  }

  .mobile-filter-toggle.active {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 12%, transparent);
    border-color: color-mix(in srgb, var(--accent-blue) 34%, transparent);
  }


  .mobile-activity-card-list {
    display: grid;
    gap: var(--mobile-space-md);
  }

  :global(.mobile-activity-card) {
    overflow: hidden;
  }


  .mobile-activity-card__button {
    display: flex;
    flex-direction: column;
    gap: var(--mobile-space-sm);
    width: 100%;
    min-height: var(--mobile-hit-target);
    padding: var(--mobile-space-md);
    border: 0;
    color: inherit;
    background: transparent;
    text-align: left;
  }

  .mobile-activity-card__top {
    display: flex;
    align-items: center;
    gap: var(--mobile-space-sm);
    min-width: 0;
  }

  .mobile-activity-card__chips {
    display: flex;
    align-items: center;
    gap: var(--mobile-space-xs);
    min-width: 0;
  }

  .mobile-activity-card__chips :global(.kit-chip--sm) {
    min-height: calc(var(--mobile-hit-target) * 0.55);
    padding: 0 var(--mobile-space-xs);
    font-size: var(--font-size-xs);
  }

  .mobile-activity-card__top time {
    margin-left: auto;
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-weight: 700;
  }

  .mobile-activity-number {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-weight: 700;
  }

  .mobile-activity-card__title {
    display: -webkit-box;
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-xl);
    font-weight: 800;
    line-height: 1.22;
    letter-spacing: -0.018em;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 3;
    line-clamp: 3;
  }

  .mobile-activity-card__meta {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: baseline;
    gap: var(--mobile-space-xs) var(--mobile-space-sm);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.25;
  }

  .mobile-activity-card__meta span {
    min-width: 0;
  }

  .mobile-activity-card__meta span:first-child {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mobile-activity-card__meta span:last-child {
    justify-self: end;
    text-align: right;
    white-space: nowrap;
  }

  :global(.mobile-activity-events) {
    padding: 0 var(--mobile-space-sm) var(--mobile-space-sm);
    --kit-timeline-halo: var(--bg-surface);
  }

  :global(.mobile-activity-events .kit-timeline-item) {
    gap: var(--mobile-space-xs);
  }

  :global(.mobile-activity-events .kit-timeline-item__rail) {
    width: 14px;
  }

  :global(.mobile-activity-events .kit-timeline-item__content) {
    padding: 0 0 var(--mobile-space-xs);
  }

  :global(.mobile-activity-events .kit-timeline-item:last-child .kit-timeline-item__content) {
    padding-bottom: 0;
  }

  .mobile-activity-event-slot {
    display: flex;
    align-items: stretch;
    gap: var(--mobile-space-xs);
  }

  .mobile-activity-event-slot > .mobile-activity-event {
    flex: 1;
    min-width: 0;
  }

  /* A notification event button cannot nest the mark-seen button, so the
     touch-sized seen control sits beside it as a sibling instead. */
  .mobile-activity-event-seen {
    flex: 0 0 auto;
    min-width: var(--mobile-hit-target);
    min-height: var(--mobile-hit-target);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: thin solid var(--border-muted);
    border-radius: var(--radius-md);
    color: var(--accent-blue);
    background: var(--bg-inset);
  }

  .mobile-activity-event-seen:active {
    background: color-mix(in srgb, var(--accent-blue) 14%, transparent);
  }

  .mobile-activity-event {
    min-height: var(--mobile-hit-target);
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--mobile-space-sm);
    padding: var(--mobile-space-sm);
    border: thin solid var(--border-muted);
    border-radius: var(--radius-md);
    color: inherit;
    background: var(--bg-inset);
    text-align: left;
  }


  .mobile-activity-event__body {
    min-width: 0;
  }

  .mobile-activity-event__body strong {
    display: block;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 750;
  }

  .mobile-activity-event__body span {
    display: block;
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mobile-activity-event time {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 750;
  }

  .mobile-activity-empty,
  .mobile-activity-error,
  .mobile-activity-capped {
    padding: var(--mobile-space-lg);
    border: thin solid var(--border-default);
    border-radius: var(--radius-lg);
    color: var(--text-muted);
    background: var(--bg-surface);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .mobile-activity-error {
    color: var(--accent-red);
  }

  .mobile-activity-capped {
    margin-top: var(--mobile-space-md);
    color: var(--accent-amber);
  }
</style>
