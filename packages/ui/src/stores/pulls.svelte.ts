import type { KanbanStatus, PullRequest } from "../api/types.js";
import {
  providerDefaultHost,
  providerItemPath,
  providerRouteParams,
  type ProviderRouteRef,
} from "../api/provider-routes.js";
import type { MiddlemanClient } from "../types.js";
import { bucketCIChecks, parseCIChecks } from "../utils/ci-buckets.js";
import { normalizeKanbanStatus } from "./workflow.svelte.js";

export type FetchPullResult =
  | { status: "found"; pull: PullRequest }
  | { status: "not-found" }
  | { status: "error"; message: string };

export interface PullSelection {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
}

type PullIdentityRef = ProviderRouteRef;

type PullsParams = {
  repo?: string;
  state?: string;
  kanban?: KanbanStatus;
  starred?: boolean;
  q?: string;
  limit?: number;
  offset?: number;
};

export type PullAttributeFilter = "approved" | "draft" | "ready" | "merge_conflicts" | "failed_ci";

export interface PullsStoreOptions {
  client: MiddlemanClient;
  getGlobalRepo?: () => string | undefined;
  getGroupByRepo?: () => boolean;
  getView?: () => "list" | "board";
}

function apiErrorMessage(error: { detail?: string; title?: string }, fallback: string): string {
  return error.detail ?? error.title ?? fallback;
}

export function createPullsStore(opts: PullsStoreOptions) {
  const apiClient = opts.client;
  const getGlobalRepo = opts.getGlobalRepo ?? (() => undefined);
  const getGroupByRepo = opts.getGroupByRepo ?? (() => false);
  const getView = opts.getView ?? ((): "list" | "board" => "list");

  // --- state ---

  let pulls = $state<PullRequest[]>([]);
  let loading = $state(false);
  let storeError = $state<string | null>(null);
  let filterKanban = $state<KanbanStatus | undefined>(undefined);
  let attributeFilters = $state<PullAttributeFilter[]>([]);
  let kanbanStatusFilters = $state<KanbanStatus[]>([]);
  let filterStarred = $state(false);
  let filterState = $state<string>("open");
  let searchQuery = $state<string | undefined>(undefined);
  let selectedPR = $state<PullSelection | null>(null);

  // --- reads ---

  function getPulls(): PullRequest[] {
    return pulls;
  }

  function getFilteredPulls(): PullRequest[] {
    return pulls.filter((pr) => matchesAttributeFilters(pr) && matchesKanbanStatusFilters(pr));
  }

  function isLoading(): boolean {
    return loading;
  }

  function getError(): string | null {
    return storeError;
  }

  function getSelectedPR(): PullSelection | null {
    return selectedPR;
  }

  function pullIdentityKey(ref: Pick<PullIdentityRef, "provider" | "platformHost" | "repoPath">): string {
    return JSON.stringify([ref.provider, ref.platformHost ?? "", ref.repoPath]);
  }

  function pullRef(pr: PullRequest): PullIdentityRef {
    return {
      provider: pr.repo.provider,
      platformHost: pr.repo.platform_host,
      owner: pr.repo.owner,
      name: pr.repo.name,
      repoPath: pr.repo.repo_path,
    };
  }

  function pullMatchesRef(pr: PullRequest, ref: PullIdentityRef, number: number): boolean {
    return (
      pr.Number === number &&
      pr.repo.provider === ref.provider &&
      pr.repo.platform_host === ref.platformHost &&
      pr.repo.repo_path === ref.repoPath &&
      pr.repo.owner === ref.owner &&
      pr.repo.name === ref.name
    );
  }

  function pullMatchesSelection(pr: PullRequest, sel: PullSelection): boolean {
    return pullMatchesRef(pr, sel, sel.number);
  }

  function concretePlatformHost(ref: Pick<PullIdentityRef, "provider" | "platformHost">): string {
    const host = ref.platformHost ?? providerDefaultHost(ref.provider);
    if (!host) throw new Error("pull request missing platform host");
    return host;
  }

  /** Groups pulls by full provider identity into a Map. */
  function pullsByRepo(): Map<string, PullRequest[]> {
    const map = new Map<string, PullRequest[]>();
    for (const pr of getFilteredPulls()) {
      const key = pullIdentityKey(pullRef(pr));
      const existing = map.get(key);
      if (existing !== undefined) {
        existing.push(pr);
      } else {
        map.set(key, [pr]);
      }
    }
    return map;
  }

  function getFilterKanban(): KanbanStatus | undefined {
    return filterKanban;
  }

  function getAttributeFilters(): PullAttributeFilter[] {
    return attributeFilters;
  }

  function getKanbanStatusFilters(): KanbanStatus[] {
    return kanbanStatusFilters;
  }

  function getLocalFilterCount(): number {
    return attributeFilters.length + kanbanStatusFilters.length;
  }

  function getFilterStarred(): boolean {
    return filterStarred;
  }

  function setFilterStarred(v: boolean): void {
    filterStarred = v;
  }

  function getFilterState(): string {
    return filterState;
  }

  function setFilterState(s: string): void {
    filterState = s;
  }

  /**
   * Returns PRs in display order: grouped by repo when
   * groupByRepo is true or when in board view, flat
   * chronological otherwise.
   */
  function getDisplayOrderPRs(): PullRequest[] {
    if (getGroupByRepo() || getView() === "board") {
      const grouped = pullsByRepo();
      const ordered: PullRequest[] = [];
      for (const prs of grouped.values()) {
        ordered.push(...prs);
      }
      return ordered;
    }
    return getFilteredPulls();
  }

  function selectNextPR(): void {
    const list = getDisplayOrderPRs();
    if (list.length === 0) return;
    const sel = selectedPR;
    if (sel === null) {
      const first = list[0];
      if (first !== undefined) {
        selectPRFromPull(first);
      }
      return;
    }
    const idx = list.findIndex((pr) => pullMatchesSelection(pr, sel));
    const next = list[idx + 1];
    if (next !== undefined) {
      selectPRFromPull(next);
    }
  }

  function selectPrevPR(): void {
    const list = getDisplayOrderPRs();
    if (list.length === 0) return;
    const sel = selectedPR;
    if (sel === null) {
      const last = list[list.length - 1];
      if (last !== undefined) {
        selectPRFromPull(last);
      }
      return;
    }
    const idx = list.findIndex((pr) => pullMatchesSelection(pr, sel));
    if (idx > 0) {
      const prev = list[idx - 1];
      if (prev !== undefined) {
        selectPRFromPull(prev);
      }
    }
  }

  // --- writes ---

  function setFilterKanban(kanban: KanbanStatus | undefined): void {
    filterKanban = kanban;
  }

  function toggleAttributeFilter(filter: PullAttributeFilter): void {
    attributeFilters = toggleFilterValue(attributeFilters, filter);
  }

  function toggleKanbanStatusFilter(status: KanbanStatus): void {
    kanbanStatusFilters = toggleFilterValue(kanbanStatusFilters, status);
  }

  function clearLocalFilters(): void {
    attributeFilters = [];
    kanbanStatusFilters = [];
  }

  function getSearchQuery(): string | undefined {
    return searchQuery;
  }

  function setSearchQuery(q: string | undefined): void {
    searchQuery = q;
  }

  function selectPR(
    owner: string,
    name: string,
    number: number,
    provider: string,
    platformHost: string | undefined,
    repoPath: string,
  ): void {
    selectedPR = {
      provider,
      ...(platformHost && { platformHost }),
      owner,
      name,
      repoPath,
      number,
    };
  }

  function selectPRFromPull(pr: PullRequest): void {
    const ref = pullRef(pr);
    selectPR(ref.owner, ref.name, pr.Number, ref.provider, ref.platformHost, ref.repoPath);
  }

  function clearSelection(): void {
    selectedPR = null;
  }

  /** Returns the current kanban status for a PR. */
  function getPullKanbanStatus(ref: PullIdentityRef, number: number): KanbanStatus | undefined {
    const pr = pulls.find((p) => pullMatchesRef(p, ref, number));
    return pr?.KanbanStatus as KanbanStatus | undefined;
  }

  /** Optimistically update a single PR's kanban status. */
  function optimisticKanbanUpdate(ref: PullIdentityRef, number: number, status: KanbanStatus): void {
    pulls = pulls.map((pr) => (pullMatchesRef(pr, ref, number) ? { ...pr, KanbanStatus: status } : pr));
  }

  async function togglePRStar(ref: PullIdentityRef, number: number, currentlyStarred: boolean): Promise<void> {
    try {
      if (currentlyStarred) {
        const { error } = await apiClient.DELETE("/starred", {
          body: {
            item_type: "pr",
            provider: ref.provider,
            platform_host: concretePlatformHost(ref),
            owner: ref.owner,
            name: ref.name,
            number,
          },
        });
        if (error) {
          throw new Error(apiErrorMessage(error, "failed to unstar PR"));
        }
      } else {
        const { error } = await apiClient.PUT("/starred", {
          body: {
            item_type: "pr",
            provider: ref.provider,
            platform_host: concretePlatformHost(ref),
            owner: ref.owner,
            name: ref.name,
            number,
          },
        });
        if (error) {
          throw new Error(apiErrorMessage(error, "failed to star PR"));
        }
      }
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
      return;
    }
    await loadPulls();
  }

  async function fetchSinglePull(
    owner: string,
    name: string,
    number: number,
    identity: ProviderRouteRef,
  ): Promise<FetchPullResult> {
    const ref = identity;
    try {
      const { data, error, response } = await apiClient.GET(providerItemPath("pulls", ref, ""), {
        params: {
          path: { ...providerRouteParams(ref), number },
        },
      });
      if (error || !data) {
        if (response?.status === 404) {
          return { status: "not-found" };
        }
        return {
          status: "error",
          message: `API returned ${response?.status ?? "unknown"}`,
        };
      }
      const mr = data.merge_request;
      return {
        status: "found",
        pull: {
          ...mr,
          repo: data.repo,
          platform_host: data.platform_host,
          repo_owner: data.repo_owner,
          repo_name: data.repo_name,
          detail_loaded: data.detail_loaded,
          detail_fetched_at: data.detail_fetched_at,
          worktree_links: data.worktree_links,
        } as PullRequest,
      };
    } catch (err) {
      return {
        status: "error",
        message: err instanceof Error ? err.message : "network error",
      };
    }
  }

  async function loadPulls(params?: PullsParams): Promise<void> {
    loading = true;
    storeError = null;
    try {
      const globalRepo = getGlobalRepo();
      const merged = {
        state: filterState,
        ...(globalRepo !== undefined && { repo: globalRepo }),
        ...(filterKanban !== undefined && {
          kanban: filterKanban,
        }),
        ...(filterStarred && { starred: true }),
        ...(searchQuery !== undefined && { q: searchQuery }),
        ...params,
      };
      const { data, error } = await apiClient.GET("/pulls", {
        params: { query: merged },
      });
      if (error) {
        throw new Error(apiErrorMessage(error, "failed to load pulls"));
      }
      pulls = (data as PullRequest[]) ?? [];
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  function toggleFilterValue<T extends string>(values: T[], value: T): T[] {
    if (values.includes(value)) {
      return values.filter((item) => item !== value);
    }
    return [...values, value];
  }

  function matchesAttributeFilters(pr: PullRequest): boolean {
    if (attributeFilters.length === 0) return true;
    return attributeFilters.every((filter) => matchesAttributeFilter(pr, filter));
  }

  function matchesAttributeFilter(pr: PullRequest, filter: PullAttributeFilter): boolean {
    if (filter === "approved") {
      return pr.ReviewDecision.trim().toUpperCase() === "APPROVED";
    }
    if (filter === "draft") {
      return pr.IsDraft;
    }
    if (filter === "ready") {
      return pr.State === "open" && !pr.IsDraft;
    }
    if (filter === "merge_conflicts") {
      return pr.MergeableState === "dirty";
    }
    return hasFailedCI(pr);
  }

  function hasFailedCI(pr: PullRequest): boolean {
    const status = pr.CIStatus.trim().toLowerCase();
    if (status === "failure" || status === "failed" || status === "error") {
      return true;
    }
    const parsed = parseCIChecks(pr.CIChecksJSON);
    if (parsed.error !== null) return false;
    return bucketCIChecks(parsed.checks).failed.length > 0;
  }

  function matchesKanbanStatusFilters(pr: PullRequest): boolean {
    return kanbanStatusFilters.length === 0 || kanbanStatusFilters.includes(normalizeKanbanStatus(pr.KanbanStatus));
  }

  return {
    getPulls,
    getFilteredPulls,
    isLoading,
    getError,
    getSelectedPR,
    pullsByRepo,
    getFilterKanban,
    getAttributeFilters,
    getKanbanStatusFilters,
    getLocalFilterCount,
    getFilterStarred,
    setFilterStarred,
    getFilterState,
    setFilterState,
    getDisplayOrderPRs,
    selectNextPR,
    selectPrevPR,
    setFilterKanban,
    toggleAttributeFilter,
    toggleKanbanStatusFilter,
    clearLocalFilters,
    getSearchQuery,
    setSearchQuery,
    selectPR,
    clearSelection,
    getPullKanbanStatus,
    optimisticKanbanUpdate,
    togglePRStar,
    loadPulls,
    fetchSinglePull,
  };
}

export type PullsStore = ReturnType<typeof createPullsStore>;
