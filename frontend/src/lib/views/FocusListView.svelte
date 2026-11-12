<script lang="ts">
  import { Effect } from "effect";
  import { pollWhileVisible } from "../effect/poll-while-visible.js";
  import { onDestroy, tick, untrack } from "svelte";
  import type { Attachment } from "svelte/attachments";
  import {
    FilterDropdown,
    ScrollBox,
    StatusDot,
    type MenuAnchorPoint,
  } from "@kenn-io/kit-ui";
  import { getAppRuntime } from "../app/runtime-context.js";
  import type { AppExecution } from "../app/runtime.js";
  import { getStores, getNavigate } from "../context.js";
  import PullRows from "../components/sidebar/PullRows.svelte";
  import GroupedSidebarSection from "../components/shared/GroupedSidebarSection.svelte";
  import FilterSectionsPanel from "../components/shared/FilterSectionsPanel.svelte";
  import LabelFilterMenu from "../components/shared/LabelFilterMenu.svelte";
  import { issueListFilterMenu, pullListFilterMenu } from "../components/shared/list-filter-menus.js";
  import { repoGroups, repoItemLabel } from "../components/shared/repo-groups.js";
  import { labelFilterChoices } from "../utils/label-filter.js";
  import IssueItem from "../components/sidebar/IssueItem.svelte";
  import ListLoadMore from "../components/sidebar/ListLoadMore.svelte";
  import ItemActionsMenu from "../components/sidebar/ItemActionsMenu.svelte";
  import type { ListItemTarget } from "../stores/item-actions.js";
  import { createRangeSelection, isSelectAllShortcut } from "../utils/range-selection.svelte.js";
  import type { Issue, PullRequest } from "../api/types.js";
  import type { PullSidebarRow } from "../utils/pull-stack-tree.js";
  import { createRepoLabelFormatter } from "../utils/repo-label.js";
  import RepoTypeahead from "../components/RepoTypeahead.svelte";
  import MobileTriageSearchBar from "../components/mobile/MobileTriageSearchBar.svelte";
  import { getGlobalRepo, setGlobalRepo } from "../stores/filter.svelte.js";
  import {
    mobileListOriginState,
    rememberMobileListPosition,
    scrollViewportOf,
    takeMobileListPosition,
  } from "../stores/mobile-list-return.js";
  import { observeIntersection } from "../browser/observers.js";
  import {
    buildFocusIssueRoute,
    buildFocusPullRequestRoute,
    buildIssueRoute,
    buildPullRequestRoute,
    type IssueRouteRef,
    type PullRequestRouteRef,
  } from "../routes.js";

  const { pulls, issues, sync, settings, grouping, collapsedRepos } = getStores();
  const runtime = getAppRuntime();
  const navigate = getNavigate();

  interface Props {
    listType: "mrs" | "issues";
    repo?: string;
    routeFamily?: "focus" | "canonical";
    showRepoSelector?: boolean;
    phonePresentation?: boolean;
    chunked?: boolean;
  }

  const {
    listType,
    repo,
    routeFamily = "focus",
    showRepoSelector = false,
    phonePresentation = false,
    chunked = false,
  }: Props = $props();

  let searchInput = $state("");
  let filtersExpanded = $state(false);
  let pageLimit = $state(30);
  let listRoot = $state<HTMLDivElement | null>(null);
  // Scroll offset parked by a previous visit to this exact list, applied once
  // rows are on screen so Back from a detail lands on the same rows.
  let pendingScrollTop = $state<number | null>(null);
  let paginationArmed = true;
  let paginationIntersecting = false;
  let searchExecution: AppExecution<void, never> | null = null;

  function loadList(): void {
    if (listType === "mrs") pulls.loadPulls(repoParams);
    else issues.loadIssues(repoParams);
  }

  const selectedRepo = $derived(showRepoSelector ? getGlobalRepo() : repo);
  const repoLabel = $derived(selectedRepo ?? "All repositories");

  const repoParams = $derived(
    !selectedRepo && !chunked
      ? undefined
      : {
          ...(selectedRepo ? { repo: selectedRepo } : {}),
          ...(chunked ? { limit: pageLimit } : {}),
        },
  );

  const listIdentity = $derived(`${listType}:${selectedRepo ?? ""}:${chunked}`);

  function parkListPosition(): void {
    rememberMobileListPosition(listIdentity, {
      scrollTop: scrollViewportOf(listRoot)?.scrollTop ?? 0,
      pageLimit,
    });
  }

  $effect(() => {
    const identity = listIdentity;
    const parked = takeMobileListPosition(identity);
    pageLimit = parked?.pageLimit ?? 30;
    pendingScrollTop = parked?.scrollTop ?? null;
    paginationArmed = true;
    filtersExpanded = false;
    searchInput = untrack(() => listType === "mrs"
      ? pulls.getSearchQuery() ?? ""
      : issues.getIssueSearchQuery() ?? "");
    const execution = untrack(() => runtime.runCommand(
      pollWhileVisible(Effect.sync(loadList), "15 seconds", { immediate: true }),
      {
        operation: "poll focus list",
        safeContext: { listType, identity },
        onFailure: () => {},
      },
    ));
    const unsubscribeSync = sync.subscribeSyncCompleteWithoutLiveUpdates(loadList);

    return () => {
      searchExecution?.interrupt();
      execution.interrupt();
      unsubscribeSync();
    };
  });

  onDestroy(() => searchExecution?.interrupt());

  function applySearch(value: string, submitted = false): void {
    if (submitted) searchExecution?.interrupt();
    const q = value.trim() || undefined;
    if (listType === "mrs") pulls.setSearchQuery(q);
    else issues.setIssueSearchQuery(q);
    if (!submitted || q === undefined) return resetPageAndLoad();
    pageLimit = 30;
    paginationArmed = true;
    if (listType === "mrs") pulls.loadPulls(repoParams, { submitted: true });
    else issues.loadIssues(repoParams, { submitted: true });
  }

  function onSearchInput(value: string): void {
    searchInput = value;
    if (listType === "mrs") pulls.searchTextChanged();
    else issues.searchTextChanged();
    searchExecution?.interrupt();
    searchExecution = runtime.runCommand(
      Effect.sleep("300 millis").pipe(
        Effect.andThen(Effect.sync(() => applySearch(value))),
      ),
      {
        operation: "debounce focus list search",
        safeContext: { listType },
        onFailure: () => {},
      },
    );
  }

  function routeRefForPull(pr: PullRequest): PullRequestRouteRef {
    return {
      provider: pr.repo.provider,
      platformHost: pr.repo.platform_host,
      owner: pr.repo.owner,
      name: pr.repo.name,
      repoPath: pr.repo.repo_path,
      number: pr.Number,
    };
  }

  function routeRefForIssue(issue: Issue): IssueRouteRef {
    return {
      provider: issue.repo.provider,
      platformHost: issue.repo.platform_host,
      owner: issue.repo.owner,
      name: issue.repo.name,
      repoPath: issue.repo.repo_path,
      number: issue.Number,
    };
  }

  function handlePRSelect(ref: PullRequestRouteRef): void {
    parkListPosition();
    navigate(
      routeFamily === "canonical"
        ? buildPullRequestRoute(ref)
        : buildFocusPullRequestRoute(ref),
      { state: mobileListOriginState("pulls") },
    );
  }

  function handleIssueSelect(ref: IssueRouteRef): void {
    parkListPosition();
    navigate(
      routeFamily === "canonical"
        ? buildIssueRoute(ref)
        : buildFocusIssueRoute(ref),
      { state: mobileListOriginState("issues") },
    );
  }

  $effect(() => {
    const rowCount = listType === "mrs" ? shownPulls.length : shownIssues.length;
    if (pendingScrollTop === null || rowCount === 0) return;
    const top = pendingScrollTop;
    pendingScrollTop = null;
    void tick().then(() => {
      const viewport = scrollViewportOf(listRoot);
      if (viewport) viewport.scrollTop = top;
    });
  });

  const prLoaded = $derived(pulls.getPulls());
  const prItems = $derived(pulls.getFilteredPulls());
  const prLoading = $derived(pulls.isLoading());
  const prError = $derived(pulls.getError());

  const issueLoaded = $derived(issues.getIssues());
  const issueItems = $derived(issues.getFilteredIssues());
  const issueLoading = $derived(issues.isIssuesLoading());
  const issueError = $derived(issues.getIssuesError());
  const listCapped = $derived(
    listType === "mrs" ? pulls.isListCapped() : issues.isIssueListCapped(),
  );

  // Rows in on-screen order. The list opens items on their own route, so no
  // row is opened here to seed a selection.
  const pullSelection = createRangeSelection<PullRequest>({
    rows: () => shownPulls,
    key: (pr) => String(pr.ID),
    openedKey: () => null,
  });
  const issueSelection = createRangeSelection<Issue>({
    rows: () => shownIssues,
    key: (issue) => String(issue.ID),
    openedKey: () => null,
  });
  let contextMenu = $state.raw<{
    targets: ListItemTarget[];
    anchor: MenuAnchorPoint;
    row: HTMLElement | undefined;
  } | null>(null);
  let contextMenuOpen = $state(false);

  function handlePRClick(event: MouseEvent, pr: PullRequest): void {
    if (pullSelection.activate(event, pr)) handlePRSelect(routeRefForPull(pr));
  }

  function handleIssueClick(event: MouseEvent, issue: Issue): void {
    if (issueSelection.activate(event, issue)) handleIssueSelect(routeRefForIssue(issue));
  }

  function showContextMenu(event: MouseEvent, targets: ListItemTarget[]): void {
    event.preventDefault();
    contextMenu = {
      targets,
      anchor: { x: event.clientX, y: event.clientY },
      row: event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined,
    };
    contextMenuOpen = true;
  }

  // Right-clicking outside the selection acts on that item alone.
  function openPRContextMenu(event: MouseEvent, pr: PullRequest): void {
    const multi = pullSelection.selected.length > 1 && pullSelection.has(pr);
    if (!multi) pullSelection.clear();
    showContextMenu(event, (multi ? pullSelection.selected : [pr]).map((item) => ({ kind: "pull", item })));
  }

  function openIssueContextMenu(event: MouseEvent, issue: Issue): void {
    const multi = issueSelection.selected.length > 1 && issueSelection.has(issue);
    if (!multi) issueSelection.clear();
    showContextMenu(event, (multi ? issueSelection.selected : [issue]).map((item) => ({ kind: "issue", item })));
  }

  // A menu action can finish after this view unmounts, so reload the store's
  // current query rather than this view's parameters.
  function reloadAfterItemAction(): void {
    const options = { operation: "reload focus list after item action", safeContext: { listType }, onFailure: () => {} };
    if (listType === "mrs") runtime.runCommand(pulls.loadPullsEffect(), options);
    else runtime.runCommand(issues.loadIssuesEffect(), options);
  }

  const activeSelection = $derived(listType === "mrs" ? pullSelection : issueSelection);

  function handleSelectionKeys(event: KeyboardEvent): void {
    if (event.key === "Escape" && activeSelection.active) {
      event.preventDefault();
      activeSelection.clear();
    } else if (isSelectAllShortcut(event)) {
      event.preventDefault();
      activeSelection.selectAll();
    }
  }

  function resetPageAndLoad(): void {
    pageLimit = 30;
    paginationArmed = true;
    loadList();
  }

  function loadMore(): void {
    if (!chunked) {
      if (listType === "mrs") pulls.loadMorePulls();
      else issues.loadMoreIssues();
      return;
    }
    pageLimit = Math.min(pageLimit + 30, 500);
    loadList();
  }

  const autoloadMore: Attachment<HTMLElement> = (node) => {
    if (typeof IntersectionObserver === "undefined") {
      if (paginationArmed) {
        paginationArmed = false;
        loadMore();
      }
      return;
    }

    const root = node.closest<HTMLElement>(".kit-scrollbox__viewport");
    const armPagination = () => {
      const loading = listType === "mrs" ? pulls.isLoading() : issues.isIssuesLoading();
      if (paginationArmed || loading) return;
      paginationArmed = true;
      if (!paginationIntersecting) return;
      paginationArmed = false;
      loadMore();
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
              loadMore();
            }
          },
          { root, rootMargin: "240px 0px" },
        ).pipe(Effect.andThen(Effect.never)),
      ),
      {
        operation: "observe focus list pagination",
        safeContext: { listType },
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

  const prRepoLabelFormatter = $derived(
    createRepoLabelFormatter(
      prItems.map((pr) => ({
        provider: pr.repo.provider,
        platformHost: pr.repo.platform_host,
        owner: pr.repo.owner,
        name: pr.repo.name,
        repoPath: pr.repo.repo_path,
      })),
      { showOrgNames: !grouping.getHideOrgName() },
    ),
  );
  const issueRepoLabelFormatter = $derived(
    createRepoLabelFormatter(
      issueLoaded.map((issue) => ({
        provider: issue.repo.provider,
        platformHost: issue.repo.platform_host,
        owner: issue.repo.owner,
        name: issue.repo.name,
        repoPath: issue.repo.repo_path,
      })),
      { showOrgNames: !grouping.getHideOrgName() },
    ),
  );

  // The shared menu is the only filter UI, and the rows below honor every
  // choice in it, so this list matches the desktop sidebar's.
  const filterMenu = $derived(
    listType === "mrs"
      ? pullListFilterMenu({ pulls, grouping, labelMenu: labelFilterMenu, reload: resetPageAndLoad })
      : issueListFilterMenu({ issues, grouping, labelMenu: labelFilterMenu, reload: resetPageAndLoad }),
  );
  const labelChoices = $derived(
    listType === "mrs"
      ? labelFilterChoices(prLoaded, pulls.getLabelFilters())
      : labelFilterChoices(issueLoaded, issues.getLabelFilters()),
  );

  const prGroups = $derived(
    listType === "mrs" && grouping.getGroupingMode() === "byRepo"
      ? repoGroups(pulls.pullsByRepo(), prRepoLabelFormatter).map((group) => ({
          ...group,
          rows: pulls.getSidebarRows(group.items),
          collapsed: collapsedRepos.isCollapsed("pulls", group.collapseKey),
        }))
      : null,
  );
  const prFlatRows = $derived(listType === "mrs" ? pulls.getSidebarRows(prItems) : []);
  const issueGroups = $derived(
    listType === "issues" && grouping.getGroupByRepo()
      ? repoGroups(issues.issuesByRepo(), issueRepoLabelFormatter).map((group) => ({
          ...group,
          collapsed: collapsedRepos.isCollapsed("issues", group.collapseKey),
        }))
      : null,
  );
  const shownPulls = $derived(
    prGroups === null
      ? prFlatRows.map((row) => row.pr)
      : prGroups.flatMap((group) => (group.collapsed ? [] : group.rows.map((row) => row.pr))),
  );
  const shownIssues = $derived(
    issueGroups === null
      ? (listType === "issues" ? issueItems : [])
      : issueGroups.flatMap((group) => (group.collapsed ? [] : group.items)),
  );

  const itemCount = $derived(
    listType === "mrs" ? prItems.length : issueItems.length,
  );
  const itemLabel = $derived(
    listType === "mrs" ? "PRs" : "issues",
  );
</script>

{#snippet labelFilterMenu()}
  <LabelFilterMenu
    choices={labelChoices}
    oncycle={(name) => (listType === "mrs" ? pulls.cycleLabelFilter(name) : issues.cycleLabelFilter(name))}
    emptyText={listType === "mrs" ? "No labels on loaded pull requests" : "No labels on loaded issues"}
  />
{/snippet}

{#snippet pullRows(rows: PullSidebarRow[], showRepo: boolean)}
  <PullRows
    {rows}
    {showRepo}
    repoLabel={(pr) => repoItemLabel(pr, prRepoLabelFormatter)}
    isSelected={() => false}
    isMultiSelected={(pr) => pullSelection.has(pr)}
    onclick={handlePRClick}
    oncontextmenu={openPRContextMenu}
  />
{/snippet}

{#snippet issueRows(rows: Issue[], showRepo: boolean)}
  {#each rows as issue (issue.ID)}
    <IssueItem
      {issue}
      repoLabel={repoItemLabel(issue, issueRepoLabelFormatter)}
      {showRepo}
      selected={false}
      multiSelected={issueSelection.has(issue)}
      onclick={(event) => handleIssueClick(event, issue)}
      oncontextmenu={(event) => openIssueContextMenu(event, issue)}
    />
  {/each}
{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="focus-list" bind:this={listRoot} onkeydown={handleSelectionKeys}>
  {#if !showRepoSelector}
    <div class="header">
      <span class="header-label">{repoLabel}</span>
      <span class="count-badge">{itemCount}{listCapped ? "+" : ""} {itemLabel}</span>
      {#if !phonePresentation}
        <FilterDropdown
          label="Filters"
          title="Filters"
          active={filterMenu.changed}
          badgeCount={filterMenu.filterCount}
          sections={filterMenu.sections}
          resetLabel="Reset view"
          onReset={filterMenu.reset}
          minWidth="190px"
          align="end"
        />
      {/if}
    </div>
  {/if}
  {#if phonePresentation}
    <div id="focus-list-filters" class="filter-bar" class:filter-bar--expanded={filtersExpanded}>
      {#if filtersExpanded}
        {#if showRepoSelector}
          <div class="mobile-repo-filter">
            <RepoTypeahead
              selected={selectedRepo}
              onchange={setGlobalRepo}
              allowPresetManagement={false}
              mobile
            />
          </div>
        {/if}
        <FilterSectionsPanel
          sections={filterMenu.sections}
          resetLabel="Reset view"
          onReset={filterMenu.reset}
        />
      {/if}
    </div>
  {/if}
  <MobileTriageSearchBar
    bind:value={searchInput}
    placeholder="Search {itemLabel}..."
    searchAriaLabel="Search {itemLabel}"
    filterControls="focus-list-filters"
    {filtersExpanded}
    filtersActive={filtersExpanded || filterMenu.changed}
    oninput={onSearchInput}
    onsubmit={(value) => applySearch(value, true)}
    ontoggle={() => filtersExpanded = !filtersExpanded}
  />

  {#if (listType === "mrs" ? pulls.getFilterState() : issues.getIssueFilterState()) !== "open"}
    <p class="state-note">
      Showing items closed after tracking began
    </p>
  {/if}

  <ScrollBox class="list-body" label="Focus list">
    {#if settings.isSettingsLoaded() && !settings.hasConfiguredRepos()}
      <p class="state-message">No repositories configured.</p>
    {:else if listType === "mrs"}
      {#if prLoading && prLoaded.length === 0}
        <p class="state-message">Loading...</p>
      {:else if prError !== null && prLoaded.length === 0}
        <p class="state-message state-message--error">
          Error: {prError}
        </p>
      {:else if prLoaded.length === 0 && sync.getSyncState()?.running}
        <div class="state-message sync-message">
          <StatusDot status="working" label="Syncing pull requests" size={6} animated />
          <span aria-hidden="true">Syncing...</span>
        </div>
      {:else if prLoaded.length === 0 && !sync.getSyncState()?.last_run_at}
        <p class="state-message">Waiting for first sync...</p>
      {:else if prLoaded.length === 0}
        <p class="state-message">No pull requests found.</p>
      {:else if prItems.length === 0 && !listCapped}
        <p class="state-message">No pull requests match these filters.</p>
      {:else if prGroups !== null}
        {#each prGroups as group (group.key)}
          <GroupedSidebarSection
            label={group.label}
            isPrivate={group.isPrivate}
            count={listCapped ? `${group.items.length}+` : group.items.length}
            collapsed={group.collapsed}
            onclick={() => collapsedRepos.toggle("pulls", group.collapseKey)}
          >
            {@render pullRows(group.rows, false)}
          </GroupedSidebarSection>
        {/each}
      {:else}
        {@render pullRows(prFlatRows, !repo)}
      {/if}
    {:else}
      {#if issueLoading && issueLoaded.length === 0}
        <p class="state-message">Loading...</p>
      {:else if issueError !== null && issueLoaded.length === 0}
        <p class="state-message state-message--error">
          Error: {issueError}
        </p>
      {:else if issueLoaded.length === 0 && sync.getSyncState()?.running}
        <div class="state-message sync-message">
          <StatusDot status="working" label="Syncing issues" size={6} animated />
          <span aria-hidden="true">Syncing...</span>
        </div>
      {:else if issueLoaded.length === 0 && !sync.getSyncState()?.last_run_at}
        <p class="state-message">Waiting for first sync...</p>
      {:else if issueLoaded.length === 0 && !listCapped}
        <p class="state-message">No issues found.</p>
      {:else if issueItems.length === 0 && !listCapped}
        <p class="state-message">No issues match these filters.</p>
      {:else if issueGroups !== null}
        {#each issueGroups as group (group.key)}
          <GroupedSidebarSection
            label={group.label}
            isPrivate={group.isPrivate}
            count={listCapped ? `${group.items.length}+` : group.items.length}
            collapsed={group.collapsed}
            onclick={() => collapsedRepos.toggle("issues", group.collapseKey)}
          >
            {@render issueRows(group.items, false)}
          </GroupedSidebarSection>
        {/each}
      {:else}
        {@render issueRows(issueItems, !repo)}
      {/if}
    {/if}
    {#if listCapped && (!chunked || pageLimit < 500)}
      {#if itemCount === 0}
        <!-- Local filters can hide every loaded row, leaving no list to scroll for the next chunk. -->
        <ListLoadMore loading={prLoading || issueLoading} onclick={loadMore} />
      {/if}
      <div
        class="focus-list-loading-sentinel"
        aria-live="polite"
        {@attach autoloadMore}
      >
        {#if prLoading || issueLoading}Loading more…{/if}
      </div>
    {/if}
  </ScrollBox>
</div>

<ItemActionsMenu
  bind:open={contextMenuOpen}
  anchor={contextMenu?.anchor}
  returnFocus={contextMenu?.row}
  targets={contextMenu?.targets ?? []}
  onChanged={reloadAfterItemAction}
  selectionActive={activeSelection.active}
  onClearSelection={() => activeSelection.clear()}
/>

<style>
  .focus-list {
    display: flex;
    flex-direction: column;
    height: 100%;
    width: 100%;
  }

  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    flex-shrink: 0;
  }

  .header-label {
    flex: 1;
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .count-badge {
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: 10px;
    padding: 2px 7px;
    flex-shrink: 0;
  }

  .state-note {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    padding: 4px 10px;
    margin: 0;
    border-bottom: 1px solid var(--border-muted);
  }

  .state-message {
    padding: 24px 16px;
    font-size: var(--font-size-md);
    color: var(--text-muted);
    text-align: center;
  }

  .state-message--error {
    color: var(--accent-red);
  }

  .sync-message {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
  }

  .focus-list-loading-sentinel {
    min-height: 1px;
  }

  :global(.mobile-main) .focus-list {
    --focus-mobile-space-2xs: 4.5px;
    --focus-mobile-space-xs: 6.5px;
    --focus-mobile-space-sm: 10px;
    --focus-mobile-space-md: 13px;
    --focus-mobile-hit-target: 40px;
    --focus-mobile-radius-sm: 8.5px;
    background: var(--bg-primary);
  }

  :global(.mobile-main) .header {
    gap: var(--focus-mobile-space-sm);
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    border-bottom: thin solid var(--border-default);
  }

  :global(.mobile-main) .header-label {
    font-size: var(--font-size-md);
    line-height: 1.35;
  }

  :global(.mobile-main) .count-badge {
    min-height: calc(var(--focus-mobile-hit-target) * 0.72);
    display: inline-flex;
    align-items: center;
    border: thin solid var(--border-muted);
    border-radius: 999px;
    padding: var(--focus-mobile-space-2xs) var(--focus-mobile-space-sm);
    font-size: var(--font-size-xs);
    line-height: 1.25;
  }

  .filter-bar {
    display: none;
    order: 2;
    flex-wrap: wrap;
    align-items: stretch;
    gap: var(--focus-mobile-space-sm);
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    border-bottom: thin solid var(--border-muted);
    background: var(--bg-surface);
  }

  .filter-bar--expanded {
    display: flex;
    /* Short phone viewports must still reach the last filters and Reset. */
    max-height: 60%;
    min-height: 0;
    overflow-y: auto;
  }

  :global(.mobile-main) .focus-list :global(.label-filter__row),
  :global(.mobile-main) .focus-list :global(.sidebar-group-header) {
    min-height: var(--mobile-chrome-hit-target);
  }

  :global(.mobile-main) .mobile-repo-filter {
    flex: 1 0 100%;
    min-width: 0;
  }

  :global(.mobile-main) .mobile-repo-filter :global(.typeahead-popover) {
    left: auto;
    right: 0;
  }

  :global(.mobile-main) .state-note {
    order: 3;
  }

  :global(.mobile-main) .focus-list > :global(.list-body) {
    order: 4;
  }

  :global(.mobile-main) .state-note,
  :global(.mobile-main) .state-message {
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    font-size: var(--font-size-sm);
    line-height: 1.35;
  }
</style>
