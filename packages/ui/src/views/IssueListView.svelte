<script lang="ts">
  import { getSidebar, getStores } from "../context.js";
  import { CollapsibleSidebar } from "@kenn-io/kit-ui";
  import IssueList
    from "../components/sidebar/IssueList.svelte";
  import IssueDetail
    from "../components/detail/IssueDetail.svelte";
  import WorkspaceDockPanel from "../components/workspace/WorkspaceDockPanel.svelte";
  import type { IssueDetail as IssueDetailResponse } from "../api/types.js";
  import type { IssueDetailSyncMode } from "../stores/issues.svelte.js";
  import type { IssueRouteRef } from "../routes.js";
  import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
  import { identityEquals, type InlineWorkspaceController, type WorkspaceItemIdentity } from "../workspace-inline.js";

  const { isSidebarToggleEnabled, toggleSidebar } = getSidebar();
  const { issues } = getStores();

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
    inlineWorkspace?: InlineWorkspaceController | null;
    /** ActivityFeedView embeds this view and owns a single outer dock; it
     * passes false so the embedded view never renders its own. */
    renderWorkspaceDock?: boolean;
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
    inlineWorkspace = null,
    renderWorkspaceDock = true,
  }: Props = $props();

  function detailMatchesSelected(
    detail: IssueDetailResponse | null,
    ref: IssueRouteRef | null,
  ): boolean {
    return (
      !!detail &&
      !!ref &&
      detail.repo_owner === ref.owner &&
      detail.repo_name === ref.name &&
      detail.issue.Number === ref.number &&
      canonicalProvider(detail.repo?.provider ?? "") === canonicalProvider(ref.provider) &&
      resolvedPlatformHost(ref.provider, detail.repo?.platform_host) ===
        resolvedPlatformHost(ref.provider, ref.platformHost) &&
      detail.repo?.repo_path === ref.repoPath
    );
  }

  function refreshSelectedDetail(): Promise<void> | undefined {
    if (selectedIssue === null) return undefined;
    const ref = selectedIssue;
    return issues.loadIssueDetail(ref.owner, ref.name, ref.number, {
      sync: false,
      provider: ref.provider,
      platformHost: ref.platformHost,
      repoPath: ref.repoPath,
    });
  }

  const claimIdentity = $derived<WorkspaceItemIdentity | null>(
    selectedIssue
      ? {
          provider: selectedIssue.provider,
          platformHost: selectedIssue.platformHost,
          owner: selectedIssue.owner,
          name: selectedIssue.name,
          repoPath: selectedIssue.repoPath,
          number: selectedIssue.number,
          itemType: "issue",
        }
      : null,
  );

  $effect(() => {
    const controller = inlineWorkspace;
    if (!controller) return;
    const detail = issues.getIssueDetail();
    if (!claimIdentity || !detailMatchesSelected(detail, selectedIssue ?? null)) {
      controller.release();
      return;
    }
    const ref = controller.effectiveWorkspaceRef(claimIdentity, detail?.workspace ?? null);
    if (ref) controller.claim(claimIdentity, ref);
    else controller.release();
  });

  $effect(() => {
    const controller = inlineWorkspace;
    if (!controller) return;
    return () => controller.release();
  });

  $effect(() => {
    const controller = inlineWorkspace;
    if (!controller) return;
    return controller.onIdentityInvalidated((identity) => {
      if (claimIdentity && identityEquals(identity, claimIdentity)) {
        void refreshSelectedDetail();
      }
    });
  });
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
    {#snippet detailContent()}
      <IssueDetail
        owner={selectedIssue.owner}
        name={selectedIssue.name}
        number={selectedIssue.number}
        provider={selectedIssue.provider}
        platformHost={selectedIssue.platformHost}
        repoPath={selectedIssue.repoPath}
        autoSync={autoSyncDetail}
        hideStaleWhileLoading={hideStaleDetailWhileLoading}
        {inlineWorkspace}
      />
    {/snippet}

    <div class="detail-host">
      {#if inlineWorkspace && renderWorkspaceDock}
        <WorkspaceDockPanel
          controller={inlineWorkspace}
          active={claimIdentity !== null && inlineWorkspace.isClaimedFor(claimIdentity)}
        >
          {@render detailContent()}
        </WorkspaceDockPanel>
      {:else}
        {@render detailContent()}
      {/if}
    </div>
  {:else}
    <div class="placeholder-content">
      <p class="placeholder-text">Select an issue</p>
      <p class="placeholder-hint">j/k to navigate</p>
    </div>
  {/if}
</CollapsibleSidebar>

<style>
  .detail-host {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
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
</style>
