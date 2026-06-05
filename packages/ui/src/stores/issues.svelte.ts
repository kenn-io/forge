import type { Issue, IssueDetail, IssuesParams, Label } from "../api/types.js";
import { providerItemPath, providerRouteParams } from "../api/provider-routes.js";
import type { MiddlemanClient } from "../types.js";

export type IssueDetailSyncMode = boolean | "background";

export interface IssueSelection {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
}

export interface IssueDetailRequestOptions {
  sync?: IssueDetailSyncMode;
  provider: string;
  platformHost?: string | undefined;
  repoPath: string;
}

type IssueDetailRequestRef = {
  owner: string;
  name: string;
  number: number;
  provider: string;
  platformHost?: string | undefined;
  repoPath: string;
};

export interface IssuesStoreOptions {
  client: MiddlemanClient;
  getGlobalRepo?: () => string | undefined;
  getGroupByRepo?: () => boolean;
  getPage?: () => string;
  sync?: {
    refreshSyncStatus?: () => Promise<void>;
  };
}

function apiErrorMessage(error: { detail?: string; title?: string }, fallback: string): string {
  return error.detail ?? error.title ?? fallback;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function syncIntentRank(mode: IssueDetailSyncMode): number {
  if (mode === true) return 2;
  if (mode === "background") return 1;
  return 0;
}

function strongerSyncMode(a: IssueDetailSyncMode, b: IssueDetailSyncMode): IssueDetailSyncMode {
  return syncIntentRank(b) > syncIntentRank(a) ? b : a;
}

export function createIssuesStore(opts: IssuesStoreOptions) {
  const apiClient = opts.client;
  const getGlobalRepo = opts.getGlobalRepo ?? (() => undefined);
  const getGroupByRepo = opts.getGroupByRepo ?? (() => false);
  const getPage = opts.getPage ?? (() => "");
  const syncDep = opts.sync;

  async function refreshIssuesIfActive(): Promise<void> {
    if (getPage() === "issues") {
      await loadIssues();
    }
  }

  // --- list state ---

  let issues = $state<Issue[]>([]);
  let loading = $state(false);
  let storeError = $state<string | null>(null);
  let filterStarred = $state(false);
  let filterState = $state<string>("open");
  let searchQuery = $state<string | undefined>(undefined);
  let selectedIssue = $state<IssueSelection | null>(null);

  // --- detail state ---

  let issueDetail = $state<IssueDetail | null>(null);
  let detailLoading = $state(false);
  let detailSyncing = $state(false);
  let detailError = $state<string | null>(null);
  let issueDetailLoaded = $state(false);
  // Tracks the issue (if any) whose local body has been edited since
  // the last server confirmation. While set, background sync paths
  // preserve the local body for THIS specific issue — a poll on a
  // different issue is unaffected, and navigating away doesn't
  // strand the flag on the wrong target.
  type UnsavedIssueTarget = {
    provider: string;
    platformHost: string | undefined;
    owner: string;
    name: string;
    number: number;
  };
  let unsavedLocalBody = $state<UnsavedIssueTarget | null>(null);
  let detailPollHandle: ReturnType<typeof setInterval> | null = null;
  let issueSyncGeneration = 0;
  let activeDetailLoad: {
    key: string;
    promise: Promise<void> | null;
    syncMode: IssueDetailSyncMode;
  } | null = null;

  // --- list reads ---

  function getIssues(): Issue[] {
    return issues;
  }
  function isIssuesLoading(): boolean {
    return loading;
  }
  function getIssuesError(): string | null {
    return storeError;
  }
  function getSelectedIssue(): IssueSelection | null {
    return selectedIssue;
  }
  function getIssueFilterStarred(): boolean {
    return filterStarred;
  }
  function getIssueSearchQuery(): string | undefined {
    return searchQuery;
  }

  function issuesByRepo(): Map<string, Issue[]> {
    const map = new Map<string, Issue[]>();
    for (const issue of issues) {
      const key = `${issue.repo_owner ?? ""}/${issue.repo_name ?? ""}`;
      const existing = map.get(key);
      if (existing) existing.push(issue);
      else map.set(key, [issue]);
    }
    return map;
  }

  // --- detail reads ---

  function getIssueDetail(): IssueDetail | null {
    return issueDetail;
  }
  function isIssueDetailLoading(): boolean {
    return detailLoading;
  }
  function isIssueDetailSyncing(): boolean {
    return detailSyncing;
  }
  function getIssueDetailError(): string | null {
    return detailError;
  }

  function getIssueDetailLoaded(): boolean {
    return issueDetailLoaded;
  }

  function isIssueStaleRefreshing(): boolean {
    if (!issueDetail || !detailSyncing) return false;
    const fetchedAt = issueDetail.detail_fetched_at;
    if (!fetchedAt) return false;
    const fetchedMs = new Date(fetchedAt).getTime();
    const updatedMs = new Date(issueDetail.issue.UpdatedAt).getTime();
    const hourAgo = Date.now() - 3_600_000;
    return fetchedMs < hourAgo && updatedMs > fetchedMs;
  }

  // --- list writes ---

  function setIssueFilterStarred(v: boolean): void {
    filterStarred = v;
  }
  function setIssueSearchQuery(q: string | undefined): void {
    searchQuery = q;
  }
  function getIssueFilterState(): string {
    return filterState;
  }
  function setIssueFilterState(s: string): void {
    filterState = s;
  }

  function selectIssue(
    owner: string,
    name: string,
    number: number,
    provider: string,
    platformHost: string | undefined,
    repoPath: string,
  ): void {
    selectedIssue = {
      provider,
      ...(platformHost && { platformHost }),
      owner,
      name,
      repoPath,
      number,
    };
  }
  function clearIssueSelection(): void {
    selectedIssue = null;
  }

  async function loadIssues(params?: IssuesParams): Promise<void> {
    loading = true;
    storeError = null;
    try {
      const globalRepo = getGlobalRepo();
      const { data, error: requestError } = await apiClient.GET("/issues", {
        params: {
          query: {
            state: filterState,
            ...(globalRepo !== undefined && {
              repo: globalRepo,
            }),
            ...(filterStarred && { starred: true }),
            ...(searchQuery !== undefined && {
              q: searchQuery,
            }),
            ...params,
          },
        },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to load issues"));
      }
      issues = (data ?? []) as Issue[];
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  // --- detail writes ---

  // Apply a fresh IssueDetail from the server. When the user has an
  // unsynced local body edit on the same issue, keep that body so a
  // polling refresh can't revert a pending optimistic toggle. Match on
  // provider + platformHost too so an unrelated repo with the same
  // owner/name/number (different host or provider) doesn't inherit
  // another repo's pending body.
  function withPreservedLocalBody(next: IssueDetail): IssueDetail {
    if (!unsavedLocalBody) return next;
    if (!issueDetail) return next;
    if (
      unsavedLocalBody.provider !== next.repo?.provider ||
      unsavedLocalBody.platformHost !== next.repo?.platform_host ||
      unsavedLocalBody.owner !== next.repo_owner ||
      unsavedLocalBody.name !== next.repo_name ||
      unsavedLocalBody.number !== next.issue?.Number
    ) {
      return next;
    }
    if (
      issueDetail.repo_owner !== next.repo_owner ||
      issueDetail.repo_name !== next.repo_name ||
      issueDetail.issue?.Number !== next.issue?.Number
    ) {
      return next;
    }
    return {
      ...next,
      issue: { ...next.issue, Body: issueDetail.issue.Body },
    };
  }

  function hasUnsavedLocalBody(): boolean {
    return unsavedLocalBody !== null;
  }

  function currentIssuePlatformHost(owner: string, name: string, number: number): string | undefined {
    if (
      issueDetail &&
      issueDetail.repo_owner === owner &&
      issueDetail.repo_name === name &&
      issueDetail.issue.Number === number
    ) {
      return issueDetail.platform_host;
    }
    if (
      selectedIssue &&
      selectedIssue.owner === owner &&
      selectedIssue.name === name &&
      selectedIssue.number === number
    ) {
      return selectedIssue.platformHost;
    }
    return undefined;
  }

  function isIssueDetailShowingRef(ref: IssueDetailRequestRef): boolean {
    return (
      issueDetail !== null &&
      issueDetail.repo_owner === ref.owner &&
      issueDetail.repo_name === ref.name &&
      issueDetail.issue.Number === ref.number &&
      issueDetail.repo?.provider === ref.provider &&
      issueDetail.repo?.platform_host === ref.platformHost &&
      issueDetail.repo?.repo_path === ref.repoPath
    );
  }

  function currentIssueDetailRef(owner: string, name: string, number: number): IssueDetailRequestRef {
    const provider = issueDetail?.repo?.provider ?? selectedIssue?.provider;
    const repoPath = issueDetail?.repo?.repo_path ?? selectedIssue?.repoPath;
    if (!provider || !repoPath) {
      throw new Error("issue detail missing provider repo identity");
    }
    return issueDetailRequestRef(owner, name, number, {
      provider,
      platformHost: issueDetail?.repo?.platform_host ?? selectedIssue?.platformHost,
      repoPath,
    });
  }

  function issueDetailRequestRef(
    owner: string,
    name: string,
    number: number,
    options: IssueDetailRequestOptions,
  ): IssueDetailRequestRef {
    return {
      owner,
      name,
      number,
      provider: options.provider,
      platformHost: options.platformHost,
      repoPath: options.repoPath,
    };
  }

  async function loadIssueDetail(
    owner: string,
    name: string,
    number: number,
    options: IssueDetailRequestOptions,
  ): Promise<void> {
    const requestRef = issueDetailRequestRef(owner, name, number, options);
    const syncMode = options.sync ?? true;
    // Dedup by item identity only. A second caller with a different
    // sync mode joins the in-flight load and may promote the sync
    // intent if its requested mode is stronger.
    const key = `${requestRef.provider}:${requestRef.platformHost}:${requestRef.repoPath}/${number}`;
    if (detailLoading && activeDetailLoad?.key === key && activeDetailLoad.promise !== null) {
      activeDetailLoad.syncMode = strongerSyncMode(activeDetailLoad.syncMode, syncMode);
      return activeDetailLoad.promise;
    }

    const gen = ++issueSyncGeneration;
    const currentLoad: {
      key: string;
      promise: Promise<void> | null;
      syncMode: IssueDetailSyncMode;
    } = { key, promise: null, syncMode };
    activeDetailLoad = currentLoad;

    detailLoading = true;
    detailSyncing = false;
    detailError = null;
    const promise = (async () => {
      try {
        const { data, error: requestError } = await apiClient.GET(providerItemPath("issues", requestRef, ""), {
          params: {
            path: {
              ...providerRouteParams(requestRef),
              number: requestRef.number,
            },
          },
        });
        if (gen !== issueSyncGeneration) return;
        if (requestError) {
          throw new Error(apiErrorMessage(requestError, "failed to load issue"));
        }
        issueDetail = data
          ? withPreservedLocalBody({
              ...data,
              events: data.events ?? [],
            } as IssueDetail)
          : null;
        issueDetailLoaded = data?.detail_loaded ?? false;
      } catch (err) {
        if (gen !== issueSyncGeneration) return;
        detailError = err instanceof Error ? err.message : String(err);
      } finally {
        if (gen === issueSyncGeneration) detailLoading = false;
        if (activeDetailLoad === currentLoad) activeDetailLoad = null;
      }

      // Use the latest promoted sync intent so a stronger caller's
      // request isn't lost when it joined an in-flight load.
      const finalSyncMode = currentLoad.syncMode;
      if (gen === issueSyncGeneration && finalSyncMode === true) {
        void syncIssueDetail(owner, name, number, gen, requestRef);
      } else if (gen === issueSyncGeneration && finalSyncMode === "background") {
        void enqueueBackgroundIssueSync(owner, name, number, gen, issueDetail?.detail_fetched_at, requestRef);
      }
    })();
    currentLoad.promise = promise;
    return promise;
  }

  async function enqueueBackgroundIssueSync(
    owner: string,
    name: string,
    number: number,
    gen: number,
    previousFetchedAt: string | undefined,
    requestRef: IssueDetailRequestRef,
  ): Promise<void> {
    detailSyncing = true;
    try {
      const { error: requestError } = await apiClient.POST(providerItemPath("issues", requestRef, "/sync/async"), {
        params: {
          path: {
            ...providerRouteParams(requestRef),
            number: requestRef.number,
          },
        },
      });
      if (requestError) return;
      await refreshAfterBackgroundIssueSync(owner, name, number, gen, previousFetchedAt, requestRef);
    } finally {
      if (gen === issueSyncGeneration) detailSyncing = false;
      void syncDep?.refreshSyncStatus?.();
    }
  }

  async function refreshAfterBackgroundIssueSync(
    owner: string,
    name: string,
    number: number,
    gen: number,
    previousFetchedAt: string | undefined,
    requestRef: IssueDetailRequestRef,
  ): Promise<void> {
    for (const ms of [300, 700, 1_500, 3_000, 5_000]) {
      await delay(ms);
      if (gen !== issueSyncGeneration) return;
      await refreshIssueDetail(owner, name, number, requestRef, gen);
      if (gen !== issueSyncGeneration) return;
      const fetchedAt = issueDetail?.detail_fetched_at;
      if (fetchedAt && fetchedAt !== previousFetchedAt) {
        return;
      }
    }
  }

  async function syncIssueDetail(
    owner: string,
    name: string,
    number: number,
    gen: number,
    ref: IssueDetailRequestRef,
  ): Promise<void> {
    detailSyncing = true;
    try {
      const { data, error: requestError } = await apiClient.POST(providerItemPath("issues", ref, "/sync"), {
        params: {
          path: { ...providerRouteParams(ref), number: ref.number },
        },
      });
      if (gen !== issueSyncGeneration) return;
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "sync failed"));
      }
      if (data) {
        detailError = null;
        issueDetail = withPreservedLocalBody({
          ...data,
          events: data.events ?? [],
        } as IssueDetail);
        issueDetailLoaded = data.detail_loaded ?? issueDetailLoaded;
      }
    } catch {
      // Sync failure is non-fatal.
    } finally {
      if (gen === issueSyncGeneration) detailSyncing = false;
    }
    // Always refresh rate limits -- the API calls happened
    // regardless of whether user navigated away.
    void syncDep?.refreshSyncStatus?.();
    if (gen === issueSyncGeneration) {
      await refreshIssuesIfActive();
    }
  }

  async function refreshIssueDetail(
    owner: string,
    name: string,
    number: number,
    ref: IssueDetailRequestRef,
    expectedGen: number = issueSyncGeneration,
  ): Promise<void> {
    try {
      const { data } = await apiClient.GET(providerItemPath("issues", ref, ""), {
        params: {
          path: { ...providerRouteParams(ref), number: ref.number },
        },
      });
      // Re-check the generation after the awaited request: if the
      // selected issue changed mid-flight, dropping the assignment
      // keeps the new selection's data from being clobbered.
      if (expectedGen !== issueSyncGeneration) return;
      if (data !== undefined) {
        issueDetail = withPreservedLocalBody({
          ...data,
          events: data.events ?? [],
        } as IssueDetail);
        issueDetailLoaded = data.detail_loaded ?? issueDetailLoaded;
      }
    } catch {
      /* silent */
    }
  }

  function startIssueDetailPolling(
    owner: string,
    name: string,
    number: number,
    options: IssueDetailRequestOptions,
  ): void {
    const requestRef = issueDetailRequestRef(owner, name, number, options);
    stopIssueDetailPolling();
    detailPollHandle = setInterval(() => {
      void refreshIssueDetail(owner, name, number, requestRef);
    }, 60_000);
  }

  function stopIssueDetailPolling(): void {
    if (detailPollHandle !== null) {
      clearInterval(detailPollHandle);
      detailPollHandle = null;
    }
  }

  function clearIssueDetail(): void {
    ++issueSyncGeneration;
    activeDetailLoad = null;
    issueDetail = null;
    detailLoading = false;
    detailSyncing = false;
    detailError = null;
    issueDetailLoaded = false;
    unsavedLocalBody = null;
  }

  async function setIssueLabels(owner: string, name: string, number: number, labels: string[]): Promise<Label[]> {
    const ref = currentIssueDetailRef(owner, name, number);
    const { data, error: requestError } = await apiClient.PUT(providerItemPath("issues", ref, "/labels"), {
      params: {
        path: { ...providerRouteParams(ref), number },
      },
      body: { labels },
    });
    if (requestError) {
      const message = apiErrorMessage(requestError, "failed to update labels");
      detailError = message;
      throw new Error(message);
    }
    const nextLabels = (data?.labels ?? []) as Label[];
    if (isIssueDetailShowingRef(ref) && issueDetail) {
      issueDetail = {
        ...issueDetail,
        issue: {
          ...issueDetail.issue,
          labels: nextLabels,
        },
      };
    }
    void refreshIssuesIfActive();
    return nextLabels;
  }

  async function submitIssueComment(owner: string, name: string, number: number, body: string): Promise<void> {
    const ref = currentIssueDetailRef(owner, name, number);

    detailError = null;
    try {
      const { error: requestError } = await apiClient.POST(providerItemPath("issues", ref, "/comments"), {
        params: {
          path: { ...providerRouteParams(ref), number },
        },
        body: { body },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to post comment"));
      }
    } catch (err) {
      detailError = err instanceof Error ? err.message : String(err);
      return;
    }
    // Supersede any in-flight syncIssueDetail so its stale response
    // cannot overwrite the detail we are about to fetch.
    const gen = ++issueSyncGeneration;
    detailSyncing = false;
    // Silent refresh: avoid flipping loading flag, which would
    // unmount the detail tree and reset scroll position.
    await refreshIssueDetail(owner, name, number, ref);
    // Pull authoritative state from GitHub so issue row metadata
    // catches up. Skip if the user navigated away mid-refresh.
    if (gen === issueSyncGeneration) {
      void syncIssueDetail(owner, name, number, gen, ref);
    }
  }

  async function editIssueComment(
    owner: string,
    name: string,
    number: number,
    commentID: number,
    body: string,
  ): Promise<boolean> {
    const ref = currentIssueDetailRef(owner, name, number);

    detailError = null;
    try {
      const { error: requestError } = await apiClient.PATCH(providerItemPath("issues", ref, "/comments/{comment_id}"), {
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
        throw new Error(apiErrorMessage(requestError, "failed to edit comment"));
      }
    } catch (err) {
      detailError = err instanceof Error ? err.message : String(err);
      return false;
    }
    await refreshIssueDetail(owner, name, number, ref);
    return true;
  }

  // Replaces the in-memory issue body without touching the server. Pair
  // with saveIssueBodyInBackground for instant-feedback edits like
  // task-list checkbox clicks. Marks the body as unsaved so a
  // background refresh can't revert it before the debounced PATCH
  // lands.
  function setLocalIssueBody(
    provider: string,
    platformHost: string | undefined,
    owner: string,
    name: string,
    number: number,
    body: string,
  ): void {
    if (!issueDetail) return;
    if (issueDetail.repo_owner !== owner || issueDetail.repo_name !== name || issueDetail.issue.Number !== number) {
      return;
    }
    unsavedLocalBody = { provider, platformHost, owner, name, number };
    issueDetail = {
      ...issueDetail,
      issue: { ...issueDetail.issue, Body: body },
    };
  }

  // Single-flight body-save state, keyed per issue. Each entry
  // tracks the in-flight PATCH and the latest queued body waiting
  // to send once the in-flight save completes. The queue collapses
  // to a single pending body so out-of-order PATCH responses can't
  // overwrite a newer body with an older one.
  type QueuedIssueSave = {
    body: string;
    routeRef: {
      provider: string;
      platformHost?: string | undefined;
      repoPath: string;
    };
  };
  const inflightIssueSaves = new Map<string, Promise<void>>();
  const queuedIssueSaves = new Map<string, QueuedIssueSave>();
  function issueSaveQueueKey(
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

  async function runIssueBodyPatch(
    owner: string,
    name: string,
    number: number,
    body: string,
    routeRef: QueuedIssueSave["routeRef"],
  ): Promise<void> {
    const ref = issueDetailRequestRef(owner, name, number, routeRef);
    let succeeded = false;
    // Capture whether the locally-displayed body still equals what we
    // sent BEFORE we overwrite `issueDetail` with the server response.
    // A server-side body normalization (e.g. line endings) would
    // otherwise masquerade as a newer user edit on a post-overwrite
    // check and strand `unsavedLocalBody`. Also include provider +
    // platform host so a stale response from a since-navigated-away
    // host can't replace the now-displayed issue from another host
    // that happens to share owner/name/number.
    let localBodyMatchesSent = false;
    try {
      const { data, error: requestError } = await apiClient.PATCH(providerItemPath("issues", ref, ""), {
        params: {
          path: {
            ...providerRouteParams(ref),
            number,
          },
        },
        body: { body },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to update issue"));
      }
      succeeded = true;
      localBodyMatchesSent =
        issueDetail !== null &&
        issueDetail.repo?.provider === routeRef.provider &&
        issueDetail.repo?.platform_host === routeRef.platformHost &&
        issueDetail.repo_owner === owner &&
        issueDetail.repo_name === name &&
        issueDetail.issue.Number === number &&
        issueDetail.issue.Body === body;
      if (data && localBodyMatchesSent) {
        issueDetail = data as IssueDetail;
      }
    } catch (err) {
      detailError = err instanceof Error ? err.message : String(err);
    }
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
    refreshIssuesIfActive().catch(() => {});
  }

  // Fire-and-forget PATCH for the issue body. Does NOT apply an
  // optimistic update or revert on failure — the caller already owns
  // local state. On error, detailError surfaces a banner.
  //
  // The caller passes the full route ref so the PATCH always targets
  // the captured issue even if the user has since navigated away.
  // Only the response is gated on the currently displayed detail.
  // Saves for the same issue are serialized so older requests can't
  // overwrite newer bodies via out-of-order responses.
  function saveIssueBodyInBackground(
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
    const key = issueSaveQueueKey(routeRef.provider, routeRef.platformHost, owner, name, number);
    queuedIssueSaves.set(key, { body, routeRef });
    const existing = inflightIssueSaves.get(key);
    if (existing) return existing;
    const flight = (async () => {
      try {
        while (queuedIssueSaves.has(key)) {
          const next = queuedIssueSaves.get(key)!;
          queuedIssueSaves.delete(key);
          await runIssueBodyPatch(owner, name, number, next.body, next.routeRef);
        }
      } finally {
        inflightIssueSaves.delete(key);
      }
    })();
    inflightIssueSaves.set(key, flight);
    return flight;
  }

  async function toggleIssueStar(
    owner: string,
    name: string,
    number: number,
    currentlyStarred: boolean,
  ): Promise<void> {
    const platformHost = currentIssuePlatformHost(owner, name, number);

    try {
      if (currentlyStarred) {
        const { error: requestError } = await apiClient.DELETE("/starred", {
          body: {
            item_type: "issue",
            owner,
            name,
            number,
            ...(platformHost && {
              platform_host: platformHost,
            }),
          },
        });
        if (requestError) {
          throw new Error(apiErrorMessage(requestError, "failed to unstar issue"));
        }
      } else {
        const { error: requestError } = await apiClient.PUT("/starred", {
          body: {
            item_type: "issue",
            owner,
            name,
            number,
            ...(platformHost && {
              platform_host: platformHost,
            }),
          },
        });
        if (requestError) {
          throw new Error(apiErrorMessage(requestError, "failed to star issue"));
        }
      }
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
      return;
    }
    await loadIssues();
    if (
      issueDetail &&
      issueDetail.repo_owner === owner &&
      issueDetail.repo_name === name &&
      issueDetail.issue.Number === number
    ) {
      await loadIssueDetail(owner, name, number, currentIssueDetailRef(owner, name, number));
    }
  }

  // --- navigation ---

  function getDisplayOrderIssues(): Issue[] {
    if (getGroupByRepo()) {
      const grouped = issuesByRepo();
      const ordered: Issue[] = [];
      for (const items of grouped.values()) {
        ordered.push(...items);
      }
      return ordered;
    }
    return issues;
  }

  function selectNextIssue(): void {
    const list = getDisplayOrderIssues();
    if (list.length === 0) return;
    if (selectedIssue === null) {
      const first = list[0];
      if (first !== undefined) {
        selectedIssue = {
          owner: first.repo_owner ?? "",
          name: first.repo_name ?? "",
          number: first.Number,
          provider: first.repo?.provider,
          platformHost: first.platform_host,
          repoPath: first.repo?.repo_path,
        };
      }
      return;
    }
    const idx = list.findIndex(
      (i) =>
        (i.repo_owner ?? "") === selectedIssue!.owner &&
        (i.repo_name ?? "") === selectedIssue!.name &&
        i.Number === selectedIssue!.number &&
        (!selectedIssue!.platformHost || i.platform_host === selectedIssue!.platformHost),
    );
    if (idx < list.length - 1) {
      const next = list[idx + 1];
      if (next !== undefined) {
        selectedIssue = {
          owner: next.repo_owner ?? "",
          name: next.repo_name ?? "",
          number: next.Number,
          provider: next.repo?.provider,
          platformHost: next.platform_host,
          repoPath: next.repo?.repo_path,
        };
      }
    }
  }

  function selectPrevIssue(): void {
    const list = getDisplayOrderIssues();
    if (list.length === 0) return;
    if (selectedIssue === null) {
      const last = list[list.length - 1];
      if (last !== undefined) {
        selectedIssue = {
          owner: last.repo_owner ?? "",
          name: last.repo_name ?? "",
          number: last.Number,
          provider: last.repo?.provider,
          platformHost: last.platform_host,
          repoPath: last.repo?.repo_path,
        };
      }
      return;
    }
    const idx = list.findIndex(
      (i) =>
        (i.repo_owner ?? "") === selectedIssue!.owner &&
        (i.repo_name ?? "") === selectedIssue!.name &&
        i.Number === selectedIssue!.number &&
        (!selectedIssue!.platformHost || i.platform_host === selectedIssue!.platformHost),
    );
    if (idx > 0) {
      const prev = list[idx - 1];
      if (prev !== undefined) {
        selectedIssue = {
          owner: prev.repo_owner ?? "",
          name: prev.repo_name ?? "",
          number: prev.Number,
          provider: prev.repo?.provider,
          platformHost: prev.platform_host,
          repoPath: prev.repo?.repo_path,
        };
      }
    }
  }

  return {
    getIssues,
    isIssuesLoading,
    getIssuesError,
    getSelectedIssue,
    getIssueFilterStarred,
    setIssueFilterStarred,
    getIssueSearchQuery,
    setIssueSearchQuery,
    getIssueFilterState,
    setIssueFilterState,
    issuesByRepo,
    selectIssue,
    clearIssueSelection,
    loadIssues,
    getIssueDetail,
    isIssueDetailLoading,
    isIssueDetailSyncing,
    getIssueDetailError,
    getIssueDetailLoaded,
    isIssueStaleRefreshing,
    loadIssueDetail,
    startIssueDetailPolling,
    stopIssueDetailPolling,
    clearIssueDetail,
    setIssueLabels,
    submitIssueComment,
    editIssueComment,
    setLocalIssueBody,
    saveIssueBodyInBackground,
    hasUnsavedLocalBody,
    toggleIssueStar,
    selectNextIssue,
    selectPrevIssue,
  };
}

export type IssuesStore = ReturnType<typeof createIssuesStore>;
