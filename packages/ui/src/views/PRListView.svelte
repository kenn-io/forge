<script module lang="ts">
  type DetailTab = "conversation" | "files";

  const filesScrollPositions: Record<string, number> = Object.create(null) as Record<string, number>;
</script>

<script lang="ts">
  import { untrack } from "svelte";
  import {
    getNavigate, getSidebar, getStores,
  } from "../context.js";
  import CollapsibleResizableSidebar from "../components/shared/CollapsibleResizableSidebar.svelte";
  import PullList from "../components/sidebar/PullList.svelte";
  import PullDetail
    from "../components/detail/PullDetail.svelte";
  import DiffFilesLayout from "../components/diff/DiffFilesLayout.svelte";
  import type { ProviderCapabilities, PullDetail as PullDetailResponse } from "../api/types.js";
  import type { DetailSyncMode } from "../stores/detail.svelte.js";
  import { reviewThreadsFromEvents } from "../components/diff/review-thread-context.js";
  import {
    buildFocusPullRequestRoute,
    buildPullRequestFilesRoute,
    buildPullRequestRoute,
    type PullRequestRouteRef,
  } from "../routes.js";
  import { canonicalProvider } from "../api/provider-routes.js";

  type StackMemberNavigate = (ref: PullRequestRouteRef) => boolean | void;

  const { isSidebarToggleEnabled, toggleSidebar } = getSidebar();
  const navigate = getNavigate();
  const { detail: detailStore } = getStores();
  const splitViewStorageKey = "pr-detail-split-view";
  const regularConversationPanelWidth = 800 + 24 + 24;
  const minSplitViewWidth = regularConversationPanelWidth * 2;

  const defaultProviderCapabilities: ProviderCapabilities = {
    read_repositories: true,
    read_merge_requests: true,
    read_issues: true,
    read_comments: true,
    read_releases: true,
    read_labels: true,
    read_ci: true,
    comment_mutation: true,
    thread_reply: false,
    thread_resolve: false,
    label_mutation: true,
    state_mutation: true,
    merge_mutation: true,
    review_mutation: true,
    workflow_approval: true,
    ready_for_review: true,
    issue_mutation: true,
    review_draft_mutation: false,
    review_thread_resolution: false,
    read_review_threads: false,
    native_multiline_ranges: false,
    supported_review_actions: [],
  };

  interface Props {
    selectedPR?: PullRequestRouteRef | null;
    detailTab?: DetailTab;
    isSidebarCollapsed?: boolean;
    hideSidebar?: boolean;
    sidebarWidth?: number;
    autoSyncDetail?: DetailSyncMode;
    hideStaleDetailWhileLoading?: boolean;
    workflowApprovalSync?: boolean;
    routeFamily?: "canonical" | "focus";
    onSidebarResize?: (width: number) => void;
    onDetailTabChange?: (tab: DetailTab) => void;
    onStackMemberNavigate?: StackMemberNavigate;
  }

  let {
    selectedPR = null,
    detailTab = "conversation",
    isSidebarCollapsed = false,
    hideSidebar = false,
    sidebarWidth = 340,
    autoSyncDetail = "background",
    hideStaleDetailWhileLoading = false,
    workflowApprovalSync = true,
    routeFamily = "canonical",
    onSidebarResize,
    onDetailTabChange,
    onStackMemberNavigate,
  }: Props = $props();

  let detailHost: HTMLDivElement | undefined = $state();
  let detailHostWidth = $state(0);
  let splitViewEnabled = $state(loadSplitViewPreference());

  const splitViewAvailable = $derived(selectedPR !== null && detailHostWidth >= minSplitViewWidth);
  const splitViewActive = $derived(splitViewAvailable && splitViewEnabled);

  function safeGetItem(key: string): string | null {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  }

  function safeSetItem(key: string, value: string): void {
    try {
      localStorage.setItem(key, value);
    } catch {
      /* ignore */
    }
  }

  function loadSplitViewPreference(): boolean {
    return safeGetItem(splitViewStorageKey) === "1";
  }

  function setSplitViewEnabled(enabled: boolean): void {
    splitViewEnabled = enabled;
    safeSetItem(splitViewStorageKey, enabled ? "1" : "0");
  }

  function filesScrollKey(): string | null {
    if (selectedPR === null) return null;
    return [
      selectedPR.provider,
      selectedPR.platformHost ?? "",
      selectedPR.repoPath,
      selectedPR.number,
    ].join("\0");
  }

  function filesScrollTop(): number {
    const key = filesScrollKey();
    return key ? (filesScrollPositions[key] ?? 0) : 0;
  }

  function rememberFilesScroll(scrollTop: number): void {
    const key = filesScrollKey();
    if (!key) return;
    filesScrollPositions[key] = scrollTop;
  }

  function selectDetailTab(tab: DetailTab): void {
    if (onDetailTabChange) {
      onDetailTabChange(tab);
      return;
    }
    if (selectedPR === null) return;
    navigate(
      tab === "files"
        ? buildPullRequestFilesRoute(selectedPR)
        : buildPullRequestRoute(selectedPR),
    );
  }

  function handleStackMemberNavigate(ref: PullRequestRouteRef): boolean | void {
    if (onStackMemberNavigate) return onStackMemberNavigate(ref);
    if (routeFamily !== "focus") return undefined;
    navigate(buildFocusPullRequestRoute(ref));
    return true;
  }

  function detailMatchesSelected(
    detail: PullDetailResponse | null,
    ref: PullRequestRouteRef | null,
  ): boolean {
    return !!detail && !!ref
      && detail.repo_owner === ref.owner
      && detail.repo_name === ref.name
      && detail.merge_request.Number === ref.number
      && canonicalProvider(detail.repo?.provider ?? "") === canonicalProvider(ref.provider)
      && detail.repo?.platform_host === ref.platformHost
      && detail.repo?.repo_path === ref.repoPath;
  }

  const selectedDetail = $derived.by(() => {
    const detail = detailStore.getDetail();
    return detailMatchesSelected(detail, selectedPR) ? detail : null;
  });

  $effect(() => {
    if (selectedPR === null || (!splitViewActive && detailTab !== "files")) return;
    const ref = selectedPR;
    untrack(() => {
      if (detailMatchesSelected(detailStore.getDetail(), ref)) return;
      void detailStore.loadDetail(
        ref.owner,
        ref.name,
        ref.number,
        {
          sync: false,
          provider: ref.provider,
          platformHost: ref.platformHost,
          repoPath: ref.repoPath,
        },
      );
    });
  });

  $effect(() => {
    const host = detailHost;
    if (!host) {
      detailHostWidth = 0;
      return;
    }

    detailHostWidth = Math.round(host.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;

    const observer = new ResizeObserver((entries) => {
      detailHostWidth = Math.round(
        entries[0]?.contentRect.width ?? host.getBoundingClientRect().width,
      );
    });
    observer.observe(host);

    return () => {
      observer.disconnect();
    };
  });
</script>

<CollapsibleResizableSidebar
  isCollapsed={isSidebarCollapsed}
  {hideSidebar}
  {sidebarWidth}
  {onSidebarResize}
  showCollapsedStrip={isSidebarToggleEnabled()}
  onExpand={toggleSidebar}
  mainEmpty={selectedPR === null}
>
  {#snippet sidebar()}
    <PullList
      getDetailTab={() => detailTab}
      showSelectedDiffSidebar={false}
      {sidebarWidth}
    />
  {/snippet}

  {#if selectedPR !== null}
    <div class="detail-host" bind:this={detailHost}>
      <div class="detail-tabs">
        <button
          class="detail-tab"
          class:detail-tab--active={detailTab === "conversation"}
          onclick={() => selectDetailTab("conversation")}
        >
          Conversation
        </button>
        <button
          class="detail-tab"
          class:detail-tab--active={detailTab === "files"}
          onclick={() => selectDetailTab("files")}
        >
          Files changed
        </button>
        {#if splitViewAvailable}
          <button
            type="button"
            class="detail-split-toggle"
            class:detail-split-toggle--active={splitViewActive}
            aria-pressed={splitViewActive}
            onclick={() => setSplitViewEnabled(!splitViewEnabled)}
          >
            Split view
          </button>
        {/if}
      </div>

      {#if splitViewActive}
        <div class="detail-split-layout">
          <section class="detail-split-pane" aria-label="Conversation">
            <PullDetail
              owner={selectedPR.owner}
              name={selectedPR.name}
              number={selectedPR.number}
              provider={selectedPR.provider}
              platformHost={selectedPR.platformHost}
              repoPath={selectedPR.repoPath}
              autoSync={autoSyncDetail}
              {workflowApprovalSync}
              hideTabs={true}
              hideStaleWhileLoading={hideStaleDetailWhileLoading}
              onStackMemberNavigate={handleStackMemberNavigate}
            />
          </section>
          <section class="detail-split-pane detail-split-pane--files" aria-label="Files changed">
            {#key `${selectedPR.provider}/${selectedPR.platformHost ?? ""}/${selectedPR.repoPath}/${selectedPR.number}`}
              <DiffFilesLayout
                owner={selectedPR.owner}
                name={selectedPR.name}
                number={selectedPR.number}
                provider={selectedPR.provider}
                platformHost={selectedPR.platformHost}
                repoPath={selectedPR.repoPath}
                diffHeadSHA={selectedDetail?.diff_head_sha}
                capabilities={selectedDetail?.repo?.capabilities ?? defaultProviderCapabilities}
                reviewThreads={reviewThreadsFromEvents(selectedDetail?.events)}
                initialScrollTop={filesScrollTop()}
                onScrollTopChange={rememberFilesScroll}
              />
            {/key}
          </section>
        </div>
      {:else if detailTab === "files"}
        {#key `${selectedPR.provider}/${selectedPR.platformHost ?? ""}/${selectedPR.repoPath}/${selectedPR.number}`}
          <DiffFilesLayout
            owner={selectedPR.owner}
            name={selectedPR.name}
            number={selectedPR.number}
            provider={selectedPR.provider}
            platformHost={selectedPR.platformHost}
            repoPath={selectedPR.repoPath}
            diffHeadSHA={selectedDetail?.diff_head_sha}
            capabilities={selectedDetail?.repo?.capabilities ?? defaultProviderCapabilities}
            reviewThreads={reviewThreadsFromEvents(selectedDetail?.events)}
            initialScrollTop={filesScrollTop()}
            onScrollTopChange={rememberFilesScroll}
          />
        {/key}
      {:else}
        <PullDetail
          owner={selectedPR.owner}
          name={selectedPR.name}
          number={selectedPR.number}
          provider={selectedPR.provider}
          platformHost={selectedPR.platformHost}
          repoPath={selectedPR.repoPath}
          autoSync={autoSyncDetail}
          {workflowApprovalSync}
          hideTabs={true}
          hideStaleWhileLoading={hideStaleDetailWhileLoading}
          onStackMemberNavigate={handleStackMemberNavigate}
        />
      {/if}
    </div>
  {:else}
    <div class="placeholder-content">
      <p class="placeholder-text">Select a PR</p>
      <p class="placeholder-hint">
        j/k to navigate &middot; 1/2 to switch views
      </p>
    </div>
  {/if}
</CollapsibleResizableSidebar>

<style>
  .detail-host {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .detail-tabs {
    display: flex;
    align-items: center;
    gap: 0;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    flex-shrink: 0;
  }

  .placeholder-content {
    text-align: center;
  }

  .placeholder-text {
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .placeholder-hint {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    margin-top: 8px;
    opacity: 0.7;
  }

  .detail-tab {
    font-size: var(--font-size-sm);
    font-weight: 500;
    padding: 8px 16px;
    color: var(--text-secondary);
    border-bottom: 2px solid transparent;
    transition: color 0.1s, border-color 0.1s;
  }

  .detail-tab:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .detail-tab--active {
    color: var(--text-primary);
    border-bottom-color: var(--accent-blue);
  }

  .detail-split-toggle {
    margin-left: auto;
    margin-right: 8px;
    min-height: 28px;
    padding: 4px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    background: var(--bg-primary);
    font-size: var(--font-size-xs);
    font-weight: 600;
    transition:
      color 0.1s,
      border-color 0.1s,
      background 0.1s;
  }

  .detail-split-toggle:hover {
    color: var(--text-primary);
    border-color: var(--border-strong, var(--border-default));
    background: var(--bg-surface-hover);
  }

  .detail-split-toggle--active {
    color: var(--accent-blue);
    border-color: var(--accent-blue);
    background: var(--accent-blue-soft, var(--bg-primary));
  }

  .detail-split-layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    flex: 1;
    min-height: 0;
    min-width: 0;
    overflow: hidden;
  }

  .detail-split-pane {
    display: flex;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .detail-split-pane--files {
    border-left: 1px solid var(--border-default);
  }

  .detail-split-pane :global(.pull-detail-content) {
    max-width: 800px;
  }
</style>
