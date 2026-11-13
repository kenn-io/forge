import { createListPaging } from "./list-paging.svelte.js";
import { untrack } from "svelte";
import { Effect } from "effect";
import { reuseUnchanged } from "../utils/reuse-unchanged.js";
import { executeGeneratedApiRequest, GeneratedApi } from "../api/generated-api.js";
import { TransientTransportError, type ApiProblemError } from "../api/effect-errors.js";
import { retryIdempotentRead } from "../api/retry-policy.js";
import { GeneratedProblemResponse } from "../api/runtime.js";
import type { PullRequest, PullsParams, UnsetStarredParams } from "../api/types.js";
import type { VisiblePullRef } from "../api/generated/models/visiblePullRef.js";
import type { AppRuntime } from "../app/runtime.js";
import { searchEmptyReporter } from "../app/telemetry.js";
import {
  providerDefaultHost,
  providerRouteParams,
  type ProviderRouteRef,
  providerHostRouteParams,
  providerUsesHostRoute,
} from "../api/provider-routes.js";
import {
  cycleFilterInSections,
  PULL_ATTRIBUTE_SECTIONS,
  pullMatchesAttributeFilters,
  toggleFilterInSections,
  type PullAttributeFilter,
} from "../utils/pull-attributes.js";
import {
  cycleLabelFilter as cycleLabel,
  labelFilterParams,
  matchesLabelFilters,
  type LabelFilters,
} from "../utils/label-filter.js";
import { showFlash } from "./flash.svelte.js";
import { PullsWorkflow, type FetchPullResult } from "./pulls-workflow.js";
import { ProviderMutations, providerMutationFailureMessage } from "./ordered-mutations.js";
import { providerItemKey, providerMutationKey } from "./provider-key.js";
import { nextWorkspaceLifecycleTick } from "./workspace-create-pending.svelte.js";
import type { TriFilter } from "../utils/tri-filter.js";
import { readInvolvesMeFilter, writeInvolvesMeFilter } from "./involves-me-filter.js";
import { readUnassignedFilter, writeUnassignedFilter } from "./unassigned-filter.js";
import { repoIdentityKey } from "../utils/repo-label.js";

import { groupPullStacks, type PullSidebarRow } from "../utils/pull-stack-tree.js";
import { repositoryKeyFromWire } from "../api/repository-key.js";

export type { FetchPullResult } from "./pulls-workflow.js";

export interface PullSelection {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
}

type PullIdentityRef = ProviderRouteRef;

export interface PullsStoreOptions {
  runtime: AppRuntime;
  getGlobalRepo?: () => string | undefined;
  getGroupByRepo?: () => boolean;
  optimisticDetailStarUpdate?: (ref: ProviderRouteRef, number: number, starred: boolean, envelopeTick: number) => void;
}

/** Rows on each side of the opened pull request that a visible-set refresh covers. */
const VISIBLE_REFRESH_RADIUS = 50;
function visiblePullRef(ref: ProviderRouteRef, number: number): VisiblePullRef {
  return {
    provider: ref.provider,
    ...(ref.platformHost && { platform_host: ref.platformHost }),
    owner: ref.owner,
    name: ref.name,
    number,
  };
}

function apiErrorMessage(error: { detail?: string; title?: string }, fallback: string): string {
  return error.detail ?? error.title ?? fallback;
}

function readErrorMessage(error: ApiProblemError | TransientTransportError): string {
  if (error._tag === "ApiProblemError") {
    return apiErrorMessage(error.problem, "failed to load pulls");
  }
  return "Could not reach Kenn Forge";
}

export function createPullsStore(opts: PullsStoreOptions) {
  const runtime = opts.runtime;
  const getGlobalRepo = opts.getGlobalRepo ?? (() => undefined);
  const getGroupByRepo = opts.getGroupByRepo ?? (() => false);

  const STACK_TREE_KEY = "kenn-forge:pullStackTree";
  let stackTree = $state(readStackTree());
  let selectionVersion = $state(0);
  let stackExpansion = $state<Record<string, { expanded: boolean; selectionVersion: number }>>({});

  function readStackTree(): boolean {
    try {
      return localStorage.getItem(STACK_TREE_KEY) === "1";
    } catch {
      return false;
    }
  }

  function getStackTree(): boolean {
    return stackTree;
  }

  function setStackTree(value: boolean): void {
    stackTree = value;
    try {
      localStorage.setItem(STACK_TREE_KEY, value ? "1" : "0");
    } catch {
      // Keep the view usable when browser storage is unavailable.
    }
  }

  function toggleStack(key: string, expanded: boolean): void {
    stackExpansion[key] = { expanded: !expanded, selectionVersion };
  }

  function getSidebarRows(items: PullRequest[]): PullSidebarRow[] {
    if (!stackTree) return items.map((pr) => ({ pr, depth: 0, stackKey: "", memberCount: 0, expanded: false }));
    return groupPullStacks(items).flatMap(({ key, members }) => {
      const override = stackExpansion[key];
      const selection = selectedPR;
      const selectedChild = selection !== null && members.slice(1).some((pr) => pullMatchesSelection(pr, selection));
      const expanded =
        override?.expanded === true || (selectedChild && override?.selectionVersion !== selectionVersion);
      return (expanded ? members : members.slice(0, 1)).map((pr, index) => ({
        pr,
        depth: index,
        stackKey: key,
        memberCount: index === 0 && pr.stack ? members.length : 0,
        expanded,
      }));
    });
  }

  // --- state ---

  // List responses are only ever replaced, never mutated, so skip deep proxies.
  let pulls = $state.raw<PullRequest[]>([]);
  let loading = $state(false);
  let listCapped = $state(false);
  let storeError = $state<string | null>(null);
  let attributeFilters = $state<PullAttributeFilter[]>([]);
  let labelFilters = $state.raw<LabelFilters>(new Map());
  let filterStarred = $state(false);
  let involvesMe = $state(readInvolvesMeFilter("pulls"));
  let unassigned = $state(readUnassignedFilter("pulls"));
  let filterState = $state<string>("open");
  let searchQuery = $state<string | undefined>(undefined);
  let selectedPR = $state<PullSelection | null>(null);
  let activeListParams: PullsParams | undefined;
  const paging = createListPaging();

  // --- reads ---

  function getPulls(): PullRequest[] {
    return pulls;
  }

  // The server applies every filter before its limit, so each page it returns
  // already matches. The browser checks loaded rows again only so rows from an
  // earlier query never show while a refresh for new filters fails.
  const filteredPulls = $derived(
    attributeFilters.length === 0 && labelFilters.size === 0
      ? pulls
      : pulls.filter(
          (pr) => pullMatchesAttributeFilters(pr, attributeFilters) && matchesLabelFilters(pr.labels, labelFilters),
        ),
  );

  function getFilteredPulls(): PullRequest[] {
    return filteredPulls;
  }

  function isLoading(): boolean {
    return loading;
  }

  function isListCapped(): boolean {
    return listCapped;
  }

  function getError(): string | null {
    return storeError;
  }

  function getSelectedPR(): PullSelection | null {
    return selectedPR;
  }

  function pullRepoKey(pr: PullRequest): string {
    return repoIdentityKey({
      provider: pr.repo.provider,
      platformHost: pr.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(pr.repo),
      owner: pr.repo.owner,
      name: pr.repo.name,
      repoPath: pr.repo.repo_path,
    });
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

  const filteredPullsByRepo = $derived.by(() => {
    const map = new Map<string, PullRequest[]>();
    for (const pr of filteredPulls) {
      const key = pullRepoKey(pr);
      const existing = map.get(key);
      if (existing !== undefined) {
        existing.push(pr);
      } else {
        map.set(key, [pr]);
      }
    }
    return map;
  });

  /** Groups pulls by full provider identity into a Map. */
  function pullsByRepo(): Map<string, PullRequest[]> {
    return filteredPullsByRepo;
  }

  function getAttributeFilters(): PullAttributeFilter[] {
    return attributeFilters;
  }

  function getLabelFilters(): LabelFilters {
    return labelFilters;
  }

  function getFilterStarred(): boolean {
    return filterStarred;
  }

  function setFilterStarred(v: boolean): void {
    filterStarred = v;
  }

  function getInvolvesMe(): TriFilter {
    return involvesMe;
  }

  function setInvolvesMe(value: TriFilter): void {
    involvesMe = value;
    writeInvolvesMeFilter("pulls", value);
  }

  function getUnassigned(): TriFilter {
    return unassigned;
  }

  function setUnassigned(value: TriFilter): void {
    unassigned = value;
    writeUnassignedFilter("pulls", value);
  }

  function getFilterState(): string {
    return filterState;
  }

  function setFilterState(s: string): void {
    filterState = s;
  }

  /** Returns PRs in display order: grouped by repo or flat chronological. */
  const displayOrderPulls = $derived(getDisplayGroups().flatMap((items) => getSidebarRows(items).map((row) => row.pr)));

  function getDisplayOrderPRs(): PullRequest[] {
    return displayOrderPulls;
  }

  function getDisplayGroups(): PullRequest[][] {
    return getGroupByRepo() ? [...pullsByRepo().values()] : [getFilteredPulls()];
  }

  function getNavigationIndex(list: PullRequest[], selection: PullSelection): number {
    const index = list.findIndex((pr) => pullMatchesSelection(pr, selection));
    if (index >= 0 || !stackTree) return index;
    // A manually hidden child keeps its detail open; use its visible root as the cursor.
    const group = getDisplayGroups().find((items) => items.some((pr) => pullMatchesSelection(pr, selection)));
    const stack =
      group && groupPullStacks(group).find(({ members }) => members.some((pr) => pullMatchesSelection(pr, selection)));
    return stack ? list.findIndex((pr) => pr.ID === stack.members[0]?.ID) : -1;
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
    const idx = getNavigationIndex(list, sel);
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
    const idx = getNavigationIndex(list, sel);
    if (idx > 0) {
      const prev = list[idx - 1];
      if (prev !== undefined) {
        selectPRFromPull(prev);
      }
    }
  }

  // --- writes ---

  function applyAttributeFilters(next: PullAttributeFilter[]): void {
    attributeFilters = next;
    loadPulls(activeListParams);
  }

  function toggleAttributeFilter(filter: PullAttributeFilter): void {
    applyAttributeFilters(toggleFilterInSections(PULL_ATTRIBUTE_SECTIONS, attributeFilters, filter));
  }

  /** Cycles an attribute through off, required, and its opposite. */
  function cycleAttributeFilter(filter: PullAttributeFilter, opposite: PullAttributeFilter): void {
    applyAttributeFilters(cycleFilterInSections(PULL_ATTRIBUTE_SECTIONS, attributeFilters, filter, opposite));
  }

  function cycleLabelFilter(name: string): void {
    labelFilters = cycleLabel(labelFilters, name);
    loadPulls(activeListParams);
  }

  function clearLabelFilters(): void {
    if (labelFilters.size === 0) return;
    labelFilters = new Map();
    loadPulls(activeListParams);
  }

  function setAttributeFilters(next: PullAttributeFilter[]): void {
    applyAttributeFilters(next);
  }

  function clearAttributeFilters(): void {
    if (attributeFilters.length > 0) applyAttributeFilters([]);
  }

  function getSearchQuery(): string | undefined {
    return searchQuery;
  }

  const emptySearch = searchEmptyReporter(runtime);
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
    const selection = {
      provider,
      ...(platformHost && { platformHost }),
      owner,
      name,
      repoPath,
      number,
    };
    const changed = untrack(() => JSON.stringify(selectedPR) !== JSON.stringify(selection));
    // A manual collapse lasts for this visit, not a later navigation back to the same PR.
    if (changed) untrack(() => (selectionVersion += 1));
    selectedPR = selection;
    // After the selection lands, so a stack it expands is in the window.
    if (changed) untrack(() => refreshVisibleAround(selection));
  }

  /**
   * Asks the server to refresh what the user is probably looking at: the
   * opened pull request and the rows around it. The server adds the opened
   * pull request's stack and worktree-linked pull requests, checks them all
   * in batched reads, and syncs only the stale ones.
   */
  // A selection made before the list loads is refreshed again with its rows
  // once they arrive.
  let visibleRefreshAwaitsRows: PullSelection | null = null;

  function refreshVisibleAround(selection: PullSelection): void {
    const rows = displayOrderPulls;
    visibleRefreshAwaitsRows = rows.length === 0 ? selection : null;
    const index = rows.findIndex((pr) => pullMatchesSelection(pr, selection));
    const start = Math.max(0, index - VISIBLE_REFRESH_RADIUS);
    const nearby = rows.slice(start, start + 2 * VISIBLE_REFRESH_RADIUS + 1);
    const body = {
      focus: visiblePullRef(selection, selection.number),
      visible: nearby.map((pr) => visiblePullRef(pullRef(pr), pr.Number)),
    };
    runtime.runCommand(
      executeGeneratedApiRequest("POST visible pull request refresh", (client, signal) =>
        client.PullRequestsService.refreshVisiblePulls(body, { signal }),
      ),
      {
        operation: "refresh visible pull requests",
        safeContext: { visible: body.visible.length },
        onFailure: () => {},
      },
    );
  }

  function selectPRFromPull(pr: PullRequest): void {
    const ref = pullRef(pr);
    selectPR(ref.owner, ref.name, pr.Number, ref.provider, ref.platformHost, ref.repoPath);
  }

  function clearSelection(): void {
    selectedPR = null;
  }

  function optimisticStarUpdate(ref: PullIdentityRef, number: number, starred: boolean): void {
    pulls = pulls.map((pr) => (pullMatchesRef(pr, ref, number) ? { ...pr, Starred: starred } : pr));
  }

  function togglePRStar(ref: PullIdentityRef, number: number, currentlyStarred: boolean): void {
    const platformHost = concretePlatformHost(ref);
    const starredItem: UnsetStarredParams = {
      item_type: "pr",
      provider: ref.provider,
      platform_host: platformHost,
      owner: ref.owner,
      name: ref.name,
      number,
    };
    const nextStarred = !currentlyStarred;
    const mutationTick = nextWorkspaceLifecycleTick();
    let mutationSettled = false;
    const program = Effect.gen(function* () {
      const mutations = yield* ProviderMutations;
      const commit = (
        currentlyStarred
          ? executeGeneratedApiRequest<void>("DELETE pull request star", (client, signal) =>
              client.SettingsService.unsetStarred(starredItem, { signal }),
            )
          : executeGeneratedApiRequest<void>("PUT pull request star", (client, signal) =>
              client.SettingsService.setStarred(starredItem, { signal }),
            )
      ).pipe(Effect.as(nextStarred));
      const refreshOnStale = executeGeneratedApiRequest(
        "GET pull request after stale list star mutation",
        (client, signal) =>
          providerUsesHostRoute(ref)
            ? client.PullRequestsService.getPullOnHost({ ...providerHostRouteParams(ref), number: number }, { signal })
            : client.PullRequestsService.getPull({ ...providerRouteParams(ref), number: number }, { signal }),
      ).pipe(Effect.map((response) => Boolean(response.merge_request.Starred)));
      yield* mutations.submit({
        key: providerMutationKey(
          "pull",
          { provider: ref.provider, platformHost, owner: ref.owner, name: ref.name, number },
          "star",
        ),
        baseline: currentlyStarred,
        optimistic: nextStarred,
        apply: (starred) =>
          Effect.sync(() => {
            optimisticStarUpdate(ref, number, starred);
            opts.optimisticDetailStarUpdate?.(ref, number, starred, mutationTick);
          }),
        commit,
        refreshOnStale,
      });
      mutationSettled = true;
      const workflow = yield* PullsWorkflow;
      yield* workflow.invalidate(
        providerItemKey({ provider: ref.provider, platformHost, owner: ref.owner, name: ref.name, number }),
      );
      yield* loadPullsEffect();
    });
    runtime.runCommand(program, {
      operation: currentlyStarred ? "unstar pull request from list" : "star pull request from list",
      safeContext: { provider: ref.provider, platformHost, owner: ref.owner, name: ref.name, number },
      onFailure: (failure) => {
        if (mutationSettled) {
          storeError = providerMutationFailureMessage(failure, "failed to refresh pull requests");
          return;
        }
        showFlash(
          providerMutationFailureMessage(failure, currentlyStarred ? "failed to unstar PR" : "failed to star PR"),
          { tone: "danger" },
        );
      },
    });
  }

  function fetchSinglePull(
    owner: string,
    name: string,
    number: number,
    identity: ProviderRouteRef,
    onResult: (result: FetchPullResult) => void,
  ): void {
    const ref = identity;
    const key = providerItemKey({
      provider: ref.provider,
      platformHost: concretePlatformHost(ref),
      owner,
      name,
      number,
    });
    const program = Effect.gen(function* () {
      const api = yield* GeneratedApi;
      const request = Effect.tryPromise({
        try: async (signal): Promise<FetchPullResult> => {
          try {
            const data = providerUsesHostRoute(ref)
              ? await api.client.PullRequestsService.getPullOnHost(
                  { ...providerHostRouteParams(ref), number },
                  { signal },
                )
              : await api.client.PullRequestsService.getPull({ ...providerRouteParams(ref), number }, { signal });
            const pull: PullRequest = {
              ...data.merge_request,
              repo: data.repo,
              platform_host: data.platform_host,
              repo_owner: data.repo_owner,
              repo_name: data.repo_name,
              head_repo_kind: data.head_repo_kind,
              detail_loaded: data.detail_loaded,
              ...(data.detail_fetched_at !== undefined && { detail_fetched_at: data.detail_fetched_at }),
              worktree_links: data.worktree_links,
            };
            return { status: "found", pull };
          } catch (cause) {
            if (cause instanceof GeneratedProblemResponse && cause.problem.status === 404) {
              return { status: "not-found" };
            }
            throw cause;
          }
        },
        catch: (cause) => TransientTransportError.make({ operation: "GET pull request", cause }),
      }).pipe(
        Effect.catch((failure) => {
          const result: FetchPullResult = { status: "error", message: readErrorMessage(failure) };
          return Effect.succeed(result);
        }),
      );
      const workflow = yield* PullsWorkflow;
      const result = yield* workflow.refresh(key, request);
      yield* Effect.sync(() => onResult(result));
    });
    runtime.runCommand(program, {
      operation: "refresh pull request",
      safeContext: { provider: ref.provider, platformHost: concretePlatformHost(ref), owner, name, number },
      onFailure: () => {},
    });
  }

  function invalidatePullRefresh(ref: PullIdentityRef, number: number): void {
    const key = providerItemKey({
      provider: ref.provider,
      platformHost: concretePlatformHost(ref),
      owner: ref.owner,
      name: ref.name,
      number,
    });
    const program = Effect.gen(function* () {
      const workflow = yield* PullsWorkflow;
      yield* workflow.invalidate(key);
    });
    runtime.runCommand(program, {
      operation: "invalidate pull request refresh",
      safeContext: { provider: ref.provider, platformHost: concretePlatformHost(ref), number },
      onFailure: () => {},
    });
  }

  function listQuery(params?: PullsParams): PullsParams {
    const globalRepo = getGlobalRepo();
    const query: PullsParams = {
      state: filterState,
      ...(globalRepo !== undefined && { repo: globalRepo }),
      ...(filterStarred && { starred: true }),
      ...(involvesMe === "require" && { involves_me: true }),
      ...(involvesMe === "exclude" && { not_involves_me: true }),
      ...(unassigned === "require" && { unassigned: true }),
      ...(unassigned === "exclude" && { assigned: true }),
      ...(attributeFilters.length > 0 && { attributes: attributeFilters.join(",") }),
      ...labelFilterParams(labelFilters),
      ...(searchQuery !== undefined && { q: searchQuery }),
      ...params,
    };
    return paging.withLimit(query);
  }

  function applyPage(query: PullsParams, result: readonly PullRequest[], params?: PullsParams): void {
    pulls = reuseUnchanged(pulls, result, (item) => item.ID);
    listCapped = query.limit !== undefined && result.length === query.limit;
    paging.apply(query, params);
  }

  function isLoadingMore(): boolean {
    return paging.isLoadingMore();
  }

  function loadPullsEffect(params: PullsParams | undefined = activeListParams, options?: { submitted?: boolean }) {
    const query = listQuery(params);
    const reportEmpty = options?.submitted ? emptySearch.submission(query.q) : undefined;
    const read = executeGeneratedApiRequest("GET /pulls", (client, signal) =>
      client.PullRequestsService.listPulls(query, { signal }),
    ).pipe(
      retryIdempotentRead,
      Effect.map((result) => result ?? []),
    );
    return Effect.sync(() => {
      loading = true;
      storeError = null;
    }).pipe(
      Effect.andThen(
        Effect.gen(function* () {
          const workflow = yield* PullsWorkflow;
          return yield* workflow.list(JSON.stringify(query), read);
        }),
      ),
      Effect.tap((result) =>
        Effect.sync(() => {
          applyPage(query, result, params);
          if (result.length === 0) reportEmpty?.();
          loading = false;
          const awaiting = visibleRefreshAwaitsRows;
          if (awaiting !== null && JSON.stringify(awaiting) === untrack(() => JSON.stringify(selectedPR))) {
            untrack(() => refreshVisibleAround(awaiting));
          }
        }),
      ),
      Effect.tapError((failure) =>
        Effect.sync(() => {
          storeError = readErrorMessage(failure);
          loading = false;
          if (paging.rollback(query, params)) {
            showFlash(`Failed to load more pull requests: ${storeError}`, { tone: "danger" });
          }
        }),
      ),
    );
  }

  function reconcilePullsEffect(params: PullsParams | undefined = activeListParams) {
    return Effect.suspend(() => {
      const query = listQuery(params);
      const read = executeGeneratedApiRequest("GET /pulls after provider event", (client, signal) =>
        client.PullRequestsService.listPulls(query, { signal }),
      ).pipe(
        retryIdempotentRead,
        Effect.map((result) => result ?? []),
      );
      return Effect.gen(function* () {
        const workflow = yield* PullsWorkflow;
        yield* workflow.reconcile(read, (result) =>
          Effect.sync(() => {
            applyPage(query, result, params);
          }),
        );
      });
    });
  }

  function loadPulls(params?: PullsParams, options?: { submitted?: boolean }): void {
    activeListParams = params;
    runtime.runCommand(loadPullsEffect(params, options), {
      operation: "load pull requests",
      safeContext: {},
      onFailure: (failure) => {
        storeError = readErrorMessage(failure);
        loading = false;
      },
    });
  }

  function loadMorePulls(): void {
    if (!paging.loadMore(listCapped)) return;
    loadPulls(activeListParams);
  }

  return {
    getPulls,
    getFilteredPulls,
    isLoading,
    isListCapped,
    isLoadingMore,
    getError,
    getSelectedPR,
    pullsByRepo,
    getAttributeFilters,
    getLabelFilters,
    getFilterStarred,
    setFilterStarred,
    getInvolvesMe,
    setInvolvesMe,
    getUnassigned,
    setUnassigned,
    getFilterState,
    setFilterState,
    getDisplayOrderPRs,
    getSidebarRows,
    getStackTree,
    setStackTree,
    toggleStack,
    selectNextPR,
    selectPrevPR,
    toggleAttributeFilter,
    cycleAttributeFilter,
    cycleLabelFilter,
    clearLabelFilters,
    clearAttributeFilters,
    setAttributeFilters,
    getSearchQuery,
    setSearchQuery,
    searchTextChanged: emptySearch.textChanged,
    selectPR,
    clearSelection,
    optimisticStarUpdate,
    togglePRStar,
    loadPulls,
    loadMorePulls,
    loadPullsEffect,
    reconcilePullsEffect,
    fetchSinglePull,
    invalidatePullRefresh,
  };
}

export type PullsStore = ReturnType<typeof createPullsStore>;
