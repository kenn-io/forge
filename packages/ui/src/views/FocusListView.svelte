<script lang="ts">
  import { getStores, getNavigate, getActions } from "../context.js";
  import { groupByWorkflow } from "../stores/workflow.svelte.js";
  import PullItem from "../components/sidebar/PullItem.svelte";
  import IssueItem from "../components/sidebar/IssueItem.svelte";
  import type { Issue, PullRequest } from "../api/types.js";
  import {
    buildFocusIssueRoute,
    buildFocusPullRequestRoute,
    buildIssueRoute,
    buildPullRequestRoute,
    type IssueRouteRef,
    type PullRequestRouteRef,
  } from "../routes.js";

  const { pulls, issues, sync, settings, grouping } = getStores();
  const navigate = getNavigate();
  const actions = getActions();

  const importAction = $derived(
    (actions.pull ?? []).find(
      (a) => a.id === "import-worktree",
    ),
  );
  const groupingMode = $derived(
    grouping.getGroupingMode(),
  );
  const workflowGroups = $derived(
    groupByWorkflow(pulls.getPulls()),
  );

  interface Props {
    listType: "mrs" | "issues";
    repo?: string;
    routeFamily?: "focus" | "canonical";
  }

  const { listType, repo, routeFamily = "focus" }: Props = $props();

  let searchInput = $state("");
  let debounceHandle: ReturnType<typeof setTimeout> | null =
    null;
  let refreshHandle: ReturnType<typeof setInterval> | null =
    null;

  const repoLabel = $derived(repo ?? "All repositories");

  const repoParams = $derived(
    repo ? { repo } : undefined,
  );

  $effect(() => {
    if (listType === "mrs") {
      void pulls.loadPulls(repoParams);
    } else {
      void issues.loadIssues(repoParams);
    }

    refreshHandle = setInterval(() => {
      if (listType === "mrs") {
        void pulls.loadPulls(repoParams);
      } else {
        void issues.loadIssues(repoParams);
      }
    }, 15_000);

    if (sync.getSyncState()?.running) {
      sync.onNextSyncComplete(() => {
        if (listType === "mrs") {
          void pulls.loadPulls(repoParams);
        } else {
          void issues.loadIssues(repoParams);
        }
      });
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
      const q =
        value.trim() === "" ? undefined : value.trim();
      if (listType === "mrs") {
        pulls.setSearchQuery(q);
        void pulls.loadPulls(repoParams);
      } else {
        issues.setIssueSearchQuery(q);
        void issues.loadIssues(repoParams);
      }
    }, 300);
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
    navigate(
      routeFamily === "canonical"
        ? buildPullRequestRoute(ref)
        : buildFocusPullRequestRoute(ref),
    );
  }

  function handleIssueSelect(ref: IssueRouteRef): void {
    navigate(
      routeFamily === "canonical"
        ? buildIssueRoute(ref)
        : buildFocusIssueRoute(ref),
    );
  }

  // Filter state accessors for PRs.
  const prFilterState = $derived(pulls.getFilterState());
  const prItems = $derived(pulls.getPulls());
  const prLoading = $derived(pulls.isLoading());
  const prError = $derived(pulls.getError());

  // Filter state accessors for issues.
  const issueFilterState = $derived(
    issues.getIssueFilterState(),
  );
  const issueItems = $derived(issues.getIssues());
  const issueLoading = $derived(issues.isIssuesLoading());
  const issueError = $derived(issues.getIssuesError());

  const itemCount = $derived(
    listType === "mrs" ? prItems.length : issueItems.length,
  );
  const itemLabel = $derived(
    listType === "mrs" ? "PRs" : "issues",
  );
</script>

<div class="focus-list">
  <div class="header">
    <span class="header-label">{repoLabel}</span>
    <span class="count-badge">{itemCount} {itemLabel}</span>
  </div>
  <div class="filter-bar">
    <div class="state-toggle">
      {#if listType === "mrs"}
        {#each ["open", "closed", "all"] as s (s)}
          <button
            class="state-btn"
            class:state-btn--active={prFilterState === s}
            onclick={() => {
              pulls.setFilterState(s);
              void pulls.loadPulls(repoParams);
            }}
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
            onclick={() => {
              issues.setIssueFilterState(s);
              void issues.loadIssues(repoParams);
            }}
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
    {#if listType === "mrs"}
      <div class="group-toggle">
        <button
          class="group-btn"
          class:group-btn--active={groupingMode === "byWorkflow"}
          onclick={() => grouping.setGroupingMode("byWorkflow")}
        >Status</button>
        <button
          class="group-btn"
          class:group-btn--active={groupingMode === "flat"}
          onclick={() => grouping.setGroupingMode("flat")}
        >All</button>
      </div>
    {/if}
  </div>
  <div class="search-bar">
    <div class="search-input-wrap">
      <svg
        class="search-icon"
        viewBox="0 0 16 16"
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
      >
        <circle
          cx="6.5"
          cy="6.5"
          r="4.5"
          stroke="currentColor"
          stroke-width="1.5"
        />
        <path
          d="M10 10L14 14"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
        />
      </svg>
      <input
        class="search-input"
        type="search"
        placeholder="Search {itemLabel}..."
        value={searchInput}
        oninput={onSearchInput}
      />
    </div>
  </div>

  {#if listType === "mrs" && prFilterState !== "open"}
    <p class="state-note">
      Showing items closed after tracking began
    </p>
  {:else if listType === "issues" && issueFilterState !== "open"}
    <p class="state-note">
      Showing items closed after tracking began
    </p>
  {/if}

  <div class="list-body">
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
          <span class="sync-dot"></span>
          Syncing...
        </div>
      {:else if prItems.length === 0 && !sync.getSyncState()?.last_run_at}
        <p class="state-message">Waiting for first sync...</p>
      {:else if prItems.length === 0}
        <p class="state-message">No pull requests found.</p>
      {:else if groupingMode === "byWorkflow" && prFilterState === "open"}
        {#each workflowGroups as wg (wg.group)}
          <div class="workflow-group">
            <h3 class="group-header">{wg.label}</h3>
            {#each wg.items as pr (pr.ID)}
              {@const prRef = routeRefForPull(pr)}
              <PullItem
                {pr}
                showRepo={!repo}
                selected={false}
                {importAction}
                onclick={() => handlePRSelect(prRef)}
              />
            {/each}
          </div>
        {/each}
      {:else}
        {#each prItems as pr (pr.ID)}
          {@const prRef = routeRefForPull(pr)}
          <PullItem
            {pr}
            showRepo={!repo}
            selected={false}
            {importAction}
            onclick={() => handlePRSelect(prRef)}
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
          <span class="sync-dot"></span>
          Syncing...
        </div>
      {:else if issueItems.length === 0 && !sync.getSyncState()?.last_run_at}
        <p class="state-message">Waiting for first sync...</p>
      {:else if issueItems.length === 0}
        <p class="state-message">No issues found.</p>
      {:else}
        {#each issueItems as issue (issue.ID)}
          {@const issueRef = routeRefForIssue(issue)}
          <IssueItem
            {issue}
            showRepo={!repo}
            selected={false}
            onclick={() => handleIssueSelect(issueRef)}
          />
        {/each}
      {/if}
    {/if}
  </div>
</div>

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

  .group-toggle {
    display: flex;
    gap: 2px;
    background: var(--bg-inset);
    border-radius: 6px;
    padding: 2px;
    margin-left: auto;
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

  .workflow-group {
    border-bottom: 1px solid var(--border-default);
  }

  .group-header {
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
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.1);
  }

  .state-note {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    padding: 4px 10px;
    margin: 0;
    border-bottom: 1px solid var(--border-muted);
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
    0%,
    100% {
      opacity: 0.4;
    }
    50% {
      opacity: 1;
    }
  }

  :global(.mobile-main) .focus-list {
    --focus-mobile-space-2xs: 0.36rem;
    --focus-mobile-space-xs: 0.5rem;
    --focus-mobile-space-sm: 0.75rem;
    --focus-mobile-space-md: 1rem;
    --focus-mobile-hit-target: 3.4rem;
    --focus-mobile-radius-sm: 0.65rem;
    --focus-mobile-radius-md: 0.9rem;
    background: var(--bg-primary);
  }

  :global(.mobile-main) .header {
    gap: var(--focus-mobile-space-sm);
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    border-bottom: thin solid var(--border-default);
  }

  :global(.mobile-main) .header-label {
    font-size: var(--font-size-mobile-body);
    line-height: 1.35;
  }

  :global(.mobile-main) .count-badge {
    min-height: calc(var(--focus-mobile-hit-target) * 0.72);
    display: inline-flex;
    align-items: center;
    border: thin solid var(--border-muted);
    border-radius: 999rem;
    padding: var(--focus-mobile-space-2xs) var(--focus-mobile-space-sm);
    font-size: var(--font-size-mobile-xs);
    line-height: 1.25;
  }

  :global(.mobile-main) .filter-bar {
    flex-wrap: wrap;
    align-items: stretch;
    gap: var(--focus-mobile-space-sm);
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    border-bottom: thin solid var(--border-muted);
  }

  :global(.mobile-main) .state-toggle,
  :global(.mobile-main) .group-toggle {
    gap: var(--focus-mobile-space-2xs);
    border-radius: var(--focus-mobile-radius-md);
    padding: var(--focus-mobile-space-2xs);
  }

  :global(.mobile-main) .group-toggle {
    margin-left: 0;
  }

  :global(.mobile-main) .state-btn,
  :global(.mobile-main) .group-btn {
    min-height: var(--focus-mobile-hit-target);
    border-radius: var(--focus-mobile-radius-sm);
    padding: var(--focus-mobile-space-xs) var(--focus-mobile-space-sm);
    font-size: var(--font-size-mobile-sm);
    line-height: 1.3;
    font-weight: 600;
  }

  :global(.mobile-main) .search-bar {
    gap: var(--focus-mobile-space-sm);
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    border-bottom: thin solid var(--border-default);
  }

  :global(.mobile-main) .search-icon {
    left: var(--focus-mobile-space-sm);
    width: 1.25rem;
    height: 1.25rem;
  }

  :global(.mobile-main) .search-input {
    min-height: var(--focus-mobile-hit-target);
    border: thin solid var(--border-muted);
    border-radius: var(--focus-mobile-radius-sm);
    padding:
      var(--focus-mobile-space-xs)
      var(--focus-mobile-space-sm)
      var(--focus-mobile-space-xs)
      calc(var(--focus-mobile-space-md) + 1.5rem);
    font-size: var(--font-size-mobile-body);
    line-height: 1.35;
  }

  :global(.mobile-main) .group-header {
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    font-size: var(--font-size-mobile-xs);
    line-height: 1.25;
  }

  :global(.mobile-main) .state-note,
  :global(.mobile-main) .state-message {
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    font-size: var(--font-size-mobile-sm);
    line-height: 1.35;
  }
</style>
