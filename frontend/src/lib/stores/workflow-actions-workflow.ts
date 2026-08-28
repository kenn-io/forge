import { Clock, Context, Deferred, Effect, FiberMap, Layer, Ref, Schedule, Semaphore } from "effect";

import type { components } from "../api/generated/schema.js";
import { GeneratedApi } from "../api/generated-api.js";
import { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import {
  canonicalProvider,
  providerActionsPath,
  providerRouteParams,
  resolvedPlatformHost,
  type ProviderRouteRef,
} from "../api/provider-routes.js";
import { makeOrderedCommandQueue, type CommandQueueClosed } from "../effect/ordered-command-queue.js";

export type WorkflowCatalog = components["schemas"]["WorkflowCatalogResponse"];
export type WorkflowDefinition = components["schemas"]["WorkflowDefinitionResponse"];
export type WorkflowDispatchBody = components["schemas"]["WorkflowDispatchBody"];
export type WorkflowRun = components["schemas"]["WorkflowRunResponse"];
export type WorkflowRunJob = components["schemas"]["WorkflowRunJobResponse"];
export type WorkflowRuns = components["schemas"]["WorkflowRunsResponse"];
export type WorkflowActionsError = ApiProblemError | TransientTransportError | CommandQueueClosed;

export interface WorkflowDispatchInput {
  readonly ref: ProviderRouteRef;
  readonly workflowId: string;
  readonly expectedDefinitionSha: string;
  readonly dispatchRef: string;
  readonly inputs: Readonly<Record<string, unknown>>;
  readonly actor?: string | undefined;
}

export interface AcceptedWorkflowDispatch {
  readonly id: string;
  readonly ref: ProviderRouteRef;
  readonly workflowId: string;
  readonly expectedDefinitionSha: string;
  readonly dispatchRef: string;
  readonly inputs: Readonly<Record<string, unknown>>;
  readonly actor?: string | undefined;
  readonly startedAt?: number | undefined;
}

export type WorkflowDispatchState =
  | { readonly kind: "pending"; readonly request: AcceptedWorkflowDispatch }
  | { readonly kind: "succeeded"; readonly request: AcceptedWorkflowDispatch; readonly run?: WorkflowRun }
  | { readonly kind: "locating"; readonly request: AcceptedWorkflowDispatch }
  | { readonly kind: "locating_timed_out"; readonly request: AcceptedWorkflowDispatch }
  | { readonly kind: "failed"; readonly request: AcceptedWorkflowDispatch; readonly error: WorkflowActionsError }
  | {
      readonly kind: "uncertain";
      readonly request: AcceptedWorkflowDispatch;
      readonly error: WorkflowActionsError;
      readonly candidates: readonly WorkflowRun[];
    };

export interface WorkflowActionsLoading {
  readonly catalog: boolean;
  readonly runs: boolean;
  readonly jobs: readonly string[];
}
export interface WorkflowRunsPageState {
  readonly nextCursor: string | null;
  readonly exhausted: boolean;
  readonly loadingMore: boolean;
}

export interface WorkflowActionsSnapshot {
  readonly ref: ProviderRouteRef;
  readonly catalog: WorkflowCatalog | null;
  readonly selectedWorkflow: WorkflowDefinition | null;
  readonly runs: readonly WorkflowRun[];
  readonly runsPage: WorkflowRunsPageState;
  readonly jobs: Readonly<Record<string, readonly WorkflowRunJob[]>>;
  readonly loading: WorkflowActionsLoading;
  readonly dispatches: readonly WorkflowDispatchState[];
  readonly error: WorkflowActionsError | null;
}

export type WorkflowActionsObserver = (snapshot: WorkflowActionsSnapshot) => void;

interface WorkflowActionsWorkflowShape {
  readonly watchRepository: (
    owner: string,
    ref: ProviderRouteRef,
    observer: WorkflowActionsObserver,
  ) => Effect.Effect<never>;
  readonly watchJobs: (owner: string, ref: ProviderRouteRef, runId: string) => Effect.Effect<never>;
  readonly selectWorkflow: (ref: ProviderRouteRef, workflowId: string | null) => Effect.Effect<void>;
  readonly refreshCatalog: (ref: ProviderRouteRef, workflowId: string) => Effect.Effect<void>;
  readonly loadMoreRuns: (ref: ProviderRouteRef) => Effect.Effect<void>;
  readonly newDispatchCycle: (ref: ProviderRouteRef, workflowId: string) => Effect.Effect<void>;
  readonly dispatch: (input: WorkflowDispatchInput) => Effect.Effect<AcceptedWorkflowDispatch, CommandQueueClosed>;
  readonly snapshot: (ref: ProviderRouteRef) => Effect.Effect<WorkflowActionsSnapshot>;
  readonly setEnabled: (enabled: boolean) => Effect.Effect<void>;
}

export class WorkflowActionsWorkflow extends Context.Service<WorkflowActionsWorkflow, WorkflowActionsWorkflowShape>()(
  "kenn-forge/WorkflowActionsWorkflow",
) {}

interface RepositoryOwner {
  readonly token: symbol;
  readonly observer: WorkflowActionsObserver;
}

interface RepositoryEntry {
  readonly key: string;
  readonly ref: ProviderRouteRef;
  readonly owners: Map<string, RepositoryOwner>;
  snapshot: WorkflowActionsSnapshot;
  selectedWorkflowId: string | null;
  loopGeneration: number;
  loopRunning: boolean;
  readonly runPages: Map<string, WorkflowRunPagination>;
}

interface JobDemand {
  readonly key: string;
  readonly repositoryKey: string;
  readonly runId: string;
  readonly owners: Map<string, symbol>;
  generation: number;
  running: boolean;
  restartAfterFailure: boolean;
}
interface WorkflowRunPagination {
  firstPage: readonly WorkflowRun[];
  olderRuns: readonly WorkflowRun[];
  nextCursor: string | null;
  exhausted: boolean;
  loadingMore: boolean;
}

interface DispatchCommand {
  readonly request: AcceptedWorkflowDispatch;
  readonly admitted: Deferred.Deferred<void>;
}

const reconciliationWindowMs = 60_000;
const candidateClockSkewMs = 5_000;
const activePollSchedule = Schedule.max([Schedule.spaced("5 seconds"), Schedule.recurs(1)]);
const idlePollSchedule = Schedule.max([Schedule.spaced("30 seconds"), Schedule.recurs(1)]);

function waitForPoll(active: boolean): Effect.Effect<void> {
  return Effect.repeat(Effect.void, active ? activePollSchedule : idlePollSchedule).pipe(Effect.asVoid);
}

function normalizeRepoPath(repoPath: string): string {
  return repoPath
    .trim()
    .replace(/\\/g, "/")
    .replace(/\/{2,}/g, "/")
    .replace(/^\/+|\/+$/g, "");
}

function normalizeRef(ref: ProviderRouteRef): ProviderRouteRef {
  const provider = canonicalProvider(ref.provider);
  return {
    provider,
    platformHost: resolvedPlatformHost(provider, ref.platformHost).toLowerCase(),
    owner: ref.owner,
    name: ref.name,
    repoPath: normalizeRepoPath(ref.repoPath),
  };
}

export function workflowRepositoryKey(ref: ProviderRouteRef): string {
  const normalized = normalizeRef(ref);
  const identity = [normalized.provider, normalized.platformHost ?? "", normalized.owner, normalized.name]
    .map(encodeURIComponent)
    .join("\u0000");
  return `${identity}\u0000${normalized.repoPath}`;
}

function emptySnapshot(ref: ProviderRouteRef): WorkflowActionsSnapshot {
  return {
    ref,
    catalog: null,
    selectedWorkflow: null,
    runs: [],
    runsPage: { nextCursor: null, exhausted: false, loadingMore: false },
    jobs: {},
    loading: { catalog: false, runs: false, jobs: [] },
    dispatches: [],
    error: null,
  };
}

function isTerminalRun(run: WorkflowRun): boolean {
  const status = run.status.toLowerCase();
  return status === "completed" || status === "cancelled" || status === "failure" || status === "success";
}

function dispatchNeedsPolling(state: WorkflowDispatchState, now: number): boolean {
  switch (state.kind) {
    case "pending":
      return false;
    case "succeeded":
      return state.run !== undefined && !isTerminalRun(state.run);
    case "locating":
      return true;
    case "uncertain":
      return state.request.startedAt !== undefined && now < state.request.startedAt + reconciliationWindowMs;
    case "failed":
    case "locating_timed_out":
      return false;
  }
}

function matchingCandidates(request: AcceptedWorkflowDispatch, runs: readonly WorkflowRun[]): readonly WorkflowRun[] {
  const actor = request.actor?.trim();
  if (request.startedAt === undefined || !actor) return [];
  const earliest = request.startedAt - candidateClockSkewMs;
  const latest = request.startedAt + reconciliationWindowMs;
  return runs.filter((candidate) => {
    const createdAt = candidate.created_at === undefined ? Number.NaN : Date.parse(candidate.created_at);
    return (
      candidate.workflow_id === request.workflowId &&
      candidate.event === "workflow_dispatch" &&
      candidate.ref === request.dispatchRef &&
      candidate.actor === actor &&
      Number.isFinite(createdAt) &&
      createdAt >= earliest &&
      createdAt <= latest
    );
  });
}

function runsPageState(page: WorkflowRunPagination): WorkflowRunsPageState {
  return {
    nextCursor: page.nextCursor,
    exhausted: page.exhausted,
    loadingMore: page.loadingMore,
  };
}

function combinedRuns(page: WorkflowRunPagination): readonly WorkflowRun[] {
  const firstPageIDs = new Set(page.firstPage.map((run) => run.id));
  return [...page.firstPage, ...page.olderRuns.filter((run) => !firstPageIDs.has(run.id))];
}

function reconcileDispatchStates(
  states: readonly WorkflowDispatchState[],
  runs: readonly WorkflowRun[],
  now: number,
): readonly WorkflowDispatchState[] {
  return states.map((state): WorkflowDispatchState => {
    switch (state.kind) {
      case "pending":
      case "failed":
      case "locating_timed_out":
        return state;
      case "succeeded": {
        if (state.run === undefined || isTerminalRun(state.run)) return state;
        const current = runs.find((candidate) => candidate.id === state.run?.id);
        return current === undefined ? state : { ...state, run: current };
      }
      case "locating": {
        const candidates = matchingCandidates(state.request, runs);
        const candidate = candidates[0];
        if (candidate !== undefined) return { kind: "succeeded", request: state.request, run: candidate };
        if (state.request.startedAt !== undefined && now >= state.request.startedAt + reconciliationWindowMs) {
          return { kind: "locating_timed_out", request: state.request };
        }
        return state;
      }
      case "uncertain":
        return { ...state, candidates: matchingCandidates(state.request, runs) };
    }
  });
}

function isDispatchOutcomeUncertain(error: WorkflowActionsError): boolean {
  return (
    error._tag === "TransientTransportError" ||
    (error._tag === "ApiProblemError" && error.problem.code === "mutationOutcomeUnknown")
  );
}

function actorFromMutationError(error: WorkflowActionsError): string | undefined {
  if (error._tag !== "ApiProblemError" || error.problem.code !== "mutationOutcomeUnknown") {
    return undefined;
  }
  const actor = error.problem.details?.["actor"];
  return typeof actor === "string" && actor.trim() !== "" ? actor.trim() : undefined;
}

export const WorkflowActionsWorkflowLive = Layer.effect(WorkflowActionsWorkflow)(
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const scope = yield* Effect.scope;
    const registry = yield* Semaphore.make(1);
    const requestSequence = yield* Ref.make(0);
    const repositoryFibers = yield* FiberMap.make<string, void, never>();
    const jobFibers = yield* FiberMap.make<string, void, never>();
    const repositories = new Map<string, RepositoryEntry>();
    const ownerRepositories = new Map<string, { readonly key: string; readonly token: symbol }>();
    const jobs = new Map<string, JobDemand>();
    const ownerJobs = new Map<string, { readonly key: string; readonly token: symbol }>();
    let enabled = true;

    function entryFor(ref: ProviderRouteRef): RepositoryEntry {
      const normalized = normalizeRef(ref);
      const key = workflowRepositoryKey(normalized);
      const existing = repositories.get(key);
      if (existing !== undefined) return existing;
      const created: RepositoryEntry = {
        key,
        ref: normalized,
        owners: new Map(),
        snapshot: emptySnapshot(normalized),
        selectedWorkflowId: null,
        loopGeneration: 0,
        loopRunning: false,
        runPages: new Map(),
      };
      repositories.set(key, created);
      return created;
    }

    function runPageFor(entry: RepositoryEntry, workflowId: string): WorkflowRunPagination {
      const existing = entry.runPages.get(workflowId);
      if (existing !== undefined) return existing;
      const created: WorkflowRunPagination = {
        firstPage: [],
        olderRuns: [],
        nextCursor: null,
        exhausted: false,
        loadingMore: false,
      };
      entry.runPages.set(workflowId, created);
      return created;
    }

    function notify(
      observers: readonly WorkflowActionsObserver[],
      snapshot: WorkflowActionsSnapshot,
    ): Effect.Effect<void> {
      return Effect.sync(() => {
        for (const observer of observers) {
          try {
            observer(snapshot);
          } catch {
            // Presentation cannot change application-owned workflow state.
          }
        }
      });
    }

    const updateSnapshot = Effect.fn("WorkflowActions.updateSnapshot")(function* (
      key: string,
      update: (snapshot: WorkflowActionsSnapshot, entry: RepositoryEntry) => WorkflowActionsSnapshot,
    ) {
      const projection = yield* registry.withPermit(
        Effect.sync(() => {
          const entry = repositories.get(key);
          if (entry === undefined) return undefined;
          entry.snapshot = update(entry.snapshot, entry);
          return {
            snapshot: entry.snapshot,
            observers: Array.from(entry.owners.values(), (owner) => owner.observer),
          };
        }),
      );
      if (projection !== undefined) yield* notify(projection.observers, projection.snapshot);
    });

    function readCatalog(entry: RepositoryEntry) {
      return api.execute("GET workflow catalog", (signal) =>
        api.client.GET(providerActionsPath(entry.ref, "/workflows"), {
          params: { path: providerRouteParams(entry.ref) },
          signal,
        }),
      );
    }

    function readRuns(entry: RepositoryEntry, workflowId: string, cursor?: string) {
      return api.execute("GET workflow runs", (signal) =>
        api.client.GET(providerActionsPath(entry.ref, "/runs"), {
          params: {
            path: providerRouteParams(entry.ref),
            query: {
              workflow_id: workflowId,
              per_page: 50,
              ...(cursor !== undefined && { cursor }),
            },
          },
          signal,
        }),
      );
    }

    function readJobs(entry: RepositoryEntry, runId: string) {
      return api.execute("GET workflow run jobs", (signal) =>
        api.client.GET(providerActionsPath(entry.ref, "/runs/{run_id}/jobs"), {
          params: { path: { ...providerRouteParams(entry.ref), run_id: runId } },
          signal,
        }),
      );
    }

    const repositoryHasDemand = Effect.fn("WorkflowActions.repositoryHasDemand")(function* (key: string) {
      const now = yield* Clock.currentTimeMillis;
      return yield* registry.withPermit(
        Effect.sync(() => {
          const entry = repositories.get(key);
          if (entry === undefined || !enabled) return false;
          return entry.owners.size > 0 || entry.snapshot.dispatches.some((state) => dispatchNeedsPolling(state, now));
        }),
      );
    });

    function workflowIdsForRead(entry: RepositoryEntry, now: number): readonly string[] {
      const ids = entry.selectedWorkflowId === null ? [] : [entry.selectedWorkflowId];
      for (const state of entry.snapshot.dispatches) {
        if (dispatchNeedsPolling(state, now) && !ids.includes(state.request.workflowId)) {
          ids.push(state.request.workflowId);
        }
      }
      return ids;
    }

    function repositoryShouldRun(entry: RepositoryEntry, now: number): boolean {
      const hasDispatchDemand = entry.snapshot.dispatches.some((state) => dispatchNeedsPolling(state, now));
      if (entry.owners.size === 0 && !hasDispatchDemand) return false;
      return entry.snapshot.catalog === null || workflowIdsForRead(entry, now).length > 0;
    }

    function reconcileWorkflowDispatchStates(
      states: readonly WorkflowDispatchState[],
      workflowId: string,
      runs: readonly WorkflowRun[],
      now: number,
    ): readonly WorkflowDispatchState[] {
      const reconciled = reconcileDispatchStates(
        states.filter((state) => state.request.workflowId === workflowId),
        runs,
        now,
      );
      let matchingIndex = 0;
      return states.map((state) => (state.request.workflowId === workflowId ? reconciled[matchingIndex++]! : state));
    }

    const loadCatalog = Effect.fn("WorkflowActions.loadCatalog")(function* (entry: RepositoryEntry, force = false) {
      if (!force && entry.snapshot.catalog !== null) return true;
      yield* updateSnapshot(entry.key, (snapshot) => ({
        ...snapshot,
        loading: { ...snapshot.loading, catalog: true },
      }));
      return yield* readCatalog(entry).pipe(
        Effect.matchEffect({
          onFailure: (error) =>
            updateSnapshot(entry.key, (snapshot) => ({
              ...snapshot,
              error,
              loading: { ...snapshot.loading, catalog: false },
            })).pipe(Effect.as(false)),
          onSuccess: (catalog) =>
            updateSnapshot(entry.key, (snapshot, current) => ({
              ...snapshot,
              catalog,
              selectedWorkflow:
                catalog.workflows?.find((workflow) => workflow.id === current.selectedWorkflowId) ?? null,
              error: null,
              loading: { ...snapshot.loading, catalog: false },
            })).pipe(Effect.as(true)),
        }),
      );
    });

    const loadRuns = Effect.fn("WorkflowActions.loadRuns")(function* (entry: RepositoryEntry, workflowId: string) {
      yield* updateSnapshot(entry.key, (snapshot, current) =>
        current.selectedWorkflowId !== workflowId
          ? snapshot
          : {
              ...snapshot,
              loading: { ...snapshot.loading, runs: true },
            },
      );
      yield* readRuns(entry, workflowId).pipe(
        Effect.matchEffect({
          onFailure: (error) =>
            Clock.currentTimeMillis.pipe(
              Effect.flatMap((now) =>
                updateSnapshot(entry.key, (snapshot, current) => {
                  const firstPage = runPageFor(current, workflowId).firstPage;
                  return {
                    ...snapshot,
                    dispatches: reconcileWorkflowDispatchStates(snapshot.dispatches, workflowId, firstPage, now),
                    ...(current.selectedWorkflowId === workflowId
                      ? {
                          error,
                          loading: { ...snapshot.loading, runs: false },
                        }
                      : {}),
                  };
                }),
              ),
            ),
          onSuccess: (response) =>
            Clock.currentTimeMillis.pipe(
              Effect.flatMap((now) =>
                updateSnapshot(entry.key, (snapshot, current) => {
                  const items = response.items ?? [];
                  const page = runPageFor(current, workflowId);
                  page.firstPage = items;
                  page.nextCursor = response.next_cursor ?? null;
                  page.exhausted = response.exhausted;
                  return {
                    ...snapshot,
                    ...(current.selectedWorkflowId === workflowId
                      ? {
                          runs: combinedRuns(page),
                          runsPage: runsPageState(page),
                          error: null,
                          loading: { ...snapshot.loading, runs: false },
                        }
                      : {}),
                    dispatches: reconcileWorkflowDispatchStates(snapshot.dispatches, workflowId, items, now),
                  };
                }),
              ),
            ),
        }),
      );
    });

    const runRepositoryLoop = Effect.fn("WorkflowActions.repositoryLoop")(function* (key: string, _generation: number) {
      const entry = repositories.get(key);
      if (entry === undefined) return;
      while (yield* repositoryHasDemand(key)) {
        yield* loadCatalog(entry);
        if (entry.snapshot.catalog === null) {
          if (!(yield* repositoryHasDemand(key))) return;
          yield* waitForPoll(false);
          continue;
        }
        const now = yield* Clock.currentTimeMillis;
        const workflowIds = workflowIdsForRead(entry, now);
        if (workflowIds.length === 0) return;
        for (const workflowId of workflowIds) {
          yield* loadRuns(entry, workflowId);
        }
        if (!(yield* repositoryHasDemand(key))) return;
        const active = yield* Clock.currentTimeMillis.pipe(
          Effect.map(
            (currentTime) =>
              entry.snapshot.runs.some((candidate) => !isTerminalRun(candidate)) ||
              entry.snapshot.dispatches.some((state) => dispatchNeedsPolling(state, currentTime)),
          ),
        );
        yield* waitForPoll(active);
      }
    });

    const ensureRepositoryLoop = Effect.fn("WorkflowActions.ensureRepositoryLoop")((key: string) =>
      Effect.uninterruptible(
        Effect.gen(function* () {
          const now = yield* Clock.currentTimeMillis;
          const generation = yield* registry.withPermit(
            Effect.sync(() => {
              const entry = repositories.get(key);
              if (entry === undefined || entry.loopRunning || !enabled) return undefined;
              if (!repositoryShouldRun(entry, now)) return undefined;
              entry.loopRunning = true;
              entry.loopGeneration += 1;
              return entry.loopGeneration;
            }),
          );
          if (generation !== undefined) {
            yield* FiberMap.run(repositoryFibers, key, repositoryLoop(key, generation));
          }
        }),
      ),
    );

    function repositoryLoop(key: string, generation: number): Effect.Effect<void> {
      return runRepositoryLoop(key, generation).pipe(
        Effect.ensuring(
          Effect.gen(function* () {
            const now = yield* Clock.currentTimeMillis;
            const shouldRestart = yield* registry.withPermit(
              Effect.sync(() => {
                const entry = repositories.get(key);
                if (entry === undefined || entry.loopGeneration !== generation) return false;
                entry.loopRunning = false;
                return enabled && repositoryShouldRun(entry, now);
              }),
            );
            if (shouldRestart) {
              yield* Effect.forkIn(Effect.yieldNow.pipe(Effect.andThen(ensureRepositoryLoop(key))), scope);
            }
          }),
        ),
      );
    }

    const stopRepositoryLoopIfIdle = Effect.fn("WorkflowActions.stopRepositoryLoopIfIdle")(function* (key: string) {
      const now = yield* Clock.currentTimeMillis;
      const shouldStop = yield* registry.withPermit(
        Effect.sync(() => {
          const entry = repositories.get(key);
          if (entry === undefined || !entry.loopRunning) return false;
          const hasDemand =
            enabled &&
            (entry.owners.size > 0 || entry.snapshot.dispatches.some((state) => dispatchNeedsPolling(state, now)));
          if (hasDemand) return false;
          entry.loopRunning = false;
          entry.loopGeneration += 1;
          return true;
        }),
      );
      if (shouldStop) yield* FiberMap.remove(repositoryFibers, key);
    });

    const restartRepositoryLoop = Effect.fn("WorkflowActions.restartRepositoryLoop")((key: string) =>
      Effect.uninterruptible(
        Effect.gen(function* () {
          const now = yield* Clock.currentTimeMillis;
          const shouldRestart = yield* registry.withPermit(
            Effect.sync(() => {
              const entry = repositories.get(key);
              if (entry === undefined || !enabled) return false;
              entry.loopRunning = false;
              entry.loopGeneration += 1;
              return repositoryShouldRun(entry, now);
            }),
          );
          yield* FiberMap.remove(repositoryFibers, key);
          if (shouldRestart) yield* ensureRepositoryLoop(key);
        }),
      ),
    );

    const loadMoreRuns = Effect.fn("WorkflowActions.loadMoreRuns")(function* (ref: ProviderRouteRef) {
      const entry = entryFor(ref);
      const request = yield* registry.withPermit(
        Effect.sync(() => {
          if (!enabled || entry.selectedWorkflowId === null) return undefined;
          const page = runPageFor(entry, entry.selectedWorkflowId);
          if (page.loadingMore || page.exhausted || page.nextCursor === null) return undefined;
          page.loadingMore = true;
          return { workflowId: entry.selectedWorkflowId, cursor: page.nextCursor };
        }),
      );
      if (request === undefined) return;
      yield* updateSnapshot(entry.key, (snapshot, current) => ({
        ...snapshot,
        runsPage: runsPageState(runPageFor(current, request.workflowId)),
      }));
      yield* readRuns(entry, request.workflowId, request.cursor).pipe(
        Effect.matchEffect({
          onFailure: (error) =>
            updateSnapshot(entry.key, (snapshot, current) => {
              const page = runPageFor(current, request.workflowId);
              page.loadingMore = false;
              return {
                ...snapshot,
                error,
                ...(current.selectedWorkflowId === request.workflowId ? { runsPage: runsPageState(page) } : {}),
              };
            }),
          onSuccess: (response) =>
            updateSnapshot(entry.key, (snapshot, current) => {
              const page = runPageFor(current, request.workflowId);
              const knownIDs = new Set([...page.firstPage, ...page.olderRuns].map((run) => run.id));
              page.olderRuns = [...page.olderRuns, ...(response.items ?? []).filter((run) => !knownIDs.has(run.id))];
              page.nextCursor = response.next_cursor ?? null;
              page.exhausted = response.exhausted;
              page.loadingMore = false;
              return {
                ...snapshot,
                error: null,
                ...(current.selectedWorkflowId === request.workflowId
                  ? {
                      runs: combinedRuns(page),
                      runsPage: runsPageState(page),
                    }
                  : {}),
              };
            }),
        }),
      );
    });

    const newDispatchCycle = Effect.fn("WorkflowActions.newDispatchCycle")(function* (
      ref: ProviderRouteRef,
      workflowId: string,
    ) {
      const key = workflowRepositoryKey(ref);
      yield* updateSnapshot(key, (snapshot) => ({
        ...snapshot,
        dispatches: snapshot.dispatches.filter(
          (state) => state.request.workflowId !== workflowId || state.kind === "pending" || state.kind === "locating",
        ),
      }));
      yield* stopRepositoryLoopIfIdle(key);
    });

    const refreshCatalog = Effect.fn("WorkflowActions.refreshCatalog")(function* (
      ref: ProviderRouteRef,
      workflowId: string,
    ) {
      const entry = entryFor(ref);
      if (!(yield* loadCatalog(entry, true))) return;
      yield* newDispatchCycle(entry.ref, workflowId);
      yield* restartRepositoryLoop(entry.key);
    });

    const dispatchQueue = yield* makeOrderedCommandQueue<DispatchCommand, void, never, never>(
      "workflow actions dispatch",
      (command) =>
        Deferred.await(command.admitted).pipe(
          Effect.andThen(
            Effect.gen(function* () {
              const startedAt = yield* Clock.currentTimeMillis;
              yield* updateSnapshot(workflowRepositoryKey(command.request.ref), (snapshot) => ({
                ...snapshot,
                dispatches: snapshot.dispatches.map(
                  (state): WorkflowDispatchState =>
                    state.request.id === command.request.id
                      ? { ...state, request: { ...state.request, startedAt } }
                      : state,
                ),
              }));
              return yield* api.execute("POST workflow dispatch", (signal) =>
                api.client.POST(providerActionsPath(command.request.ref, "/workflows/{workflow_id}/dispatch"), {
                  params: {
                    path: {
                      ...providerRouteParams(command.request.ref),
                      workflow_id: command.request.workflowId,
                    },
                  },
                  body: {
                    expected_definition_sha: command.request.expectedDefinitionSha,
                    inputs: command.request.inputs,
                    ref: command.request.dispatchRef,
                  },
                  signal,
                }),
              );
            }),
          ),
          Effect.matchEffect({
            onFailure: (error) =>
              updateSnapshot(workflowRepositoryKey(command.request.ref), (snapshot) => ({
                ...snapshot,
                dispatches: snapshot.dispatches.map((state): WorkflowDispatchState => {
                  if (state.request.id !== command.request.id) return state;
                  const actor = actorFromMutationError(error);
                  const request = actor === undefined ? state.request : { ...state.request, actor };
                  return isDispatchOutcomeUncertain(error)
                    ? { kind: "uncertain", request, error, candidates: [] }
                    : { kind: "failed", request, error };
                }),
              })).pipe(
                Effect.andThen(
                  isDispatchOutcomeUncertain(error)
                    ? restartRepositoryLoop(workflowRepositoryKey(command.request.ref))
                    : Effect.void,
                ),
              ),
            onSuccess: (response) =>
              updateSnapshot(workflowRepositoryKey(command.request.ref), (snapshot) => ({
                ...snapshot,
                dispatches: snapshot.dispatches.map((state): WorkflowDispatchState => {
                  if (state.request.id !== command.request.id) return state;
                  const actor = response.actor?.trim() || response.run?.actor.trim();
                  const request = actor ? { ...state.request, actor } : state.request;
                  if (response.run !== undefined) {
                    return { kind: "succeeded", request, run: response.run };
                  }
                  return { kind: "locating", request };
                }),
              })).pipe(
                Effect.andThen(
                  response.run === undefined || !isTerminalRun(response.run)
                    ? restartRepositoryLoop(workflowRepositoryKey(command.request.ref))
                    : Effect.void,
                ),
              ),
          }),
        ),
    );

    const releaseRepositoryOwner = Effect.fn("WorkflowActions.releaseRepositoryOwner")(function* (
      owner: string,
      token: symbol,
      previousKey: string | undefined,
    ) {
      const releasedKey = yield* registry.withPermit(
        Effect.sync(() => {
          const current = ownerRepositories.get(owner);
          if (current?.token !== token) return undefined;
          ownerRepositories.delete(owner);
          repositories.get(current.key)?.owners.delete(owner);
          return current.key;
        }),
      );
      if (releasedKey !== undefined) yield* stopRepositoryLoopIfIdle(releasedKey);
      if (previousKey !== undefined && previousKey !== releasedKey) {
        yield* stopRepositoryLoopIfIdle(previousKey);
      }
    });

    const watchRepository = Effect.fn("WorkflowActions.watchRepository")(function* (
      owner: string,
      ref: ProviderRouteRef,
      observer: WorkflowActionsObserver,
    ) {
      return yield* Effect.uninterruptibleMask((restore) =>
        Effect.gen(function* () {
          const token = Symbol(owner);
          const registration = yield* registry.withPermit(
            Effect.sync(() => {
              if (!enabled) return undefined;
              const entry = entryFor(ref);
              const previous = ownerRepositories.get(owner);
              if (previous !== undefined) repositories.get(previous.key)?.owners.delete(owner);
              entry.owners.set(owner, { token, observer });
              ownerRepositories.set(owner, { key: entry.key, token });
              return { entry, previousKey: previous?.key };
            }),
          );
          if (registration === undefined) return yield* restore(Effect.never);

          const lifetime = Effect.gen(function* () {
            yield* notify([observer], registration.entry.snapshot);
            if (registration.previousKey !== undefined && registration.previousKey !== registration.entry.key) {
              yield* stopRepositoryLoopIfIdle(registration.previousKey);
            }
            yield* ensureRepositoryLoop(registration.entry.key);
            return yield* Effect.never;
          });
          return yield* restore(lifetime).pipe(
            Effect.ensuring(releaseRepositoryOwner(owner, token, registration.previousKey)),
          );
        }),
      );
    });

    const finishJobRead = Effect.fn("WorkflowActions.finishJobRead")(function* (
      key: string,
      generation: number,
      succeeded: boolean,
    ) {
      const shouldRestart = yield* registry.withPermit(
        Effect.sync(() => {
          const demand = jobs.get(key);
          if (demand === undefined || demand.generation !== generation) return false;
          demand.running = false;
          if (succeeded) demand.restartAfterFailure = false;
          if (succeeded || !enabled || demand.owners.size === 0 || !demand.restartAfterFailure) {
            return false;
          }
          demand.restartAfterFailure = false;
          return true;
        }),
      );
      if (shouldRestart) {
        yield* Effect.forkIn(Effect.yieldNow.pipe(Effect.andThen(startJobRead(key))), scope);
      }
    });

    function jobRead(key: string, generation: number): Effect.Effect<void> {
      const demand = jobs.get(key);
      const repository = demand === undefined ? undefined : repositories.get(demand.repositoryKey);
      if (demand === undefined || repository === undefined) return Effect.void;
      let succeeded = false;
      return updateSnapshot(repository.key, (snapshot) => ({
        ...snapshot,
        loading: {
          ...snapshot.loading,
          jobs: snapshot.loading.jobs.includes(demand.runId)
            ? snapshot.loading.jobs
            : [...snapshot.loading.jobs, demand.runId],
        },
      })).pipe(
        Effect.andThen(readJobs(repository, demand.runId)),
        Effect.matchEffect({
          onFailure: (error) =>
            updateSnapshot(repository.key, (snapshot) => ({
              ...snapshot,
              error,
              loading: {
                ...snapshot.loading,
                jobs: snapshot.loading.jobs.filter((id) => id !== demand.runId),
              },
            })),
          onSuccess: (response) =>
            Effect.sync(() => {
              succeeded = true;
            }).pipe(
              Effect.andThen(
                updateSnapshot(repository.key, (snapshot) => ({
                  ...snapshot,
                  jobs: { ...snapshot.jobs, [demand.runId]: response.items ?? [] },
                  error: null,
                  loading: {
                    ...snapshot.loading,
                    jobs: snapshot.loading.jobs.filter((id) => id !== demand.runId),
                  },
                })),
              ),
            ),
        }),
        Effect.ensuring(Effect.suspend(() => finishJobRead(key, generation, succeeded))),
      );
    }

    const startJobRead = Effect.fn("WorkflowActions.startJobRead")((key: string) =>
      Effect.uninterruptible(
        Effect.gen(function* () {
          const generation = yield* registry.withPermit(
            Effect.sync(() => {
              const demand = jobs.get(key);
              if (demand === undefined || demand.running || demand.owners.size === 0 || !enabled) {
                return undefined;
              }
              const repository = repositories.get(demand.repositoryKey);
              if (repository?.snapshot.jobs[demand.runId] !== undefined) return undefined;
              demand.running = true;
              demand.generation += 1;
              return demand.generation;
            }),
          );
          if (generation !== undefined) yield* FiberMap.run(jobFibers, key, jobRead(key, generation));
        }),
      ),
    );

    const stopJobDemandIfIdle = Effect.fn("WorkflowActions.stopJobDemandIfIdle")(function* (key: string) {
      const shouldStop = yield* registry.withPermit(
        Effect.sync(() => {
          const demand = jobs.get(key);
          if (demand === undefined || demand.owners.size > 0 || !demand.running) return false;
          demand.running = false;
          demand.generation += 1;
          demand.restartAfterFailure = false;
          return true;
        }),
      );
      if (shouldStop) yield* FiberMap.remove(jobFibers, key);
    });

    const releaseJobsOwner = Effect.fn("WorkflowActions.releaseJobsOwner")(function* (
      owner: string,
      token: symbol,
      previousKey: string | undefined,
    ) {
      const releasedKey = yield* registry.withPermit(
        Effect.sync(() => {
          const current = ownerJobs.get(owner);
          if (current?.token !== token) return undefined;
          ownerJobs.delete(owner);
          jobs.get(current.key)?.owners.delete(owner);
          return current.key;
        }),
      );
      if (releasedKey !== undefined) yield* stopJobDemandIfIdle(releasedKey);
      if (previousKey !== undefined && previousKey !== releasedKey) {
        yield* stopJobDemandIfIdle(previousKey);
      }
    });

    const watchJobs = Effect.fn("WorkflowActions.watchJobs")(function* (
      owner: string,
      ref: ProviderRouteRef,
      runId: string,
    ) {
      return yield* Effect.uninterruptibleMask((restore) =>
        Effect.gen(function* () {
          const token = Symbol(owner);
          const repository = entryFor(ref);
          const key = `${repository.key}\u0000${encodeURIComponent(runId)}`;
          const registration = yield* registry.withPermit(
            Effect.sync(() => {
              if (!enabled) return undefined;
              const previous = ownerJobs.get(owner);
              if (previous !== undefined) jobs.get(previous.key)?.owners.delete(owner);
              const demand = jobs.get(key) ?? {
                key,
                repositoryKey: repository.key,
                runId,
                owners: new Map<string, symbol>(),
                generation: 0,
                running: false,
                restartAfterFailure: false,
              };
              jobs.set(key, demand);
              demand.owners.set(owner, token);
              if (demand.running) demand.restartAfterFailure = true;
              ownerJobs.set(owner, { key, token });
              return { previousKey: previous?.key };
            }),
          );
          if (registration === undefined) return yield* restore(Effect.never);

          const lifetime = Effect.gen(function* () {
            if (registration.previousKey !== undefined && registration.previousKey !== key) {
              yield* stopJobDemandIfIdle(registration.previousKey);
            }
            yield* startJobRead(key);
            return yield* Effect.never;
          });
          return yield* restore(lifetime).pipe(
            Effect.ensuring(releaseJobsOwner(owner, token, registration.previousKey)),
          );
        }),
      );
    });

    const selectWorkflow = Effect.fn("WorkflowActions.selectWorkflow")(function* (
      ref: ProviderRouteRef,
      workflowId: string | null,
    ) {
      const entry = entryFor(ref);
      yield* updateSnapshot(entry.key, (snapshot, current) => {
        current.selectedWorkflowId = workflowId;
        const page = workflowId === null ? undefined : runPageFor(current, workflowId);
        return {
          ...snapshot,
          selectedWorkflow: snapshot.catalog?.workflows?.find((workflow) => workflow.id === workflowId) ?? null,
          runs: page === undefined ? [] : combinedRuns(page),
          runsPage:
            page === undefined ? { nextCursor: null, exhausted: false, loadingMore: false } : runsPageState(page),
          error: null,
          loading: { ...snapshot.loading, runs: false },
        };
      });
      yield* restartRepositoryLoop(entry.key);
    });

    const dispatch = Effect.fn("WorkflowActions.dispatch")(function* (input: WorkflowDispatchInput) {
      const sequence = yield* Ref.updateAndGet(requestSequence, (current) => current + 1);
      const request: AcceptedWorkflowDispatch = {
        id: `workflow-dispatch-${sequence}`,
        ref: normalizeRef(input.ref),
        workflowId: input.workflowId,
        expectedDefinitionSha: input.expectedDefinitionSha,
        dispatchRef: input.dispatchRef,
        inputs: { ...input.inputs },
        ...(input.actor !== undefined && { actor: input.actor }),
      };
      const entry = entryFor(request.ref);
      const admitted = yield* Deferred.make<void>();
      const acknowledgement = yield* dispatchQueue.accept({ request, admitted });
      yield* Effect.uninterruptible(
        updateSnapshot(entry.key, (snapshot) => ({
          ...snapshot,
          dispatches: [...snapshot.dispatches, { kind: "pending", request }],
        })).pipe(
          Effect.andThen(Deferred.succeed(admitted, undefined)),
          Effect.andThen(Effect.forkIn(acknowledgement.pipe(Effect.catch(() => Effect.void)), scope)),
        ),
      );
      return request;
    });

    const snapshot = Effect.fn("WorkflowActions.snapshot")(function* (ref: ProviderRouteRef) {
      return yield* registry.withPermit(Effect.sync(() => entryFor(ref).snapshot));
    });

    const setEnabled = Effect.fn("WorkflowActions.setEnabled")(function* (nextEnabled: boolean) {
      const shouldClear = yield* registry.withPermit(
        Effect.sync(() => {
          enabled = nextEnabled;
          if (nextEnabled) return false;
          ownerRepositories.clear();
          ownerJobs.clear();
          for (const entry of repositories.values()) {
            entry.owners.clear();
            entry.loopRunning = false;
            entry.loopGeneration += 1;
            for (const page of entry.runPages.values()) page.loadingMore = false;
            entry.snapshot = {
              ...entry.snapshot,
              runsPage: { ...entry.snapshot.runsPage, loadingMore: false },
              loading: { catalog: false, runs: false, jobs: [] },
            };
          }
          for (const demand of jobs.values()) {
            demand.owners.clear();
            demand.running = false;
            demand.generation += 1;
            demand.restartAfterFailure = false;
          }
          return true;
        }),
      );
      if (shouldClear) {
        yield* FiberMap.clear(repositoryFibers);
        yield* FiberMap.clear(jobFibers);
      }
    });

    return {
      watchRepository,
      watchJobs,
      selectWorkflow,
      refreshCatalog,
      loadMoreRuns,
      newDispatchCycle,
      dispatch,
      snapshot,
      setEnabled,
    };
  }),
);
