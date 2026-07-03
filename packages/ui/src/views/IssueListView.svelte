<script lang="ts">
  import { getSidebar } from "../context.js";
  import { CollapsibleSidebar } from "@kenn-io/kit-ui";
  import IssueList
    from "../components/sidebar/IssueList.svelte";
  import IssueDetail
    from "../components/detail/IssueDetail.svelte";
  import type { IssueDetailSyncMode } from "../stores/issues.svelte.js";
  import type { IssueRouteRef } from "../routes.js";

  const { isSidebarToggleEnabled, toggleSidebar } = getSidebar();

  interface Props {
    selectedIssue?: IssueRouteRef | null;
    isSidebarCollapsed?: boolean;
    hideSidebar?: boolean;
    sidebarWidth?: number;
    /** Float the expanded sidebar over the list (narrow-container hosts). */
    sidebarOverlay?: boolean;
    autoSyncDetail?: IssueDetailSyncMode;
    hideStaleDetailWhileLoading?: boolean;
    onSidebarResize?: (width: number) => void;
  }

  let {
    selectedIssue = null,
    isSidebarCollapsed = false,
    hideSidebar = false,
    sidebarWidth = 340,
    sidebarOverlay = false,
    autoSyncDetail = "background",
    hideStaleDetailWhileLoading = false,
    onSidebarResize,
  }: Props = $props();
</script>

<CollapsibleSidebar
  isCollapsed={isSidebarCollapsed}
  {hideSidebar}
  {sidebarWidth}
  {onSidebarResize}
  overlay={sidebarOverlay}
  showCollapsedStrip={isSidebarToggleEnabled()}
  onExpand={toggleSidebar}
  mainEmpty={selectedIssue === null}
>
  {#snippet sidebar()}
    <IssueList {sidebarWidth} />
  {/snippet}

  {#if selectedIssue !== null}
    <IssueDetail
      owner={selectedIssue.owner}
      name={selectedIssue.name}
      number={selectedIssue.number}
      provider={selectedIssue.provider}
      platformHost={selectedIssue.platformHost}
      repoPath={selectedIssue.repoPath}
      autoSync={autoSyncDetail}
      hideStaleWhileLoading={hideStaleDetailWhileLoading}
    />
  {:else}
    <div class="placeholder-content">
      <p class="placeholder-text">Select an issue</p>
      <p class="placeholder-hint">j/k to navigate</p>
    </div>
  {/if}
</CollapsibleSidebar>

<style>
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
</style>
