import { Effect } from "effect";

import type { AppExecution, AppRuntime } from "../app/runtime.js";
import type { ProviderRouteRef } from "../api/provider-routes.js";
import {
  WorkflowActionsWorkflow,
  workflowRepositoryKey,
  type WorkflowActionsLoading,
  type WorkflowActionsSnapshot,
  type WorkflowCatalog,
  type WorkflowDefinition,
  type WorkflowDispatchInput,
  type WorkflowDispatchState,
  type WorkflowRun,
  type WorkflowRunJob,
} from "./workflow-actions-workflow.js";

export interface WorkflowActionsStoreOptions {
  readonly runtime: AppRuntime;
}

type WorkflowEnvironment = NonNullable<WorkflowCatalog["environments"]>[number];
type OwnerExecution = AppExecution<never, never>;

export interface WorkflowActionsStore {
  readonly claimRepository: (owner: string, ref: ProviderRouteRef) => void;
  readonly releaseRepository: (owner: string) => void;
  readonly selectWorkflow: (ref: ProviderRouteRef, workflowId: string | null) => void;
  readonly refreshCatalog: (ref: ProviderRouteRef, workflowId: string) => void;
  readonly loadMoreRuns: (ref: ProviderRouteRef) => void;
  readonly newDispatchCycle: (ref: ProviderRouteRef, workflowId: string) => void;
  readonly expandRun: (owner: string, ref: ProviderRouteRef, runId: string) => void;
  readonly collapseRun: (owner: string) => void;
  readonly dispatch: (input: WorkflowDispatchInput) => void;
  readonly setEnabled: (enabled: boolean) => void;
  readonly getSnapshot: (ref: ProviderRouteRef) => WorkflowActionsSnapshot | null;
  readonly getCatalog: (ref: ProviderRouteRef) => WorkflowCatalog | null;
  readonly getEnvironments: (ref: ProviderRouteRef) => readonly WorkflowEnvironment[];
  readonly getSelectedWorkflow: (ref: ProviderRouteRef) => WorkflowDefinition | null;
  readonly getRuns: (ref: ProviderRouteRef) => readonly WorkflowRun[];
  readonly getJobs: (ref: ProviderRouteRef, runId: string) => readonly WorkflowRunJob[];
  readonly getLoading: (ref: ProviderRouteRef) => WorkflowActionsLoading;
  readonly getDispatches: (ref: ProviderRouteRef) => readonly WorkflowDispatchState[];
}

const notLoading: WorkflowActionsLoading = { catalog: false, runs: false, jobs: [] };

export function createWorkflowActionsStore(options: WorkflowActionsStoreOptions): WorkflowActionsStore {
  const runtime = options.runtime;
  let enabled = true;
  let projections = $state.raw<Readonly<Record<string, WorkflowActionsSnapshot>>>({});
  const repositoryOwners = new Map<string, OwnerExecution>();
  const jobOwners = new Map<string, OwnerExecution>();

  function project(snapshot: WorkflowActionsSnapshot): void {
    projections = { ...projections, [workflowRepositoryKey(snapshot.ref)]: snapshot };
  }

  function runOwner(owner: string, ref: ProviderRouteRef): OwnerExecution {
    return runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        return yield* workflow.watchRepository(owner, ref, project);
      }),
      {
        operation: "watch workflow Actions repository",
        safeContext: {
          provider: ref.provider,
          owner: ref.owner,
          name: ref.name,
        },
        onFailure: () => {},
      },
    );
  }

  function claimRepository(owner: string, ref: ProviderRouteRef): void {
    if (!enabled) return;
    repositoryOwners.get(owner)?.interrupt();
    repositoryOwners.set(owner, runOwner(owner, ref));
  }

  function releaseRepository(owner: string): void {
    repositoryOwners.get(owner)?.interrupt();
    repositoryOwners.delete(owner);
  }

  function selectWorkflow(ref: ProviderRouteRef, workflowId: string | null): void {
    if (!enabled) return;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        yield* workflow.selectWorkflow(ref, workflowId);
      }),
      {
        operation: "select provider workflow",
        safeContext: { provider: ref.provider, owner: ref.owner, name: ref.name },
        onFailure: () => {},
      },
    );
  }

  function refreshCatalog(ref: ProviderRouteRef, workflowId: string): void {
    if (!enabled) return;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        yield* workflow.refreshCatalog(ref, workflowId);
      }),
      {
        operation: "refresh provider workflow catalog",
        safeContext: { provider: ref.provider, owner: ref.owner, name: ref.name, workflowId },
        onFailure: () => {},
      },
    );
  }

  function loadMoreRuns(ref: ProviderRouteRef): void {
    if (!enabled) return;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        yield* workflow.loadMoreRuns(ref);
      }),
      {
        operation: "load more provider workflow runs",
        safeContext: { provider: ref.provider, owner: ref.owner, name: ref.name },
        onFailure: () => {},
      },
    );
  }

  function newDispatchCycle(ref: ProviderRouteRef, workflowId: string): void {
    if (!enabled) return;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        yield* workflow.newDispatchCycle(ref, workflowId);
      }),
      {
        operation: "start new provider workflow dispatch cycle",
        safeContext: { provider: ref.provider, owner: ref.owner, name: ref.name, workflowId },
        onFailure: () => {},
      },
    );
  }

  function expandRun(owner: string, ref: ProviderRouteRef, runId: string): void {
    if (!enabled) return;
    jobOwners.get(owner)?.interrupt();
    const execution = runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        return yield* workflow.watchJobs(owner, ref, runId);
      }),
      {
        operation: "watch workflow run jobs",
        safeContext: { provider: ref.provider, owner: ref.owner, name: ref.name, runId },
        onFailure: () => {},
      },
    );
    jobOwners.set(owner, execution);
  }

  function collapseRun(owner: string): void {
    jobOwners.get(owner)?.interrupt();
    jobOwners.delete(owner);
  }

  function dispatch(input: WorkflowDispatchInput): void {
    if (!enabled) return;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        yield* workflow.dispatch(input);
      }),
      {
        operation: "dispatch provider workflow",
        safeContext: {
          provider: input.ref.provider,
          owner: input.ref.owner,
          name: input.ref.name,
          workflowId: input.workflowId,
        },
        onFailure: () => {},
      },
    );
  }

  function setEnabled(nextEnabled: boolean): void {
    enabled = nextEnabled;
    if (!nextEnabled) {
      for (const execution of repositoryOwners.values()) execution.interrupt();
      for (const execution of jobOwners.values()) execution.interrupt();
      repositoryOwners.clear();
      jobOwners.clear();
      projections = {};
    }
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        yield* workflow.setEnabled(nextEnabled);
      }),
      {
        operation: nextEnabled ? "enable workflow Actions" : "disable workflow Actions",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  function getSnapshot(ref: ProviderRouteRef): WorkflowActionsSnapshot | null {
    return projections[workflowRepositoryKey(ref)] ?? null;
  }

  function getCatalog(ref: ProviderRouteRef): WorkflowCatalog | null {
    return getSnapshot(ref)?.catalog ?? null;
  }

  function getEnvironments(ref: ProviderRouteRef): readonly WorkflowEnvironment[] {
    return getSnapshot(ref)?.catalog?.environments ?? [];
  }

  function getSelectedWorkflow(ref: ProviderRouteRef): WorkflowDefinition | null {
    return getSnapshot(ref)?.selectedWorkflow ?? null;
  }

  function getRuns(ref: ProviderRouteRef): readonly WorkflowRun[] {
    return getSnapshot(ref)?.runs ?? [];
  }

  function getJobs(ref: ProviderRouteRef, runId: string): readonly WorkflowRunJob[] {
    return getSnapshot(ref)?.jobs[runId] ?? [];
  }

  function getLoading(ref: ProviderRouteRef): WorkflowActionsLoading {
    return getSnapshot(ref)?.loading ?? notLoading;
  }

  function getDispatches(ref: ProviderRouteRef): readonly WorkflowDispatchState[] {
    return getSnapshot(ref)?.dispatches ?? [];
  }

  return {
    claimRepository,
    releaseRepository,
    selectWorkflow,
    refreshCatalog,
    loadMoreRuns,
    newDispatchCycle,
    expandRun,
    collapseRun,
    dispatch,
    setEnabled,
    getSnapshot,
    getCatalog,
    getEnvironments,
    getSelectedWorkflow,
    getRuns,
    getJobs,
    getLoading,
    getDispatches,
  };
}
