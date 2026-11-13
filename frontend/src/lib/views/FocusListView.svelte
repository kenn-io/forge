<script lang="ts">
  import { Effect } from "effect";
  import { pollWhileVisible } from "../effect/poll-while-visible.js";
  import { onDestroy, tick, untrack } from "svelte";
  import XIcon from "@lucide/svelte/icons/x";
  import type { Attachment } from "svelte/attachments";
  import {
    FilterDropdown,
    ScrollBox,
    StatusDot,
    type FilterDropdownSection,
    type MenuAnchorPoint,
  } from "@kenn-io/kit-ui";
  import { getAppRuntime } from "../app/runtime-context.js";
  import type { AppExecution } from "../app/runtime.js";
  import { getStores, getNavigate } from "../context.js";
  import PullItem from "../components/sidebar/PullItem.svelte";
  import IssueItem from "../components/sidebar/IssueItem.svelte";
  import ItemActionsMenu from "../components/sidebar/ItemActionsMenu.svelte";
  import type { ListItemTarget } from "../stores/item-actions.js";
  import { createRangeSelection, isSelectAllShortcut } from "../utils/range-selection.svelte.js";
  import type { Issue, PullRequest } from "../api/types.js";
  import { createRepoLabelFormatter } from "../utils/repo-label.js";
  import { pullMatchesAttributeFilters } from "../utils/pull-attributes.js";
  import PullFilterMenu from "../components/PullFilterMenu.svelte";
  import {
    afterSet,
    choiceItems,
    pullListFilterOptions,
    triFilter,
    type FilterRow,
  } from "../components/pullFilters.js";
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

  const { pulls, issues, sync, settings, grouping } = getStores();
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

  function setListState(state: string): void {
    if (listType === "mrs") pulls.setFilterState(state);
    else issues.setIssueFilterState(state);
    resetPageAndLoad();
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
    const rowCount = listType === "mrs" ? prItems.length : issueItems.length;
    if (pendingScrollTop === null || rowCount === 0) return;
    const top = pendingScrollTop;
    pendingScrollTop = null;
    void tick().then(() => {
      const viewport = scrollViewportOf(listRoot);
      if (viewport) viewport.scrollTop = top;
    });
  });

  // Filter state accessors for PRs.
  const prFilterState = $derived(pulls.getFilterState());
  // Labels have no control here, so only the shared attribute filters apply.
  const prItems = $derived(
    pulls.getPulls().filter((pr) => pullMatchesAttributeFilters(pr, pulls.getAttributeFilters())),
  );
  const prLoading = $derived(pulls.isLoading());
  const prError = $derived(pulls.getError());

  // Filter state accessors for issues.
  const issueFilterState = $derived(
    issues.getIssueFilterState(),
  );
  const issueItems = $derived(issues.getIssues());
  const issueLoading = $derived(issues.isIssuesLoading());
  const issueError = $derived(issues.getIssuesError());
  const listCapped = $derived(
    listType === "mrs" ? pulls.isListCapped() : issues.isIssueListCapped(),
  );

  // Rows in on-screen order. The list opens items on their own route, so no
  // row is opened here to seed a selection.
  const pullSelection = createRangeSelection<PullRequest>({
    rows: () => (listType === "mrs" ? prItems : []),
    key: (pr) => String(pr.ID),
    openedKey: () => null,
  });
  const issueSelection = createRangeSelection<Issue>({
    rows: () => (listType === "issues" ? issueItems : []),
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
      issueItems.map((issue) => ({
        provider: issue.repo.provider,
        platformHost: issue.repo.platform_host,
        owner: issue.repo.owner,
        name: issue.repo.name,
        repoPath: issue.repo.repo_path,
      })),
      { showOrgNames: !grouping.getHideOrgName() },
    ),
  );

  const itemCount = $derived(
    listType === "mrs" ? prItems.length : issueItems.length,
  );
  const itemLabel = $derived(
    listType === "mrs" ? "PRs" : "issues",
  );
  const prFilters = $derived(pullListFilterOptions(pulls, resetPageAndLoad));
  const issueInvolvement = $derived<FilterRow[]>([
    triFilter("involves-me", "Involves me", afterSet({ get: issues.getInvolvesMe, set: issues.setInvolvesMe }, resetPageAndLoad)),
    triFilter("unassigned", "Unassigned", afterSet({ get: issues.getUnassigned, set: issues.setUnassigned }, resetPageAndLoad)),
    ...(issues.canFilterReferencedByPR()
      ? [triFilter(
          "referenced-by-pr",
          "Referenced by PR",
          afterSet({ get: issues.getReferencedByPR, set: issues.setReferencedByPR }, resetPageAndLoad),
        )]
      : []),
  ]);
  const involvementRows = $derived(listType === "mrs" ? prFilters.involvement : issueInvolvement);
  const compactFiltersActive = $derived(
    listType === "mrs"
      ? prFilters.state.value !== prFilters.state.defaultValue
        || prFilters.ci.value !== prFilters.ci.defaultValue
        || [...prFilters.involvement, ...prFilters.conditions].some((row) => row.active || row.excluded)
      : issueFilterState !== "open"
        || issueInvolvement.some((row) => row.active || row.excluded)
        || issues.getHideBots(),
  );
  const issueFilterSections = $derived<FilterDropdownSection[]>([
    {
      title: "State",
      items: ["open", "closed", "all"].map((state) => ({
        id: `state-${state}`,
        label: state === "open" ? "Open" : state === "closed" ? "Closed" : "All",
        active: issueFilterState === state,
        onSelect: () => setListState(state),
      })),
    },
    {
      title: "Issue",
      items: [
        ...issueInvolvement,
        {
          id: "hide-bot-authored-issues",
          label: "Hide bot-authored issues",
          active: issues.getHideBots(),
          onSelect: () => void issues.setHideBots(!issues.getHideBots()),
        },
      ],
    },
  ]);
</script>

{#snippet filterButton(row: { label: string; active: boolean; excluded?: boolean; onSelect: () => void })}
  <button
    type="button"
    class="visibility-btn"
    class:visibility-btn--active={row.active}
    class:visibility-btn--excluded={row.excluded}
    aria-pressed={row.active || row.excluded === true}
    onclick={row.onSelect}
  >
    {#if row.excluded}
      <XIcon size={11} strokeWidth={2.4} aria-hidden="true" />
    {/if}
    {row.label}{#if row.excluded}<span class="kit-sr-only">, excluded</span>{/if}
  </button>
{/snippet}

{#snippet listFilterMenu(label: string, align: "start" | "end")}
  {#if listType === "mrs"}
    <PullFilterMenu
      filters={prFilters}
      {label}
      title="Filters"
      active={compactFiltersActive}
      showBadge={false}
      minWidth="180px"
      {align}
    />
  {:else}
    <FilterDropdown
      {label}
      title="Filters"
      active={compactFiltersActive}
      showBadge={false}
      sections={issueFilterSections}
      minWidth="180px"
      {align}
    />
  {/if}
{/snippet}

{#snippet compactPhoneFilters()}
  <div class="compact-phone-filter-menu">
    {@render listFilterMenu("Filters", "end")}
  </div>
{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="focus-list" bind:this={listRoot} onkeydown={handleSelectionKeys}>
  {#if !showRepoSelector}
    <div class="header">
      <span class="header-label">{repoLabel}</span>
      <span class="count-badge">{itemCount}{listCapped ? "+" : ""} {itemLabel}</span>
    </div>
  {/if}
  <div
    id="focus-list-filters"
    class="filter-bar"
    class:filter-bar--expanded={filtersExpanded}
    class:filter-bar--compactable={!showRepoSelector}
  >
    {#if showRepoSelector && filtersExpanded}
      <div class="mobile-repo-filter">
        <RepoTypeahead
          selected={selectedRepo}
          onchange={setGlobalRepo}
          allowPresetManagement={false}
          mobile
        />
      </div>
    {/if}
    {#if !showRepoSelector && !phonePresentation}
      <!-- The PR conditions live only in this menu at desktop widths, so it always shows for PRs. -->
      <div class="compact-filter-menu" class:compact-filter-menu--always={listType === "mrs"}>
        {@render listFilterMenu("List filters", "start")}
      </div>
    {/if}
    <div class="state-toggle">
      {#if listType === "mrs"}
        {#each ["open", "closed", "all"] as s (s)}
          <button
            class="state-btn"
            class:state-btn--active={prFilterState === s}
            onclick={() => setListState(s)}
          >
            {s === "open"
              ? "Open"
              : s === "closed"
                ? "Closed"
                : "All"}
          </button>
        {/each}
      {:else}
        {#each ["open", "closed", "all"] as s (s)}
          <button
            class="state-btn"
            class:state-btn--active={issueFilterState === s}
            onclick={() => setListState(s)}
          >
            {s === "open"
              ? "Open"
              : s === "closed"
                ? "Closed"
                : "All"}
          </button>
        {/each}
      {/if}
    </div>
    <div class="visibility-controls">
      {#each involvementRows as row (row.id)}
        {@render filterButton(row)}
      {/each}
      {#if listType === "mrs" && showRepoSelector}
        {#each prFilters.conditions as row (row.id)}
          {@render filterButton(row)}
        {/each}
        {#each choiceItems(prFilters.ci) as item (item.id)}
          {@render filterButton(item)}
        {/each}
      {/if}
      {#if listType === "issues"}
        <button
          type="button"
          class="visibility-btn"
          class:visibility-btn--active={issues.getHideBots()}
          aria-label="Hide bot-authored issues"
          aria-pressed={issues.getHideBots()}
          onclick={() => void issues.setHideBots(!issues.getHideBots())}
        >Hide bots</button>
      {/if}
    </div>
  </div>
  <MobileTriageSearchBar
    bind:value={searchInput}
    placeholder="Search {itemLabel}..."
    searchAriaLabel="Search {itemLabel}"
    filterControls="focus-list-filters"
    {filtersExpanded}
    filterControl={!showRepoSelector && phonePresentation ? compactPhoneFilters : undefined}
    oninput={onSearchInput}
    onsubmit={(value) => applySearch(value, true)}
    ontoggle={() => filtersExpanded = !filtersExpanded}
  />

  {#if listType === "mrs" && prFilterState !== "open"}
    <p class="state-note">
      Showing items closed after tracking began
    </p>
  {:else if listType === "issues" && issueFilterState !== "open"}
    <p class="state-note">
      Showing items closed after tracking began
    </p>
  {/if}

  <ScrollBox class="list-body" label="Focus list">
    {#if settings.isSettingsLoaded() && !settings.hasConfiguredRepos()}
      <p class="state-message">No repositories configured.</p>
    {:else if listType === "mrs"}
      {#if prLoading && prItems.length === 0}
        <p class="state-message">Loading...</p>
      {:else if prError !== null && prItems.length === 0}
        <p class="state-message state-message--error">
          Error: {prError}
        </p>
      {:else if prItems.length === 0 && sync.getSyncState()?.running}
        <div class="state-message sync-message">
          <StatusDot status="working" label="Syncing pull requests" size={6} animated />
          <span aria-hidden="true">Syncing...</span>
        </div>
      {:else if prItems.length === 0 && !sync.getSyncState()?.last_run_at}
        <p class="state-message">Waiting for first sync...</p>
      {:else if prItems.length === 0}
        <p class="state-message">No pull requests found.</p>
      {:else}
        {#each prItems as pr (pr.ID)}
          <PullItem
            {pr}
            repoLabel={prRepoLabelFormatter.format({
              provider: pr.repo.provider,
              platformHost: pr.repo.platform_host,
              owner: pr.repo.owner,
              name: pr.repo.name,
              repoPath: pr.repo.repo_path,
            })}
            showRepo={!repo}
            selected={false}
            multiSelected={pullSelection.has(pr)}
            onclick={(event) => handlePRClick(event, pr)}
            oncontextmenu={(event) => openPRContextMenu(event, pr)}
          />
        {/each}
      {/if}
    {:else}
      {#if issueLoading && issueItems.length === 0}
        <p class="state-message">Loading...</p>
      {:else if issueError !== null && issueItems.length === 0}
        <p class="state-message state-message--error">
          Error: {issueError}
        </p>
      {:else if issueItems.length === 0 && sync.getSyncState()?.running}
        <div class="state-message sync-message">
          <StatusDot status="working" label="Syncing issues" size={6} animated />
          <span aria-hidden="true">Syncing...</span>
        </div>
      {:else if issueItems.length === 0 && !sync.getSyncState()?.last_run_at}
        <p class="state-message">Waiting for first sync...</p>
      {:else if issueItems.length === 0 && !listCapped}
        <p class="state-message">No issues found.</p>
      {:else}
        {#each issueItems as issue (issue.ID)}
          <IssueItem
            {issue}
            repoLabel={issueRepoLabelFormatter.format({
              provider: issue.repo.provider,
              platformHost: issue.repo.platform_host,
              owner: issue.repo.owner,
              name: issue.repo.name,
              repoPath: issue.repo.repo_path,
            })}
            showRepo={!repo}
            selected={false}
            multiSelected={issueSelection.has(issue)}
            onclick={(event) => handleIssueClick(event, issue)}
            oncontextmenu={(event) => openIssueContextMenu(event, issue)}
          />
        {/each}
      {/if}
    {/if}
    {#if listCapped && (!chunked || pageLimit < 500)}
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
    container: focus-list / inline-size;
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

  .filter-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
    background: var(--bg-surface);
  }

  .visibility-controls {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-left: auto;
  }

  .compact-filter-menu {
    display: none;
    flex-shrink: 0;
  }

  .compact-filter-menu--always {
    display: block;
  }

  .compact-filter-menu :global(.kit-filter-dropdown__btn) {
    width: 26px;
    justify-content: center;
    padding: 3px;
  }

  .compact-filter-menu :global(.kit-filter-dropdown__trigger-label),
  .compact-filter-menu :global(.kit-filter-dropdown__trigger-detail) {
    display: none;
  }

  .compact-phone-filter-menu :global(.kit-filter-dropdown__trigger-label),
  .compact-phone-filter-menu :global(.kit-filter-dropdown__trigger-detail) {
    display: none;
  }

  .visibility-btn {
    font-size: var(--font-size-xs);
    padding: 2px 8px;
    border: none;
    border-radius: 4px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    white-space: nowrap;
  }

  .visibility-btn {
    border: 1px solid var(--border-default);
    background: var(--bg-inset);
    font-weight: 600;
  }

  .visibility-btn--active {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 12%, transparent);
    border-color: color-mix(in srgb, var(--accent-blue) 34%, transparent);
  }

  .visibility-btn--excluded {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--accent-red);
    background: color-mix(in srgb, var(--accent-red) 12%, transparent);
    border-color: color-mix(in srgb, var(--accent-red) 34%, transparent);
  }

  .state-toggle {
    display: flex;
    gap: 2px;
    background: var(--bg-inset);
    border-radius: 6px;
    padding: 2px;
  }

  .state-btn {
    font-size: var(--font-size-xs);
    padding: 2px 8px;
    border: none;
    border-radius: 4px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    white-space: nowrap;
  }

  .state-btn--active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
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

  @container focus-list (max-width: 640px) {
    .filter-bar--compactable .state-toggle,
    .filter-bar--compactable .visibility-controls {
      display: none;
    }

    .filter-bar--compactable .compact-filter-menu {
      display: block;
    }
  }

  :global(.mobile-main) .focus-list {
    --focus-mobile-space-2xs: 4.5px;
    --focus-mobile-space-xs: 6.5px;
    --focus-mobile-space-sm: 10px;
    --focus-mobile-space-md: 13px;
    --focus-mobile-hit-target: 40px;
    --focus-mobile-radius-sm: 8.5px;
    --focus-mobile-radius-md: 12px;
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

  :global(.mobile-main) .filter-bar {
    display: none;
    order: 2;
    flex-wrap: wrap;
    align-items: stretch;
    gap: var(--focus-mobile-space-sm);
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    border-bottom: thin solid var(--border-muted);
  }

  :global(.mobile-main) .filter-bar--expanded {
    display: flex;
  }

  :global(.mobile-main) .mobile-repo-filter {
    flex: 1 0 100%;
    min-width: 0;
  }

  :global(.mobile-main) .mobile-repo-filter :global(.typeahead-popover) {
    left: auto;
    right: 0;
  }

  :global(.mobile-main) .state-toggle {
    gap: var(--focus-mobile-space-2xs);
    border-radius: var(--focus-mobile-radius-md);
    padding: var(--focus-mobile-space-2xs);
  }

  :global(.mobile-main) .visibility-controls {
    flex-wrap: wrap;
    align-items: stretch;
    gap: var(--focus-mobile-space-sm);
    margin-left: 0;
  }

  :global(.mobile-main) .state-btn,
  :global(.mobile-main) .visibility-btn {
    min-height: var(--focus-mobile-hit-target);
    border-radius: var(--focus-mobile-radius-sm);
    padding: var(--focus-mobile-space-xs) var(--focus-mobile-space-sm);
    font-size: var(--font-size-sm);
    line-height: 1.3;
    font-weight: 600;
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
