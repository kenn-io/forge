<script lang="ts">
  import { Effect } from "effect";
  import { pollWhileVisible } from "../effect/poll-while-visible.js";
  import { onDestroy, tick, untrack } from "svelte";
  import type { Attachment } from "svelte/attachments";
  import { ScrollBox, StatusDot } from "@kenn-io/kit-ui";
  import { getAppRuntime } from "../app/runtime-context.js";
  import type { AppExecution } from "../app/runtime.js";
  import { getStores, getNavigate, getActions } from "../context.js";
  import { groupByWorkflow } from "../stores/workflow.svelte.js";
  import PullItem from "../components/sidebar/PullItem.svelte";
  import IssueItem from "../components/sidebar/IssueItem.svelte";
  import type { Issue, PullRequest } from "../api/types.js";
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

  const { pulls, issues, sync, settings, grouping, activity } = getStores();
  const runtime = getAppRuntime();
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
    groupByWorkflow(pulls.getPulls(), activity.getUseWorkspaceActivityForRecency()),
  );

  interface Props {
    listType: "mrs" | "issues";
    repo?: string;
    routeFamily?: "focus" | "canonical";
    showRepoSelector?: boolean;
    chunked?: boolean;
  }

  const {
    listType,
    repo,
    routeFamily = "focus",
    showRepoSelector = false,
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

  function toggleInvolvesMe(): void {
    if (listType === "mrs") {
      pulls.setInvolvesMe(!pulls.getInvolvesMe());
    } else {
      issues.setInvolvesMe(!issues.getInvolvesMe());
    }
    resetPageAndLoad();
  }

  function toggleReferencedByPR(): void {
    issues.setReferencedByPR(!issues.getReferencedByPR());
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
    const unsubscribeSync = sync.subscribeSyncComplete(loadList);

    return () => {
      searchExecution?.interrupt();
      execution.interrupt();
      unsubscribeSync();
    };
  });

  onDestroy(() => searchExecution?.interrupt());

  function onSearchInput(value: string): void {
    searchInput = value;

    searchExecution?.interrupt();
    searchExecution = runtime.runCommand(
      Effect.sleep("300 millis").pipe(
        Effect.andThen(Effect.sync(() => {
          const q = value.trim() === "" ? undefined : value.trim();
          if (listType === "mrs") pulls.setSearchQuery(q);
          else issues.setIssueSearchQuery(q);
          resetPageAndLoad();
        })),
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
  const listCapped = $derived(
    listType === "mrs" ? pulls.isListCapped() : issues.isIssueListCapped(),
  );

  function resetPageAndLoad(): void {
    pageLimit = 30;
    paginationArmed = true;
    loadList();
  }

  function loadMore(): void {
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
  const involvesMe = $derived(
    listType === "mrs" ? pulls.getInvolvesMe() : issues.getInvolvesMe(),
  );
</script>

<div class="focus-list" bind:this={listRoot}>
  {#if !showRepoSelector}
    <div class="header">
      <span class="header-label">{repoLabel}</span>
      <span class="count-badge">{itemCount} {itemLabel}</span>
    </div>
  {/if}
  <div
    id="focus-list-filters"
    class="filter-bar"
    class:filter-bar--expanded={filtersExpanded}
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
    <div class="state-toggle">
      {#if listType === "mrs"}
        {#each ["open", "closed", "all"] as s (s)}
          <button
            class="state-btn"
            class:state-btn--active={prFilterState === s}
            onclick={() => {
              pulls.setFilterState(s);
              resetPageAndLoad();
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
              resetPageAndLoad();
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
    <div class="visibility-controls">
      <button
        type="button"
        class="visibility-btn"
        class:visibility-btn--active={involvesMe}
        aria-pressed={involvesMe}
        onclick={toggleInvolvesMe}
      >Involves me</button>
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
      {:else}
        {#if issues.canFilterReferencedByPR()}
          <button
            type="button"
            class="visibility-btn"
            class:visibility-btn--active={issues.getReferencedByPR()}
            aria-pressed={issues.getReferencedByPR()}
            onclick={toggleReferencedByPR}
          >Referenced by PR</button>
        {/if}
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
    oninput={onSearchInput}
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
          <StatusDot status="working" label="Syncing pull requests" size={6} />
          <span aria-hidden="true">Syncing...</span>
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
                repoLabel={prRepoLabelFormatter.format({
                  provider: pr.repo.provider,
                  platformHost: pr.repo.platform_host,
                  owner: pr.repo.owner,
                  name: pr.repo.name,
                  repoPath: pr.repo.repo_path,
                })}
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
            repoLabel={prRepoLabelFormatter.format({
              provider: pr.repo.provider,
              platformHost: pr.repo.platform_host,
              owner: pr.repo.owner,
              name: pr.repo.name,
              repoPath: pr.repo.repo_path,
            })}
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
          <StatusDot status="working" label="Syncing issues" size={6} />
          <span aria-hidden="true">Syncing...</span>
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
            repoLabel={issueRepoLabelFormatter.format({
              provider: issue.repo.provider,
              platformHost: issue.repo.platform_host,
              owner: issue.repo.owner,
              name: issue.repo.name,
              repoPath: issue.repo.repo_path,
            })}
            showRepo={!repo}
            selected={false}
            onclick={() => handleIssueSelect(issueRef)}
          />
        {/each}
      {/if}
    {/if}
    {#if chunked && listCapped && pageLimit < 500}
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
  }

  .visibility-controls {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-left: auto;
  }

  .group-btn,
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

  .group-btn--active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
  }

  .visibility-btn--active {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 12%, transparent);
    border-color: color-mix(in srgb, var(--accent-blue) 34%, transparent);
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

  :global(.mobile-main) .focus-list {
    --focus-mobile-space-2xs: 4.5px;
    --focus-mobile-space-xs: 6.5px;
    --focus-mobile-space-sm: 10px;
    --focus-mobile-space-md: 13px;
    --focus-mobile-hit-target: 44px;
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

  :global(.mobile-main) .state-toggle,
  :global(.mobile-main) .group-toggle {
    gap: var(--focus-mobile-space-2xs);
    border-radius: var(--focus-mobile-radius-md);
    padding: var(--focus-mobile-space-2xs);
  }

  :global(.mobile-main) .group-toggle {
    margin-left: 0;
  }

  :global(.mobile-main) .visibility-controls {
    flex-wrap: wrap;
    align-items: stretch;
    gap: var(--focus-mobile-space-sm);
    margin-left: 0;
  }

  :global(.mobile-main) .state-btn,
  :global(.mobile-main) .group-btn,
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

  :global(.mobile-main) .group-header {
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    font-size: var(--font-size-xs);
    line-height: 1.25;
  }

  :global(.mobile-main) .state-note,
  :global(.mobile-main) .state-message {
    padding: var(--focus-mobile-space-sm) var(--focus-mobile-space-md);
    font-size: var(--font-size-sm);
    line-height: 1.35;
  }
</style>
