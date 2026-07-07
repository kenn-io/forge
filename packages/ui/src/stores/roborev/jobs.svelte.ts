import type { RoborevClient } from "../../api/roborev/client.js";
import type { components, operations } from "../../api/roborev/generated/schema.js";
import { isPanelParent, panelCostUsd, panelElapsedStart } from "../../utils/roborev-panel.js";

type ReviewJob = components["schemas"]["ReviewJob"];
type JobStats = components["schemas"]["JobStats"];
type ListJobsQuery = NonNullable<operations["list-jobs"]["parameters"]["query"]>;

export interface JobsStoreOptions {
  client: RoborevClient;
  navigate: (path: string) => void;
  onError?: (msg: string) => void;
}

type SortColumn = "id" | "status" | "verdict" | "agent" | "elapsed" | "cost" | "job_type" | "enqueued_at";
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
  let panelRequestedVersions: Record<string, number> = {};
  let activePanelFetchVersions: Record<string, number> = {};
  let pendingPanelRefreshes: Record<string, boolean> = {};
  let interestedPanelRun: string | undefined = undefined;

  // SSE
  let sseConnected = $state(false);
  let eventSource: EventSource | null = null;

  // Version tracking for race conditions
  let requestVersion = 0;

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
      const expandedRuns: Record<string, true> = {};
      for (const job of jobs) {
        const runUuid = job.panel_run_uuid;
        if (runUuid && wantsPanelMembers(runUuid)) {
          expandedRuns[runUuid] = true;
        }
      }
      if (interestedPanelRun) expandedRuns[interestedPanelRun] = true;
      for (const runUuid of Object.keys(expandedRuns)) void fetchPanelMembers(runUuid);
      // Clear highlight if the row is no longer visible.
      // Do NOT clear selectedJobId — the selected job may
      // be on a later page (deep link, older job). The
      // drawer fetches its review independently.
      adjustHiddenHighlight();
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
      case "showAutoDesign":
        filterShowAutoDesign = value as boolean;
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

  async function fetchPanelMembers(runUuid: string): Promise<void> {
    const version = (panelRequestedVersions[runUuid] ?? 0) + 1;
    panelRequestedVersions = { ...panelRequestedVersions, [runUuid]: version };
    if (loadingMembers[runUuid] === true) {
      pendingPanelRefreshes = { ...pendingPanelRefreshes, [runUuid]: true };
      return;
    }

    await runPanelMembersFetch(runUuid, version);
  }

  async function runPanelMembersFetch(runUuid: string, version: number): Promise<void> {
    activePanelFetchVersions = { ...activePanelFetchVersions, [runUuid]: version };
    loadingMembers = { ...loadingMembers, [runUuid]: true };
    const { [runUuid]: _startedError, ...startedErrors } = panelMemberErrors;
    panelMemberErrors = startedErrors;
    try {
      const { data, error } = await client.GET("/api/jobs", {
        params: { query: { panel_run: runUuid, limit: 0, omit_prompt: "true" } },
      });
      if (error) throw new Error("Failed to load panel members");
      if (panelRequestedVersions[runUuid] !== version) return;
      const members = (data?.jobs ?? [])
        .filter((job) => job.panel_role === "member")
        .sort((a, b) => (a.panel_member_index ?? 0) - (b.panel_member_index ?? 0));
      panelMembers = { ...panelMembers, [runUuid]: members };
      const { [runUuid]: _memberError, ...memberErrors } = panelMemberErrors;
      panelMemberErrors = memberErrors;
      adjustHiddenHighlight(runUuid);
      if (sortColumn === "cost" || sortColumn === "elapsed") {
        jobs = sortJobs(jobs);
      }
    } catch (err) {
      if (panelRequestedVersions[runUuid] === version) {
        const message = err instanceof Error ? err.message : String(err);
        panelMemberErrors = { ...panelMemberErrors, [runUuid]: message };
        opts.onError?.(message);
      }
    } finally {
      if (activePanelFetchVersions[runUuid] === version) {
        const { [runUuid]: _active, ...activeRest } = activePanelFetchVersions;
        activePanelFetchVersions = activeRest;
        loadingMembers = { ...loadingMembers, [runUuid]: false };
        if (pendingPanelRefreshes[runUuid] === true) {
          const { [runUuid]: _pending, ...rest } = pendingPanelRefreshes;
          pendingPanelRefreshes = rest;
          const queuedVersion = panelRequestedVersions[runUuid];
          if (queuedVersion !== undefined && queuedVersion > version && wantsPanelMembers(runUuid)) {
            void runPanelMembersFetch(runUuid, queuedVersion);
          }
        }
      }
    }
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
      void fetchPanelMembers(runUuid);
    }
  }

  function ensurePanelMembers(runUuid: string): void {
    if (panelMembers[runUuid] === undefined && loadingMembers[runUuid] !== true) {
      void fetchPanelMembers(runUuid);
    }
  }

  function setPanelMemberInterest(runUuid: string | undefined): void {
    interestedPanelRun = runUuid;
    if (runUuid !== undefined) void fetchPanelMembers(runUuid);
  }

  function refreshPanelMembers(runUuid: string): void {
    void fetchPanelMembers(runUuid);
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
  function isSSEConnected(): boolean {
    return sseConnected;
  }

  return {
    getJobs,
    getVisibleJobs,
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
    getFilterShowAutoDesign,
    getSortColumn,
    getSortDirection,
    isSSEConnected,
    togglePanel,
    ensurePanelMembers,
    setPanelMemberInterest,
    refreshPanelMembers,
    isPanelExpanded,
    getPanelMembers,
    getPanelMemberError,
    isLoadingMembers,
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
