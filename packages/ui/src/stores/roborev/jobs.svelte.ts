import type { RoborevClient } from "../../api/roborev/client.js";
import type { components, operations } from "../../api/roborev/generated/schema.js";

type ReviewJob = components["schemas"]["ReviewJob"];
type JobStats = components["schemas"]["JobStats"];
type ListJobsQuery = NonNullable<operations["list-jobs"]["parameters"]["query"]>;

export interface JobsStoreOptions {
  client: RoborevClient;
  navigate: (path: string) => void;
  onError?: (msg: string) => void;
}

type SortColumn = "id" | "status" | "verdict" | "agent" | "elapsed" | "job_type" | "enqueued_at";
type SortDirection = "asc" | "desc";

export function createJobsStore(opts: JobsStoreOptions) {
  const client = opts.client;

  // State
  let jobs = $state<ReviewJob[]>([]);
  let loading = $state(false);
  let hasMore = $state(false);
  let stats = $state<JobStats>({ done: 0, closed: 0, open: 0 });
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

  // Sorting (client-side)
  let sortColumn = $state<SortColumn>("id");
  let sortDirection = $state<SortDirection>("desc");

  // SSE
  let sseConnected = $state(false);
  let eventSource: EventSource | null = null;

  // Version tracking for race conditions
  let requestVersion = 0;

  function buildQuery(): ListJobsQuery {
    const q: ListJobsQuery = { limit: 50 };
    if (filterRepo) q.repo = filterRepo;
    if (filterBranch) q.branch = filterBranch;
    if (filterStatus) q.status = filterStatus;
    if (filterSearch) q.git_ref = filterSearch;
    if (filterHideClosed) q.closed = "false";
    if (filterJobType) q.job_type = filterJobType;
    return q;
  }

  function getElapsedSeconds(job: ReviewJob): number {
    if (!job.started_at) return -1;
    const start = new Date(job.started_at).getTime();
    const end = job.finished_at ? new Date(job.finished_at).getTime() : Date.now();
    return Math.max(0, Math.floor((end - start) / 1000));
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

  async function loadJobs(): Promise<void> {
    const version = ++requestVersion;
    loading = true;
    storeError = null;
    try {
      const { data, error } = await client.GET("/api/jobs", {
        params: { query: buildQuery() },
      });
      if (error) throw new Error("Failed to load jobs");
      if (version !== requestVersion) return;
      jobs = sortJobs(data?.jobs ?? []);
      hasMore = data?.has_more ?? false;
      stats = data?.stats ?? { done: 0, closed: 0, open: 0 };
      // Clear highlight if the row is no longer visible.
      // Do NOT clear selectedJobId — the selected job may
      // be on a later page (deep link, older job). The
      // drawer fetches its review independently.
      if (highlightedJobId !== undefined) {
        const ids = new Set(jobs.map((j) => j.id));
        if (!ids.has(highlightedJobId)) {
          highlightedJobId = undefined;
        }
      }
    } catch (err) {
      if (version !== requestVersion) return;
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      if (version === requestVersion) loading = false;
    }
  }

  async function loadMore(): Promise<void> {
    if (!hasMore || loading || jobs.length === 0) return;
    const cursor = Math.min(...jobs.map((j) => j.id));
    const version = ++requestVersion;
    loading = true;
    try {
      const q = buildQuery();
      q.before = cursor;
      const { data, error } = await client.GET("/api/jobs", {
        params: { query: q },
      });
      if (error) {
        throw new Error("Failed to load more jobs");
      }
      if (version !== requestVersion) return;
      const fresh = data?.jobs ?? [];
      const existingIds = new Set(jobs.map((j) => j.id));
      const newJobs = fresh.filter((j) => !existingIds.has(j.id));
      jobs = sortJobs([...jobs, ...newJobs]);
      hasMore = data?.has_more ?? false;
    } catch (err) {
      if (version !== requestVersion) return;
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      if (version === requestVersion) loading = false;
    }
  }

  // Filter actions
  function setFilter(key: string, value: string | boolean | undefined): void {
    switch (key) {
      case "repo":
        filterRepo = value as string | undefined;
        break;
      case "branch":
        filterBranch = value as string | undefined;
        break;
      case "status":
        filterStatus = value as string | undefined;
        break;
      case "search":
        filterSearch = value as string | undefined;
        break;
      case "hideClosed":
        filterHideClosed = value as boolean;
        break;
      case "jobType":
        filterJobType = value as string | undefined;
        break;
    }
    void loadJobs();
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
  async function cancelJob(id: number): Promise<void> {
    const { error } = await client.POST("/api/job/cancel", {
      body: { job_id: id },
    });
    if (error) {
      opts.onError?.("Failed to cancel job");
      return;
    }
    jobs = jobs.map((j) => (j.id === id ? { ...j, status: "canceled" } : j));
    void loadJobs();
  }

  async function rerunJob(id: number): Promise<void> {
    const { error } = await client.POST("/api/job/rerun", {
      body: { job_id: id },
    });
    if (error) {
      opts.onError?.("Failed to rerun job");
      return;
    }
    void loadJobs();
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

  // SSE for real-time updates
  function connectSSE(baseUrl: string): void {
    disconnectSSE();
    const url = `${baseUrl}/api/stream/events`;
    eventSource = new EventSource(url);
    eventSource.onopen = () => {
      sseConnected = true;
    };
    eventSource.onerror = () => {
      sseConnected = false;
    };
    eventSource.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data);
        if (data.type === "job.status_changed" || data.type === "review.completed") {
          void loadJobs();
        }
      } catch {
        // Ignore parse errors from malformed SSE data
      }
    };
  }

  function disconnectSSE(): void {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
      sseConnected = false;
    }
  }

  // Selection helpers for keyboard nav
  function selectNextJob(): void {
    if (jobs.length === 0) return;
    if (selectedJobId === undefined) {
      selectJob(jobs[0]!.id);
      return;
    }
    const idx = jobs.findIndex((j) => j.id === selectedJobId);
    if (idx < jobs.length - 1) {
      selectJob(jobs[idx + 1]!.id);
    }
  }

  function selectPrevJob(): void {
    if (jobs.length === 0) return;
    if (selectedJobId === undefined) {
      selectJob(jobs[jobs.length - 1]!.id);
      return;
    }
    const idx = jobs.findIndex((j) => j.id === selectedJobId);
    if (idx > 0) {
      selectJob(jobs[idx - 1]!.id);
    }
  }

  // Highlight navigation (j/k without opening drawer)
  function highlightJob(id: number): void {
    highlightedJobId = id;
  }

  function highlightNextJob(): void {
    if (jobs.length === 0) return;
    if (highlightedJobId === undefined) {
      highlightedJobId = jobs[0]!.id;
      return;
    }
    const idx = jobs.findIndex((j) => j.id === highlightedJobId);
    if (idx < jobs.length - 1) {
      highlightedJobId = jobs[idx + 1]!.id;
    }
  }

  function highlightPrevJob(): void {
    if (jobs.length === 0) return;
    if (highlightedJobId === undefined) {
      highlightedJobId = jobs[jobs.length - 1]!.id;
      return;
    }
    const idx = jobs.findIndex((j) => j.id === highlightedJobId);
    if (idx > 0) {
      highlightedJobId = jobs[idx - 1]!.id;
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
  function getSortColumn(): SortColumn {
    return sortColumn;
  }
  function getSortDirection(): SortDirection {
    return sortDirection;
  }
  function isSSEConnected(): boolean {
    return sseConnected;
  }

  return {
    getJobs,
    isLoading,
    getHasMore,
    getStats,
    getError,
    getSelectedJobId,
    getHighlightedJobId,
    getFilterRepo,
    getFilterBranch,
    getFilterStatus,
    getFilterSearch,
    getFilterHideClosed,
    getFilterJobType,
    getSortColumn,
    getSortDirection,
    isSSEConnected,
    loadJobs,
    loadMore,
    setFilter,
    setSortColumn,
    cancelJob,
    rerunJob,
    setSelectedJobId,
    selectJob,
    deselectJob,
    selectNextJob,
    selectPrevJob,
    highlightJob,
    highlightNextJob,
    highlightPrevJob,
    connectSSE,
    disconnectSSE,
  };
}

export type JobsStore = ReturnType<typeof createJobsStore>;
