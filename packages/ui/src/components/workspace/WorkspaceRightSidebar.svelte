<script lang="ts">
  import { onDestroy } from "svelte";
  import { getStores } from "../../context.js";
  import {
    createRoborevClient,
  } from "../../api/roborev/client.js";
  import {
    createJobsStore,
  } from "../../stores/roborev/jobs.svelte.js";
  import {
    createReviewStore,
  } from "../../stores/roborev/review.svelte.js";
  import {
    createLogStore,
  } from "../../stores/roborev/log.svelte.js";
  import type { StoreInstances } from "../../types.js";
  import type {
    components,
  } from "../../api/roborev/generated/schema.js";
  import SidebarStoreScope
    from "./SidebarStoreScope.svelte";
  import PullDetail
    from "../detail/PullDetail.svelte";
  import IssueDetail
    from "../detail/IssueDetail.svelte";
  import FilterBar
    from "../roborev/FilterBar.svelte";
  import DaemonStatus
    from "../roborev/DaemonStatus.svelte";
  import JobTable
    from "../roborev/JobTable.svelte";
  import ReviewDrawer
    from "../roborev/ReviewDrawer.svelte";
  import WorkspaceDiffPanel
    from "./WorkspaceDiffPanel.svelte";

  type RepoWithCount =
    components["schemas"]["RepoWithCount"];

  interface Props {
    activeTab: "diff" | "pr" | "issue" | "reviews";
    workspaceID: string;
    provider: string;
    platformHost?: string | undefined;
    repoOwner: string;
    repoName: string;
    repoPath: string;
    ownerItemType: "pull_request" | "issue";
    ownerItemNumber: number;
    associatedPRNumber: number | null;
    branch: string;
    roborevBaseUrl: string;
    refreshToken?: number;
  }

  let {
    activeTab,
    workspaceID,
    provider,
    platformHost,
    repoOwner,
    repoName,
    repoPath,
    ownerItemType,
    ownerItemNumber,
    associatedPRNumber,
    branch,
    roborevBaseUrl,
    refreshToken = 0,
  }: Props = $props();

  const parentStores = getStores();

  // svelte-ignore state_referenced_locally — intentional snapshot; stores are created once
  const baseUrl = roborevBaseUrl;

  // Sidebar-local roborev stores
  const roborevClient = createRoborevClient(
    baseUrl,
  );
  const sidebarJobs = createJobsStore({
    client: roborevClient,
    navigate: () => {},
  });
  const sidebarReview = createReviewStore({
    client: roborevClient,
  });
  const sidebarLog = createLogStore({
    client: roborevClient,
    baseUrl,
  });

  // Overlay sidebar stores onto parent stores
  const sidebarStores: StoreInstances = {
    ...parentStores,
    roborevJobs: sidebarJobs,
    roborevReview: sidebarReview,
    roborevLog: sidebarLog,
  };

  // Repo resolution state
  let resolvedRootPath = $state<string | null>(null);
  let repoResolutionError = $state<string | null>(
    null,
  );
  let lastResolvedKey = $state("");
  let negativeMatch = $state(false);

  function repoKey(): string {
    return `${repoOwner}/${repoName}`;
  }

  async function resolveRepo(): Promise<void> {
    if (!repoName) {
      resolvedRootPath = null;
      repoResolutionError = null;
      negativeMatch = false;
      lastResolvedKey = "";
      return;
    }
    const { data } = await roborevClient.GET(
      "/api/repos",
    );
    const repos: RepoWithCount[] =
      data?.repos ?? [];
    const matches = repos.filter(
      (r) =>
        r.name.toLowerCase() ===
        repoName.toLowerCase(),
    );
    if (matches.length === 0) {
      resolvedRootPath = null;
      repoResolutionError = null;
      negativeMatch = true;
      return;
    }
    if (matches.length === 1) {
      resolvedRootPath = matches[0]!.root_path;
      repoResolutionError = null;
      negativeMatch = false;
      lastResolvedKey = repoKey();
      return;
    }
    // Multiple matches — disambiguate by owner
    const ownerMatch = matches.filter((r) =>
      r.root_path
        .split("/")
        .some(
          (seg) =>
            seg.toLowerCase() ===
            repoOwner.toLowerCase(),
        ),
    );
    if (ownerMatch.length === 1) {
      resolvedRootPath = ownerMatch[0]!.root_path;
      repoResolutionError = null;
      negativeMatch = false;
      lastResolvedKey = repoKey();
      return;
    }
    resolvedRootPath = null;
    repoResolutionError =
      "Multiple repos matched \u2014 " +
      "select one on the Reviews page";
    negativeMatch = false;
  }

  // Resolve repo on workspace change
  $effect(() => {
    const key = repoKey();
    if (key !== lastResolvedKey || negativeMatch) {
      void resolveRepo().then(() => {
        if (resolvedRootPath) {
          sidebarJobs.setFilter(
            "repo",
            resolvedRootPath,
          );
          sidebarJobs.setFilter("branch", branch);
          void sidebarJobs.loadJobs();
        }
      });
    }
  });

  // Update branch filter when branch changes within
  // the same resolved repo
  // svelte-ignore state_referenced_locally
  let lastBranch = $state(branch);
  $effect(() => {
    if (branch !== lastBranch && resolvedRootPath) {
      lastBranch = branch;
      sidebarJobs.setFilter("branch", branch);
      void sidebarJobs.loadJobs();
    }
  });

  // Sync selectedJobId → review store (mirrors
  // ReviewsView effect #2)
  $effect(() => {
    const id = sidebarJobs.getSelectedJobId();
    sidebarReview.setSelectedJobId(id);
  });

  // Reset drawer tab when job selected (mirrors
  // ReviewsView effect #3)
  let drawerTab = $state<
    "review" | "log" | "prompt"
  >("review");
  $effect(() => {
    const id = sidebarJobs.getSelectedJobId();
    if (id !== undefined) {
      drawerTab = "review";
    }
  });

  // Re-resolve repo on daemon recovery
  $effect(() => {
    const available =
      parentStores.roborevDaemon?.isAvailable() ??
      false;
    if (available && negativeMatch) {
      void retryResolve();
    }
  });

  // Re-resolve on tab activation when negative
  $effect(() => {
    if (activeTab === "reviews" && negativeMatch) {
      void retryResolve();
    }
  });

  async function retryResolve(): Promise<void> {
    await resolveRepo();
    if (resolvedRootPath) {
      sidebarJobs.setFilter(
        "repo",
        resolvedRootPath,
      );
      sidebarJobs.setFilter("branch", branch);
      void sidebarJobs.loadJobs();
    }
  }

  // Determine if we have valid context
  const hasRepo = $derived(
    repoOwner !== "" && repoName !== "",
  );
  const hasPR = $derived(
    associatedPRNumber !== null &&
    associatedPRNumber > 0 &&
    hasRepo
  );
  const hasIssue = $derived(
    ownerItemType === "issue" &&
    ownerItemNumber > 0 &&
    hasRepo
  );

  // Connect/disconnect SSE based on daemon availability
  let sseConnected = $state(false);
  $effect(() => {
    const available =
      parentStores.roborevDaemon?.isAvailable() ??
      false;
    if (available && !sseConnected) {
      sidebarJobs.connectSSE(baseUrl);
      sseConnected = true;
    } else if (!available && sseConnected) {
      sidebarJobs.disconnectSSE();
      sseConnected = false;
    }
  });

  onDestroy(() => {
    sidebarJobs.disconnectSSE();
    sidebarLog.stopStreaming();
  });
</script>

<div class="right-sidebar-content">
  {#if activeTab === "diff"}
    {#key `diff:${workspaceID}`}
      <WorkspaceDiffPanel
        {workspaceID}
        {provider}
        {platformHost}
        {repoOwner}
        {repoName}
        {repoPath}
        itemNumber={ownerItemNumber}
        active={activeTab === "diff"}
        {refreshToken}
      />
    {/key}
  {:else if activeTab === "pr"}
    {#if hasPR}
      {#key `pr:${provider}:${platformHost ?? ""}:${repoPath}:${associatedPRNumber ?? 0}:${refreshToken}`}
        <div class="pr-scroll">
          <PullDetail
            {provider}
            {platformHost}
            owner={repoOwner}
            name={repoName}
            {repoPath}
            number={associatedPRNumber ?? 0}
            hideTabs={true}
            hideWorkspaceAction={true}
          />
        </div>
      {/key}
    {:else}
      <div class="empty-state">No linked PR</div>
    {/if}
  {:else if activeTab === "issue"}
    {#if hasIssue}
      {#key `issue:${provider}:${platformHost ?? ""}:${repoPath}:${ownerItemNumber}:${refreshToken}`}
        <div class="pr-scroll">
          <IssueDetail
            {provider}
            {platformHost}
            owner={repoOwner}
            name={repoName}
            {repoPath}
            number={ownerItemNumber}
          />
        </div>
      {/key}
    {:else}
      <div class="empty-state">No linked issue</div>
    {/if}
  {:else if activeTab === "reviews"}
    {#if !hasRepo}
      <div class="empty-state">
        No reviews for this worktree
      </div>
    {:else if repoResolutionError}
      <div class="empty-state">
        {repoResolutionError}
      </div>
    {:else if resolvedRootPath === null && !negativeMatch}
      <div class="empty-state">
        Resolving repo...
      </div>
    {:else if negativeMatch}
      <div class="empty-state">
        No reviews for this worktree
      </div>
    {:else}
      <SidebarStoreScope stores={sidebarStores}>
        <div class="sidebar-reviews">
          <div class="sidebar-reviews-header">
            <FilterBar disabled={!parentStores.roborevDaemon?.isAvailable()} />
            <DaemonStatus />
          </div>
          <div class="sidebar-reviews-body">
            <div class="sidebar-reviews-table">
              <JobTable />
            </div>
            <ReviewDrawer activeTab={drawerTab} />
          </div>
        </div>
      </SidebarStoreScope>
    {/if}
  {/if}
</div>

<style>
  .right-sidebar-content {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    background: var(--bg-surface);
  }

  .pr-scroll {
    flex: 1;
    overflow-y: auto;
  }

  .empty-state {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 1;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    padding: 24px;
    text-align: center;
  }

  .sidebar-reviews {
    display: flex;
    flex-direction: column;
    flex: 1;
    overflow: hidden;
  }

  .sidebar-reviews-header {
    flex-shrink: 0;
  }

  .sidebar-reviews-body {
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .sidebar-reviews-table {
    flex: 1;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }
</style>
