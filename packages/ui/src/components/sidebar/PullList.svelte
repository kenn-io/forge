<script lang="ts">
  import { getStores, getNavigate, getSidebar, getActions, getHostState } from "../../context.js";
  import { groupByWorkflow } from "../../stores/workflow.svelte.js";
  import DiffSidebar from "../diff/DiffSidebar.svelte";
  import PullItem from "./PullItem.svelte";
  import Chip from "../shared/Chip.svelte";
  import FilterDropdown from "../shared/FilterDropdown.svelte";
  import LeftSidebarToggle from "../shared/LeftSidebarToggle.svelte";
  import type { KanbanStatus, PullRequest } from "../../api/types.js";
  import type { GroupingMode } from "../../stores/grouping.svelte.js";
  import type { PullAttributeFilter } from "../../stores/pulls.svelte.js";
  import {
    buildPullRequestFilesRoute,
    buildPullRequestRoute,
    type PullRequestRouteRef,
  } from "../../routes.js";

  const { pulls, sync, grouping, collapsedRepos, settings } = getStores();
  const navigate = getNavigate();
  const actions = getActions();
  const hostState = getHostState();
  const { isEmbedded, isSidebarToggleEnabled, toggleSidebar } = getSidebar();

  const importAction = $derived(
    (actions.pull ?? []).find(
      (a) => a.id === "import-worktree",
    ),
  );
  const activeWorktreeKey = $derived(
    hostState.getActiveWorktreeKey?.(),
  );
  const groupingMode = $derived(
    grouping.getGroupingMode(),
  );
  const workflowGroups = $derived(
    groupByWorkflow(pulls.getFilteredPulls()),
  );
  const pullStateOptions = ["open", "closed", "all"] as const;
  const attributeFilterOptions: {
    value: PullAttributeFilter;
    label: string;
  }[] = [
    { value: "approved", label: "Approved" },
    { value: "draft", label: "Draft" },
    { value: "ready", label: "Ready for review" },
    { value: "merge_conflicts", label: "Merge conflicts" },
    { value: "failed_ci", label: "Failed CI" },
  ];
  const kanbanFilterOptions: {
    value: KanbanStatus;
    label: string;
  }[] = [
    { value: "new", label: "New" },
    { value: "reviewing", label: "Reviewing" },
    { value: "waiting", label: "Waiting" },
    { value: "awaiting_merge", label: "Awaiting merge" },
  ];
  const groupingOptions: {
    value: GroupingMode;
    label: string;
  }[] = [
    { value: "byRepo", label: "Repo" },
    { value: "byWorkflow", label: "Status" },
    { value: "flat", label: "All" },
  ];
  // Playwright-measured with the local PR filter icon and sidebar toggle:
  // the full PR filter row needs 423px, so collapse with a small buffer.
  const COMPACT_FILTER_MAX_WIDTH = 430;
  // Keep the full filter groups visible at medium widths, but collapse the
  // local PR filter trigger label before it crowds the sidebar toggle.
  const LOCAL_FILTER_ICON_ONLY_MAX_WIDTH = 520;

  interface Props {
    getDetailTab?: () => string;
    showSelectedDiffSidebar?: boolean;
    sidebarWidth?: number;
  }
  const {
    getDetailTab: _getDetailTab = () => "conversation",
    showSelectedDiffSidebar = true,
    sidebarWidth = 340,
  }: Props = $props();

  let searchInput = $state(pulls.getSearchQuery() ?? "");
  let debounceHandle: ReturnType<typeof setTimeout> | null = null;
  let refreshHandle: ReturnType<typeof setInterval> | null = null;
  const visiblePulls = $derived(pulls.getDisplayOrderPRs());

  $effect(() => {
    void pulls.loadPulls();

    refreshHandle = setInterval(() => {
      void pulls.loadPulls();
    }, 15_000);

    // If sync is currently running on first load, refresh when it completes
    if (sync.getSyncState()?.running) {
      sync.onNextSyncComplete(() => void pulls.loadPulls());
    }

    return () => {
      if (refreshHandle !== null) clearInterval(refreshHandle);
    };
  });

  function onSearchInput(e: Event): void {
    const value = (e.target as HTMLInputElement).value;
    searchInput = value;

    if (debounceHandle !== null) clearTimeout(debounceHandle);
    debounceHandle = setTimeout(() => {
      pulls.setSearchQuery(value.trim() === "" ? undefined : value.trim());
      void pulls.loadPulls();
    }, 300);
  }

  function pullStateLabel(state: string): string {
    if (state === "open") return "Open";
    if (state === "closed") return "Closed";
    return "All";
  }

  function pullStateDropdownLabel(state: string): string {
    if (state === "all") return "All states";
    return pullStateLabel(state);
  }

  function groupingDropdownLabel(mode: GroupingMode): string {
    if (mode === "byRepo") return "By repo";
    if (mode === "byWorkflow") return "By status";
    return "Flat list";
  }

  function setPullState(state: string): void {
    pulls.setFilterState(state);
    void pulls.loadPulls();
  }

  function resetCompactView(): void {
    pulls.clearLocalFilters();
    grouping.setGroupingMode("byRepo");
    if (pulls.getFilterState() !== "open") {
      setPullState("open");
    }
  }

  const toolbarFilterSections = $derived.by(() => [
    {
      title: "State",
      items: pullStateOptions.map((state) => ({
        id: `state-${state}`,
        label: pullStateDropdownLabel(state),
        active: pulls.getFilterState() === state,
        onSelect: () => setPullState(state),
      })),
    },
    {
      title: "Group",
      items: groupingOptions.map((option) => ({
        id: `group-${option.value}`,
        label: groupingDropdownLabel(option.value),
        active: groupingMode === option.value,
        onSelect: () => grouping.setGroupingMode(option.value),
      })),
    },
  ]);

  const localFilterSections = $derived.by(() => [
    {
      title: "PR",
      items: attributeFilterOptions.map((option) => ({
        id: `pr-${option.value}`,
        label: option.label,
        active: pulls.getAttributeFilters().includes(option.value),
        onSelect: () => pulls.toggleAttributeFilter(option.value),
      })),
    },
    {
      title: "Kanban",
      items: kanbanFilterOptions.map((option) => ({
        id: `kanban-${option.value}`,
        label: option.label,
        active: pulls.getKanbanStatusFilters().includes(option.value),
        onSelect: () => pulls.toggleKanbanStatusFilter(option.value),
      })),
    },
  ]);
  const compactFilterSections = $derived.by(() => [
    ...toolbarFilterSections,
    ...localFilterSections,
  ]);
  const hasCompactFilterChanges = $derived(
    pulls.getFilterState() !== "open"
      || groupingMode !== "byRepo"
      || pulls.getLocalFilterCount() > 0,
  );
  const useCompactFilters = $derived(
    sidebarWidth <= COMPACT_FILTER_MAX_WIDTH,
  );
  const useIconOnlyLocalFilters = $derived(
    sidebarWidth <= LOCAL_FILTER_ICON_ONLY_MAX_WIDTH,
  );

  interface PullGroup {
    key: string;
    collapseKey: string;
    label: string;
    showRepo: boolean;
    items: PullRequest[];
  }

  const groupedPulls = $derived.by((): PullGroup[] | null => {
    if (groupingMode === "byRepo") {
      return [...pulls.pullsByRepo().entries()].map(([repo, prs]) => ({
        key: `repo:${repo}`,
        collapseKey: repo,
        label: repo,
        showRepo: false,
        items: prs,
      }));
    }
    if (groupingMode === "byWorkflow") {
      return workflowGroups.map((wg) => ({
        key: `status:${wg.group}`,
        collapseKey: `status:${wg.group}`,
        label: wg.label,
        showRepo: true,
        items: wg.items,
      }));
    }
    return null;
  });

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

  function handleSelect(ref: PullRequestRouteRef): void {
    pulls.selectPR(
      ref.owner,
      ref.name,
      ref.number,
      ref.provider,
      ref.platformHost,
      ref.repoPath,
    );
    if (_getDetailTab() === "files") {
      navigate(buildPullRequestFilesRoute(ref));
    } else {
      navigate(buildPullRequestRoute(ref));
    }
  }

  function isSelected(ref: PullRequestRouteRef): boolean {
    const sel = pulls.getSelectedPR();
    return sel !== null
      && sel.owner === ref.owner
      && sel.name === ref.name
      && sel.number === ref.number
      && sel.platformHost === ref.platformHost;
  }

  const selectedPRGroup = $derived.by(() => {
    const sel = pulls.getSelectedPR();
    const groups = groupedPulls;
    if (sel === null || groups === null) return null;
    return groups.find((group) =>
      group.items.some((p) =>
        (p.repo_owner ?? "") === sel.owner
        && (p.repo_name ?? "") === sel.name
        && p.Number === sel.number
        && (!sel.platformHost || p.platform_host === sel.platformHost),
      ),
    ) ?? null;
  });

  const keepSelectedGroupExpanded = $derived(
    showSelectedDiffSidebar
      && _getDetailTab() === "files"
      && selectedPRGroup !== null,
  );

  const selectedVisiblePR = $derived.by(() => {
    const sel = pulls.getSelectedPR();
    if (sel === null) return null;
    const pr = visiblePulls.find(
      (p) => (p.repo_owner ?? "") === sel.owner
        && (p.repo_name ?? "") === sel.name
        && p.Number === sel.number
        && (!sel.platformHost || p.platform_host === sel.platformHost),
    );
    if (!pr) return null;
    // Collapsed grouped modes hide the selected PR row, so the files tab
    // renders the fallback file list instead of losing the diff sidebar.
    if (
      selectedPRGroup !== null
      && !keepSelectedGroupExpanded
      && collapsedRepos.isCollapsed("pulls", selectedPRGroup.collapseKey)
    ) {
      return null;
    }
    return pr;
  });

  const isDiffFocus = $derived(
    showSelectedDiffSidebar
      && _getDetailTab() === "files"
      && selectedVisiblePR !== null,
  );

  // True when in files tab and selected PR isn't actually rendered in sidebar
  // (either filtered out of list, or in user-collapsed repo group).
  const needsFallbackFileList = $derived(
    showSelectedDiffSidebar
      && _getDetailTab() === "files"
      && pulls.getSelectedPR() !== null
      && selectedVisiblePR === null,
  );

  const isSelectedActiveWorktree = $derived.by(() => {
    const key = activeWorktreeKey;
    const pr = selectedVisiblePR;
    if (!key || !pr || !pr.worktree_links) return false;
    return pr.worktree_links.some((l) => l.worktree_key === key);
  });
</script>

<div class="pull-list">
  <div class="filter-bar" class:filter-bar--compact={useCompactFilters}>
    <Chip size="sm" uppercase={false} class="chip--muted list-count-chip">
      {visiblePulls.length} PRs
    </Chip>
    <div class="state-toggle">
      {#each pullStateOptions as s (s)}
        <button
          class="state-btn"
          class:state-btn--active={pulls.getFilterState() === s}
          onclick={() => setPullState(s)}
        >{pullStateLabel(s)}</button>
      {/each}
    </div>
    <div class="group-toggle">
      {#each groupingOptions as option (option.value)}
        <button
          class="group-btn"
          class:group-btn--active={groupingMode === option.value}
          onclick={() => grouping.setGroupingMode(option.value)}
        >{option.label}</button>
      {/each}
    </div>
    <div class="compact-filter-menu">
      <FilterDropdown
        label="Filters"
        title="Filters"
        active={hasCompactFilterChanges}
        badgeCount={pulls.getLocalFilterCount()}
        sections={compactFilterSections}
        resetLabel="Reset view"
        onReset={resetCompactView}
        minWidth="190px"
      />
    </div>
    <div
      class="local-filter-menu"
      class:local-filter-menu--icon-only={useIconOnlyLocalFilters}
    >
      <FilterDropdown
        label="PR filters"
        title="PR filters"
        active={pulls.getLocalFilterCount() > 0}
        badgeCount={pulls.getLocalFilterCount()}
        sections={localFilterSections}
        resetLabel="Clear filters"
        onReset={pulls.clearLocalFilters}
        minWidth="190px"
      />
    </div>
    {#if isSidebarToggleEnabled()}
      <LeftSidebarToggle
        state="expanded"
        label="sidebar"
        onclick={toggleSidebar}
        class="left-sidebar-toggle--push"
      />
    {/if}
  </div>
  <div class="search-bar">
    <div class="search-input-wrap">
      <svg class="search-icon" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="6.5" cy="6.5" r="4.5" stroke="currentColor" stroke-width="1.5" />
        <path d="M10 10L14 14" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
      </svg>
      <input
        class="search-input"
        type="search"
        placeholder="Search PRs..."
        value={searchInput}
        oninput={onSearchInput}
      />
    </div>
    <button
      class="star-filter-btn"
      class:star-filter-btn--active={pulls.getFilterStarred()}
      onclick={() => { pulls.setFilterStarred(!pulls.getFilterStarred()); void pulls.loadPulls(); }}
      title={pulls.getFilterStarred() ? "Show all" : "Show starred only"}
    >
      {#if pulls.getFilterStarred()}
        <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
          <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25z"/>
        </svg>
      {:else}
        <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
          <path d="M8 .25a.75.75 0 01.673.418l1.882 3.815 4.21.612a.75.75 0 01.416 1.279l-3.046 2.97.719 4.192a.75.75 0 01-1.088.791L8 12.347l-3.766 1.98a.75.75 0 01-1.088-.79l.72-4.194L.818 6.374a.75.75 0 01.416-1.28l4.21-.611L7.327.668A.75.75 0 018 .25zm0 2.445L6.615 5.5a.75.75 0 01-.564.41l-3.097.45 2.24 2.184a.75.75 0 01.216.664l-.528 3.084 2.769-1.456a.75.75 0 01.698 0l2.77 1.456-.53-3.084a.75.75 0 01.216-.664l2.24-2.183-3.096-.45a.75.75 0 01-.564-.41L8 2.694z"/>
        </svg>
      {/if}
    </button>
  </div>

  {#if pulls.getFilterState() !== "open"}
    <p class="state-note">Showing items closed after middleman began tracking them</p>
  {/if}
  <div
    data-test="pr-list"
    class="list-body"
    class:list-body--diff-focus={isDiffFocus}
    class:list-body--diff-focus-worktree={isDiffFocus && isSelectedActiveWorktree}
  >
    {#if settings.isSettingsLoaded() && !settings.hasConfiguredRepos()}
      <p class="state-message">No repositories configured.<br />
        {#if !isEmbedded()}<button class="settings-link" onclick={() => navigate("/settings")}>Add one in Settings</button>{/if}</p>
    {:else if pulls.isLoading() && pulls.getPulls().length === 0}
      <p class="state-message">Loading…</p>
    {:else if pulls.getError() !== null && pulls.getPulls().length === 0}
      <p class="state-message state-message--error">Error: {pulls.getError()}</p>
    {:else if visiblePulls.length === 0 && sync.getSyncState()?.running && pulls.getPulls().length === 0}
      <div class="state-message sync-message">
        <span class="sync-dot"></span>
        Syncing from GitHub…
      </div>
    {:else if visiblePulls.length === 0 && !sync.getSyncState()?.last_run_at && pulls.getPulls().length === 0}
      <p class="state-message">Waiting for first sync…</p>
    {:else if visiblePulls.length === 0 && pulls.getLocalFilterCount() > 0}
      <p class="state-message">No pull requests match these filters.</p>
    {:else if visiblePulls.length === 0}
      <p class="state-message">No pull requests found.</p>
    {:else}
      {#if groupedPulls !== null}
        {#each groupedPulls as group (group.key)}
          {@const userCollapsed = collapsedRepos.isCollapsed("pulls", group.collapseKey)}
          {@const hasSelectedPR = keepSelectedGroupExpanded && selectedPRGroup?.key === group.key}
          {@const collapsed = userCollapsed && !hasSelectedPR}
          <div class="repo-group">
            <button
              type="button"
              class="repo-header"
              aria-expanded={!collapsed}
              onclick={() => collapsedRepos.toggle("pulls", group.collapseKey)}
            >
              <svg
                class="repo-header__chevron"
                class:repo-header__chevron--collapsed={collapsed}
                width="10" height="10" viewBox="0 0 10 10"
                fill="none" stroke="currentColor" stroke-width="1.5"
              >
                <polyline points="2,3 5,7 8,3" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <span class="repo-header__name">{group.label}</span>
              <span class="repo-header__count">{group.items.length}</span>
            </button>
            {#if !collapsed}
              {#each group.items as pr (pr.ID)}
                {@const prRef = routeRefForPull(pr)}
                {@const prSelected = isSelected(prRef)}
                <PullItem
                  {pr}
                  showRepo={group.showRepo}
                  selected={prSelected}
                  {importAction}
                  onclick={() => handleSelect(prRef)}
                />
                {#if showSelectedDiffSidebar && prSelected && _getDetailTab() === "files"}
                  <div class="diff-files-wrap">
                    <DiffSidebar showCommits={false} />
                  </div>
                {/if}
              {/each}
            {/if}
          </div>
        {/each}
      {:else}
        {#each visiblePulls as pr (pr.ID)}
          {@const prRef = routeRefForPull(pr)}
          {@const prSelected = isSelected(prRef)}
          <PullItem
            {pr}
            showRepo={true}
            selected={prSelected}
            {importAction}
            onclick={() => handleSelect(prRef)}
          />
          {#if showSelectedDiffSidebar && prSelected && _getDetailTab() === "files"}
            <div class="diff-files-wrap">
              <DiffSidebar showCommits={false} />
            </div>
          {/if}
        {/each}
      {/if}
    {/if}
  </div>
  {#if needsFallbackFileList}
    <div class="diff-files-wrap">
                  <DiffSidebar showCommits={false} />
                </div>
  {/if}
  <div class="sidebar-footer">
    {#if !isEmbedded()}
      <button class="add-repo-link" onclick={() => navigate("/settings")}>
        + Add repository
      </button>
    {/if}
  </div>
</div>

<style>
  .pull-list {
    display: flex;
    flex-direction: column;
    height: 100%;
    width: 100%;
  }

  .filter-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
    background: var(--bg-surface);
    overflow: hidden;
  }

  .search-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-default);
    flex-shrink: 0;
    background: var(--bg-surface);
  }

  .search-input-wrap {
    position: relative;
    flex: 1;
    min-width: 0;
  }

  .search-icon {
    position: absolute;
    left: 8px;
    top: 50%;
    transform: translateY(-50%);
    width: 13px;
    height: 13px;
    color: var(--text-muted);
    pointer-events: none;
  }

  .search-input {
    width: 100%;
    font-size: var(--font-size-sm);
    padding: 5px 8px 5px 28px;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
  }

  .search-input:focus {
    border-color: var(--accent-blue);
    outline: none;
  }

  .star-filter-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    cursor: pointer;
    flex-shrink: 0;
    transition: color 0.1s, background 0.1s;
  }

  .star-filter-btn:hover {
    color: var(--accent-amber);
    background: var(--bg-surface-hover);
  }

  .star-filter-btn--active {
    color: var(--accent-amber);
  }

  :global(.list-count-chip) {
    flex-shrink: 0;
  }

  .list-body {
    flex: 1;
    overflow-y: auto;
  }

  /* Diff focus: combine typographic mute on siblings + a continuous
     accent rail that extends from the selected card through the inline
     file list, binding them as one visual unit. */
  .list-body--diff-focus :global(.pull-item:not(.selected) .title) {
    color: var(--text-muted);
    font-weight: 400;
    transition: color 0.15s ease;
  }

  .list-body--diff-focus :global(.pull-item:not(.selected) .state-dot) {
    opacity: 0.45;
  }

  .list-body--diff-focus :global(.pull-item:not(.selected):hover .title) {
    color: var(--text-secondary);
  }

  .list-body--diff-focus .diff-files-wrap {
    border-left: 3px solid var(--accent-blue);
  }

  .list-body--diff-focus-worktree .diff-files-wrap {
    border-left-color: var(--accent-teal, var(--accent-green));
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

  .sync-message {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
  }

  .sync-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent-green);
    animation: pulse 1.5s ease-in-out infinite;
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.4; }
    50% { opacity: 1; }
  }

  .repo-group {
    border-bottom: 1px solid var(--border-default);
  }

  .repo-header {
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    padding: 6px 12px 4px;
    background: var(--bg-inset);
    border-bottom: 1px solid var(--border-muted);
    position: sticky;
    top: 0;
    z-index: 1;

    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    text-align: left;
    border-top: none;
    border-left: none;
    border-right: none;
    cursor: pointer;
    font-family: inherit;
  }

  .repo-header:hover {
    background: var(--bg-surface-hover);
  }

  .repo-header[aria-expanded="false"] {
    border-bottom: none;
  }

  .repo-header__chevron {
    color: var(--text-muted);
    transition: transform 120ms ease;
    flex-shrink: 0;
  }

  .repo-header__chevron--collapsed {
    transform: rotate(-90deg);
  }

  .repo-header__name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .repo-header__count {
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .sidebar-footer {
    padding: 8px 12px;
    border-top: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .add-repo-link {
    font-size: var(--font-size-sm);
    color: var(--text-muted);
    cursor: pointer;
    transition: color 0.1s;
    padding: 0;
  }

  .add-repo-link:hover {
    color: var(--accent-blue);
  }

  .state-toggle {
    display: flex;
    gap: 2px;
    background: var(--bg-inset);
    border-radius: 6px;
    padding: 2px;
    animation: sidebar-filter-pop-out 120ms ease-out;
    transform-origin: right center;
  }

  .compact-filter-menu {
    display: none;
    flex-shrink: 0;
    transform-origin: left center;
  }

  .local-filter-menu {
    flex-shrink: 0;
  }

  .local-filter-menu--icon-only :global(.filter-btn) {
    width: 34px;
    justify-content: center;
    gap: 0;
    padding-inline: 0;
  }

  .local-filter-menu--icon-only :global(.filter-trigger-label) {
    display: none;
  }

  .local-filter-menu--icon-only :global(.filter-badge) {
    position: absolute;
    top: -5px;
    right: -5px;
  }

  .compact-filter-menu :global(.filter-btn) {
    width: 34px;
    justify-content: center;
    gap: 0;
    padding-inline: 0;
  }

  .compact-filter-menu :global(.filter-trigger-label),
  .compact-filter-menu :global(.filter-trigger-detail) {
    display: none;
  }

  .compact-filter-menu :global(.filter-badge) {
    position: absolute;
    top: -5px;
    right: -5px;
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
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.1);
  }
  .state-note {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    padding: 4px 10px;
    margin: 0;
    border-bottom: 1px solid var(--border-muted);
  }
  .group-toggle {
    display: flex;
    gap: 2px;
    background: var(--bg-inset);
    border-radius: 6px;
    padding: 2px;
    animation: sidebar-filter-pop-out 120ms ease-out;
    transform-origin: right center;
  }
  .group-btn {
    font-size: var(--font-size-xs);
    padding: 2px 8px;
    border: none;
    border-radius: 4px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    white-space: nowrap;
  }
  .group-btn--active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.1);
  }

  .filter-bar--compact .state-toggle,
  .filter-bar--compact .group-toggle {
    display: none;
  }

  .filter-bar--compact .local-filter-menu {
    display: none;
  }

  .filter-bar--compact .compact-filter-menu {
    display: block;
    animation: sidebar-filter-collapse-in 120ms ease-out;
  }

  @keyframes sidebar-filter-collapse-in {
    from {
      opacity: 0.2;
      transform: translateX(-10px) scale(0.82);
    }
    to {
      opacity: 1;
      transform: translateX(0) scale(1);
    }
  }

  @keyframes sidebar-filter-pop-out {
    from {
      opacity: 0;
      transform: translateX(8px) scale(0.92);
    }
    to {
      opacity: 1;
      transform: translateX(0) scale(1);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .state-toggle,
    .group-toggle,
    .filter-bar--compact .compact-filter-menu {
      animation: none;
    }
  }

  .diff-files-wrap {
    max-height: 40vh;
    overflow-y: auto;
  }
</style>
