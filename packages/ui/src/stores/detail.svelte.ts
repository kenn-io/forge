import type { KanbanStatus, Label, PullDetail } from "../api/types.js";
import type { ApplySuggestionRequest } from "../utils/markdown-suggestions.js";
import {
  isProblem,
  problemConflictContext,
  problemConflictReason,
  type ConflictReason,
  type ProblemBody,
} from "../api/problems.js";
import {
  providerDefaultHost,
  providerItemPath,
  providerRouteParams,
  type ProviderRouteRef,
} from "../api/provider-routes.js";
import type { MiddlemanClient } from "../types.js";
import { showFlash } from "./flash.svelte.js";

export type DetailSyncMode = boolean | "background";

export interface DetailRequestOptions {
  sync?: DetailSyncMode;
  workflowApprovalSync?: boolean;
  provider: string;
  platformHost?: string | undefined;
  repoPath: string;
}

type DetailRequestRef = {
  owner: string;
  name: string;
  number: number;
  provider: string;
  platformHost?: string | undefined;
  repoPath: string;
};

export interface DetailStoreOptions {
  client: MiddlemanClient;
  getPage?: () => string;
  pulls?: {
    loadPulls: (params?: unknown) => Promise<void>;
    optimisticKanbanUpdate?: (ref: ProviderRouteRef, number: number, status: KanbanStatus) => void;
    getPullKanbanStatus?: (ref: ProviderRouteRef, number: number) => KanbanStatus | undefined;
  };
  sync?: {
    subscribeSyncComplete: (cb: () => void) => () => void;
    refreshSyncStatus?: () => Promise<void>;
  };
}

export interface ApplySuggestionConflict {
  reason: Exclude<ConflictReason, "conflict">;
  context?: string | undefined;
  expectedHeadSha: string;
  ref: ProviderRouteRef;
  number: number;
}

function apiErrorMessage(error: { detail?: string; title?: string }, fallback: string): string {
  return error.detail ?? error.title ?? fallback;
}

function applySuggestionRefreshReason(problem: ProblemBody): Exclude<ConflictReason, "conflict"> | undefined {
  const reason: ConflictReason | undefined = problemConflictReason(problem);
  if (
    reason === "stale_state" ||
    reason === "head_unknown" ||
    reason === "not_open" ||
    reason === "head_repo_unknown"
  ) {
    return reason;
  }
  return undefined;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function syncIntentRank(mode: DetailSyncMode): number {
  if (mode === true) return 2;
  if (mode === "background") return 1;
  return 0;
}

function strongerSyncMode(a: DetailSyncMode, b: DetailSyncMode): DetailSyncMode {
  return syncIntentRank(b) > syncIntentRank(a) ? b : a;
}

function needsWorkflowApprovalSync(detail: PullDetail | null, enabled: boolean): boolean {
  if (!enabled || !detail) return false;
  const pr = detail.merge_request;
  return Boolean(
    detail.repo?.capabilities?.workflow_approval &&
    pr?.State === "open" &&
    detail.workflow_approval?.checked === false &&
    pr.CIHadPending,
  );
}

export function createDetailStore(opts: DetailStoreOptions) {
  const apiClient = opts.client;
  const getPage = opts.getPage ?? (() => "");
  const pullsDep = opts.pulls;
  const syncDep = opts.sync;

  // --- state ---

  let detail = $state<PullDetail | null>(null);
  let loading = $state(false);
  let syncing = $state(false);
  let storeError = $state<string | null>(null);
  let detailLoaded = $state(false);
  let syncGeneration = 0;
  let selectionGeneration = 0;
  let activeSelectionKey: string | null = null;
  // Tracks the PR (if any) whose local body has been edited since
  // the last server confirmation. While set, background sync paths
  // preserve the local body when applying refreshed server data for
  // THIS specific PR — a poll on a different PR is unaffected, and
  // navigating away doesn't strand the flag on the wrong target.
  type UnsavedTarget = {
    provider: string;
    platformHost: string | undefined;
    owner: string;
    name: string;
    number: number;
  };
  let unsavedLocalBody = $state<UnsavedTarget | null>(null);
  let activeLoad: {
    key: string;
    promise: Promise<void> | null;
    syncMode: DetailSyncMode;
    workflowApprovalSync: boolean;
  } | null = null;
  let activeCIRefresh: {
    key: string;
    promise: Promise<void>;
  } | null = null;
  // Latest detail_fetched_at seen in any server payload for the current
  // selection. The store's own detail_fetched_at intentionally freezes
  // while refreshes are content-identical, so background-sync convergence
  // must baseline against this value — the frozen store timestamp would
  // make the previous cycle's sync look like this cycle's completion and
  // end the convergence loop before the new sync has landed.
  let lastObservedFetchedAt: string | undefined;
  // Provider synchronization is eventually complete. Keep a successfully
  // deleted comment hidden locally until an ordinary sync no longer returns it.
  const hiddenDeletedCommentIDs: Record<string, number[]> = {};

  // Per-PR monotonic counters for kanban updates.
  const kanbanSeqByPR = new Map<string, number>();

  // --- polling ---

  let detailPollHandle: ReturnType<typeof setInterval> | null = null;
  let unsubSyncComplete: (() => void) | null = null;

  // --- reads ---

  function getDetail(): PullDetail | null {
    return detail;
  }

  function isDetailLoading(): boolean {
    return loading;
  }

  function isDetailSyncing(): boolean {
    return syncing;
  }

  function getDetailError(): string | null {
    return storeError;
  }

  function getDetailLoaded(): boolean {
    return detailLoaded;
  }

  function isStaleRefreshing(): boolean {
    if (!detail || !syncing) return false;
    const fetchedAt = detail.detail_fetched_at;
    if (!fetchedAt) return false;
    const fetchedMs = new Date(fetchedAt).getTime();
    const updatedMs = new Date(detail.merge_request.UpdatedAt).getTime();
    const hourAgo = Date.now() - 3_600_000;
    return fetchedMs < hourAgo && updatedMs > fetchedMs;
  }

  // --- internal helpers ---

  function prKey(ref: DetailRequestRef): string {
    return `${ref.provider}:${ref.platformHost ?? ""}:${ref.repoPath}/${ref.number}`;
  }

  function detailRequestRef(
    owner: string,
    name: string,
    number: number,
    options: DetailRequestOptions | DetailRequestRef,
  ): DetailRequestRef {
    return {
      owner,
      name,
      number,
      provider: options.provider,
      platformHost: options.platformHost,
      repoPath: options.repoPath,
    };
  }

  // Apply a fresh PullDetail from the server. When the user has an
  // unsynced local body edit on the same PR, keep that body so a
  // polling refresh can't revert a pending optimistic toggle. Match on
  // provider + platformHost too so an unrelated repo with the same
  // owner/name/number (different host or provider) doesn't inherit
  // another repo's pending body.
  function withPreservedLocalBody(next: PullDetail): PullDetail {
    next = withHiddenDeletedComments(next);
    if (!unsavedLocalBody) return next;
    if (!detail) return next;
    if (
      unsavedLocalBody.provider !== next.repo?.provider ||
      unsavedLocalBody.platformHost !== next.repo?.platform_host ||
      unsavedLocalBody.owner !== next.repo_owner ||
      unsavedLocalBody.name !== next.repo_name ||
      unsavedLocalBody.number !== next.merge_request?.Number
    ) {
      return next;
    }
    if (
      detail.repo_owner !== next.repo_owner ||
      detail.repo_name !== next.repo_name ||
      detail.merge_request?.Number !== next.merge_request?.Number
    ) {
      return next;
    }
    return {
      ...next,
      merge_request: {
        ...next.merge_request,
        Body: detail.merge_request.Body,
      },
    };
  }

  function withHiddenDeletedComments(next: PullDetail): PullDetail {
    if (Object.keys(hiddenDeletedCommentIDs).length === 0) return next;
    const key = `${next.repo.provider}:${next.repo.platform_host ?? ""}:${next.repo.repo_path}/${next.merge_request.Number}`;
    const hidden = hiddenDeletedCommentIDs[key];
    if (!hidden || hidden.length === 0) return next;
    const events = next.events ?? [];
    const stillHidden = hidden.filter((id) =>
      events.some((event) => event.EventType === "issue_comment" && event.PlatformID === id),
    );
    if (stillHidden.length === 0) {
      delete hiddenDeletedCommentIDs[key];
      return next;
    }
    hiddenDeletedCommentIDs[key] = stillHidden;
    return {
      ...next,
      events: events.filter(
        (event) =>
          event.EventType !== "issue_comment" || event.PlatformID === null || !stillHidden.includes(event.PlatformID),
      ),
    };
  }

  function hideDeletedComment(ref: DetailRequestRef, commentID: number): void {
    const key = prKey(ref);
    const hidden = hiddenDeletedCommentIDs[key] ?? [];
    if (!hidden.includes(commentID)) hiddenDeletedCommentIDs[key] = [...hidden, commentID];
    if (!isDetailShowingRef(ref) || !detail) return;
    detail = {
      ...detail,
      events: (detail.events ?? []).filter(
        (event) => event.EventType !== "issue_comment" || event.PlatformID !== commentID,
      ),
    };
  }

  function hasUnsavedLocalBody(): boolean {
    return unsavedLocalBody !== null;
  }

  function isDetailShowingRef(ref: DetailRequestRef): boolean {
    return (
      detail !== null &&
      detail.repo_owner === ref.owner &&
      detail.repo_name === ref.name &&
      detail.merge_request.Number === ref.number &&
      detail.repo?.provider === ref.provider &&
      detail.repo?.platform_host === ref.platformHost &&
      detail.repo?.repo_path === ref.repoPath
    );
  }

  function currentDetailRef(owner: string, name: string, number: number): DetailRequestRef {
    if (!detail?.repo?.provider || !detail.repo.repo_path) {
      throw new Error("pull detail missing provider repo identity");
    }
    return detailRequestRef(owner, name, number, {
      provider: detail.repo.provider,
      platformHost: detail.repo.platform_host,
      repoPath: detail.repo.repo_path,
    });
  }

  function concretePlatformHost(ref: Pick<DetailRequestRef, "provider" | "platformHost">): string {
    const host = ref.platformHost ?? providerDefaultHost(ref.provider);
    if (!host) throw new Error("pull detail missing platform host");
    return host;
  }

  async function refreshPullsIfActive(): Promise<void> {
    if (getPage() === "pulls" && pullsDep) {
      await pullsDep.loadPulls();
    }
  }

  // A refreshed payload whose content matches the displayed detail must
  // not replace it: the sync timestamp moves on every background poll,
  // and swapping in an equal-but-new object re-rendered the whole PR
  // panel (and re-ran the scroll-restore effect) every polling cycle
  // even though nothing about the PR changed.
  function detailContentUnchanged(next: PullDetail): boolean {
    if (detail === null) return false;
    // Only the fetch timestamp is volatile-by-design; everything else —
    // including warnings, which the PR panel renders — is content.
    const strip = (d: PullDetail): string => {
      const { detail_fetched_at: _fetchedAt, ...rest } = d;
      return JSON.stringify(rest);
    };
    return strip(detail) === strip(next);
  }

  function applyRefreshedDetail(next: PullDetail): void {
    if (detailContentUnchanged(next)) return;
    detail = next;
  }

  function noteObservedFetchedAt(fetchedAt: string | null | undefined): void {
    if (fetchedAt != null) lastObservedFetchedAt = fetchedAt;
  }

  function observedFetchedAtBaseline(): string | undefined {
    return lastObservedFetchedAt ?? detail?.detail_fetched_at;
  }

  async function refreshDetail(
    owner: string,
    name: string,
    number: number,
    expectedGen: number = syncGeneration,
    identity: DetailRequestRef,
  ): Promise<{ ok: boolean; error?: string; fetchedAt?: string }> {
    const ref = detailRequestRef(owner, name, number, identity);
    try {
      const { data, error: requestError } = await apiClient.GET(providerItemPath("pulls", ref, ""), {
        params: {
          path: { ...providerRouteParams(ref), number: ref.number },
        },
      });
      // Re-check the generation after the awaited request: if the
      // selected PR changed mid-flight, dropping the assignment keeps
      // the new selection's data from being clobbered.
      if (expectedGen !== syncGeneration) return { ok: false };
      if (requestError) {
        return { ok: false, error: apiErrorMessage(requestError, "failed to refresh pull request") };
      }
      if (data !== undefined) {
        applyRefreshedDetail(
          withPreservedLocalBody({
            ...data,
            events: data.events ?? [],
          } as PullDetail),
        );
        noteObservedFetchedAt(data.detail_fetched_at);
        detailLoaded = data.detail_loaded ?? detailLoaded;
        return { ok: true, ...(data.detail_fetched_at != null && { fetchedAt: data.detail_fetched_at }) };
      }
      return { ok: false, error: "Pull request refresh returned no detail" };
    } catch (err) {
      return { ok: false, error: err instanceof Error ? err.message : String(err) };
    }
  }

  async function syncDetail(
    owner: string,
    name: string,
    number: number,
    gen: number,
    identity: DetailRequestRef,
  ): Promise<boolean> {
    const ref = detailRequestRef(owner, name, number, identity);
    syncing = true;
    let refreshed = false;
    try {
      const { data, error: requestError } = await apiClient.POST(providerItemPath("pulls", ref, "/sync"), {
        params: {
          path: { ...providerRouteParams(ref), number: ref.number },
        },
      });
      if (gen !== syncGeneration) return false;
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "sync failed"));
      }
      if (data) {
        storeError = null;
        applyRefreshedDetail(
          withPreservedLocalBody({
            ...data,
            events: data.events ?? [],
          } as PullDetail),
        );
        noteObservedFetchedAt(data.detail_fetched_at);
        detailLoaded = data.detail_loaded ?? detailLoaded;
        refreshed = true;
      }
    } catch {
      // Sync failure is non-fatal.
    } finally {
      if (gen === syncGeneration) syncing = false;
    }
    // Always refresh rate limits -- the API calls happened
    // regardless of whether user navigated away.
    void syncDep?.refreshSyncStatus?.();
    if (gen === syncGeneration) {
      await refreshPullsIfActive();
    }
    return refreshed;
  }

  function failClosedAfterApplySuggestionConflict(reason: Exclude<ConflictReason, "conflict">): void {
    if (!detail) return;
    if (reason === "not_open") {
      detail = {
        ...detail,
        merge_request: {
          ...detail.merge_request,
          State: "closed",
        },
      };
      return;
    }
    detail = {
      ...detail,
      platform_head_sha: "",
    };
  }

  // --- writes ---

  function clearDetail(): void {
    ++syncGeneration;
    ++selectionGeneration;
    activeSelectionKey = null;
    activeLoad = null;
    detail = null;
    loading = false;
    syncing = false;
    storeError = null;
    detailLoaded = false;
    unsavedLocalBody = null;
    lastObservedFetchedAt = undefined;
  }

  async function loadDetail(owner: string, name: string, number: number, options: DetailRequestOptions): Promise<void> {
    const syncMode = options.sync ?? true;
    const requestRef = detailRequestRef(owner, name, number, options);
    // Dedup by item identity only. A second caller with a different
    // sync mode joins the in-flight load and may promote the sync
    // intent if its requested mode is stronger.
    const key = prKey(requestRef);
    if (activeSelectionKey !== key) {
      activeSelectionKey = key;
      ++selectionGeneration;
      // The observed-timestamp baseline belongs to the previous selection;
      // carrying it over would let another PR's sync clock gate this one.
      lastObservedFetchedAt = undefined;
    }
    if (loading && activeLoad?.key === key && activeLoad.promise !== null) {
      activeLoad.syncMode = strongerSyncMode(activeLoad.syncMode, syncMode);
      activeLoad.workflowApprovalSync ||= options.workflowApprovalSync ?? true;
      return activeLoad.promise;
    }

    const gen = ++syncGeneration;
    const currentLoad: {
      key: string;
      promise: Promise<void> | null;
      syncMode: DetailSyncMode;
      workflowApprovalSync: boolean;
    } = {
      key,
      promise: null,
      syncMode,
      workflowApprovalSync: options.workflowApprovalSync ?? true,
    };
    activeLoad = currentLoad;

    // Keep the previously loaded detail visible while the new one
    // is in flight. Nulling `detail` here flipped consumers to a
    // "Loading…" empty state for every prop change, which produced
    // a visible flash when, for example, the workspace right
    // sidebar updates from one PR to another. Consumers that need
    // a "first load" empty state should check `detail === null`
    // alongside `loading`.
    loading = true;
    syncing = false;
    storeError = null;
    detailLoaded = false;
    const promise = (async () => {
      try {
        const { data, error: requestError } = await apiClient.GET(providerItemPath("pulls", requestRef, ""), {
          params: {
            path: {
              ...providerRouteParams(requestRef),
              number: requestRef.number,
            },
          },
        });
        if (gen !== syncGeneration) return;
        if (requestError) {
          throw new Error(requestError.detail ?? requestError.title ?? "failed to load pull request");
        }
        detail = data
          ? withPreservedLocalBody({
              ...data,
              events: data.events ?? [],
            } as PullDetail)
          : null;
        noteObservedFetchedAt(data?.detail_fetched_at);
        detailLoaded = data?.detail_loaded ?? false;
      } catch (err) {
        if (gen !== syncGeneration) return;
        storeError = err instanceof Error ? err.message : String(err);
      } finally {
        if (gen === syncGeneration) loading = false;
        if (activeLoad === currentLoad) activeLoad = null;
      }

      // Use the latest promoted sync intent so a stronger caller's
      // request isn't lost when it joined an in-flight load.
      const finalSyncMode = currentLoad.syncMode;
      if (gen === syncGeneration && finalSyncMode === true) {
        void syncDetail(owner, name, number, gen, requestRef);
      } else if (gen === syncGeneration && finalSyncMode === "background") {
        if (needsWorkflowApprovalSync(detail, currentLoad.workflowApprovalSync)) {
          void syncDetail(owner, name, number, gen, requestRef);
          return;
        }
        void enqueueBackgroundDetailSync(owner, name, number, gen, observedFetchedAtBaseline(), requestRef);
      }
    })();
    currentLoad.promise = promise;
    return promise;
  }

  async function enqueueBackgroundDetailSync(
    owner: string,
    name: string,
    number: number,
    gen: number,
    previousFetchedAt: string | undefined,
    identity: DetailRequestRef,
  ): Promise<void> {
    const ref = detailRequestRef(owner, name, number, identity);
    syncing = true;
    try {
      const { error: requestError } = await apiClient.POST(providerItemPath("pulls", ref, "/sync/async"), {
        params: {
          path: { ...providerRouteParams(ref), number: ref.number },
        },
      });
      if (requestError) return;
      await refreshAfterBackgroundDetailSync(owner, name, number, gen, previousFetchedAt, identity);
    } finally {
      if (gen === syncGeneration) syncing = false;
      void syncDep?.refreshSyncStatus?.();
    }
  }

  async function refreshAfterBackgroundDetailSync(
    owner: string,
    name: string,
    number: number,
    gen: number,
    previousFetchedAt: string | undefined,
    identity: DetailRequestRef,
  ): Promise<void> {
    for (const ms of [300, 700, 1_500, 3_000, 5_000]) {
      await delay(ms);
      if (gen !== syncGeneration) return;
      // Convergence is judged on the response's fetch timestamp, not the
      // store's: content-identical refreshes intentionally leave the
      // store untouched, so the store timestamp can stay frozen while
      // the background sync has in fact completed.
      const { fetchedAt } = await refreshDetail(owner, name, number, gen, identity);
      if (gen !== syncGeneration) return;
      if (fetchedAt && fetchedAt !== previousFetchedAt) {
        return;
      }
    }
  }

  async function refreshDetailOnly(
    owner: string,
    name: string,
    number: number,
    identity: DetailRequestOptions,
  ): Promise<void> {
    const ref = detailRequestRef(owner, name, number, identity);
    await refreshDetail(owner, name, number, syncGeneration, ref);
  }

  async function syncDetailNow(
    owner: string,
    name: string,
    number: number,
    identity: DetailRequestOptions,
  ): Promise<boolean> {
    const ref = detailRequestRef(owner, name, number, identity);
    activeLoad = null;
    const gen = ++syncGeneration;
    return syncDetail(owner, name, number, gen, ref);
  }

  async function refreshPendingCI(
    owner: string,
    name: string,
    number: number,
    identity: DetailRequestOptions,
  ): Promise<void> {
    const ref = detailRequestRef(owner, name, number, identity);
    if (!isDetailShowingRef(ref)) return;
    const key = prKey(ref);
    if (activeCIRefresh?.key === key) {
      return activeCIRefresh.promise;
    }
    const gen = syncGeneration;
    const promise = (async () => {
      syncing = true;
      try {
        const { data, error: requestError } = await apiClient.POST(providerItemPath("pulls", ref, "/ci-refresh"), {
          params: {
            path: { ...providerRouteParams(ref), number: ref.number },
          },
        });
        if (gen !== syncGeneration) return;
        if (requestError) {
          showFlash(apiErrorMessage(requestError, "Failed to refresh CI checks"), { tone: "danger" });
          return;
        }
        if (data) {
          storeError = null;
          applyRefreshedDetail(
            withPreservedLocalBody({
              ...data,
              events: data.events ?? [],
            } as PullDetail),
          );
          noteObservedFetchedAt(data.detail_fetched_at);
          detailLoaded = data.detail_loaded ?? detailLoaded;
          const warning = data.warnings?.[0];
          if (warning) {
            showFlash(warning, { tone: "warning" });
          }
          if (needsWorkflowApprovalSync(detail, identity.workflowApprovalSync ?? true)) {
            await syncDetail(owner, name, number, gen, ref);
          }
        }
      } finally {
        if (gen === syncGeneration) syncing = false;
        void syncDep?.refreshSyncStatus?.();
      }
      if (gen === syncGeneration) {
        await refreshPullsIfActive();
      }
    })();
    activeCIRefresh = { key, promise };
    promise.finally(() => {
      if (activeCIRefresh?.promise === promise) activeCIRefresh = null;
    });
    return promise;
  }

  async function updateKanbanState(owner: string, name: string, number: number, status: KanbanStatus): Promise<void> {
    const ref = currentDetailRef(owner, name, number);
    const key = prKey(ref);
    const seq = (kanbanSeqByPR.get(key) ?? 0) + 1;
    kanbanSeqByPR.set(key, seq);

    const prevDetailStatus = isDetailShowingRef(ref) ? (detail!.merge_request.KanbanStatus as KanbanStatus) : undefined;
    const prevPullsStatus = pullsDep?.getPullKanbanStatus?.(ref, number);

    if (prevDetailStatus !== undefined) {
      detail = {
        ...detail!,
        merge_request: {
          ...detail!.merge_request,
          KanbanStatus: status,
        },
      };
    }
    pullsDep?.optimisticKanbanUpdate?.(ref, number, status);

    try {
      const { error: requestError } = await apiClient.PUT(providerItemPath("pulls", ref, "/state"), {
        params: {
          path: { ...providerRouteParams(ref), number },
        },
        body: { status },
      });
      if (requestError) {
        throw new Error(requestError.detail ?? requestError.title ?? "failed to update kanban state");
      }
    } catch (err) {
      if (seq === kanbanSeqByPR.get(key)) {
        showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
        if (prevDetailStatus !== undefined && isDetailShowingRef(ref)) {
          detail = {
            ...detail!,
            merge_request: {
              ...detail!.merge_request,
              KanbanStatus: prevDetailStatus,
            },
          };
        }
        if (prevPullsStatus !== undefined) {
          pullsDep?.optimisticKanbanUpdate?.(ref, number, prevPullsStatus);
        }
        const reloads: Promise<void>[] = [];
        if (pullsDep) reloads.push(pullsDep.loadPulls());
        if (isDetailShowingRef(ref)) {
          reloads.push(loadDetail(owner, name, number, ref));
        }
        await Promise.all(reloads);
        if (seq === kanbanSeqByPR.get(key)) {
          kanbanSeqByPR.set(key, seq - 1);
        }
      }
      return;
    }

    if (seq === kanbanSeqByPR.get(key)) {
      const refreshes: Promise<void>[] = [refreshPullsIfActive()];
      if (isDetailShowingRef(ref)) {
        refreshes.push(loadDetail(owner, name, number, ref));
      }
      await Promise.all(refreshes);
    }
  }

  async function setPullLabels(owner: string, name: string, number: number, labels: string[]): Promise<Label[]> {
    const ref = currentDetailRef(owner, name, number);
    const { data, error: requestError } = await apiClient.PUT(providerItemPath("pulls", ref, "/labels"), {
      params: {
        path: { ...providerRouteParams(ref), number },
      },
      body: { labels },
    });
    if (requestError) {
      const message = apiErrorMessage(requestError, "failed to update labels");
      showFlash(message, { tone: "danger" });
      throw new Error(message);
    }
    const nextLabels = (data?.labels ?? []) as Label[];
    if (isDetailShowingRef(ref) && detail) {
      detail = {
        ...detail,
        merge_request: {
          ...detail.merge_request,
          labels: nextLabels,
        },
      };
    }
    void refreshPullsIfActive();
    return nextLabels;
  }

  async function setPullAssignees(owner: string, name: string, number: number, assignees: string[]): Promise<string[]> {
    const ref = currentDetailRef(owner, name, number);
    const { data, error: requestError } = await apiClient.PUT(providerItemPath("pulls", ref, "/assignees"), {
      params: {
        path: { ...providerRouteParams(ref), number },
      },
      body: { assignees },
    });
    if (requestError) {
      const message = apiErrorMessage(requestError, "failed to update assignees");
      throw new Error(message);
    }
    const nextAssignees = data?.assignees ?? [];
    if (isDetailShowingRef(ref) && detail) {
      detail = {
        ...detail,
        merge_request: {
          ...detail.merge_request,
          assignees: nextAssignees,
        },
      };
    }
    void refreshPullsIfActive();
    return nextAssignees;
  }

  async function setPullReviewers(owner: string, name: string, number: number, reviewers: string[]): Promise<string[]> {
    const ref = currentDetailRef(owner, name, number);
    const { data, error: requestError } = await apiClient.PUT(providerItemPath("pulls", ref, "/reviewers"), {
      params: {
        path: { ...providerRouteParams(ref), number },
      },
      body: { reviewers },
    });
    if (requestError) {
      const message = apiErrorMessage(requestError, "failed to update reviewers");
      throw new Error(message);
    }
    const nextReviewers = data?.reviewers ?? [];
    if (isDetailShowingRef(ref) && detail) {
      detail = {
        ...detail,
        merge_request: {
          ...detail.merge_request,
          requested_reviewers: nextReviewers,
        },
      };
    }
    void refreshPullsIfActive();
    return nextReviewers;
  }

  async function updatePRContent(
    owner: string,
    name: string,
    number: number,
    fields: { title?: string; body?: string },
  ): Promise<void> {
    const ref = currentDetailRef(owner, name, number);
    if (!detail || !isDetailShowingRef(ref)) return;

    const prevTitle = detail.merge_request.Title;
    const prevBody = detail.merge_request.Body;

    // Optimistic update.
    detail = {
      ...detail,
      merge_request: {
        ...detail.merge_request,
        ...(fields.title !== undefined && {
          Title: fields.title,
        }),
        ...(fields.body !== undefined && {
          Body: fields.body,
        }),
      },
    };

    try {
      const { data, error: requestError } = await apiClient.PATCH(providerItemPath("pulls", ref, ""), {
        params: {
          path: { ...providerRouteParams(ref), number },
        },
        body: fields,
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to update PR"));
      }
      // Apply server-canonical response.
      if (data && isDetailShowingRef(ref)) {
        detail = data as PullDetail;
        if (
          unsavedLocalBody &&
          unsavedLocalBody.provider === ref.provider &&
          unsavedLocalBody.platformHost === ref.platformHost &&
          unsavedLocalBody.owner === owner &&
          unsavedLocalBody.name === name &&
          unsavedLocalBody.number === number
        ) {
          unsavedLocalBody = null;
        }
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
      // Revert optimistic update.
      if (isDetailShowingRef(ref) && detail) {
        detail = {
          ...detail,
          merge_request: {
            ...detail.merge_request,
            Title: prevTitle,
            Body: prevBody,
          },
        };
      }
      throw err;
    }
    // Refresh pulls list independently -- don't let a
    // refresh failure revert a successful edit.
    refreshPullsIfActive().catch(() => {});
  }

  // Replaces the in-memory PR body without touching the server. Pair
  // with savePRBodyInBackground for instant-feedback edits (e.g. task-
  // list checkbox clicks): apply the change locally first, then push
  // it asynchronously so the click never blocks on the network. Marks
  // the body as unsaved so a background refresh can't revert it
  // before the debounced PATCH lands.
  function setLocalPRBody(
    provider: string,
    platformHost: string | undefined,
    owner: string,
    name: string,
    number: number,
    body: string,
  ): void {
    if (
      !detail ||
      detail.repo?.provider !== provider ||
      detail.repo?.platform_host !== platformHost ||
      detail.repo_owner !== owner ||
      detail.repo_name !== name ||
      detail.merge_request.Number !== number
    ) {
      return;
    }
    unsavedLocalBody = { provider, platformHost, owner, name, number };
    detail = {
      ...detail,
      merge_request: { ...detail.merge_request, Body: body },
    };
  }

  // Single-flight body-save state, keyed per PR. Each entry tracks
  // the in-flight PATCH and the latest queued body waiting to send
  // once the in-flight save completes. The queue collapses to a
  // single pending body — if the user toggles many times while a
  // save is in flight, only the latest body lands. This prevents
  // out-of-order PATCH responses from clobbering a newer body with
  // an older one.
  type QueuedSave = {
    body: string;
    routeRef: {
      provider: string;
      platformHost?: string | undefined;
      repoPath: string;
    };
  };
  const inflightSaves = new Map<string, Promise<void>>();
  const queuedSaves = new Map<string, QueuedSave>();
  function saveQueueKey(
    provider: string,
    platformHost: string | undefined,
    owner: string,
    name: string,
    number: number,
  ): string {
    // JSON encoding stores each field as its own array element, so
    // an owner or name that contains a delimiter character can't
    // forge a collision with a different target. provider and
    // platformHost are part of the key so the same owner/name/number
    // on different hosts or providers can't share a queue slot.
    return JSON.stringify([provider, platformHost ?? "", owner, name, number]);
  }

  async function runPRBodyPatch(
    owner: string,
    name: string,
    number: number,
    body: string,
    routeRef: QueuedSave["routeRef"],
  ): Promise<void> {
    const ref = detailRequestRef(owner, name, number, routeRef);
    let succeeded = false;
    // Capture whether the locally-displayed body still equals what we
    // sent BEFORE we overwrite `detail` with the server response. If
    // the server normalizes the body (e.g. line endings), a post-
    // overwrite comparison would falsely look like a newer user edit
    // and strand `unsavedLocalBody` indefinitely. Also include
    // provider + platform host so a save response that returns after
    // the user navigated to the same owner/name/number on another
    // host doesn't replace the new repo's detail.
    let localBodyMatchesSent = false;
    try {
      const { data, error: requestError } = await apiClient.PATCH(providerItemPath("pulls", ref, ""), {
        params: {
          path: { ...providerRouteParams(ref), number },
        },
        body: { body },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to update PR"));
      }
      succeeded = true;
      localBodyMatchesSent =
        detail !== null &&
        detail.repo_owner === owner &&
        detail.repo_name === name &&
        detail.merge_request.Number === number &&
        detail.repo?.provider === routeRef.provider &&
        detail.repo?.platform_host === routeRef.platformHost &&
        detail.merge_request.Body === body;
      if (data && localBodyMatchesSent) {
        detail = data as PullDetail;
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    }
    // Clear the unsaved-body flag only when the captured local body
    // matched what we sent — i.e. no newer toggle landed during the
    // request. Using the captured value (rather than a fresh check)
    // is what keeps a server-normalized body from masquerading as a
    // newer edit and leaving the flag stuck.
    if (
      succeeded &&
      localBodyMatchesSent &&
      unsavedLocalBody &&
      unsavedLocalBody.provider === routeRef.provider &&
      unsavedLocalBody.platformHost === routeRef.platformHost &&
      unsavedLocalBody.owner === owner &&
      unsavedLocalBody.name === name &&
      unsavedLocalBody.number === number
    ) {
      unsavedLocalBody = null;
    }
    refreshPullsIfActive().catch(() => {});
  }

  // Fire-and-forget PATCH for the PR body. Does NOT apply an optimistic
  // update or revert on failure — the caller already owns local state.
  // On error, the shared flash surfaces the failure without poisoning load state.
  //
  // The caller passes the full route ref so the PATCH always targets
  // the captured PR even if the user has since navigated away. Only
  // the response is gated on the currently displayed detail. Saves
  // for the same PR are serialized so older requests can't overwrite
  // newer bodies via out-of-order responses.
  function savePRBodyInBackground(
    owner: string,
    name: string,
    number: number,
    body: string,
    routeRef: {
      provider: string;
      platformHost?: string | undefined;
      repoPath: string;
    },
  ): Promise<void> {
    const key = saveQueueKey(routeRef.provider, routeRef.platformHost, owner, name, number);
    queuedSaves.set(key, { body, routeRef });
    const existing = inflightSaves.get(key);
    if (existing) return existing;
    const flight = (async () => {
      try {
        while (queuedSaves.has(key)) {
          const next = queuedSaves.get(key)!;
          queuedSaves.delete(key);
          await runPRBodyPatch(owner, name, number, next.body, next.routeRef);
        }
      } finally {
        inflightSaves.delete(key);
      }
    })();
    inflightSaves.set(key, flight);
    return flight;
  }

  function startDetailPolling(owner: string, name: string, number: number, identity: DetailRequestOptions): void {
    const ref = detailRequestRef(owner, name, number, identity);
    stopDetailPolling();
    detailPollHandle = setInterval(() => {
      void enqueueBackgroundDetailSync(owner, name, number, syncGeneration, observedFetchedAtBaseline(), ref);
    }, 60_000);
    if (syncDep) {
      unsubSyncComplete = syncDep.subscribeSyncComplete(() => {
        void refreshDetail(owner, name, number, syncGeneration, ref);
      });
    }
  }

  function stopDetailPolling(): void {
    if (detailPollHandle !== null) {
      clearInterval(detailPollHandle);
      detailPollHandle = null;
    }
    if (unsubSyncComplete !== null) {
      unsubSyncComplete();
      unsubSyncComplete = null;
    }
  }

  async function toggleDetailPRStar(
    owner: string,
    name: string,
    number: number,
    currentlyStarred: boolean,
  ): Promise<void> {
    if (detail !== null) {
      detail = {
        ...detail,
        merge_request: {
          ...detail.merge_request,
          Starred: !currentlyStarred,
        },
      };
    }
    try {
      if (currentlyStarred) {
        const ref = currentDetailRef(owner, name, number);
        const { error: requestError } = await apiClient.DELETE("/starred", {
          body: {
            item_type: "pr",
            provider: ref.provider,
            platform_host: concretePlatformHost(ref),
            owner: ref.owner,
            name: ref.name,
            number,
          },
        });
        if (requestError) {
          throw new Error(requestError.detail ?? requestError.title ?? "failed to unstar pull request");
        }
      } else {
        const ref = currentDetailRef(owner, name, number);
        const { error: requestError } = await apiClient.PUT("/starred", {
          body: {
            item_type: "pr",
            provider: ref.provider,
            platform_host: concretePlatformHost(ref),
            owner: ref.owner,
            name: ref.name,
            number,
          },
        });
        if (requestError) {
          throw new Error(requestError.detail ?? requestError.title ?? "failed to star pull request");
        }
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
      if (detail !== null) {
        detail = {
          ...detail,
          merge_request: {
            ...detail.merge_request,
            Starred: currentlyStarred,
          },
        };
      }
      return;
    }
    await refreshPullsIfActive();
  }

  async function submitComment(owner: string, name: string, number: number, body: string): Promise<boolean> {
    const ref = currentDetailRef(owner, name, number);
    try {
      const { error: requestError } = await apiClient.POST(providerItemPath("pulls", ref, "/comments"), {
        params: {
          path: { ...providerRouteParams(ref), number },
        },
        body: { body },
      });
      if (requestError) {
        throw new Error(requestError.detail ?? requestError.title ?? "failed to post comment");
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
      return false;
    }
    // Supersede any in-flight syncDetail so its stale response
    // cannot overwrite the detail we are about to fetch.
    const gen = ++syncGeneration;
    syncing = false;
    // Silent refresh: avoid flipping loading flag, which would
    // unmount the detail tree and reset scroll position.
    await refreshDetail(owner, name, number, syncGeneration, currentDetailRef(owner, name, number));
    // Pull authoritative state from GitHub so PR row metadata
    // (last_activity_at, comment_count) and the pulls list catch
    // up. Skip if the user navigated away mid-refresh.
    if (gen === syncGeneration) {
      void syncDetail(owner, name, number, gen, currentDetailRef(owner, name, number));
    }
    return true;
  }

  async function editComment(
    owner: string,
    name: string,
    number: number,
    commentID: number,
    body: string,
  ): Promise<boolean> {
    const ref = currentDetailRef(owner, name, number);
    try {
      const { error: requestError } = await apiClient.PATCH(providerItemPath("pulls", ref, "/comments/{comment_id}"), {
        params: {
          path: {
            ...providerRouteParams(ref),
            number,
            comment_id: commentID,
          },
        },
        body: { body },
      });
      if (requestError) {
        throw new Error(requestError.detail ?? requestError.title ?? "failed to edit comment");
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
      return false;
    }
    await refreshDetail(owner, name, number, syncGeneration, currentDetailRef(owner, name, number));
    return true;
  }

  async function deleteComment(owner: string, name: string, number: number, commentID: number): Promise<boolean> {
    const ref = currentDetailRef(owner, name, number);
    storeError = null;
    try {
      const { error: requestError } = await apiClient.DELETE(providerItemPath("pulls", ref, "/comments/{comment_id}"), {
        headers: { "Content-Type": "application/json" },
        params: {
          path: {
            ...providerRouteParams(ref),
            number,
            comment_id: commentID,
          },
        },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to delete comment"));
      }
    } catch (err) {
      if (isDetailShowingRef(ref)) {
        storeError = err instanceof Error ? err.message : String(err);
      }
      return false;
    }
    hideDeletedComment(ref, commentID);
    if (isDetailShowingRef(ref)) {
      void syncDetail(owner, name, number, syncGeneration, ref);
    }
    return true;
  }

  async function replyToDiscussion(
    owner: string,
    name: string,
    number: number,
    discussionID: string,
    body: string,
  ): Promise<boolean> {
    const ref = currentDetailRef(owner, name, number);
    try {
      const { error: requestError } = await apiClient.POST(
        providerItemPath("pulls", ref, "/discussions/{discussion_id}/reply"),
        {
          params: {
            path: {
              ...providerRouteParams(ref),
              number,
              discussion_id: discussionID,
            },
          },
          body: { body },
        },
      );
      if (requestError) {
        throw new Error(requestError.detail ?? requestError.title ?? "failed to reply to thread");
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
      return false;
    }
    await refreshDetail(owner, name, number, syncGeneration, currentDetailRef(owner, name, number));
    return true;
  }

  async function applyReviewSuggestions(
    owner: string,
    name: string,
    number: number,
    input: ApplySuggestionRequest,
    onConflict?: (conflict: ApplySuggestionConflict) => void,
  ): Promise<boolean> {
    const ref = currentDetailRef(owner, name, number);
    const requestSelectionGeneration = selectionGeneration;
    const expectedHeadSHA = detail?.platform_head_sha ?? "";
    try {
      const { data: applyResult, error: requestError } = await apiClient.POST(
        providerItemPath("pulls", ref, "/review-suggestions/apply"),
        {
          params: {
            path: {
              ...providerRouteParams(ref),
              number,
            },
          },
          body: {
            expected_head_sha: expectedHeadSHA,
            ...(input.message ? { message: input.message } : {}),
            suggestions: input.suggestions.map((suggestion) => ({
              thread_id: suggestion.threadID,
              replacement: suggestion.replacement,
            })),
          },
        },
      );
      if (requestSelectionGeneration !== selectionGeneration || !isDetailShowingRef(ref)) {
        // A typed stale-head response belongs to the original detail view;
        // do not attach its inline conflict handling to a new selection.
        if (requestError && isProblem(requestError) && applySuggestionRefreshReason(requestError)) return false;
        if (requestError) throw new Error(apiErrorMessage(requestError, "failed to apply suggestion"));
        showFlash("Suggestion was applied after navigation. Refresh before applying it again.", {
          tone: "warning",
        });
        return true;
      }
      if (requestError) {
        const message = apiErrorMessage(requestError, "failed to apply suggestion");
        const refreshReason = isProblem(requestError) ? applySuggestionRefreshReason(requestError) : undefined;
        if (refreshReason) {
          if (onConflict) {
            onConflict({
              reason: refreshReason,
              context: isProblem(requestError) ? problemConflictContext(requestError) : undefined,
              expectedHeadSha: expectedHeadSHA,
              ref,
              number,
            });
          } else if (isDetailShowingRef(ref)) {
            const refreshGeneration = ++syncGeneration;
            const refreshed = await syncDetail(owner, name, number, refreshGeneration, ref);
            if (!refreshed && isDetailShowingRef(ref)) {
              failClosedAfterApplySuggestionConflict(refreshReason);
            }
          }
          storeError = message;
          return false;
        }
        throw new Error(message);
      }
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
      return false;
    }
    const refreshGeneration = ++syncGeneration;
    await syncDetail(owner, name, number, refreshGeneration, ref);
    return true;
  }

  return {
    getDetail,
    isDetailLoading,
    isDetailSyncing,
    getDetailError,
    getDetailLoaded,
    isStaleRefreshing,
    clearDetail,
    loadDetail,
    refreshDetailOnly,
    syncDetailNow,
    refreshPendingCI,
    updateKanbanState,
    setPullLabels,
    setPullAssignees,
    setPullReviewers,
    updatePRContent,
    setLocalPRBody,
    savePRBodyInBackground,
    hasUnsavedLocalBody,
    startDetailPolling,
    stopDetailPolling,
    toggleDetailPRStar,
    submitComment,
    editComment,
    deleteComment,
    replyToDiscussion,
    applyReviewSuggestions,
  };
}

export type DetailStore = ReturnType<typeof createDetailStore>;
