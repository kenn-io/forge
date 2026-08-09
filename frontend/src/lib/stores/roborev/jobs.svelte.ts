import { Effect, Option, Ref } from "effect";
import type { AppRuntime } from "../../app/runtime.js";
import { executeRoborevRequest, type RoborevClient, RoborevStreamError } from "../../api/roborev/client.js";
import type { components, operations } from "../../api/roborev/generated/schema.js";
import { isPanelParent, panelCostUsd, panelElapsedStart } from "../../utils/roborev-panel.js";
import {
  makeRoborevOwner,
  RoborevMutationError,
  roborevMutationFailureMessage,
  RoborevResponseError,
  RoborevWorkflow,
} from "./roborev-workflow.js";

type ReviewJob = components["schemas"]["ReviewJob"];
type JobStats = components["schemas"]["JobStats"];
type CancelJobResponse = components["schemas"]["CancelJobOutputBody"];
type RerunJobResponse = components["schemas"]["RerunJobOutputBody"];
type ListJobsQuery = NonNullable<operations["list-jobs"]["parameters"]["query"]>;

export interface JobsStoreOptions {
  client: RoborevClient;
  runtime: AppRuntime;
  owner: string;
  navigate: (path: string) => void;
  onError?: (msg: string) => void;
}

export interface JobStatusCounts {
  queued: number;
  running: number;
  done: number;
  failed: number;
}

interface JobsAuthority {
  readonly jobs: ReadonlyArray<ReviewJob>;
  readonly hasMore: boolean;
  readonly stats: JobStats;
  readonly filteredStatusCounts: Option.Option<JobStatusCounts>;
  readonly countScope: string;
  readonly queryScope: string;
}

type SortColumn = "id" | "status" | "verdict" | "agent" | "elapsed" | "cost" | "job_type" | "enqueued_at";
type SortDirection = "asc" | "desc";
type StringFilterKey = "repo" | "branch" | "status" | "search" | "jobType";
type BooleanFilterKey = "hideClosed" | "showAutoDesign";
type FilterKey = StringFilterKey | BooleanFilterKey;

export function createJobsStore(opts: JobsStoreOptions) {
  const client = opts.client;

  // State
  let jobs = $state<ReviewJob[]>([]);
  let loading = $state(false);
  let hasMore = $state(false);
  let stats = $state<JobStats>({ done: 0, closed: 0, open: 0 });
  let filteredStatusCounts = $state<JobStatusCounts | undefined>(undefined);
  let filteredStatusCountsScope: string | undefined;
  let storeError = $state<string | null>(null);
  let selectedJobId = $state<number | undefined>(undefined);
  let highlightedJobId = $state<number | undefined>(undefined);

  // Filters
  let filterRepo = $state<string | undefined>(undefined);
  let filterBranch = $state<string | undefined>(undefined);
  let filterStatus = $state<string | undefined>(undefined);
  let filterSearch = $state<string | undefined>(undefined);
  let filterHideClosed = $state(false);
  let filterJobType = $state<string | undefined>(undefined);
  let filterShowAutoDesign = $state(false);

  // Sorting (client-side)
  let sortColumn = $state<SortColumn>("id");
  let sortDirection = $state<SortDirection>("desc");

  // Panel expansion, keyed by panel_run_uuid. Member lists are cached per
  // run and refreshed alongside the listing so SSE-driven reloads keep
  // expanded panels live.
  let expandedPanels = $state<Record<string, boolean>>({});
  let panelMembers = $state<Record<string, ReviewJob[]>>({});
  let panelMemberErrors = $state<Record<string, string>>({});
  let loadingMembers = $state<Record<string, boolean>>({});
  let interestedPanelRun: string | undefined = undefined;

  // Roborev streams newline-delimited JSON, not server-sent events.
  let eventStreamConnected = $state(false);
  let activeEventOwner: string | undefined;

  function buildQuery(): ListJobsQuery {
    const q: ListJobsQuery = { limit: 50 };
    if (filterRepo) q.repo = [filterRepo];
    if (filterBranch) q.branch = filterBranch;
    if (filterStatus) q.status = filterStatus;
    if (filterSearch) q.git_ref = filterSearch;
    if (filterHideClosed) q.closed = "false";
    if (filterJobType) q.job_type = filterJobType;
    if (!filterShowAutoDesign) q.hide_classify_jobs = "true";
    return q;
  }

  function queryHasActiveFilters(query: ListJobsQuery): boolean {
    return Boolean(
      query.repo ||
      query.branch ||
      query.status ||
      query.git_ref ||
      query.closed ||
      query.job_type ||
      query.hide_classify_jobs,
    );
  }

  function hasActiveFilters(): boolean {
    return queryHasActiveFilters(buildQuery());
  }

  function countJobsByStatus(filteredJobs: ReviewJob[]): JobStatusCounts {
    const counts: JobStatusCounts = { queued: 0, running: 0, done: 0, failed: 0 };
    for (const job of filteredJobs) {
      if (job.status === "queued") counts.queued += 1;
      else if (job.status === "running") counts.running += 1;
      else if (job.status === "done") counts.done += 1;
      else if (job.status === "failed") counts.failed += 1;
    }
    return counts;
  }

  function getElapsedSeconds(job: ReviewJob): number {
    const startedAt = panelElapsedStart(job, getPanelMembersForJob(job));
    if (!startedAt) return -1;
    const start = new Date(startedAt).getTime();
    const end = job.finished_at ? new Date(job.finished_at).getTime() : Date.now();
    return Math.max(0, Math.floor((end - start) / 1000));
  }

  function getPanelMembersForJob(job: ReviewJob): ReviewJob[] | undefined {
    const runUuid = job.panel_run_uuid;
    return runUuid ? panelMembers[runUuid] : undefined;
  }

  function getPanelParentForMemberId(memberId: number): ReviewJob | undefined {
    for (const job of jobs) {
      const runUuid = job.panel_run_uuid;
      if (!runUuid || !isPanelParent(job)) continue;
      if (panelMembers[runUuid]?.some((member) => member.id === memberId)) {
        return job;
      }
    }
    return undefined;
  }

  function wantsPanelMembers(runUuid: string): boolean {
    return expandedPanels[runUuid] === true || interestedPanelRun === runUuid;
  }

  function getSortValue(job: ReviewJob, col: SortColumn): string | number {
    switch (col) {
      case "id":
        return job.id;
      case "status":
        return job.status;
      case "verdict":
        return job.verdict ?? "";
      case "agent":
        return job.agent;
      case "elapsed":
        return getElapsedSeconds(job);
      case "cost":
        return panelCostUsd(job, getPanelMembersForJob(job)) ?? -1;
      case "job_type":
        return job.job_type;
      case "enqueued_at":
        return job.enqueued_at;
      default:
        return job.id;
    }
  }

  function sortJobs(list: ReviewJob[]): ReviewJob[] {
    const dir = sortDirection === "asc" ? 1 : -1;
    return [...list].sort((a, b) => {
      const av = getSortValue(a, sortColumn);
      const bv = getSortValue(b, sortColumn);
      if (av < bv) return -1 * dir;
      if (av > bv) return 1 * dir;
      return 0;
    });
  }

  const fetchJobsAuthority = Effect.fn("RoborevJobs.fetchAuthority")(function* (
    query: ListJobsQuery,
    strictCount: boolean,
  ) {
    const filtered = queryHasActiveFilters(query);
    const countQuery: ListJobsQuery = { ...query, limit: 0, omit_prompt: "true" };
    const countScope = JSON.stringify(countQuery);
    const countRequest = executeRoborevRequest("count filtered Roborev jobs", (signal) =>
      client.GET("/api/jobs", {
        params: { query: countQuery },
        signal,
      }),
    );
    const result = yield* Effect.all(
      {
        list: executeRoborevRequest("list Roborev jobs", (signal) =>
          client.GET("/api/jobs", { params: { query }, signal }),
        ),
        count: filtered
          ? strictCount
            ? countRequest.pipe(Effect.map(Option.some))
            : countRequest.pipe(Effect.option)
          : Effect.succeed(Option.none()),
      },
      { concurrency: "unbounded" },
    );
    if (result.list.error !== undefined) {
      return yield* Effect.fail(
        RoborevResponseError.make({
          operation: "list Roborev jobs",
          message: "Failed to load jobs",
          cause: result.list.error,
        }),
      );
    }
    if (strictCount && Option.isSome(result.count) && result.count.value.error !== undefined) {
      return yield* Effect.fail(
        RoborevResponseError.make({
          operation: "count filtered Roborev jobs",
          message: "Failed to count filtered jobs",
          cause: result.count.value.error,
        }),
      );
    }
    return {
      jobs: result.list.data?.jobs ?? [],
      hasMore: result.list.data?.has_more ?? false,
      stats: result.list.data?.stats ?? { done: 0, closed: 0, open: 0 },
      filteredStatusCounts: Option.filter(result.count, (count) => count.error === undefined).pipe(
        Option.map((count) => countJobsByStatus(count.data?.jobs ?? [])),
      ),
      countScope,
      queryScope: JSON.stringify(query),
    } satisfies JobsAuthority;
  });

  const publishJobsAuthority = (authority: JobsAuthority) =>
    Effect.sync(() => {
      if (JSON.stringify(buildQuery()) !== authority.queryScope) return [];
      jobs = sortJobs([...authority.jobs]);
      hasMore = authority.hasMore;
      stats = authority.stats;
      if (Option.isSome(authority.filteredStatusCounts)) {
        filteredStatusCounts = authority.filteredStatusCounts.value;
        filteredStatusCountsScope = authority.countScope;
      }
      const runs = new Set<string>();
      for (const job of jobs) {
        const runUuid = job.panel_run_uuid;
        if (runUuid && wantsPanelMembers(runUuid)) runs.add(runUuid);
      }
      if (interestedPanelRun) runs.add(interestedPanelRun);
      adjustHiddenHighlight();
      return Array.from(runs);
    });

  const refreshJobsAuthority = (query: ListJobsQuery, strictCount: boolean) =>
    fetchJobsAuthority(query, strictCount).pipe(
      Effect.tap((authority) =>
        publishJobsAuthority(authority).pipe(
          Effect.flatMap((expandedRuns) => Effect.forEach(expandedRuns, fetchPanelMembersEffect, { discard: true })),
        ),
      ),
    );

  const loadJobsRequestEffect = (strictCount = false) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      const query = buildQuery();
      return yield* workflow.jobs(
        opts.owner,
        Effect.sync(() => {
          loading = true;
          storeError = null;
          const countScope = JSON.stringify({ ...query, limit: 0, omit_prompt: "true" });
          if (hasActiveFilters() && filteredStatusCountsScope !== countScope) filteredStatusCounts = undefined;
        }).pipe(
          Effect.andThen(refreshJobsAuthority(query, strictCount)),
          Effect.asVoid,
          Effect.ensuring(
            Effect.sync(() => {
              loading = false;
            }),
          ),
        ),
      );
    });

  const loadJobsEffect = () =>
    loadJobsRequestEffect().pipe(
      Effect.catch((failure) =>
        Effect.sync(() => {
          storeError = failure instanceof Error ? failure.message : String(failure);
        }),
      ),
    );

  function loadJobs(): void {
    opts.runtime.runCommand(loadJobsEffect(), {
      operation: "load Roborev jobs",
      safeContext: { owner: opts.owner },
      onFailure: () => {},
    });
  }

  const loadMoreEffect = () =>
    Effect.gen(function* () {
      if (!hasMore || loading || jobs.length === 0) return;
      const workflow = yield* RoborevWorkflow;
      const cursor = Math.min(...jobs.map((job) => job.id));
      const query = buildQuery();
      query.before = cursor;
      yield* workflow.jobs(
        opts.owner,
        Effect.gen(function* () {
          yield* Effect.sync(() => {
            loading = true;
            storeError = null;
          });
          const result = yield* executeRoborevRequest("load more Roborev jobs", (signal) =>
            client.GET("/api/jobs", { params: { query }, signal }),
          );
          if (result.error) {
            return yield* Effect.fail(
              RoborevResponseError.make({
                operation: "load more Roborev jobs",
                message: "Failed to load more jobs",
                cause: result.error,
              }),
            );
          }
          yield* Effect.sync(() => {
            const existingIds = new Set(jobs.map((job) => job.id));
            const fresh = (result.data?.jobs ?? []).filter((job) => !existingIds.has(job.id));
            jobs = sortJobs([...jobs, ...fresh]);
            hasMore = result.data?.has_more ?? false;
          });
        }).pipe(
          Effect.catch((failure) =>
            Effect.sync(() => {
              storeError = failure instanceof Error ? failure.message : String(failure);
            }),
          ),
          Effect.ensuring(
            Effect.sync(() => {
              loading = false;
            }),
          ),
        ),
      );
    });

  function loadMore(): void {
    opts.runtime.runCommand(loadMoreEffect(), {
      operation: "load more Roborev jobs",
      safeContext: { owner: opts.owner },
      onFailure: () => {},
    });
  }

  // Filter actions
  function setFilter(key: StringFilterKey, value: string | undefined): void;
  function setFilter(key: BooleanFilterKey, value: boolean): void;
  function setFilter(key: FilterKey, value: string | boolean | undefined): void {
    switch (key) {
      case "repo":
        if (typeof value === "boolean") return;
        filterRepo = value;
        break;
      case "branch":
        if (typeof value === "boolean") return;
        filterBranch = value;
        break;
      case "status":
        if (typeof value === "boolean") return;
        filterStatus = value;
        break;
      case "search":
        if (typeof value === "boolean") return;
        filterSearch = value;
        break;
      case "hideClosed":
        if (typeof value !== "boolean") return;
        filterHideClosed = value;
        break;
      case "jobType":
        if (typeof value === "boolean") return;
        filterJobType = value;
        break;
      case "showAutoDesign":
        if (typeof value !== "boolean") return;
        filterShowAutoDesign = value;
        break;
    }
    loadJobs();
  }

  function setRepoBranchFilter(repo: string | undefined, branch: string | undefined): void {
    filterRepo = repo;
    filterBranch = branch;
    loadJobs();
  }

  function setSortColumn(col: SortColumn): void {
    if (sortColumn === col) {
      sortDirection = sortDirection === "asc" ? "desc" : "asc";
    } else {
      sortColumn = col;
      sortDirection = col === "id" ? "desc" : "asc";
    }
    jobs = sortJobs(jobs);
  }

  // Job actions
  const fetchJobAuthority = Effect.fn("RoborevJobs.fetchJobAuthority")(function* (id: number) {
    const result = yield* executeRoborevRequest("load authoritative Roborev job", (signal) =>
      client.GET("/api/jobs", {
        params: { query: { id, limit: 1, omit_prompt: "true" } satisfies ListJobsQuery },
        signal,
      }),
    );
    if (result.error !== undefined) {
      return yield* Effect.fail(
        RoborevResponseError.make({
          operation: "load authoritative Roborev job",
          message: "Failed to revalidate job",
          cause: result.error,
        }),
      );
    }
    return result.data?.jobs?.[0];
  });

  const reconcileJobMutation = <A>(
    id: number,
    query: ListJobsQuery,
    acknowledged: Option.Option<A>,
    observedValue: A,
    isApplied: (job: ReviewJob) => boolean,
  ) =>
    Effect.all(
      {
        target: fetchJobAuthority(id),
        page: refreshJobsAuthority(query, true),
      },
      { concurrency: "unbounded" },
    ).pipe(
      Effect.map(({ target }) =>
        target !== undefined && isApplied(target)
          ? Option.some(Option.getOrElse(acknowledged, () => observedValue))
          : Option.none<A>(),
      ),
    );

  const cancelJobEffect = (id: number) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      const query = buildQuery();
      const observedValue = { success: true } satisfies CancelJobResponse;
      return yield* workflow.mutate({
        key: `job:${id}`,
        operation: "cancel Roborev job",
        mutation: executeRoborevRequest("cancel Roborev job", (signal) =>
          client.POST("/api/job/cancel", {
            body: { job_id: id },
            signal,
          }),
        ).pipe(
          Effect.flatMap((result) =>
            result.error || !result.data
              ? Effect.fail(
                  RoborevMutationError.make({
                    operation: "cancel Roborev job",
                    cause: result.error ?? new Error("Roborev cancellation response was empty"),
                  }),
                )
              : Effect.succeed(result.data),
          ),
        ),
        reconcile: (acknowledged) =>
          reconcileJobMutation(id, query, acknowledged, observedValue, (job) => job.status === "canceled"),
        onAcknowledgedRefreshFailure: () =>
          Effect.sync(() => opts.onError?.("Job was canceled, but the refreshed job list is unavailable")),
      });
    });

  function cancelJob(id: number): void {
    opts.runtime.runCommand(cancelJobEffect(id), {
      operation: "cancel Roborev job",
      safeContext: { job_id: id, owner: opts.owner },
      onFailure: (failure) => opts.onError?.(roborevMutationFailureMessage("Failed to cancel job", failure)),
    });
  }

  const rerunJobEffect = (id: number) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      const query = buildQuery();
      const baselineRetryCount = yield* Ref.make<Option.Option<number>>(Option.none());
      const observedValue = { success: true } satisfies RerunJobResponse;
      return yield* workflow.mutate({
        key: `job:${id}`,
        operation: "rerun Roborev job",
        mutation: Effect.gen(function* () {
          const baseline = yield* fetchJobAuthority(id).pipe(
            Effect.catchTag("TransientTransportError", (cause) =>
              Effect.fail(
                RoborevResponseError.make({
                  operation: "load authoritative Roborev rerun baseline",
                  message: "Failed to load Roborev rerun baseline before submission",
                  cause,
                }),
              ),
            ),
          );
          if (baseline === undefined) {
            return yield* Effect.fail(
              RoborevResponseError.make({
                operation: "load authoritative Roborev rerun baseline",
                message: "Roborev job was not found before rerun",
                cause: new Error(`missing Roborev job ${id}`),
              }),
            );
          }
          yield* Ref.set(baselineRetryCount, Option.some(baseline.retry_count));
          const result = yield* executeRoborevRequest("rerun Roborev job", (signal) =>
            client.POST("/api/job/rerun", {
              body: { job_id: id },
              signal,
            }),
          );
          return yield* result.error || !result.data
            ? Effect.fail(
                RoborevMutationError.make({
                  operation: "rerun Roborev job",
                  cause: result.error ?? new Error("Roborev rerun response was empty"),
                }),
              )
            : Effect.succeed(result.data);
        }),
        reconcile: (acknowledged) =>
          Ref.get(baselineRetryCount).pipe(
            Effect.flatMap(
              Option.match({
                onNone: () =>
                  Effect.fail(
                    RoborevResponseError.make({
                      operation: "reconcile Roborev rerun",
                      message: "Rerun baseline was unavailable during reconciliation",
                      cause: new Error(`missing Roborev rerun baseline ${id}`),
                    }),
                  ),
                onSome: (retryCount) =>
                  reconcileJobMutation(id, query, acknowledged, observedValue, (job) => job.retry_count > retryCount),
              }),
            ),
          ),
        onAcknowledgedRefreshFailure: () =>
          Effect.sync(() => opts.onError?.("Job was rerun, but the refreshed job list is unavailable")),
      });
    });

  function rerunJob(id: number): void {
    opts.runtime.runCommand(rerunJobEffect(id), {
      operation: "rerun Roborev job",
      safeContext: { job_id: id, owner: opts.owner },
      onFailure: (failure) => opts.onError?.(roborevMutationFailureMessage("Failed to rerun job", failure)),
    });
  }

  const fetchPanelMembersEffect = (runUuid: string) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      yield* workflow.panel(opts.owner, runUuid, {
        onStart: Effect.sync(() => {
          loadingMembers = { ...loadingMembers, [runUuid]: true };
          const { [runUuid]: _startedError, ...startedErrors } = panelMemberErrors;
          panelMemberErrors = startedErrors;
        }),
        read: executeRoborevRequest("load Roborev panel members", (signal) =>
          client.GET("/api/jobs", {
            params: { query: { panel_run: runUuid, limit: 0, omit_prompt: "true" } },
            signal,
          }),
        ).pipe(
          Effect.flatMap((result) =>
            result.error
              ? Effect.fail(
                  RoborevResponseError.make({
                    operation: "load Roborev panel members",
                    message: "Failed to load panel members",
                    cause: result.error,
                  }),
                )
              : Effect.succeed(
                  (result.data?.jobs ?? [])
                    .filter((job) => job.panel_role === "member")
                    .sort((a, b) => (a.panel_member_index ?? 0) - (b.panel_member_index ?? 0)),
                ),
          ),
        ),
        onSuccess: (members) =>
          Effect.sync(() => {
            panelMembers = { ...panelMembers, [runUuid]: members };
            const { [runUuid]: _memberError, ...memberErrors } = panelMemberErrors;
            panelMemberErrors = memberErrors;
            adjustHiddenHighlight(runUuid);
            if (sortColumn === "cost" || sortColumn === "elapsed") jobs = sortJobs(jobs);
          }),
        onFailure: (failure) =>
          Effect.sync(() => {
            const message = failure instanceof Error ? failure.message : String(failure);
            panelMemberErrors = { ...panelMemberErrors, [runUuid]: message };
            opts.onError?.(message);
          }),
        onSettled: Effect.sync(() => {
          loadingMembers = { ...loadingMembers, [runUuid]: false };
        }),
      });
    });

  function fetchPanelMembers(runUuid: string): void {
    opts.runtime.runCommand(fetchPanelMembersEffect(runUuid), {
      operation: "load Roborev panel members",
      safeContext: { owner: opts.owner },
      onFailure: () => {},
    });
  }

  function togglePanel(job: ReviewJob): void {
    if (!isPanelParent(job)) return;
    const runUuid = job.panel_run_uuid;
    if (!runUuid) return;
    const open = expandedPanels[runUuid] === true;
    if (open && highlightedJobId !== undefined) {
      const highlightedMember = panelMembers[runUuid]?.some((member) => member.id === highlightedJobId) ?? false;
      if (highlightedMember) highlightedJobId = job.id;
    }
    expandedPanels = { ...expandedPanels, [runUuid]: !open };
    if (!open && panelMembers[runUuid] === undefined && loadingMembers[runUuid] !== true) {
      fetchPanelMembers(runUuid);
    }
  }

  function ensurePanelMembers(runUuid: string): void {
    if (panelMembers[runUuid] === undefined && loadingMembers[runUuid] !== true) {
      fetchPanelMembers(runUuid);
    }
  }

  function setPanelMemberInterest(runUuid: string | undefined): void {
    interestedPanelRun = runUuid;
    if (runUuid !== undefined) fetchPanelMembers(runUuid);
  }

  function refreshPanelMembers(runUuid: string): void {
    fetchPanelMembers(runUuid);
  }

  function adjustHiddenHighlight(runUuid?: string): void {
    if (highlightedJobId === undefined) return;
    if (getVisibleJobs().some((job) => job.id === highlightedJobId)) return;
    const parent =
      runUuid !== undefined
        ? jobs.find((job) => isPanelParent(job) && job.panel_run_uuid === runUuid)
        : getPanelParentForMemberId(highlightedJobId);
    highlightedJobId = parent?.id;
  }

  function isPanelExpanded(runUuid: string): boolean {
    return expandedPanels[runUuid] === true;
  }

  function getPanelMembers(runUuid: string): ReviewJob[] | undefined {
    return panelMembers[runUuid];
  }

  function getPanelMemberError(runUuid: string): string | undefined {
    return panelMemberErrors[runUuid];
  }

  function isLoadingMembers(runUuid: string): boolean {
    return loadingMembers[runUuid] === true;
  }

  // Selection — setSelectedJobId sets state only (no
  // navigation), used by the route-sync effect to avoid
  // an infinite effect_update_depth_exceeded cycle.
  function setSelectedJobId(id: number | undefined): void {
    selectedJobId = id;
  }

  function selectJob(id: number): void {
    selectedJobId = id;
    highlightedJobId = id;
    if (!window.location.pathname.endsWith(`/reviews/${id}`)) {
      opts.navigate(`/reviews/${id}`);
    }
  }

  function deselectJob(): void {
    selectedJobId = undefined;
    opts.navigate("/reviews");
  }

  const connectEventStreamEffect = (baseUrl: string, eventOwner: string) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      const reconcile = loadJobsRequestEffect(true).pipe(
        Effect.provideService(RoborevWorkflow, workflow),
        Effect.mapError((cause) =>
          RoborevStreamError.make({
            operation: "reconcile Roborev jobs before reconnect",
            retryable: true,
            cause,
          }),
        ),
      );
      yield* workflow.connectEvents({
        owner: eventOwner,
        baseUrl,
        onOpen: Effect.sync(() => {
          if (activeEventOwner !== eventOwner) return;
          eventStreamConnected = true;
        }),
        onEvent: () => reconcile,
        onReconnect: () => reconcile,
        onError: () =>
          Effect.sync(() => {
            if (activeEventOwner !== eventOwner) return;
            eventStreamConnected = false;
          }),
      });
    });

  function connectEventStream(baseUrl: string): string {
    const eventOwner = makeRoborevOwner(`${opts.owner}:events`);
    activeEventOwner = eventOwner;
    eventStreamConnected = false;
    opts.runtime.runCommand(connectEventStreamEffect(baseUrl, eventOwner), {
      operation: "connect Roborev event stream",
      safeContext: { owner: eventOwner },
      onFailure: () => {
        if (activeEventOwner !== eventOwner) return;
        eventStreamConnected = false;
      },
    });
    return eventOwner;
  }

  const disconnectEventStreamEffect = (eventOwner: string) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      yield* workflow.disconnectEvents(eventOwner);
      yield* Effect.sync(() => {
        if (activeEventOwner !== eventOwner) return;
        activeEventOwner = undefined;
        eventStreamConnected = false;
      });
    });

  function disconnectEventStream(eventOwner: string): void {
    opts.runtime.runCommand(disconnectEventStreamEffect(eventOwner), {
      operation: "disconnect Roborev event stream",
      safeContext: { owner: eventOwner },
      onFailure: () => {},
    });
  }

  // Selection helpers for keyboard nav
  function selectNextJob(): void {
    const visibleJobs = getVisibleJobs();
    if (visibleJobs.length === 0) return;
    if (selectedJobId === undefined) {
      selectJob(visibleJobs[0]!.id);
      return;
    }
    const idx = visibleJobs.findIndex((j) => j.id === selectedJobId);
    if (idx < visibleJobs.length - 1) {
      selectJob(visibleJobs[idx + 1]!.id);
    }
  }

  function selectPrevJob(): void {
    const visibleJobs = getVisibleJobs();
    if (visibleJobs.length === 0) return;
    if (selectedJobId === undefined) {
      selectJob(visibleJobs[visibleJobs.length - 1]!.id);
      return;
    }
    const idx = visibleJobs.findIndex((j) => j.id === selectedJobId);
    if (idx > 0) {
      selectJob(visibleJobs[idx - 1]!.id);
    }
  }

  function getVisibleJobs(): ReviewJob[] {
    const visible: ReviewJob[] = [];
    for (const job of jobs) {
      visible.push(job);
      const runUuid = job.panel_run_uuid;
      if (runUuid && expandedPanels[runUuid] === true && panelMembers[runUuid] !== undefined) {
        visible.push(...(panelMembers[runUuid] ?? []));
      }
    }
    return visible;
  }

  // Highlight navigation (j/k without opening drawer)
  function highlightJob(id: number): void {
    highlightedJobId = id;
  }

  function highlightNextJob(): void {
    const visibleJobs = getVisibleJobs();
    if (visibleJobs.length === 0) return;
    if (highlightedJobId === undefined) {
      highlightedJobId = visibleJobs[0]!.id;
      return;
    }
    const idx = visibleJobs.findIndex((j) => j.id === highlightedJobId);
    if (idx < visibleJobs.length - 1) {
      highlightedJobId = visibleJobs[idx + 1]!.id;
    }
  }

  function highlightPrevJob(): void {
    const visibleJobs = getVisibleJobs();
    if (visibleJobs.length === 0) return;
    if (highlightedJobId === undefined) {
      highlightedJobId = visibleJobs[visibleJobs.length - 1]!.id;
      return;
    }
    const idx = visibleJobs.findIndex((j) => j.id === highlightedJobId);
    if (idx > 0) {
      highlightedJobId = visibleJobs[idx - 1]!.id;
    }
  }

  // Getters
  function getJobs(): ReviewJob[] {
    return jobs;
  }
  function isLoading(): boolean {
    return loading;
  }
  function getHasMore(): boolean {
    return hasMore;
  }
  function getStats(): JobStats {
    return stats;
  }
  function getFilteredStatusCounts(): JobStatusCounts | undefined {
    return filteredStatusCounts;
  }
  function usesFilteredStatusCounts(): boolean {
    return hasActiveFilters();
  }
  function getError(): string | null {
    return storeError;
  }
  function getSelectedJobId(): number | undefined {
    return selectedJobId;
  }
  function getHighlightedJobId(): number | undefined {
    return highlightedJobId;
  }
  function getFilterRepo(): string | undefined {
    return filterRepo;
  }
  function getFilterBranch(): string | undefined {
    return filterBranch;
  }
  function getFilterStatus(): string | undefined {
    return filterStatus;
  }
  function getFilterSearch(): string | undefined {
    return filterSearch;
  }
  function getFilterHideClosed(): boolean {
    return filterHideClosed;
  }
  function getFilterJobType(): string | undefined {
    return filterJobType;
  }
  function getFilterShowAutoDesign(): boolean {
    return filterShowAutoDesign;
  }
  function getSortColumn(): SortColumn {
    return sortColumn;
  }
  function getSortDirection(): SortDirection {
    return sortDirection;
  }
  function isEventStreamConnected(): boolean {
    return eventStreamConnected;
  }

  return {
    getJobs,
    getVisibleJobs,
    isLoading,
    getHasMore,
    getStats,
    getFilteredStatusCounts,
    usesFilteredStatusCounts,
    getError,
    getSelectedJobId,
    getHighlightedJobId,
    getFilterRepo,
    getFilterBranch,
    getFilterStatus,
    getFilterSearch,
    getFilterHideClosed,
    getFilterJobType,
    getFilterShowAutoDesign,
    getSortColumn,
    getSortDirection,
    isEventStreamConnected,
    togglePanel,
    ensurePanelMembers,
    setPanelMemberInterest,
    refreshPanelMembers,
    isPanelExpanded,
    getPanelMembers,
    getPanelMemberError,
    isLoadingMembers,
    loadJobs,
    loadJobsEffect,
    loadMore,
    loadMoreEffect,
    setFilter,
    setRepoBranchFilter,
    setSortColumn,
    cancelJob,
    cancelJobEffect,
    rerunJob,
    rerunJobEffect,
    setSelectedJobId,
    selectJob,
    deselectJob,
    selectNextJob,
    selectPrevJob,
    highlightJob,
    highlightNextJob,
    highlightPrevJob,
    connectEventStream,
    connectEventStreamEffect,
    disconnectEventStream,
    disconnectEventStreamEffect,
  };
}

export type JobsStore = ReturnType<typeof createJobsStore>;
