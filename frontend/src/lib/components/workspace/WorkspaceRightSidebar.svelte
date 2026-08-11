<script lang="ts">
  import { EmptyState, Spinner } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import { getStores } from "../../context.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    createRoborevClient,
    executeRoborevRequest,
  } from "../../api/roborev/client.js";
  import {
    makeRoborevOwner,
    RoborevResponseError,
    RoborevWorkflow,
  } from "../../stores/roborev/roborev-workflow.js";
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
  import KataLinksPanel
    from "../kata/KataLinksPanel.svelte";

  type RepoWithCount =
    components["schemas"]["RepoWithCount"];

  interface Props {
    activeTab: "diff" | "pr" | "issue" | "reviews" | "kata";
    workspaceID: string;
    workspaceHostKey?: string | undefined;
    provider: string;
    platformHost?: string | undefined;
    repoOwner: string;
    repoName: string;
    repoPath: string;
    ownerItemType: "pull_request" | "issue" | "kata_task" | "adhoc";
    ownerItemNumber: number;
    associatedPRNumber: number | null;
    branch: string;
    roborevBaseUrl: string;
    refreshToken?: number;
    diffRefreshToken?: number;
    disabled?: boolean;
  }

  let {
    activeTab,
    workspaceID,
    workspaceHostKey = undefined,
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
    diffRefreshToken = 0,
    disabled = false,
  }: Props = $props();

  const parentStores = getStores();
  const appRuntime = getAppRuntime();

  // svelte-ignore state_referenced_locally — intentional snapshot; stores are created once
  const baseUrl = roborevBaseUrl;

  // Sidebar-local roborev stores
  const roborevClient = createRoborevClient(
    baseUrl,
  );
  // svelte-ignore state_referenced_locally — the sidebar stores are scoped to this mounted workspace
  const roborevOwner = makeRoborevOwner(`workspace-sidebar:${workspaceID}`);
  // Repository resolution is an independent catalog consumer. Reusing the
  // sidebar store owner would let a picker or resolver cancel unrelated work.
  // svelte-ignore state_referenced_locally — the resolver owner is scoped to this mounted sidebar
  const repoResolutionOwner = makeRoborevOwner(`workspace-repository:${workspaceID}`);
  const sidebarJobs = createJobsStore({
    client: roborevClient,
    runtime: appRuntime,
    owner: roborevOwner,
    navigate: () => {},
  });
  const sidebarReview = createReviewStore({
    client: roborevClient,
    runtime: appRuntime,
    owner: roborevOwner,
  });
  const sidebarLog = createLogStore({
    runtime: appRuntime,
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

  const resolveRepoEffect = () => {
    const requestedName = repoName;
    const requestedOwner = repoOwner;
    const requestedKey = repoKey();
    if (!requestedName) {
      return Effect.sync(() => {
        resolvedRootPath = null;
        repoResolutionError = null;
        negativeMatch = false;
        lastResolvedKey = "";
        return null;
      });
    }
    return Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      return yield* workflow.catalog(
        repoResolutionOwner,
        executeRoborevRequest("resolve workspace Roborev repository", (signal) =>
          roborevClient.GET("/api/repos", { signal }),
        ).pipe(
          Effect.flatMap((result) =>
            result.error
              ? Effect.fail(
                  RoborevResponseError.make({
                    operation: "resolve workspace Roborev repository",
                    message: "Failed to resolve repository",
                    cause: result.error,
                  }),
                )
              : Effect.succeed(result.data?.repos ?? []),
          ),
          Effect.map((repos: RepoWithCount[]) => {
            const matches = repos.filter(
              (repo) => repo.name.toLowerCase() === requestedName.toLowerCase(),
            );
            if (matches.length === 1) return matches[0]?.root_path ?? null;
            if (matches.length === 0) return null;
            const ownerMatches = matches.filter((repo) =>
              repo.root_path
                .split("/")
                .some((segment) => segment.toLowerCase() === requestedOwner.toLowerCase()),
            );
            return ownerMatches.length === 1 ? ownerMatches[0]?.root_path ?? null : "ambiguous";
          }),
          Effect.tap((resolution) =>
            Effect.sync(() => {
              resolvedRootPath = resolution === "ambiguous" ? null : resolution;
              repoResolutionError = resolution === "ambiguous"
                ? "Multiple repos matched \u2014 select one on the Reviews page"
                : null;
              negativeMatch = resolution === null;
              if (typeof resolution === "string" && resolution !== "ambiguous") lastResolvedKey = requestedKey;
            }),
          ),
          Effect.map((resolution) => (resolution === "ambiguous" ? null : resolution)),
          Effect.catch(() =>
            Effect.sync(() => {
              resolvedRootPath = null;
              repoResolutionError = "Failed to resolve repository";
              negativeMatch = false;
              return null;
            }),
          ),
        ),
      );
    });
  };

  const resolveAndLoadEffect = () =>
    resolveRepoEffect().pipe(
      Effect.tap((rootPath) =>
        rootPath === null
          ? Effect.void
          : Effect.sync(() => {
              sidebarJobs.setRepoBranchFilter(rootPath, branch);
            }),
      ),
      Effect.asVoid,
    );

  function retryResolve(): void {
    appRuntime.runCommand(resolveAndLoadEffect(), {
      operation: "resolve workspace Roborev repository",
      safeContext: { owner: repoResolutionOwner },
      onFailure: () => {},
    });
  }

  // Resolve repo on workspace change
  $effect(() => {
    const key = repoKey();
    if (key === lastResolvedKey && !negativeMatch) return;
    const execution = appRuntime.runCommand(resolveAndLoadEffect(), {
      operation: "resolve workspace Roborev repository",
      safeContext: { owner: repoResolutionOwner },
      onFailure: () => {},
    });
    return execution.interrupt;
  });

  // Update branch filter when branch changes within
  // the same resolved repo
  // svelte-ignore state_referenced_locally
  let lastBranch = $state(branch);
  $effect(() => {
    if (branch !== lastBranch && resolvedRootPath) {
      lastBranch = branch;
      sidebarJobs.setFilter("branch", branch);
      sidebarJobs.loadJobs();
    }
  });

  // Sync selectedJobId → review store (mirrors
  // ReviewsView effect #2)
  $effect(() => {
    const id = sidebarJobs.getSelectedJobId();
    sidebarReview.setSelectedJobId(id);
    if (id !== undefined) sidebarReview.loadReview(id);
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
      retryResolve();
    }
  });

  // Re-resolve on tab activation when negative
  $effect(() => {
    if (activeTab === "reviews" && negativeMatch) {
      retryResolve();
    }
  });

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
  const hasMergeTarget = $derived(
    ownerItemType === "pull_request"
      ? ownerItemNumber > 0 && hasRepo
      : hasPR
  );

  // Connect/disconnect the NDJSON event stream based on daemon availability.
  $effect(() => {
    const available =
      parentStores.roborevDaemon?.isAvailable() ??
      false;
    if (!available) {
      return;
    }
    const eventOwner = sidebarJobs.connectEventStream(baseUrl);
    return () => sidebarJobs.disconnectEventStream(eventOwner);
  });

  onDestroy(() => {
    sidebarJobs.dispose();
    appRuntime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* RoborevWorkflow;
        yield* workflow.stop(roborevOwner);
        yield* workflow.stopCatalog(repoResolutionOwner);
      }),
      {
        operation: "stop workspace Roborev stores",
        safeContext: { owner: roborevOwner },
        onFailure: () => {},
      },
    );
  });
</script>

<div class="right-sidebar-content">
  {#if activeTab === "diff"}
    {#key `diff:${workspaceHostKey ?? "self"}:${workspaceID}`}
      <WorkspaceDiffPanel
        {workspaceID}
        {workspaceHostKey}
        {provider}
        {platformHost}
        {repoOwner}
        {repoName}
        {repoPath}
        itemNumber={ownerItemNumber}
        active={activeTab === "diff"}
        {refreshToken}
        {diffRefreshToken}
        {disabled}
        showMergeTarget={hasMergeTarget}
      />
    {/key}
  {:else if activeTab === "pr"}
    {#if hasPR}
      {#key `pr:${provider}:${platformHost ?? ""}:${repoPath}:${associatedPRNumber ?? 0}:${refreshToken}`}
        <div class="pr-scroll" inert={disabled}>
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
      <EmptyState title="No linked PR" />
    {/if}
  {:else if activeTab === "issue"}
    {#if hasIssue}
      {#key `issue:${provider}:${platformHost ?? ""}:${repoPath}:${ownerItemNumber}:${refreshToken}`}
        <div class="pr-scroll" inert={disabled}>
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
      <EmptyState title="No linked issue" />
    {/if}
  {:else if activeTab === "kata"}
    {#if workspaceHostKey}
      <EmptyState title="Kata links are unavailable for remote workspaces" />
    {:else}
      <KataLinksPanel
        subject={{ kind: "workspace", workspaceID }}
        active={activeTab === "kata"}
        {disabled}
      />
    {/if}
  {:else if activeTab === "reviews"}
    {#if !hasRepo}
      <EmptyState title="No reviews for this worktree" />
    {:else if repoResolutionError}
      <EmptyState title={repoResolutionError} />
    {:else if resolvedRootPath === null && !negativeMatch}
      <div class="loading-placeholder">
        <Spinner size={14} label="Resolving repo" />
        Resolving repo...
      </div>
    {:else if negativeMatch}
      <EmptyState title="No reviews for this worktree" />
    {:else}
      <SidebarStoreScope stores={sidebarStores}>
        <div class="sidebar-reviews" inert={disabled}>
          <div class="sidebar-reviews-header">
            <FilterBar disabled={disabled || !parentStores.roborevDaemon?.isAvailable()} />
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
  .loading-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: var(--space-8) var(--space-6);
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .right-sidebar-content {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    background: var(--bg-surface);
  }

  .pr-scroll {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
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
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
</style>
