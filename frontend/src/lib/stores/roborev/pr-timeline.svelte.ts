import { Effect } from "effect";
import type * as RoborevModels from "../../api/roborev/generated/models/index.js";
import { createRoborevClient } from "../../api/roborev/client.js";
import { executeGeneratedApiRequest } from "../../api/generated-api.js";
import { configuredRoborevBasePath } from "../../api/runtime-base.js";
import type { AppExecution, AppRuntime } from "../../app/runtime.js";
import type { StoreInstances } from "../../types.js";
import type { ProviderTimelineEntry } from "../../components/roborev/pr-review-timeline.js";
import { createDaemonStore } from "./daemon.svelte.js";
import { createJobsStore } from "./jobs.svelte.js";
import { createLogStore } from "./log.svelte.js";
import { resolveRoborevRepoRoot } from "./resolve-repo.js";
import { createReviewStore } from "./review.svelte.js";
import { makeRoborevOwner, RoborevWorkflow } from "./roborev-workflow.js";

type ReviewJob = RoborevModels.ReviewJob;

/** Why the local daemon contributed no reviews, when it did not. */
export type LocalReviewsUnavailable = "daemon-unreachable" | "untracked";

export interface PRRoborevTimelineOptions {
  runtime: AppRuntime;
  baseStores: StoreInstances;
  owner: string;
  onError: (message: string) => void;
}

/**
 * Owns the local roborev reads for one pull request detail. Reviews of the
 * pull request's linked workspace come from the local daemon, resolved from
 * the workspace's worktree path; nothing is read until the panel asks.
 */
export function createPRRoborevTimeline(opts: PRRoborevTimelineOptions) {
  const baseUrl = configuredRoborevBasePath();
  const client = createRoborevClient(baseUrl);
  const owner = makeRoborevOwner(opts.owner);
  const { runtime, onError } = opts;
  // True once the jobs store holds this pull request's local reviews.
  let localLoaded = $state(false);
  let loading = $state(false);
  let loadError = $state<string | null>(null);
  let unavailable = $state<LocalReviewsUnavailable | null>(null);
  let openProviderEntry = $state.raw<ProviderTimelineEntry | null>(null);
  let execution: AppExecution<void, never> | null = null;
  let loadedWorkspaceId: string | null | undefined;

  // The whole history, closed reviews included; the shared list preferences do not apply.
  const jobs = createJobsStore({
    client,
    runtime,
    owner,
    onError,
    listQuery: { limit: 0, include_findings: "true", omit_prompt: "true", hide_classify_jobs: "true" },
  });
  const review = createReviewStore({ client, runtime, owner, onError, onClosedChanged: () => jobs.loadJobs() });
  const log = createLogStore({ runtime, baseUrl, onError });
  const daemon = createDaemonStore({ client, runtime });
  const stores: StoreInstances = {
    ...opts.baseStores,
    roborevJobs: jobs,
    roborevReview: review,
    roborevLog: log,
    roborevDaemon: daemon,
  };

  const loadLocalJobs = (workspaceId: string) =>
    Effect.gen(function* () {
      yield* daemon.refreshEffect;
      if (!daemon.isAvailable()) return { kind: "unavailable", reason: "daemon-unreachable" } as const;
      const workspace = yield* executeGeneratedApiRequest("load pull request workspace", (api, signal) =>
        api.WorkspacesService.getWorkspace({ id: workspaceId }, { signal }),
      );
      if (!workspace.worktree_path) return { kind: "unavailable", reason: "untracked" } as const;
      const rootPath = yield* resolveRoborevRepoRoot(client, workspace.worktree_path);
      if (!rootPath) return { kind: "unavailable", reason: "untracked" } as const;
      yield* Effect.sync(() => jobs.setRepoBranchFilter(rootPath, workspace.git_head_ref));
      // The jobs store records a failed listing in its error state.
      yield* jobs.loadJobsEffect();
      return jobs.getError() === null ? ({ kind: "jobs" } as const) : ({ kind: "failed" } as const);
    });

  function load(workspaceId: string | null): void {
    execution?.interrupt();
    loadedWorkspaceId = workspaceId;
    if (workspaceId === null) {
      localLoaded = false;
      loadError = null;
      unavailable = null;
      return;
    }
    execution = runtime.runCommand(
      Effect.gen(function* () {
        yield* Effect.sync(() => {
          loading = true;
          loadError = null;
        });
        const result = yield* loadLocalJobs(workspaceId);
        // The open review may have finished since it loaded; its row and the drawer must agree.
        const selectedJobId = jobs.getSelectedJobId();
        if (result.kind === "jobs" && selectedJobId !== undefined) yield* review.loadReviewEffect(selectedJobId);
        yield* Effect.sync(() => {
          if (result.kind === "failed") {
            loadError = "Failed to load local reviews";
            return;
          }
          localLoaded = result.kind === "jobs";
          unavailable = result.kind === "unavailable" ? result.reason : null;
        });
      }).pipe(
        Effect.catch(() =>
          Effect.sync(() => {
            loadError = "Failed to load local reviews";
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            loading = false;
          }),
        ),
      ),
      { operation: "load pull request Roborev reviews", safeContext: { owner }, onFailure: () => {} },
    );
  }

  function reload(): void {
    if (loadedWorkspaceId !== undefined) load(loadedWorkspaceId);
  }

  function openLocal(jobId: number): void {
    openProviderEntry = null;
    jobs.selectJob(jobId);
  }

  function openProvider(entry: ProviderTimelineEntry): void {
    jobs.deselectJob();
    openProviderEntry = entry;
  }

  function closeProvider(): void {
    openProviderEntry = null;
  }

  /** Forgets everything read for the previous pull request. */
  function reset(): void {
    execution?.interrupt();
    execution = null;
    loadedWorkspaceId = undefined;
    localLoaded = false;
    loading = false;
    loadError = null;
    unavailable = null;
    openProviderEntry = null;
    jobs.deselectJob();
  }

  function dispose(): void {
    reset();
    jobs.dispose();
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* RoborevWorkflow;
        yield* workflow.stop(owner);
      }),
      { operation: "stop pull request Roborev reviews", safeContext: { owner }, onFailure: () => {} },
    );
  }

  return {
    stores,
    review,
    getLocalJobs: (): ReviewJob[] => (localLoaded ? jobs.getJobs() : []),
    isLoading: () => loading,
    getLoadError: () => loadError,
    getUnavailable: () => unavailable,
    getDaemonEndpoint: () => daemon.getEndpoint(),
    getSelectedJobId: () => jobs.getSelectedJobId(),
    getOpenProviderEntry: () => openProviderEntry,
    load,
    reload,
    openLocal,
    openProvider,
    closeProvider,
    reset,
    dispose,
  };
}

export type PRRoborevTimeline = ReturnType<typeof createPRRoborevTimeline>;
