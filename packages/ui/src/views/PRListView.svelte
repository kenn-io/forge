<script lang="ts">
  import {
    getNavigate, getSidebar,
  } from "../context.js";
  import CollapsibleResizableSidebar from "../components/shared/CollapsibleResizableSidebar.svelte";
  import PullList from "../components/sidebar/PullList.svelte";
  import PullDetail
    from "../components/detail/PullDetail.svelte";
  import DiffFilesLayout from "../components/diff/DiffFilesLayout.svelte";
  import type { DetailSyncMode } from "../stores/detail.svelte.js";
  import {
    buildFocusPullRequestRoute,
    buildPullRequestFilesRoute,
    buildPullRequestRoute,
    type PullRequestRouteRef,
  } from "../routes.js";

  type StackMemberNavigate = (ref: PullRequestRouteRef) => boolean | void;

  const { isSidebarToggleEnabled, toggleSidebar } = getSidebar();
  const navigate = getNavigate();

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
