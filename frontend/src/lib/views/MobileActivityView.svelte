<script lang="ts">
  import RepoPrivateIcon from "../components/shared/RepoPrivateIcon.svelte";
  import { activityFilterMenu, activityItemVisible, activitySubjectVisible } from "../components/activityFilters.js";
  import FilterSectionsPanel from "../components/shared/FilterSectionsPanel.svelte";
  import LabelFilterMenu from "../components/shared/LabelFilterMenu.svelte";
  import AuthorFilterMenu from "../components/shared/AuthorFilterMenu.svelte";
  import { rememberedLabelChoices } from "../utils/label-filter.js";
  import { Effect } from "effect";
  import { onDestroy, onMount, tick } from "svelte";
  import type { Attachment } from "svelte/attachments";
  import { SvelteMap } from "svelte/reactivity";
  import { getAppRuntime } from "../app/runtime-context.js";
  import { observeIntersection } from "../browser/observers.js";
  import type { AppExecution } from "../app/runtime.js";
  import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../api/types.js";
  import { getStores } from "../context.js";
  import {
    Card,
    Chip,
    ScrollBox,
    Timeline,
    TimelineItem,
    type TypeaheadOption,
    type TimelineTone,
  } from "@kenn-io/kit-ui";
  import { showFlash } from "../stores/flash.svelte.js";
  import {
    rememberMobileListPosition,
    scrollViewportOf,
    takeMobileListPosition,
  } from "../stores/mobile-list-return.js";
  import { parseAPITimestamp } from "../utils/time.js";
  import { latestActivityAt } from "../utils/effective-activity.js";
  import ItemKindChip from "../components/shared/ItemKindChip.svelte";
  import ItemStateChip from "../components/shared/ItemStateChip.svelte";
  import RepoTypeahead from "../components/RepoTypeahead.svelte";
  import MobileTriageSearchBar from "../components/mobile/MobileTriageSearchBar.svelte";
  import WorkspaceIndicator from "../components/shared/WorkspaceIndicator.svelte";
  import AgentStatusIndicator from "../components/shared/AgentStatusIndicator.svelte";
  import CheckIcon from "@lucide/svelte/icons/check";
  import {
    activityBranchKey,
    activityAuthor,
    activityItemKey,
    isDefaultBranchActivity,
    isDefaultBranchForcePushActivity,
    notificationReasonLabel,
    shortSha,
  } from "../components/activityRows.js";
  import {
    createRepoLabelFormatter,
    type RepoLabelIdentity,
  } from "../utils/repo-label.js";
  import { repositoryKeyFromWire } from "../api/repository-key.js";

  const { activity, settings, sync, grouping, issues } = getStores();
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
    previewRevision: string;
    workspaceActivityAt?: string;
  };

  const filterMenu = $derived(
    activityFilterMenu({ activity, grouping, issues, labelMenu: labelFilterMenu, authorMenu: authorFilterMenu }),
  );
  let searchInput = $state("");
  let activityPageLimit = $state(30);
  let filtersExpanded = $state(false);
  let inboxRoot = $state<HTMLElement | null>(null);
  // Scroll offset parked when a card opened a detail, applied once cards are
  // on screen so Back lands on the same cards.
  let pendingScrollTop = $state<number | null>(null);
  let flashedActivityError: string | null = null;
  let paginationArmed = true;
  let paginationIntersecting = false;
  let searchExecution: AppExecution<void, never> | null = null;
  let unsubSync: (() => void) | undefined;

  onMount(() => {
    activity.initializeFromMount();
    searchInput = activity.getActivitySearch() ?? "";
    const parked = takeMobileListPosition("activity");
    activityPageLimit = parked?.pageLimit ?? 30;
    pendingScrollTop = parked?.scrollTop ?? null;
    activity.setActivityPageLimit(activityPageLimit);
    activity.ensureActivityLoaded();
    unsubSync = sync.subscribeSyncCompleteWithoutLiveUpdates(() => {
      activity.loadActivity();
      activity.loadActivityAuthors(true);
    });
  });

  $effect(() => {
    const error = activity.getActivityError();
    if (!error) {
      flashedActivityError = null;
      return;
    }
    if (error === flashedActivityError) return;
    flashedActivityError = error;
    showFlash(error, { tone: "warning", durationMs: 8_000 });
  });

  onDestroy(() => {
    unsubSync?.();
    searchExecution?.interrupt();
  });

  const displayItems = $derived(activity.getActivityItems().filter((item) => activityItemVisible(activity, item)));

  const visibleWorkspaceActivity = $derived(
    activity.getUseWorkspaceActivityForRecency()
      ? activity.getWorkspaceActivity().filter((subject) => activitySubjectVisible(activity, subject))
      : [],
  );

  const visibleItemActivity = $derived(
    activity.getItemActivity().filter((subject) => activitySubjectVisible(activity, subject)),
  );

  // One entry per loaded PR or issue, so a label's count is items, not events.
  const labelSubjects = $derived.by(() => {
    const subjects = new SvelteMap<string, { labels?: ActivityItem["labels"] }>();
    for (const subject of [...activity.getActivityItems(), ...activity.getItemActivity()]) {
      const key = activityItemKey({
        provider: subject.repo.provider,
        platformHost: subject.repo.platform_host,
        repositoryKey: repositoryKeyFromWire(subject.repo),
        owner: subject.repo.owner,
        name: subject.repo.name,
        repoPath: subject.repo.repo_path,
        itemType: subject.item_type,
        itemNumber: subject.item_number,
      });
      subjects.set(key, { labels: subject.labels });
    }
    return [...subjects.values()];
  });
  const activityLabelChoices = rememberedLabelChoices(() => labelSubjects, activity.getLabelFilters);
  const labelChoices = $derived(activityLabelChoices());

  const groups = $derived.by(() => {
    const map = new SvelteMap<string, ActivityItem[]>();

    for (const item of displayItems) {
      const key = isDefaultBranchActivity(item)
        ? activityBranchKey({
            provider: item.repo.provider,
            platformHost: item.repo.platform_host,
            repositoryKey: repositoryKeyFromWire(item.repo),
            owner: item.repo.owner,
            name: item.repo.name,
            repoPath: item.repo.repo_path,
            branchName: item.branch_name || "default branch",
          })
        : activityItemKey({
            provider: item.repo.provider,
            platformHost: item.repo.platform_host,
            repositoryKey: repositoryKeyFromWire(item.repo),
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
        latestTime: latestActivityAt(
          representative.created_at,
          representative.item_last_activity_at,
        ),
        previewRevision: "",
      });
    }

    for (const subject of visibleItemActivity) {
      const key = activityItemKey({
        provider: subject.repo.provider,
        platformHost: subject.repo.platform_host,
        repositoryKey: repositoryKeyFromWire(subject.repo),
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
        existing.representative = {
          ...existing.representative,
          item_title: subject.item_title,
          item_url: subject.item_url,
          item_state: subject.item_state,
          item_author: subject.item_author || existing.representative.item_author || "",
          platform_host: subject.repo.platform_host,
          repo_owner: subject.repo.owner,
          repo_name: subject.repo.name,
          repo: subject.repo,
          ...(subject.workspace ? { workspace: subject.workspace } : {}),
        };
        existing.previewRevision = subject.event_ledger_revision ?? subject.activity_at;
        continue;
      }
      groupsByKey.set(key, {
        key,
        representative: subjectSelectionItem(subject, "parent"),
        events: [],
        eventCount: 0,
        latestTime: subject.activity_at,
        previewRevision: subject.event_ledger_revision ?? subject.activity_at,
      });
    }

    for (const subject of visibleWorkspaceActivity) {
      const key = activityItemKey({
        provider: subject.repo.provider,
        platformHost: subject.repo.platform_host,
        repositoryKey: repositoryKeyFromWire(subject.repo),
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
        representative: subjectSelectionItem(subject, "workspace"),
        events: [],
        eventCount: 0,
        latestTime: subject.activity_at,
        previewRevision: subject.activity_at,
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

  const visibleGroups = $derived(activityPageLimit < 500 ? groups.slice(0, activityPageLimit) : groups);

  const authorOptions = $derived.by<TypeaheadOption[]>(() =>
    activity.getActivityAuthors().map((author) => ({
      name: author,
      label: author,
    })),
  );

  const activeFilterCount = $derived(
    filterMenu.filterCount
    + (selectedRepo ? 1 : 0)
    + filterMenu.itemTypes.filter((item) => !item.active).length,
  );

  const repoLabelFormatter = $derived.by(() =>
    createRepoLabelFormatter(
      [
        ...displayItems.map(activityRepoIdentity),
        ...visibleItemActivity.map(subjectRepoIdentity),
        ...visibleWorkspaceActivity.map(subjectRepoIdentity),
      ],
      { showOrgNames: !grouping.getHideOrgName() },
    ),
  );


  function handleRepoChange(value: string | undefined): void {
    onRepoChange?.(value);
    activity.loadActivity();
  }

  function handleAuthorSelect(author: string): void {
    activity.setActivityAuthor(author || undefined);
    activity.syncToURL();
    activity.loadActivity();
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

  // Every path that leaves the feed for a detail parks the feed position
  // first, so Back lands on the same rows whether the card header or one of
  // its timeline events opened the item.
  function openItem(item: ActivityItem): void {
    if (isDefaultBranchActivity(item)) {
      const url = item.activity_url;
      if (url) window.open(url, "_blank", "noopener");
      return;
    }
    rememberMobileListPosition("activity", {
      scrollTop: scrollViewportOf(inboxRoot)?.scrollTop ?? 0,
      pageLimit: activityPageLimit,
    });
    onSelectItem?.(item);
  }

  function handleCardClick(group: ActivityGroup): void {
    openItem(group.representative);
  }

  $effect(() => {
    if (pendingScrollTop === null || visibleGroups.length === 0) return;
    const top = pendingScrollTop;
    pendingScrollTop = null;
    void tick().then(() => {
      const viewport = scrollViewportOf(inboxRoot);
      if (viewport) viewport.scrollTop = top;
    });
  });

  function loadPreviewWhenVisible(key: string, previewRevision: string): Attachment<HTMLElement> {
    return (node) => {
      void previewRevision;
      const load = () => activity.loadThreadPreview(key);

      if (typeof IntersectionObserver === "undefined") {
        load();
        return;
      }

      const root = node.closest(".kit-scrollbox__viewport");
      const execution = runtime.runCommand(
        Effect.scoped(
          observeIntersection(
            node,
            (entries) => {
              if (entries[0]?.isIntersecting) load();
            },
            { root, rootMargin: "240px 0px" },
          ).pipe(Effect.andThen(Effect.never)),
        ),
        {
          operation: "observe mobile activity preview",
          safeContext: {},
          onFailure: () => {},
        },
      );

      return () => execution.interrupt();
    };
  }

  function loadMoreActivity(): void {
    activityPageLimit = Math.min(activityPageLimit + 30, 500);
    activity.setActivityPageLimit(activityPageLimit);
    activity.loadActivity();
  }

  const autoloadMoreActivity: Attachment<HTMLElement> = (node) => {
    if (typeof IntersectionObserver === "undefined") {
      if (paginationArmed) {
        paginationArmed = false;
        loadMoreActivity();
      }
      return;
    }

    const root = node.closest<HTMLElement>(".kit-scrollbox__viewport");
    const armPagination = () => {
      if (paginationArmed || activity.isActivityLoading()) return;
      paginationArmed = true;
      if (!paginationIntersecting) return;
      paginationArmed = false;
      loadMoreActivity();
    };
    root?.addEventListener("touchstart", armPagination, { passive: true });
    root?.addEventListener("wheel", armPagination, { passive: true });
    root?.addEventListener("pointerdown", armPagination, { passive: true });
    root?.addEventListener("keydown", armPagination);

    const execution = runtime.runCommand(
      Effect.scoped(
        observeIntersection(
          node,
          (entries) => {
            const nextIntersecting = entries[0]?.isIntersecting === true;
            paginationIntersecting = nextIntersecting;
            if (nextIntersecting && paginationArmed) {
              paginationArmed = false;
              loadMoreActivity();
            }
          },
          { root, rootMargin: "240px 0px" },
        ).pipe(Effect.andThen(Effect.never)),
      ),
      {
        operation: "observe mobile activity pagination",
        safeContext: {},
        onFailure: () => {},
      },
    );

    return () => {
      paginationIntersecting = false;
      root?.removeEventListener("touchstart", armPagination);
      root?.removeEventListener("wheel", armPagination);
      root?.removeEventListener("pointerdown", armPagination);
      root?.removeEventListener("keydown", armPagination);
      execution.interrupt();
    };
  };

  function subjectSelectionItem(
    subject: ActivitySubject | WorkspaceActivitySubject,
    activityType: "parent" | "workspace",
  ): ActivityItem {
    const id = `${activityType}:${activityItemKey({
      provider: subject.repo.provider,
      platformHost: subject.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(subject.repo),
      owner: subject.repo.owner,
      name: subject.repo.name,
      repoPath: subject.repo.repo_path,
      itemType: subject.item_type,
      itemNumber: subject.item_number,
    })}`;
    return {
      id,
      cursor: id,
      activity_type: activityType,
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
    openItem(event);
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
      repositoryKey: repositoryKeyFromWire(item.repo),
      owner: item.repo.owner,
      name: item.repo.name,
      repoPath: item.repo.repo_path,
    };
  }

  function subjectRepoIdentity(subject: ActivitySubject | WorkspaceActivitySubject): RepoLabelIdentity {
    return {
      provider: subject.repo.provider,
      platformHost: subject.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(subject.repo),
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
    if (!isDefaultBranchActivity(event)) return activityAuthor(event);
    if (isDefaultBranchForcePushActivity(event)) return branchActivityTitle(event);
    return [shortSha(event.commit_sha), activityAuthor(event)]
      .filter(Boolean)
      .join(" · ");
  }
</script>

{#snippet authorFilterMenu()}
  <AuthorFilterMenu
    options={authorOptions}
    value={activity.getActivityAuthor() ?? ""}
    loading={activity.isActivityAuthorsLoading()}
    error={activity.getActivityAuthorsError() ?? ""}
    onselect={handleAuthorSelect}
  />
{/snippet}

{#snippet labelFilterMenu()}
  <LabelFilterMenu
    choices={labelChoices}
    oncycle={(name) => activity.cycleLabelFilter(name)}
    emptyText="No labels on loaded activity"
  />
{/snippet}

<section class="mobile-activity-inbox" aria-label="Mobile activity inbox" bind:this={inboxRoot}>
  <ScrollBox label="Activity inbox">
  <div class="mobile-activity-scroll">
    <div
      class="mobile-activity-search-strip"
      class:mobile-activity-search-strip--expanded={filtersExpanded}
    >
      <MobileTriageSearchBar
        bind:value={searchInput}
        placeholder="Search activity"
        searchAriaLabel="Search activity"
        filterAriaLabel={activity.getActivityAuthor()
          ? `Filters · ${activity.getActivityAuthor()}`
          : "Filters"}
        filterControls="mobile-activity-filters"
        {filtersExpanded}
        filtersActive={filtersExpanded || activeFilterCount > 0}
        oninput={handleSearchInput}
        ontoggle={() => filtersExpanded = !filtersExpanded}
      />
    </div>

    <div
      id="mobile-activity-filters"
      class="mobile-activity-filter-grid"
      class:mobile-activity-filter-grid--expanded={filtersExpanded}
      aria-label="Activity filters"
      hidden={!filtersExpanded}
    >
      <div class="mobile-identity-filters">
        <div class="mobile-filter-select mobile-filter-select--repo">
          <RepoTypeahead
            selected={selectedRepo}
            onchange={handleRepoChange}
            allowPresetManagement={false}
            mobile
          />
        </div>
      </div>

      <div class="mobile-shared-filters">
        <FilterSectionsPanel sections={filterMenu.sections} resetLabel="Reset filters" onReset={filterMenu.reset} />
      </div>
    </div>

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
          <article {@attach loadPreviewWhenVisible(group.key, group.previewRevision)}>
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
                  <AgentStatusIndicator state={item.workspace?.agent_state} />
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
                  <span>{repoLabel(item)}<RepoPrivateIcon isPrivate={item.repo.private} /></span>
                  <span>Recent activity</span>
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

    {#if (activity.isActivityCapped() || activity.isItemActivityCapped()) && activityPageLimit < 500}
      <div
        class="mobile-activity-loading-sentinel"
        aria-live="polite"
        {@attach autoloadMoreActivity}
      >
        {#if activity.isActivityLoading()}Loading more…{/if}
      </div>
    {:else if activityPageLimit >= 500 && activity.canLoadOlderItems()}
      <div class="mobile-activity-older">
        <ListLoadMore loading={activity.isLoadingOlderItems()} onclick={activity.loadOlderItems} label="Load older items" size="md" />
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
    --mobile-hit-target: 40px;
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

  .mobile-activity-search-strip {
    margin:
      calc(-1 * var(--mobile-space-md))
      calc(-1 * var(--mobile-space-sm))
      var(--mobile-space-sm);
  }

  .mobile-activity-search-strip--expanded {
    margin-bottom: 0;
  }

  .mobile-activity-filter-grid {
    display: none;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--mobile-space-xs);
    margin:
      0
      calc(-1 * var(--mobile-space-sm))
      var(--mobile-space-sm);
    padding: var(--mobile-space-sm);
    border-bottom: thin solid var(--border-muted);
    background: var(--bg-surface);
  }

  .mobile-activity-filter-grid--expanded {
    display: grid;
  }

  .mobile-shared-filters {
    grid-column: 1 / -1;
    min-width: 0;
  }

  .mobile-identity-filters {
    grid-column: 1 / -1;
    min-width: 0;
    display: grid;
    gap: 0;
  }









  .mobile-filter-select {
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

  .mobile-filter-select--repo {
    grid-column: 1 / -1;
    display: block;
    min-height: 0;
    padding: 0;
    border: 0;
    border-radius: 0;
    background: transparent;
  }

  .mobile-filter-select--repo :global(.typeahead-popover) {
    left: auto;
    right: 0;
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

  .mobile-activity-empty {
    padding: var(--mobile-space-lg);
    border: thin solid var(--border-default);
    border-radius: var(--radius-lg);
    color: var(--text-muted);
    background: var(--bg-surface);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .mobile-activity-loading-sentinel {
    display: grid;
    min-height: var(--mobile-space-lg);
    place-items: center;
    overflow-anchor: none;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .mobile-activity-older :global(button) {
    min-height: var(--mobile-hit-target);
  }
</style>
