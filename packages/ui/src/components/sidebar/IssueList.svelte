<script lang="ts">
  import { getStores, getNavigate, getSidebar } from "../../context.js";
  import IssueItem from "./IssueItem.svelte";
  import Chip from "../shared/Chip.svelte";
  import FilterDropdown from "../shared/FilterDropdown.svelte";
  import LeftSidebarToggle from "../shared/LeftSidebarToggle.svelte";
  import type { Issue } from "../../api/types.js";
  import {
    buildIssueRoute,
    type IssueRouteRef,
  } from "../../routes.js";

  const { issues, sync, grouping, collapsedRepos, settings } = getStores();
  const navigate = getNavigate();
  const { isEmbedded, isSidebarToggleEnabled, toggleSidebar } = getSidebar();

  interface Props {
    sidebarWidth?: number;
  }

  const { sidebarWidth = 340 }: Props = $props();

  const issueStateOptions = ["open", "closed", "all"] as const;
  const groupingOptions = [
    { byRepo: true, label: "By Repo" },
    { byRepo: false, label: "All" },
  ];
  // Playwright-measured with a buffered "9999 issues" count label:
  // the full issue filter row first fits at 373px.
  const COMPACT_FILTER_MAX_WIDTH = 372;

  let searchInput = $state(issues.getIssueSearchQuery() ?? "");
  let debounceHandle: ReturnType<typeof setTimeout> | null = null;
  let refreshHandle: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    void issues.loadIssues();

    refreshHandle = setInterval(() => {
      void issues.loadIssues();
    }, 15_000);

    if (sync.getSyncState()?.running) {
      sync.onNextSyncComplete(() => void issues.loadIssues());
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
      issues.setIssueSearchQuery(value.trim() === "" ? undefined : value.trim());
      void issues.loadIssues();
    }, 300);
  }

  function issueStateLabel(state: string): string {
    if (state === "open") return "Open";
    if (state === "closed") return "Closed";
    return "All";
  }

  function setIssueState(state: string): void {
    issues.setIssueFilterState(state);
    void issues.loadIssues();
  }

  const compactFilterSections = $derived.by(() => [
    {
      title: "State",
      items: issueStateOptions.map((state) => ({
        id: `state-${state}`,
        label: issueStateLabel(state),
        active: issues.getIssueFilterState() === state,
        onSelect: () => setIssueState(state),
      })),
    },
    {
      title: "Group",
      items: groupingOptions.map((option) => ({
        id: `group-${option.byRepo ? "byRepo" : "all"}`,
        label: option.label,
        active: grouping.getGroupByRepo() === option.byRepo,
        onSelect: () => grouping.setGroupByRepo(option.byRepo),
      })),
    },
  ]);

  const hasCompactFilterChanges = $derived(
    issues.getIssueFilterState() !== "open" || !grouping.getGroupByRepo(),
  );
  const useCompactFilters = $derived(
    sidebarWidth <= COMPACT_FILTER_MAX_WIDTH,
  );

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

  function handleSelect(ref: IssueRouteRef): void {
    issues.selectIssue(
      ref.owner,
      ref.name,
      ref.number,
      ref.provider,
      ref.platformHost,
      ref.repoPath,
    );
    navigate(buildIssueRoute(ref));
  }

  function isSelected(ref: IssueRouteRef): boolean {
    const sel = issues.getSelectedIssue();
    return sel !== null
      && sel.owner === ref.owner
      && sel.name === ref.name
      && sel.number === ref.number
      && sel.platformHost === ref.platformHost;
  }
</script>

<div class="issue-list">
  <div class="filter-bar" class:filter-bar--compact={useCompactFilters}>
    <Chip size="sm" uppercase={false} class="chip--muted list-count-chip">
      {issues.getIssues().length} issues
    </Chip>
    <div class="state-toggle">
      {#each issueStateOptions as s (s)}
        <button
          class="state-btn"
          class:state-btn--active={issues.getIssueFilterState() === s}
          onclick={() => setIssueState(s)}
        >{issueStateLabel(s)}</button>
      {/each}
    </div>
    <div class="group-toggle">
      {#each groupingOptions as option (option.label)}
        <button
          class="group-btn"
          class:group-btn--active={grouping.getGroupByRepo() === option.byRepo}
          onclick={() => grouping.setGroupByRepo(option.byRepo)}
        >{option.label}</button>
      {/each}
    </div>
    <div class="compact-filter-menu">
      <FilterDropdown
        label="Filters"
        title="Filters"
        icon="more"
        active={hasCompactFilterChanges}
        showBadge={false}
        sections={compactFilterSections}
        minWidth="160px"
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
        placeholder="Search issues..."
        value={searchInput}
        oninput={onSearchInput}
      />
    </div>
    <button
      class="star-filter-btn"
      class:star-filter-btn--active={issues.getIssueFilterStarred()}
      onclick={() => { issues.setIssueFilterStarred(!issues.getIssueFilterStarred()); void issues.loadIssues(); }}
      title={issues.getIssueFilterStarred() ? "Show all" : "Show starred only"}
    >
      {#if issues.getIssueFilterStarred()}
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

  {#if issues.getIssueFilterState() !== "open"}
    <p class="state-note">Showing items closed after middleman began tracking them</p>
  {/if}
  <div class="list-body">
    {#if settings.isSettingsLoaded() && !settings.hasConfiguredRepos()}
      <p class="state-message">No repositories configured.<br />
        {#if !isEmbedded()}<button class="settings-link" onclick={() => navigate("/settings")}>Add one in Settings</button>{/if}</p>
    {:else if issues.isIssuesLoading() && issues.getIssues().length === 0}
      <p class="state-message">Loading…</p>
    {:else if issues.getIssuesError() !== null && issues.getIssues().length === 0}
      <p class="state-message state-message--error">Error: {issues.getIssuesError()}</p>
    {:else if issues.getIssues().length === 0 && sync.getSyncState()?.running}
      <div class="state-message sync-message">
        <span class="sync-dot"></span>
        Syncing from GitHub…
      </div>
    {:else if issues.getIssues().length === 0 && !sync.getSyncState()?.last_run_at}
      <p class="state-message">Waiting for first sync…</p>
    {:else if issues.getIssues().length === 0}
      <p class="state-message">No issues found.</p>
    {:else}
      {#if grouping.getGroupByRepo()}
        {#each [...issues.issuesByRepo().entries()] as [repo, repoIssues] (repo)}
          {@const collapsed = collapsedRepos.isCollapsed("issues", repo)}
          <div class="repo-group">
            <button
              type="button"
              class="repo-header"
              aria-expanded={!collapsed}
              onclick={() => collapsedRepos.toggle("issues", repo)}
            >
              <svg
                class="repo-header__chevron"
                class:repo-header__chevron--collapsed={collapsed}
                width="10" height="10" viewBox="0 0 10 10"
                fill="none" stroke="currentColor" stroke-width="1.5"
              >
                <polyline points="2,3 5,7 8,3" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <span class="repo-header__name">{repo}</span>
              <span class="repo-header__count">{repoIssues.length}</span>
            </button>
            {#if !collapsed}
              {#each repoIssues as issue (issue.ID)}
                {@const issueRef = routeRefForIssue(issue)}
                <IssueItem
                  {issue}
                  showRepo={false}
                  selected={isSelected(issueRef)}
                  onclick={() => handleSelect(issueRef)}
                />
              {/each}
            {/if}
          </div>
        {/each}
      {:else}
        {#each issues.getIssues() as issue (issue.ID)}
          {@const issueRef = routeRefForIssue(issue)}
          <IssueItem
            {issue}
            showRepo={true}
            selected={isSelected(issueRef)}
            onclick={() => handleSelect(issueRef)}
          />
        {/each}
      {/if}
    {/if}
  </div>
  <div class="sidebar-footer">
    {#if !isEmbedded()}
      <button class="add-repo-link" onclick={() => navigate("/settings")}>
        + Add repository
      </button>
    {/if}
  </div>
</div>

<style>
  .issue-list {
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

  .compact-filter-menu :global(.filter-btn) {
    width: 26px;
    justify-content: center;
    padding: 3px;
  }

  .compact-filter-menu :global(.filter-trigger-label),
  .compact-filter-menu :global(.filter-trigger-detail) {
    display: none;
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
</style>
