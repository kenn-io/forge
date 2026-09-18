<script lang="ts">
  import { Button, EmptyState, Spinner } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { getStores } from "../../context.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { createRoborevClient, executeRoborevRequest } from "../../api/roborev/client.js";
  import * as roborevAPI from "../../api/roborev/generated/client.js";
  import { createDaemonStore } from "../../stores/roborev/daemon.svelte.js";
  import { createJobsStore } from "../../stores/roborev/jobs.svelte.js";
  import { createReviewStore } from "../../stores/roborev/review.svelte.js";
  import { createLogStore } from "../../stores/roborev/log.svelte.js";
  import { makeRoborevOwner, RoborevResponseError, RoborevWorkflow } from "../../stores/roborev/roborev-workflow.js";
  import SidebarStoreScope from "./SidebarStoreScope.svelte";
  import FilterBar from "../roborev/FilterBar.svelte";
  import DaemonStatus from "../roborev/DaemonStatus.svelte";
  import JobTable from "../roborev/JobTable.svelte";
  import ReviewDrawer from "../roborev/ReviewDrawer.svelte";

  let { workspaceID, repoOwner, repoName, branch, roborevBaseUrl, disabled = false }: {
    workspaceID: string;
    repoOwner: string;
    repoName: string;
    branch: string;
    roborevBaseUrl: string;
    disabled?: boolean;
  } = $props();

  const runtime = getAppRuntime();
  // The parent keys this panel by workspace, repository, and branch.
  const client = createRoborevClient(untrack(() => roborevBaseUrl));
  const owner = makeRoborevOwner(untrack(() => `workspace-reviews:${workspaceID}`));
  const jobs = createJobsStore({ client, runtime, owner });
  const review = createReviewStore({ client, runtime, owner, onClosedChanged: () => jobs.loadJobs() });
  const log = createLogStore({ runtime, baseUrl: untrack(() => roborevBaseUrl) });
  const daemon = createDaemonStore({ client, runtime });
  const stores = { ...getStores(), roborevJobs: jobs, roborevReview: review, roborevLog: log, roborevDaemon: daemon };

  let repositorySelected = $state(false);
  let resolutionError = $state<string | null>(null);
  let loading = $state(true);
  let refreshToken = $state(0);
  let drawerTab = $state<"review" | "log" | "prompt">("review");

  function refresh(): void {
    refreshToken += 1;
  }

  // Mounting this panel or explicitly refreshing it owns all initial reads.
  // It has no polling or event stream, including while the tab stays open.
  $effect(() => {
    void refreshToken;
    const requestedName = repoName;
    const requestedOwner = repoOwner;
    const requestedBranch = branch;
    const execution = untrack(() => runtime.runCommand(
      Effect.gen(function* () {
        yield* Effect.sync(() => { loading = true; resolutionError = null; });
        yield* daemon.refreshEffect;
        if (!daemon.isAvailable() || !requestedName || !requestedOwner) return;
        if (!repositorySelected) {
          const result = yield* executeRoborevRequest("resolve workspace Roborev repository", (signal) =>
            roborevAPI.listRepos(undefined, { signal }, client),
          );
          if (result.status !== 200) {
            return yield* Effect.fail(RoborevResponseError.make({
              operation: "resolve workspace Roborev repository",
              message: "Failed to resolve repository",
              cause: result.data,
            }));
          }
          const matches = (result.data.repos ?? []).filter((repo) => repo.name.toLowerCase() === requestedName.toLowerCase());
          const candidates = matches.length > 1
            ? matches.filter((repo) => repo.root_path.split("/").some((segment) => segment.toLowerCase() === requestedOwner.toLowerCase()))
            : matches;
          if (candidates.length > 1) {
            yield* Effect.sync(() => { resolutionError = "Multiple local repositories match this workspace"; });
            return;
          }
          const rootPath = candidates[0]?.root_path;
          if (!rootPath) return;
          yield* Effect.sync(() => {
            repositorySelected = true;
            jobs.setRepoBranchFilter(rootPath, requestedBranch);
          });
        }
        yield* jobs.loadJobsEffect();
        const selectedID = jobs.getSelectedJobId();
        if (selectedID !== undefined) yield* review.loadReviewEffect(selectedID);
      }).pipe(
        Effect.catch(() => Effect.sync(() => { resolutionError = "Failed to load local reviews"; })),
        Effect.ensuring(Effect.sync(() => { loading = false; })),
      ),
      { operation: "refresh workspace local reviews", safeContext: { owner }, onFailure: () => {} },
    ));
    return execution.interrupt;
  });

  $effect(() => {
    const id = jobs.getSelectedJobId();
    review.setSelectedJobId(id);
    drawerTab = "review";
    if (id === undefined) return;
    const execution = untrack(() => runtime.runCommand(review.loadReviewEffect(id), {
      operation: "load selected local review", safeContext: { owner, jobId: id }, onFailure: () => {},
    }));
    return execution.interrupt;
  });

  onDestroy(() => {
    jobs.dispose();
    runtime.runCommand(Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      yield* workflow.stop(owner);
    }), { operation: "stop workspace local reviews", safeContext: { owner }, onFailure: () => {} });
  });
</script>

<SidebarStoreScope {stores}>
  <div class="sidebar-reviews" inert={disabled}>
    <div class="sidebar-reviews-header">
      <Button size="sm" onclick={refresh} disabled={disabled || loading} ariaLabel="Refresh local reviews">Refresh</Button>
      <DaemonStatus onRetry={refresh} />
      {#if daemon.isAvailable()}
        <FilterBar disabled={disabled || loading} onRepositoryChange={() => { repositorySelected = true; resolutionError = null; }} />
      {/if}
    </div>
    {#if loading && !repositorySelected}
      <div class="loading-placeholder"><Spinner size={14} label="Loading local reviews" /> Loading local reviews...</div>
    {:else if !daemon.isAvailable()}
      <EmptyState title="Roborev daemon not reachable" description={daemon.getEndpoint()} />
    {:else if resolutionError}
      <EmptyState title={resolutionError} />
    {:else if !repositorySelected}
      <EmptyState title="No reviews for this worktree" />
    {:else}
      <div class="sidebar-reviews-body">
        <div class="sidebar-reviews-table"><JobTable /></div>
        <ReviewDrawer activeTab={drawerTab} />
      </div>
    {/if}
  </div>
</SidebarStoreScope>

<style>
  .sidebar-reviews, .sidebar-reviews-body, .sidebar-reviews-table {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }
  .sidebar-reviews-header { flex-shrink: 0; }
  .loading-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: var(--space-8) var(--space-6);
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }
</style>
