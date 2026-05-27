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
    detailTab?: "conversation" | "files";
    isSidebarCollapsed?: boolean;
    hideSidebar?: boolean;
    sidebarWidth?: number;
    autoSyncDetail?: DetailSyncMode;
    hideStaleDetailWhileLoading?: boolean;
    workflowApprovalSync?: boolean;
    routeFamily?: "canonical" | "focus";
    onSidebarResize?: (width: number) => void;
    onDetailTabChange?: (tab: "conversation" | "files") => void;
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

  function selectDetailTab(tab: "conversation" | "files"): void {
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
    if (selectedPR === null || detailTab !== "files") return;
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
    </div>
    {#if detailTab === "files"}
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
  .detail-tabs {
    display: flex;
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
</style>
