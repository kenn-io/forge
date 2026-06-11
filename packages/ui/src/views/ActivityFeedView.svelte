<script lang="ts">
  import type { ActivityItem } from "../api/types.js";
  import ActivityFeed from "../components/ActivityFeed.svelte";
  import CommitDiffPanel from "../components/CommitDiffPanel.svelte";
  import LeftSidebarToggle from "../components/shared/LeftSidebarToggle.svelte";
  import SplitResizeHandle from "../components/shared/SplitResizeHandle.svelte";
  import type { SplitResizeEvent } from "../components/shared/split-resize.js";
  import type { PullRequestRouteRef } from "../routes.js";
  import PRListView from "./PRListView.svelte";
  import IssueListView from "./IssueListView.svelte";

  type ActivityDetailTab = "conversation" | "files";

  type DrawerPRItem = PullRequestRouteRef & {
    itemType: "pr";
    detailTab: ActivityDetailTab;
  };

  type DrawerItem = {
    itemType: "pr" | "issue";
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    owner: string;
    name: string;
    number: number;
    detailTab?: ActivityDetailTab;
  };

  type CommitDrawerItem = {
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    owner: string;
    name: string;
    branchName: string;
    commitSha: string;
    title: string;
  };

  interface Props {
    drawerItem?: DrawerItem | null;
    detailTab?: ActivityDetailTab;
    onSelectItem?: (item: ActivityItem) => void;
    onCloseDrawer?: () => void;
    onDetailTabChange?: (tab: ActivityDetailTab) => void;
    onDrawerItemChange?: (item: DrawerPRItem) => void;
    phone?: boolean;
  }

  let {
    drawerItem: controlledDrawer,
    detailTab = "conversation",
    onSelectItem,
    onCloseDrawer,
    onDetailTabChange,
    onDrawerItemChange,
    phone = false,
  }: Props = $props();

  const ACTIVITY_PANE_WIDTH_KEY = "middleman-activity-pane-width";
  const DEFAULT_ACTIVITY_PANE_WIDTH = 360;

  function loadActivityPaneWidth(): number {
    try {
      const raw = localStorage.getItem(ACTIVITY_PANE_WIDTH_KEY);
      if (raw) {
        const parsed = Number(raw);
        if (Number.isFinite(parsed) && parsed > 0) {
          return parsed;
        }
      }
    } catch {
      // Storage blocked (private mode / embedded host); use the default.
    }
    return DEFAULT_ACTIVITY_PANE_WIDTH;
  }

  function persistActivityPaneWidth(value: number): void {
    try {
      localStorage.setItem(ACTIVITY_PANE_WIDTH_KEY, String(Math.round(value)));
    } catch {
      // Storage blocked; the rail width just won't survive a reload.
    }
  }

  // Internal state used when no controlled props are
  // provided (standalone usage).
  let internalDrawer = $state<DrawerItem | null>(null);
  let commitDrawer = $state<CommitDrawerItem | null>(null);
  let internalDetailTab = $state<ActivityDetailTab>(
    "conversation",
  );
  // The width the user has dragged the rail to, restored from storage so it
  // survives reloads. The effective width below re-clamps it reactively so it
  // also survives viewport changes.
  let requestedActivityPaneWidth = $state(loadActivityPaneWidth());
  let activityPaneCollapsed = $state(false);
  // Measured width of the whole split shell so the rail's upper bound
  // scales with the viewport rather than a fixed pixel cap.
  let activityShellWidth = $state(0);

  const minActivityPaneWidth = 280;
  // Space always kept for the detail pane so the rail can never be
  // dragged wide enough to squeeze it to nothing.
  const minDetailPaneWidth = 360;
  let activityResizeStartWidth = 0;

  // No fixed ceiling: on a wide monitor the rail grows until only
  // minDetailPaneWidth remains for the detail pane. Before the shell is
  // measured the bound is open so the initial width is never clamped.
  const maxActivityPaneWidth = $derived(
    activityShellWidth > 0
      ? Math.max(minActivityPaneWidth, activityShellWidth - minDetailPaneWidth)
      : Number.POSITIVE_INFINITY,
  );

  function clampActivityPaneWidth(width: number): number {
    return Math.max(
      minActivityPaneWidth,
      Math.min(maxActivityPaneWidth, width),
    );
  }

  // Effective rail width: the requested width re-clamped to the current
  // maximum, so narrowing the viewport keeps the detail pane visible and
  // widening it restores the rail toward the requested width.
  const activityPaneWidth = $derived(
    clampActivityPaneWidth(requestedActivityPaneWidth),
  );

  const controlled = $derived(
    controlledDrawer !== undefined || onCloseDrawer !== undefined,
  );
  const activeDrawer = $derived(
    controlled ? (controlledDrawer ?? null) : internalDrawer,
  );
  const hasActiveDetail = $derived(
    activeDrawer !== null || commitDrawer !== null,
  );
  const effectiveDetailTab = $derived(
    controlled ? detailTab : internalDetailTab,
  );
  // Guarded snapshots for the drawer detail panes. As inline prop
  // object literals these would compile into deriveds that can
  // re-evaluate while the {#if} branch below is tearing down — after
  // activeDrawer has already flipped to null — and crash reading
  // activeDrawer.owner. Hoisting them behind the null check makes a
  // teardown-time re-read return null instead of throwing.
  const drawerPRSelection = $derived(
    activeDrawer?.itemType === "pr"
      ? {
          owner: activeDrawer.owner,
          name: activeDrawer.name,
          number: activeDrawer.number,
          provider: activeDrawer.provider,
          platformHost: activeDrawer.platformHost,
          repoPath: activeDrawer.repoPath,
        }
      : null,
  );
  const drawerIssueSelection = $derived(
    activeDrawer && activeDrawer.itemType !== "pr"
      ? {
          owner: activeDrawer.owner,
          name: activeDrawer.name,
          number: activeDrawer.number,
          provider: activeDrawer.provider,
          platformHost: activeDrawer.platformHost,
          repoPath: activeDrawer.repoPath,
        }
      : null,
  );

  function handleDetailTabChange(
    tab: ActivityDetailTab,
  ): void {
    if (controlled) {
      onDetailTabChange?.(tab);
      return;
    }
    internalDetailTab = tab;
  }

  function handleStackMemberNavigate(ref: PullRequestRouteRef): boolean {
    const nextDrawer: DrawerPRItem = {
      ...ref,
      itemType: "pr",
      detailTab: effectiveDetailTab,
    };
    if (!controlled) {
      internalDrawer = nextDrawer;
    }
    onDrawerItemChange?.(nextDrawer);
    return true;
  }

  function handleSelect(item: ActivityItem): void {
    commitDrawer = null;
    if (!item.repo) {
      throw new Error("activity item missing provider repo identity");
    }
    const itemType =
      item.item_type === "issue" ? "issue" : "pr";
    const entry: DrawerItem = {
      itemType,
      provider: item.repo.provider,
      platformHost: item.repo.platform_host,
      repoPath: item.repo.repo_path,
      owner: item.repo.owner,
      name: item.repo.name,
      number: item.item_number,
      detailTab: "conversation",
    };
    if (!controlled) {
      internalDrawer = entry;
      internalDetailTab = "conversation";
    }
    onSelectItem?.(item);
  }

  function handleSelectBranchCommit(item: ActivityItem): void {
    if (!item.repo) {
      throw new Error("branch activity item missing provider repo identity");
    }
    if (!item.commit_sha) return;

    commitDrawer = {
      provider: item.repo.provider,
      platformHost: item.repo.platform_host,
      repoPath: item.repo.repo_path,
      owner: item.repo.owner,
      name: item.repo.name,
      branchName: item.branch_name || "default branch",
      commitSha: item.commit_sha,
      title: item.body_preview || item.commit_sha.slice(0, 12),
    };
    if (!controlled) {
      internalDrawer = null;
    } else if (activeDrawer !== null) {
      onCloseDrawer?.();
    }
  }

  function handleClose(): void {
    activityPaneCollapsed = false;
    commitDrawer = null;
    if (!controlled) {
      internalDrawer = null;
    }
    onCloseDrawer?.();
  }

  function handleActivityPaneResizeStart(): void {
    activityResizeStartWidth = activityPaneWidth;
  }

  function handleActivityPaneResize(
    event: SplitResizeEvent,
  ): void {
    requestedActivityPaneWidth = clampActivityPaneWidth(
      activityResizeStartWidth + event.deltaX,
    );
    persistActivityPaneWidth(requestedActivityPaneWidth);
  }

  function collapseActivityPane(): void {
    activityPaneCollapsed = true;
  }

  function expandActivityPane(): void {
    activityPaneCollapsed = false;
  }

  // Escape closes the active drawer when one is open. Mirrors the
  // behavior of the previous DetailDrawer the split view replaced.
  $effect(() => {
    if (!hasActiveDetail) return;
    function onKey(event: KeyboardEvent): void {
      if (event.key !== "Escape") return;
      if (event.defaultPrevented) return;
      const target = event.target as HTMLElement | null;
      const tag = target?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA") return;
      if (target?.isContentEditable) return;
      handleClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
</script>

<div
  class="activity-shell"
  class:activity-shell--split={hasActiveDetail}
  class:activity-shell--full={!hasActiveDetail}
  class:activity-shell--phone={phone}
  bind:clientWidth={activityShellWidth}
>
  <section
    class="activity-pane"
    class:activity-pane--collapsed={hasActiveDetail && activityPaneCollapsed}
    style:--activity-pane-width={`${activityPaneWidth}px`}
  >
    {#if hasActiveDetail && activityPaneCollapsed}
      <div class="activity-collapsed-strip">
        <LeftSidebarToggle
          state="collapsed"
          label="Activity sidebar"
          onclick={expandActivityPane}
          class="left-sidebar-toggle--compact"
        />
      </div>
    {:else if hasActiveDetail}
      <div class="activity-rail-header">
        <span>Activity</span>
        <LeftSidebarToggle
          state="expanded"
          label="Activity sidebar"
          onclick={collapseActivityPane}
          class="left-sidebar-toggle--compact"
        />
      </div>
    {/if}
    <div class="activity-feed-wrap">
      <ActivityFeed
        compact={phone || hasActiveDetail}
        selectedItem={activeDrawer}
        selectedBranchCommit={commitDrawer}
        onSelectItem={handleSelect}
        onSelectBranchCommit={handleSelectBranchCommit}
      />
    </div>
  </section>

  {#if hasActiveDetail && !activityPaneCollapsed}
    <SplitResizeHandle
      class="activity-split-resize-handle"
      ariaLabel="Resize Activity rail"
      onResizeStart={handleActivityPaneResizeStart}
      onResize={handleActivityPaneResize}
    />
  {/if}

  {#if activeDrawer || commitDrawer}
    <section class="activity-detail">
      <div class="activity-detail-header">
        <span>
          {#if commitDrawer}
            Commit {commitDrawer.repoPath} {commitDrawer.branchName} {commitDrawer.title}
          {:else if activeDrawer}
            {activeDrawer.owner}/{activeDrawer.name}#{activeDrawer.number}
          {/if}
        </span>
        <button
          class="activity-rail-close"
          onclick={handleClose}
          title="Close Activity selection"
          type="button"
        >
          &times;
        </button>
      </div>

      {#if commitDrawer}
        {#key commitDrawer.commitSha}
          <CommitDiffPanel
            provider={commitDrawer.provider}
            platformHost={commitDrawer.platformHost}
            owner={commitDrawer.owner}
            name={commitDrawer.name}
            repoPath={commitDrawer.repoPath}
            commitSha={commitDrawer.commitSha}
          />
        {/key}
      {:else if drawerPRSelection}
        <PRListView
          selectedPR={drawerPRSelection}
          detailTab={effectiveDetailTab}
          isSidebarCollapsed={true}
          hideSidebar={true}
          autoSyncDetail="background"
          hideStaleDetailWhileLoading={true}
          workflowApprovalSync={false}
          onDetailTabChange={handleDetailTabChange}
          onStackMemberNavigate={handleStackMemberNavigate}
        />
      {:else if drawerIssueSelection}
        <IssueListView
          selectedIssue={drawerIssueSelection}
          isSidebarCollapsed={true}
          hideSidebar={true}
          autoSyncDetail="background"
          hideStaleDetailWhileLoading={true}
        />
      {/if}
    </section>
  {/if}
</div>

<style>
  .activity-shell {
    flex: 1;
    overflow: hidden;
    display: flex;
    min-height: 0;
    container-type: inline-size;
  }

  .activity-pane {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .activity-shell--split .activity-pane {
    width: var(--activity-pane-width, 360px);
    flex: 0 0 var(--activity-pane-width, 360px);
    border-right: 1px solid var(--border-default);
  }

  .activity-shell--split .activity-pane--collapsed {
    width: 28px;
    flex-basis: 28px;
  }

  .activity-feed-wrap {
    min-height: 0;
    flex: 1;
    display: flex;
    flex-direction: column;
  }

  .activity-shell--split .activity-pane--collapsed .activity-feed-wrap {
    display: none;
  }

  .activity-rail-header,
  .activity-detail-header {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 34px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .activity-collapsed-strip {
    width: 28px;
    flex: 1;
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding-top: 6px;
    background: var(--bg-surface);
  }

  .activity-detail-header span {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .activity-rail-close {
    width: 22px;
    height: 22px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    background: var(--bg-inset);
  }

  .activity-rail-close:hover {
    color: var(--text-primary);
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
  }

  .activity-detail {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  @container (max-width: 760px) {
    .activity-shell--split .activity-pane {
      display: none;
    }

    .activity-shell--split :global(.activity-split-resize-handle) {
      display: none;
    }
  }

  .activity-shell--phone .activity-pane {
    width: 100%;
  }

  .activity-shell--phone .activity-feed-wrap {
    min-width: 0;
  }
</style>
