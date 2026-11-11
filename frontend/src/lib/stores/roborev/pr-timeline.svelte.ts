import { Effect } from "effect";
import * as roborevAPI from "../../api/roborev/generated/client.js";
import type * as RoborevModels from "../../api/roborev/generated/models/index.js";
import { createRoborevClient, executeRoborevRequest } from "../../api/roborev/client.js";
import { executeGeneratedApiRequest } from "../../api/generated-api.js";
import { configuredRoborevBasePath } from "../../api/runtime-base.js";
import type { AppExecution, AppRuntime } from "../../app/runtime.js";
import type { StoreInstances } from "../../types.js";
import type { ProviderTimelineEntry } from "../../components/roborev/pr-review-timeline.js";
import { createDaemonStore } from "./daemon.svelte.js";
import { createJobsStore } from "./jobs.svelte.js";
import { createLogStore } from "./log.svelte.js";
import { createReviewStore } from "./review.svelte.js";
import { makeRoborevOwner, RoborevResponseError, RoborevWorkflow } from "./roborev-workflow.js";

type ReviewJob = RoborevModels.ReviewJob;

/** Why the local daemon contributed no reviews, when it did not. */
export type LocalReviewsUnavailable = "no-workspace" | "daemon-unreachable" | "untracked";

export interface PRRoborevTimelineOptions {
  runtime: AppRuntime;
  baseStores: StoreInstances;
  owner: string;
  onError: (message: string) => void;
}

// Enough for every review a pull request branch realistically collects.
const LOCAL_REVIEW_LIMIT = 200;

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
  let localJobs = $state.raw<ReviewJob[]>([]);
  let loading = $state(false);
  let loadError = $state<string | null>(null);
  let unavailable = $state<LocalReviewsUnavailable | null>(null);
  let truncated = $state(false);
  let openProviderEntry = $state.raw<ProviderTimelineEntry | null>(null);
  let execution: AppExecution<void, never> | null = null;
  let loadedWorkspaceId: string | null | undefined;

  const jobs = createJobsStore({ client, runtime, owner, onError });
  const review = createReviewStore({ client, runtime, owner, onError, onClosedChanged: () => reload() });
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
      const resolved = yield* executeRoborevRequest("resolve workspace Roborev repository", (signal) =>
        roborevAPI.resolveRepo({ path: workspace.worktree_path }, { signal }, client),
      );
      if (resolved.status !== 200) {
        return yield* Effect.fail(
          RoborevResponseError.make({
            operation: "resolve workspace Roborev repository",
            message: "Failed to resolve repository",
            cause: resolved.data,
          }),
        );
      }
      const rootPath = resolved.data.tracked ? resolved.data.repo?.root_path : undefined;
      if (!rootPath) return { kind: "unavailable", reason: "untracked" } as const;
      const listed = yield* executeRoborevRequest("list pull request Roborev reviews", (signal) =>
        roborevAPI.listJobs(
          {
            repo: [rootPath],
            branch: workspace.git_head_ref,
            hide_classify_jobs: "true",
            include_findings: "true",
            omit_prompt: "true",
            limit: LOCAL_REVIEW_LIMIT,
          },
          { signal },
          client,
        ),
      );
      if (listed.status !== 200) {
        return yield* Effect.fail(
          RoborevResponseError.make({
            operation: "list pull request Roborev reviews",
            message: "Failed to load reviews",
            cause: listed.data,
          }),
        );
      }
      return { kind: "jobs", jobs: listed.data.jobs ?? [], hasMore: listed.data.has_more } as const;
    });

  function load(workspaceId: string | null): void {
    execution?.interrupt();
    loadedWorkspaceId = workspaceId;
    if (workspaceId === null) {
      localJobs = [];
      truncated = false;
      loadError = null;
      unavailable = "no-workspace";
      return;
    }
    execution = runtime.runCommand(
      Effect.gen(function* () {
        yield* Effect.sync(() => {
          loading = true;
          loadError = null;
        });
        const result = yield* loadLocalJobs(workspaceId);
        yield* Effect.sync(() => {
          if (result.kind === "unavailable") {
            localJobs = [];
            truncated = false;
            unavailable = result.reason;
            return;
          }
          localJobs = result.jobs;
          truncated = result.hasMore;
          unavailable = null;
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
    localJobs = [];
    truncated = false;
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
    getLocalJobs: () => localJobs,
    isLoading: () => loading,
    getLoadError: () => loadError,
    getUnavailable: () => unavailable,
    isTruncated: () => truncated,
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
