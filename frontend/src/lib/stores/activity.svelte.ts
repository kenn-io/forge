import { Effect } from "effect";
import type { AppRuntime } from "../app/runtime.js";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { retryIdempotentRead } from "../api/retry-policy.js";
import type {
  ActivityItem,
  ActivityAuthorsParams,
  ActivityParams,
  ActivityResponse,
  ActivitySettings,
  ActivitySubject,
  NotificationBulkResponse,
  WorkspaceActivitySubject,
} from "../api/types.js";
import { activityItemKey } from "../components/activityRows.js";
import { ActivityWorkflow } from "./activity-workflow.js";
import { showFlash } from "./flash.svelte.js";
import { ProviderMutations, providerMutationFailureMessage } from "./ordered-mutations.js";
import { readInvolvesMeFilter, writeInvolvesMeFilter } from "./involves-me-filter.js";

export type TimeRange = "24h" | "7d" | "30d" | "90d";
export type ViewMode = "flat" | "threaded";
export type ActivityItemType = "pr" | "issue";
export type ActivityAPIItemType = ActivityItemType | "repo";

export const DEFAULT_ACTIVITY_ITEM_TYPES = ["pr", "issue"] as const;
export const DEFAULT_EVENT_TYPES = ["comment", "review", "commit", "force_push"] as const;
const PR_TIMELINE_EVENT_TYPES = ["comment", "review", "force_push"] as const;
const ISSUE_TIMELINE_EVENT_TYPES = ["comment"] as const;
const NO_ACTIVITY_FILTER_TYPE = "none";
const ACTIVITY_ITEM_TYPES_PARAM = "item_types";
const ACTIVITY_EVENT_TYPES_PARAM = "event_types";

// Default-branch force pushes and PR force pushes share a filter. Commits are
// intentionally different: the Commits filter controls only top-level branch
// activity, while commits inside PR timelines are always part of the PR.
const BRANCH_TYPE_FOR_EVENT: Partial<Record<string, string>> = {
  commit: "default_branch_commit",
  force_push: "default_branch_force_push",
};

export function buildActivityFilterTypes(
  enabledItemTypes: ReadonlySet<ActivityItemType>,
  enabledEvents: ReadonlySet<string>,
  hideDefaultBranchActivity: boolean,
  showNotifications = true,
): string[] {
  const allSelected =
    enabledItemTypes.size === DEFAULT_ACTIVITY_ITEM_TYPES.length &&
    enabledEvents.size === DEFAULT_EVENT_TYPES.length &&
    !hideDefaultBranchActivity &&
    showNotifications;
  // An empty list means "no type filter" — the backend returns every
  // activity_type, notifications included. Only short-circuit when the
  // notification toggle is also at its default, otherwise fall through
  // to build the explicit list that omits "notification".
  if (allSelected) return [];

  const types: string[] = [];
  if (enabledItemTypes.size === 0) types.push(NO_ACTIVITY_FILTER_TYPE);
  // Opening rows are timeline events, not parent anchors. Authoritative parent
  // summaries are projected separately, so opening rows follow only the
  // timeline events that can occur on that item kind: an issue timeline has
  // comments alone, so Reviews or Force pushes must not toggle issue opening
  // rows. The Commits toggle controls top-level default-branch commits only,
  // so it does not opt into PR or issue opening rows either.
  if (enabledItemTypes.has("pr") && PR_TIMELINE_EVENT_TYPES.some((eventType) => enabledEvents.has(eventType))) {
    types.push("new_pr");
  }
  if (enabledItemTypes.has("issue") && ISSUE_TIMELINE_EVENT_TYPES.some((eventType) => enabledEvents.has(eventType))) {
    types.push("new_issue");
  }
  if (!hideDefaultBranchActivity) {
    for (const evt of DEFAULT_EVENT_TYPES) {
      const branchType = BRANCH_TYPE_FOR_EVENT[evt];
      if (branchType && enabledEvents.has(evt)) types.push(branchType);
    }
  }
  for (const evt of DEFAULT_EVENT_TYPES) {
    if (evt === "commit") {
      if (enabledItemTypes.has("pr")) types.push(evt);
    } else if (enabledEvents.has(evt)) {
      types.push(evt);
    }
  }
  if (showNotifications) types.push("notification");
  return types.length > 0 ? types : [NO_ACTIVITY_FILTER_TYPE];
}

export function buildActivityItemTypeFilter(enabledItemTypes: ReadonlySet<ActivityItemType>): ActivityAPIItemType[] {
  return [...DEFAULT_ACTIVITY_ITEM_TYPES.filter((itemType) => enabledItemTypes.has(itemType)), "repo"];
}

export function isActivityItemTypeEnabled(itemType: string, enabledItemTypes: ReadonlySet<ActivityItemType>): boolean {
  if (itemType !== "pr" && itemType !== "issue") return true;
  return enabledItemTypes.has(itemType);
}

function readSelectedFilters<T extends string>(value: string | null, defaults: readonly T[]): Set<T> {
  if (value === null) return new Set(defaults);
  const selected = value === NO_ACTIVITY_FILTER_TYPE ? [] : value.split(",");
  return new Set(defaults.filter((candidate) => selected.includes(candidate)));
}

function writeSelectedFilters<T extends string>(
  searchParams: URLSearchParams,
  name: string,
  selected: ReadonlySet<T>,
  defaults: readonly T[],
): void {
  const ordered = defaults.filter((candidate) => selected.has(candidate));
  if (ordered.length === defaults.length) {
    searchParams.delete(name);
  } else {
    searchParams.set(name, ordered.length > 0 ? ordered.join(",") : NO_ACTIVITY_FILTER_TYPE);
  }
}

function readLegacyFilterSelections(value: string): {
  itemTypes: Set<ActivityItemType>;
  events: Set<string>;
} {
  const types = value ? value.split(",") : [];
  if (types.length === 0) {
    return {
      itemTypes: new Set(DEFAULT_ACTIVITY_ITEM_TYPES),
      events: new Set(DEFAULT_EVENT_TYPES),
    };
  }

  const encodedEmptyEventDefaultScope = types.length === 1 && types[0] === "notification";
  const itemTypes = encodedEmptyEventDefaultScope
    ? new Set(DEFAULT_ACTIVITY_ITEM_TYPES)
    : new Set(
        DEFAULT_ACTIVITY_ITEM_TYPES.filter((itemType) => types.includes(itemType === "pr" ? "new_pr" : "new_issue")),
      );
  const events = encodedEmptyEventDefaultScope
    ? new Set<string>()
    : new Set(DEFAULT_EVENT_TYPES.filter((eventType) => types.includes(eventType)));
  return { itemTypes, events };
}

// Activity item ids are "<source>:<source_id>"; notification rows use
// the "ntf" source whose source_id is the notification's DB id.
export function notificationDbId(activityItemId: string): number | null {
  const prefix = "ntf:";
  if (!activityItemId.startsWith(prefix)) return null;
  const id = Number(activityItemId.slice(prefix.length));
  return Number.isInteger(id) && id > 0 ? id : null;
}

const RANGE_MS: Record<TimeRange, number> = {
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
  "30d": 30 * 24 * 60 * 60 * 1000,
  "90d": 90 * 24 * 60 * 60 * 1000,
};

interface OwnedActivityResponse {
  readonly response: ActivityResponse;
  readonly startedAt: number;
}

type ActivityPollProjection =
  | { readonly mode: "append"; readonly result: OwnedActivityResponse }
  | { readonly mode: "replace"; readonly result: OwnedActivityResponse };

export interface ActivityStoreOptions {
  runtime: AppRuntime;
  getGlobalRepo?: () => string | undefined;
  getBasePath?: () => string;
}

function apiErrorMessage(error: { detail?: string; title?: string }, fallback: string): string {
  return error.detail ?? error.title ?? fallback;
}

function readErrorMessage(
  error: ApiProblemError | TransientTransportError,
  fallback = "failed to load activity",
): string {
  if (error._tag === "ApiProblemError") {
    return apiErrorMessage(error.problem, fallback);
  }
  return "Could not reach Kenn Forge";
}

export function createActivityStore(opts: ActivityStoreOptions) {
  const runtime = opts.runtime;
  const getGlobalRepo = opts.getGlobalRepo ?? (() => undefined);
  const getBasePath = opts.getBasePath ?? (() => "/");

  // --- state ---

  let items = $state.raw<ActivityItem[]>([]);
  let itemActivity = $state.raw<ActivitySubject[]>([]);
  let workspaceActivity = $state.raw<WorkspaceActivitySubject[]>([]);
  let loading = $state(false);
  let storeError = $state<string | null>(null);
  let capped = $state(false);
  let itemActivityCapped = $state(false);
  let activityEventCursor = $state("");
  let loadedThreadKeys = $state.raw<ReadonlySet<string>>(new Set());
  let loadingThreadKeys = $state.raw<ReadonlySet<string>>(new Set());
  let failedThreadKeys = $state.raw<ReadonlySet<string>>(new Set());
  let threadLoadError = $state<string | null>(null);
  let filterTypes = $state<string[]>([]);
  let searchQuery = $state<string | undefined>(undefined);
  let authorFilter = $state<string | undefined>(undefined);
  let authorCandidates = $state.raw<string[]>([]);
  let authorsLoading = $state(false);
  let authorsError = $state<string | null>(null);
  let timeRange = $state<TimeRange>("7d");
  let viewMode = $state<ViewMode>("flat");
  let collapseThreads = $state(false);
  let rollUpCommits = $state(false);
  let collapseThreadsDefault = false;
  let fullEventProjectionRequired = false;
  let expandOverrides = $state<Set<string>>(new Set());
  let pagedActivityGeneration = 0;
  const threadRequestTokens = new Map<string, symbol>();
  let bulkRequestToken: symbol | undefined;
  let authorRequestVersion = 0;
  let authorScopeKey: string | null = null;
  let pollCount = 0;
  const AUTHORITATIVE_REFRESH_EVERY = 4;
  let activityLifecycleTick = 0;
  const notificationStateOwnership = new Map<string, { readonly tick: number; readonly state: string }>();

  let hideClosedMerged = $state(false);
  let hideBots = $state(false);
  let useWorkspaceActivityForRecency = $state(false);
  let hideDefaultBranchActivity = $state(false);
  let enabledItemTypes = $state<Set<ActivityItemType>>(new Set(DEFAULT_ACTIVITY_ITEM_TYPES));
  let enabledEvents = $state<Set<string>>(new Set(DEFAULT_EVENT_TYPES));
  let showNotifications = $state(true);
  let involvesMe = $state(readInvolvesMeFilter("activity"));
  let initialized = false;

  // --- reads ---

  function getActivityItems(): ActivityItem[] {
    return items;
  }
  function getItemActivity(): ActivitySubject[] {
    return itemActivity;
  }
  function getWorkspaceActivity(): WorkspaceActivitySubject[] {
    return workspaceActivity;
  }
  function isActivityLoading(): boolean {
    return loading;
  }
  function getActivityError(): string | null {
    return storeError;
  }
  function getThreadLoadError(): string | null {
    return threadLoadError;
  }
  function isActivityCapped(): boolean {
    return capped;
  }
  function isItemActivityCapped(): boolean {
    return itemActivityCapped;
  }
  function getActivityEventCursor(): string {
    return activityEventCursor;
  }
  function getActivityFilterTypes(): string[] {
    return filterTypes;
  }
  function getActivitySearch(): string | undefined {
    return searchQuery;
  }
  function getActivityAuthor(): string | undefined {
    return authorFilter;
  }
  function getActivityAuthors(): string[] {
    const selected = authorFilter;
    if (!selected) return authorCandidates;
    const selectedIndex = authorCandidates.findIndex((candidate) => candidate.toLowerCase() === selected.toLowerCase());
    if (selectedIndex < 0) return [selected, ...authorCandidates];
    if (authorCandidates[selectedIndex] === selected) return authorCandidates;
    const candidates = [...authorCandidates];
    candidates[selectedIndex] = selected;
    return candidates;
  }
  function isActivityAuthorsLoading(): boolean {
    return authorsLoading;
  }
  function getActivityAuthorsError(): string | null {
    return authorsError;
  }
  function getTimeRange(): TimeRange {
    return timeRange;
  }
  function getViewMode(): ViewMode {
    return viewMode;
  }
  function getCollapseThreads(): boolean {
    return collapseThreads;
  }
  function getRollUpCommits(): boolean {
    return rollUpCommits;
  }
  function isThreadItemExpanded(key: string): boolean {
    return expandOverrides.has(key) ? collapseThreads : !collapseThreads;
  }
  function getHideClosedMerged(): boolean {
    return hideClosedMerged;
  }
  function getHideBots(): boolean {
    return hideBots;
  }
  function getUseWorkspaceActivityForRecency(): boolean {
    return useWorkspaceActivityForRecency;
  }
  function getHideDefaultBranchActivity(): boolean {
    return hideDefaultBranchActivity;
  }
  function getEnabledItemTypes(): Set<ActivityItemType> {
    return enabledItemTypes;
  }
  function getEnabledEvents(): Set<string> {
    return enabledEvents;
  }
  function getShowNotifications(): boolean {
    return showNotifications;
  }
  function getInvolvesMe(): boolean {
    return involvesMe;
  }
  function isInitialized(): boolean {
    return initialized;
  }

  // --- writes ---

  function setActivityFilterTypes(types: string[]): void {
    filterTypes = types;
    invalidatePagedActivityRequests();
  }
  function setActivitySearch(q: string | undefined): void {
    searchQuery = q;
    invalidatePagedActivityRequests();
  }
  function setActivityAuthor(author: string | undefined): void {
    authorFilter = author?.trim() || undefined;
    invalidatePagedActivityRequests();
  }
  function setTimeRange(range_: TimeRange): void {
    timeRange = range_;
    invalidatePagedActivityRequests();
  }
  function setViewMode(mode: ViewMode): void {
    viewMode = mode;
    invalidatePagedActivityRequests();
  }
  function setFullEventProjectionRequired(required: boolean): void {
    fullEventProjectionRequired = required;
    invalidatePagedActivityRequests();
  }
  function setRollUpCommits(value: boolean): void {
    rollUpCommits = value;
  }
  function collapseAllThreads(): void {
    collapseThreads = true;
    expandOverrides = new Set();
    syncToURL();
    loadActivity();
  }
  function expandAllThreads(): void {
    collapseThreads = false;
    expandOverrides = new Set();
    syncToURL();
    loadBulkActivity();
  }
  function toggleThreadItem(key: string): void {
    // Per-item overrides are session-only and intentionally not synced to the
    // URL; only collapse-all/expand-all persist via collapseThreads.
    const next = new Set(expandOverrides);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    expandOverrides = next;
    if (isThreadItemExpanded(key)) loadThreadEvents(key);
  }
  function setHideClosedMerged(v: boolean): void {
    hideClosedMerged = v;
    invalidatePagedActivityRequests();
  }
  function setHideBots(v: boolean): void {
    hideBots = v;
    invalidatePagedActivityRequests();
  }
  function setHideDefaultBranchActivity(v: boolean): void {
    hideDefaultBranchActivity = v;
    invalidatePagedActivityRequests();
  }
  function setEnabledItemTypes(itemTypes: Set<ActivityItemType>): void {
    enabledItemTypes = itemTypes;
    invalidatePagedActivityRequests();
  }
  function setEnabledEvents(events: Set<string>): void {
    enabledEvents = events;
    invalidatePagedActivityRequests();
  }
  function setShowNotifications(v: boolean): void {
    showNotifications = v;
    invalidatePagedActivityRequests();
  }
  function setInvolvesMe(value: boolean): void {
    involvesMe = value;
    invalidatePagedActivityRequests();
    writeInvolvesMeFilter("activity", value);
  }
  // --- hydration ---

  function hydrateDefaults(activity: ActivitySettings): void {
    viewMode = activity.view_mode;
    timeRange = activity.time_range;
    hideClosedMerged = activity.hide_closed;
    hideBots = activity.hide_bots;
    useWorkspaceActivityForRecency = activity.use_workspace_activity_for_recency;
    collapseThreadsDefault = activity.collapse_threads;
    collapseThreads = activity.collapse_threads;
    expandOverrides = new Set();
    if (initialized) {
      applyCollapsedFromURL();
      // Once a settings reload makes the live state match the new default,
      // drop the now-redundant collapsed param so a later default change is
      // not shadowed by a stale override.
      if (collapseThreads === collapseThreadsDefault) {
        deleteCollapsedParam();
      }
    }
  }

  function initializeFromMount(): void {
    syncFromURL();
    initialized = true;
    syncToURL();
  }

  // --- internals ---

  function computeSince(): string {
    return new Date(Date.now() - RANGE_MS[timeRange]).toISOString();
  }

  function buildParams(): ActivityParams {
    const p: ActivityParams = { since: computeSince() };
    p.projection = viewMode === "threaded" && collapseThreads && !fullEventProjectionRequired ? "collapsed" : "full";
    const repo = getGlobalRepo();
    if (repo) p.repo = repo;
    if (filterTypes.length > 0) p.types = filterTypes;
    const itemTypes = buildActivityItemTypeFilter(enabledItemTypes);
    if (itemTypes.length < DEFAULT_ACTIVITY_ITEM_TYPES.length + 1) {
      p.item_types = itemTypes;
    }
    if (searchQuery) p.search = searchQuery;
    if (authorFilter) p.author = authorFilter;
    if (involvesMe) p.involves_me = true;
    if (hideClosedMerged) p.hide_closed_merged = true;
    if (hideBots) p.hide_bots = true;
    if (hideDefaultBranchActivity) p.hide_default_branch = true;
    return p;
  }

  function shouldUseCollapsedAuthoritativeProjection(): boolean {
    return viewMode === "threaded" && !fullEventProjectionRequired;
  }

  function activityProjectionScope(params: ActivityParams): string {
    return JSON.stringify([
      timeRange,
      params.projection ?? "full",
      params.repo ?? "",
      params.types ?? [],
      params.item_types ?? [],
      params.search ?? "",
      params.author ?? "",
      params.involves_me ?? false,
      params.hide_closed_merged ?? false,
      params.hide_bots ?? false,
      params.hide_default_branch ?? false,
    ]);
  }

  function pagedActivityScopeKey(): string {
    const params = buildParams();
    return JSON.stringify([activityProjectionScope(params), params.projection]);
  }

  function invalidatePagedActivityRequests(): void {
    pagedActivityGeneration += 1;
    threadRequestTokens.clear();
    loadingThreadKeys = new Set();
    failedThreadKeys = new Set();
    threadLoadError = null;
    bulkRequestToken = undefined;
  }

  function loadActivityAuthorsEffect(force = false) {
    return Effect.suspend(() => {
      const repo = getGlobalRepo();
      const scopeKey = `${repo ?? ""}\0${timeRange}`;
      if (!force && scopeKey === authorScopeKey) return Effect.void;

      if (scopeKey !== authorScopeKey) {
        authorCandidates = [];
      }
      authorScopeKey = scopeKey;
      const version = ++authorRequestVersion;
      authorsLoading = true;
      authorsError = null;
      const query: ActivityAuthorsParams = { since: computeSince(), ...(repo ? { repo } : {}) };
      return executeGeneratedApiRequest("GET /activity/authors", (client, signal) =>
        client.GET("/activity/authors", { params: { query }, signal }),
      ).pipe(
        retryIdempotentRead,
        Effect.tap((response) =>
          Effect.sync(() => {
            if (version !== authorRequestVersion) return;
            authorCandidates = response.authors ?? [];
            authorsLoading = false;
          }),
        ),
        Effect.catch((failure) =>
          Effect.sync(() => {
            if (version !== authorRequestVersion) return;
            authorsError = readErrorMessage(failure, "failed to load activity authors");
            authorsLoading = false;
            authorScopeKey = null;
          }),
        ),
        Effect.onInterrupt(() =>
          Effect.sync(() => {
            if (version !== authorRequestVersion || scopeKey !== authorScopeKey) return;
            authorsLoading = false;
            authorScopeKey = null;
          }),
        ),
      );
    });
  }

  function loadActivityAuthors(force = false): void {
    runtime.runCommand(loadActivityAuthorsEffect(force), {
      operation: "load activity authors",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function activityRead(params: ActivityParams) {
    return Effect.sync(() => ++activityLifecycleTick).pipe(
      Effect.flatMap((startedAt) =>
        executeGeneratedApiRequest("GET /activity", (client, signal) =>
          client.GET("/activity", { params: { query: params }, signal }),
        ).pipe(
          retryIdempotentRead,
          Effect.map((response) => ({ response, startedAt })),
        ),
      ),
    );
  }

  function projectOwnedNotificationStates(result: OwnedActivityResponse, completeSnapshot = true): ActivityItem[] {
    const projected = (result.response.items ?? []).map((item) => {
      const owned = notificationStateOwnership.get(item.id);
      if (owned === undefined) return item;
      if (owned.tick > result.startedAt) return { ...item, item_state: owned.state };
      notificationStateOwnership.delete(item.id);
      return item;
    });
    if (completeSnapshot) {
      const present = new Set(projected.map((item) => item.id));
      for (const [id, owned] of notificationStateOwnership) {
        if (owned.tick <= result.startedAt && !present.has(id)) notificationStateOwnership.delete(id);
      }
    }
    return projected;
  }

  function projectOwnedActivityPages(results: readonly OwnedActivityResponse[]): ActivityItem[] {
    const seen = new Set<string>();
    const projected: ActivityItem[] = [];
    for (const result of results) {
      for (const item of projectOwnedNotificationStates(result, false)) {
        if (seen.has(item.id)) continue;
        seen.add(item.id);
        projected.push(item);
      }
    }
    return projected;
  }

  function stableParentKey(item: ActivityItem | ActivitySubject): string | undefined {
    const repo = item.repo;
    if (!repo) return undefined;
    const platformRepoId = repo.platform_repo_id?.trim();
    if (!platformRepoId || (item.item_type !== "pr" && item.item_type !== "issue")) return undefined;
    return activityItemKey({
      provider: repo.provider,
      platformHost: repo.platform_host,
      platformRepoId,
      owner: repo.owner,
      name: repo.name,
      repoPath: repo.repo_path,
      itemType: item.item_type,
      itemNumber: item.item_number,
    });
  }

  function reconcileItemsWithParentSubjects(subjects: ActivitySubject[]): void {
    const subjectsByKey = new Map<string, ActivitySubject>();
    for (const subject of subjects) {
      const key = stableParentKey(subject);
      if (key) subjectsByKey.set(key, subject);
    }
    items = items.map((item) => {
      const key = stableParentKey(item);
      const subject = key ? subjectsByKey.get(key) : undefined;
      if (!subject) return item;
      const { workspace: _staleWorkspace, ...retainedEvent } = item;
      return {
        ...retainedEvent,
        ...(item.activity_type === "notification" ? { activity_url: subject.item_url } : {}),
        item_author: subject.item_author ?? "",
        item_last_activity_at: subject.activity_at,
        item_state: item.activity_type === "notification" ? item.item_state : subject.item_state,
        item_title: subject.item_title,
        item_url: subject.item_url,
        platform_host: subject.repo.platform_host,
        repo_owner: subject.repo.owner,
        repo_name: subject.repo.name,
        repo: subject.repo,
        ...(item.activity_type === "notification" ? { subject_state: subject.item_state } : {}),
        ...(subject.workspace ? { workspace: subject.workspace } : {}),
      };
    });
  }

  function projectActivitySubjects(response: ActivityResponse): void {
    const subjects = response.item_activity ?? [];
    reconcileItemsWithParentSubjects(subjects);
    itemActivity = subjects;
    workspaceActivity = response.workspace_activity ?? [];
    itemActivityCapped = response.item_activity_capped ?? false;
  }

  function projectActivitySnapshot(result: OwnedActivityResponse, projection: ActivityParams["projection"]): void {
    items = projectOwnedNotificationStates(result);
    projectActivitySubjects(result.response);
    capped = result.response.capped;
    activityEventCursor = result.response.event_cursor ?? "";
    loadedThreadKeys = new Set();
    loadingThreadKeys = new Set();
    failedThreadKeys = new Set();
    threadLoadError = null;
    loading = false;
    storeError = null;
    if (projection === "collapsed") {
      for (const subject of itemActivity) {
        const key = stableParentKey(subject);
        if (key && isThreadItemExpanded(key)) loadThreadEvents(key);
      }
    }
  }

  function projectAuthoritativeActivitySnapshot(result: OwnedActivityResponse): void {
    const reconcileCollapsedThreads = shouldUseCollapsedAuthoritativeProjection();
    const received = projectOwnedNotificationStates(result);
    const receivedIDs = new Set(received.map((item) => item.id));
    const receivedSubjectKeys = new Set(
      (result.response.item_activity ?? [])
        .map((subject) => stableParentKey(subject))
        .filter((key): key is string => key !== undefined),
    );
    const previousSubjects = new Map(itemActivity.map((subject) => [stableParentKey(subject), subject] as const));
    const changedThreadKeys = new Set<string>();
    const newExpandedThreadKeys = new Set<string>();
    let restartBulkActivity = false;
    const parentFilterActive =
      searchQuery !== undefined ||
      authorFilter !== undefined ||
      involvesMe ||
      hideClosedMerged ||
      hideBots ||
      enabledItemTypes.size !== DEFAULT_ACTIVITY_ITEM_TYPES.length;
    for (const subject of result.response.item_activity ?? []) {
      const key = stableParentKey(subject);
      const previous = key ? previousSubjects.get(key) : undefined;
      if (
        key &&
        previous &&
        (previous.activity_at !== subject.activity_at ||
          previous.event_ledger_revision !== subject.event_ledger_revision)
      ) {
        changedThreadKeys.add(key);
      }
      if (reconcileCollapsedThreads && key && !previous && isThreadItemExpanded(key) && !loadedThreadKeys.has(key)) {
        newExpandedThreadKeys.add(key);
      }
    }
    const retainedThreadItems = items.filter((item) => {
      const key = stableParentKey(item);
      if (result.response.item_activity_capped && !parentFilterActive) {
        return (
          (item.item_type === "pr" || item.item_type === "issue") &&
          !receivedIDs.has(item.id) &&
          (key === undefined || !changedThreadKeys.has(key))
        );
      }
      return (
        key !== undefined && receivedSubjectKeys.has(key) && !receivedIDs.has(item.id) && !changedThreadKeys.has(key)
      );
    });
    items = [...received, ...retainedThreadItems];
    projectActivitySubjects(result.response);
    capped = result.response.capped;
    activityEventCursor = result.response.event_cursor ?? activityEventCursor;
    if (reconcileCollapsedThreads && (changedThreadKeys.size > 0 || newExpandedThreadKeys.size > 0)) {
      restartBulkActivity = changedThreadKeys.size > 0 && bulkRequestToken !== undefined;
      if (!restartBulkActivity) {
        loadedThreadKeys = new Set([...loadedThreadKeys].filter((key) => !changedThreadKeys.has(key)));
        loadingThreadKeys = new Set([...loadingThreadKeys].filter((key) => !changedThreadKeys.has(key)));
        failedThreadKeys = new Set([...failedThreadKeys].filter((key) => !changedThreadKeys.has(key)));
        if (failedThreadKeys.size === 0) threadLoadError = null;
        for (const key of changedThreadKeys) threadRequestTokens.delete(key);
        for (const key of new Set([...changedThreadKeys, ...newExpandedThreadKeys])) {
          if (isThreadItemExpanded(key)) loadThreadEvents(key);
        }
      }
    }
    loading = false;
    storeError = null;
    if (restartBulkActivity) loadBulkActivity();
  }

  function loadActivityProgram(params: ActivityParams, owner: "foreground" | "poll" = "foreground") {
    return Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const scope = activityProjectionScope(params);
      const read = activityRead(params);
      const project = (result: OwnedActivityResponse) =>
        Effect.sync(() => projectActivitySnapshot(result, params.projection));
      const clearLoading = Effect.sync(() => {
        loading = false;
      });
      yield* owner === "foreground"
        ? workflow.load(scope, read, project, clearLoading)
        : workflow.pollSnapshotRead(scope, read, project, clearLoading);
    });
  }

  function loadActivityEffect() {
    return Effect.sync(() => {
      loading = true;
      storeError = null;
    }).pipe(
      Effect.andThen(Effect.suspend(() => loadActivityProgram(buildParams()))),
      Effect.tapError((failure) =>
        Effect.sync(() => {
          storeError = readErrorMessage(failure);
          loading = false;
        }),
      ),
    );
  }

  function reconcileActivityEffect() {
    return Effect.suspend(() => {
      const params = buildParams();
      if (shouldUseCollapsedAuthoritativeProjection()) params.projection = "collapsed";
      const scope = activityProjectionScope(params);
      const read = activityRead(params);
      const project = (result: OwnedActivityResponse) =>
        Effect.sync(() => projectAuthoritativeActivitySnapshot(result));
      return Effect.gen(function* () {
        const workflow = yield* ActivityWorkflow;
        yield* Effect.all([workflow.reconcileRead(scope, read, project), loadActivityAuthorsEffect(true)], {
          concurrency: "unbounded",
          discard: true,
        });
      });
    });
  }

  function loadThreadEvents(key: string): void {
    if (loadedThreadKeys.has(key) || loadingThreadKeys.has(key)) return;
    const subject = itemActivity.find((candidate) => stableParentKey(candidate) === key);
    const platformRepoID = subject?.repo.platform_repo_id?.trim();
    if (!subject || !platformRepoID || (subject.item_type !== "pr" && subject.item_type !== "issue")) return;
    const itemType: "pr" | "issue" = subject.item_type === "pr" ? "pr" : "issue";

    const requestGeneration = pagedActivityGeneration;
    const requestScope = pagedActivityScopeKey();
    const requestToken = Symbol(key);
    const initialThreadItemIDs = new Set(items.filter((item) => stableParentKey(item) === key).map((item) => item.id));
    threadRequestTokens.set(key, requestToken);
    loadingThreadKeys = new Set([...loadingThreadKeys, key]);
    failedThreadKeys = new Set([...failedThreadKeys].filter((candidate) => candidate !== key));
    if (failedThreadKeys.size === 0) threadLoadError = null;
    const snapshotCursor = activityEventCursor;
    const baseQuery = {
      provider: subject.repo.provider,
      platform_host: subject.repo.platform_host,
      platform_repo_id: platformRepoID,
      item_type: itemType,
      item_number: subject.item_number,
      since: computeSince(),
      ...(snapshotCursor ? { at_or_before: snapshotCursor } : {}),
      limit: 100,
      ...(filterTypes.length > 0 ? { types: [...filterTypes] } : {}),
      ...(searchQuery ? { search: searchQuery } : {}),
      ...(hideClosedMerged ? { hide_closed_merged: true } : {}),
      ...(hideBots ? { hide_bots: true } : {}),
      ...(hideDefaultBranchActivity ? { hide_default_branch: true } : {}),
    };
    const isCurrentRequest = () =>
      requestGeneration === pagedActivityGeneration &&
      requestScope === pagedActivityScopeKey() &&
      threadRequestTokens.get(key) === requestToken;
    const readAllPages = Effect.gen(function* () {
      let before = "";
      const pageResults: OwnedActivityResponse[] = [];
      while (true) {
        const query = {
          ...baseQuery,
          ...(before ? { before } : {}),
        };
        const startedAt = yield* Effect.sync(() => ++activityLifecycleTick);
        const response = yield* executeGeneratedApiRequest("GET /activity/thread-events", (client, signal) =>
          client.GET("/activity/thread-events", { params: { query }, signal }),
        ).pipe(retryIdempotentRead);
        if (!(yield* Effect.sync(isCurrentRequest))) return;
        pageResults.push({ response, startedAt });
        const next = response.next_cursor ?? "";
        if (next === "") break;
        before = next;
      }
      yield* Effect.sync(() => {
        if (!isCurrentRequest()) return;
        const replacement = projectOwnedActivityPages(pageResults);
        const replacementIDs = new Set(replacement.map((item) => item.id));
        items = [
          ...items.filter((item) => {
            if (stableParentKey(item) !== key) return true;
            return !initialThreadItemIDs.has(item.id) && !replacementIDs.has(item.id);
          }),
          ...replacement,
        ];
        reconcileItemsWithParentSubjects(itemActivity);
      });
    });
    runtime.runCommand(
      readAllPages.pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            if (!isCurrentRequest()) return;
            loadedThreadKeys = new Set([...loadedThreadKeys, key]);
          }),
        ),
        Effect.tapError((failure) =>
          Effect.sync(() => {
            if (!isCurrentRequest()) return;
            failedThreadKeys = new Set([...failedThreadKeys, key]);
            threadLoadError = readErrorMessage(failure, "could not load activity thread");
          }),
        ),
        Effect.ensuring(
          Effect.sync(() => {
            if (threadRequestTokens.get(key) !== requestToken) return;
            threadRequestTokens.delete(key);
            loadingThreadKeys = new Set([...loadingThreadKeys].filter((candidate) => candidate !== key));
          }),
        ),
      ),
      {
        operation: "load activity thread",
        safeContext: { itemType: subject.item_type, itemNumber: subject.item_number },
        onFailure: () => {},
      },
    );
  }

  function retryFailedThreadLoads(): void {
    const retryKeys = [...failedThreadKeys].filter((key) => isThreadItemExpanded(key));
    failedThreadKeys = new Set();
    threadLoadError = null;
    for (const key of retryKeys) loadThreadEvents(key);
  }

  function loadBulkActivity(): void {
    if (activityEventCursor === "") {
      loadActivity();
      return;
    }
    pagedActivityGeneration += 1;
    threadRequestTokens.clear();
    loadingThreadKeys = new Set();
    const requestGeneration = pagedActivityGeneration;
    const requestScope = pagedActivityScopeKey();
    const requestToken = Symbol("bulk activity");
    bulkRequestToken = requestToken;
    const initialItemIDs = new Set(items.map((item) => item.id));
    const snapshotCursor = activityEventCursor;
    const baseParams = buildParams();
    baseParams.projection = "events";
    baseParams.limit = 500;
    baseParams.at_or_before = snapshotCursor;
    const isCurrentRequest = () =>
      requestGeneration === pagedActivityGeneration &&
      requestScope === pagedActivityScopeKey() &&
      bulkRequestToken === requestToken;
    loading = true;
    storeError = null;
    const program = Effect.gen(function* () {
      let before = "";
      const pageResults: OwnedActivityResponse[] = [];
      while (true) {
        const params = { ...baseParams, ...(before !== "" ? { before } : {}) };
        const result = yield* activityRead(params);
        if (!(yield* Effect.sync(isCurrentRequest))) return;
        pageResults.push(result);
        const next = result.response.next_cursor ?? "";
        if (next === "") break;
        before = next;
      }
      yield* Effect.sync(() => {
        if (!isCurrentRequest()) return;
        const replacement = projectOwnedActivityPages(pageResults);
        const replacementIDs = new Set(replacement.map((item) => item.id));
        items = [
          ...items.filter((item) => !initialItemIDs.has(item.id) && !replacementIDs.has(item.id)),
          ...replacement,
        ];
        reconcileItemsWithParentSubjects(itemActivity);
      });
    }).pipe(
      Effect.tapError((failure) =>
        Effect.sync(() => {
          if (!isCurrentRequest()) return;
          storeError = readErrorMessage(failure, "could not load full activity");
        }),
      ),
      Effect.ensuring(
        Effect.sync(() => {
          if (!isCurrentRequest()) return;
          bulkRequestToken = undefined;
          loading = false;
        }),
      ),
    );
    runtime.runCommand(program, {
      operation: "expand all activity",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function loadActivity(forceAuthors = false): void {
    invalidatePagedActivityRequests();
    loadActivityAuthors(forceAuthors || authorsLoading);
    runtime.runCommand(loadActivityEffect(), {
      operation: "load activity",
      safeContext: {},
      onFailure: (failure) => {
        storeError = readErrorMessage(failure);
        loading = false;
      },
    });
  }

  function refreshActivityProgram(params: ActivityParams) {
    return Effect.gen(function* () {
      if (shouldUseCollapsedAuthoritativeProjection()) params.projection = "collapsed";
      const workflow = yield* ActivityWorkflow;
      yield* workflow.pollSnapshotRead(
        activityProjectionScope(params),
        activityRead(params),
        (result) => Effect.sync(() => projectAuthoritativeActivitySnapshot(result)),
        Effect.sync(() => {
          loading = false;
        }),
      );
    });
  }

  // Mark a notification feed row as seen: queues the GitHub read
  // propagation backend-side and flips the row to read locally so the
  // unread affordance clears without waiting for the next sync. The
  // activity item id for a notification is "ntf:<db id>".
  function markNotificationSeen(item: ActivityItem): void {
    const id = notificationDbId(item.id);
    if (id === null) return;
    const mutationTick = ++activityLifecycleTick;
    const baseline = item.item_state;
    let acknowledged = false;
    const apply = (state: string) =>
      Effect.sync(() => {
        notificationStateOwnership.set(item.id, { tick: mutationTick, state });
        items = items.map((candidate) => (candidate.id === item.id ? { ...candidate, item_state: state } : candidate));
      });
    const program = Effect.gen(function* () {
      const mutations = yield* ProviderMutations;
      const commit = executeGeneratedApiRequest<NotificationBulkResponse>(
        "POST mark notification read",
        (client, signal) =>
          client.POST("/notifications/read", {
            body: { ids: [id] },
            signal,
          }),
      ).pipe(
        Effect.map((response) => {
          acknowledged = [...(response.succeeded ?? []), ...(response.queued ?? [])].includes(id);
          return acknowledged ? "read" : baseline;
        }),
      );
      yield* mutations.submit({
        key: `notification\u0000${encodeURIComponent(String(id))}\u0000seen`,
        baseline,
        optimistic: "read",
        apply,
        commit,
        refreshOnStale: Effect.succeed(baseline),
      });
      if (acknowledged) {
        const acknowledgementTick = ++activityLifecycleTick;
        notificationStateOwnership.set(item.id, { tick: acknowledgementTick, state: "read" });
        items = items.map((candidate) => (candidate.id === item.id ? { ...candidate, item_state: "read" } : candidate));
      } else {
        notificationStateOwnership.delete(item.id);
        showFlash("Failed to mark notification as read.", { tone: "danger" });
      }
    });
    runtime.runCommand(program, {
      operation: "mark notification read",
      safeContext: { notificationId: id },
      onFailure: (failure) => {
        showFlash(providerMutationFailureMessage(failure, "failed to mark notification as read"), { tone: "danger" });
      },
    });
  }

  const pollNewItems = Effect.suspend(() => {
    if (loading) return Effect.void;
    pollCount += 1;
    const params = buildParams();
    if (items.length === 0 && itemActivity.length === 0 && workspaceActivity.length === 0) {
      loading = true;
      return loadActivityProgram(params, "poll").pipe(Effect.andThen(loadActivityAuthorsEffect(true)));
    }
    if (pollCount % AUTHORITATIVE_REFRESH_EVERY === 0) {
      return refreshActivityProgram(params).pipe(Effect.andThen(loadActivityAuthorsEffect(true)));
    }
    if (activityEventCursor === "") return Effect.void;
    const authoritativeParams = { ...params };
    if (shouldUseCollapsedAuthoritativeProjection()) authoritativeParams.projection = "collapsed";
    const scope = activityProjectionScope(authoritativeParams);
    params.projection = "events";
    params.limit = 500;
    params.after = activityEventCursor;
    return Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const projectPoll = ({ mode, result }: ActivityPollProjection) =>
        Effect.gen(function* () {
          const activityChanged = yield* Effect.sync(() => {
            if (mode === "replace") {
              projectAuthoritativeActivitySnapshot(result);
              return true;
            }
            const existingIds = new Set(items.map((item) => item.id));
            const newItems = projectOwnedNotificationStates(result, false).filter((item) => !existingIds.has(item.id));
            activityEventCursor = result.response.event_cursor ?? activityEventCursor;
            const cutoff = new Date(Date.now() - RANGE_MS[timeRange]);
            const nextItems = (newItems.length > 0 ? [...newItems, ...items] : items).filter(
              (item) => new Date(item.created_at) >= cutoff,
            );
            const activityChanged = newItems.length > 0 || nextItems.length !== items.length;
            if (activityChanged) items = nextItems;
            return activityChanged;
          });
          if (activityChanged) yield* loadActivityAuthorsEffect(true);
        });
      yield* workflow.pollRead(
        scope,
        activityRead(params),
        (result) => {
          if (!result.response.capped) return projectPoll({ mode: "append", result });
          const replacementParams = buildParams();
          if (shouldUseCollapsedAuthoritativeProjection()) replacementParams.projection = "collapsed";
          return workflow.pollSnapshotRead(
            activityProjectionScope(replacementParams),
            activityRead(replacementParams),
            (replacement) => projectPoll({ mode: "replace", result: replacement }),
          );
        },
        Effect.sync(() => {
          loading = false;
        }),
      );
    });
  }).pipe(
    Effect.catch(() => Effect.void),
    Effect.asVoid,
  );

  function startActivityPolling(): void {
    const program = Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      yield* workflow.poll(pollNewItems, "15 seconds");
    });
    runtime.runCommand(program, {
      operation: "poll activity",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function stopActivityPolling(): void {
    const program = Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      yield* workflow.stopPolling;
    });
    runtime.runCommand(program, {
      operation: "stop activity polling",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function rebuildFilterTypes(): void {
    filterTypes = buildActivityFilterTypes(
      enabledItemTypes,
      enabledEvents,
      hideDefaultBranchActivity,
      showNotifications,
    );
  }

  function applyCollapsedFromURL(): void {
    const sp = new URLSearchParams(window.location.search);
    if (!sp.has("collapsed")) return;
    const v = sp.get("collapsed");
    if (v === "1") collapseThreads = true;
    else if (v === "0") collapseThreads = false;
  }

  function deleteCollapsedParam(): void {
    const sp = new URLSearchParams(window.location.search);
    if (!sp.has("collapsed")) return;
    sp.delete("collapsed");
    const qs = sp.toString();
    const path = window.location.pathname || getBasePath();
    history.replaceState(null, "", path + (qs ? `?${qs}` : ""));
  }

  function syncFromURL(): void {
    const sp = new URLSearchParams(window.location.search);
    const legacySelections = sp.has("types") ? readLegacyFilterSelections(sp.get("types") ?? "") : undefined;
    enabledItemTypes = sp.has(ACTIVITY_ITEM_TYPES_PARAM)
      ? readSelectedFilters(sp.get(ACTIVITY_ITEM_TYPES_PARAM), DEFAULT_ACTIVITY_ITEM_TYPES)
      : (legacySelections?.itemTypes ?? new Set(DEFAULT_ACTIVITY_ITEM_TYPES));
    enabledEvents = sp.has(ACTIVITY_EVENT_TYPES_PARAM)
      ? readSelectedFilters(sp.get(ACTIVITY_EVENT_TYPES_PARAM), DEFAULT_EVENT_TYPES)
      : (legacySelections?.events ?? new Set(DEFAULT_EVENT_TYPES));
    if (sp.has("search")) searchQuery = sp.get("search") ?? undefined;
    authorFilter = sp.get("author")?.trim() || undefined;
    if (sp.has("range")) {
      const rangeParam = sp.get("range");
      if (rangeParam && rangeParam in RANGE_MS) timeRange = rangeParam as TimeRange;
    }
    if (sp.has("view")) {
      const viewParam = sp.get("view");
      if (viewParam === "flat" || viewParam === "threaded") viewMode = viewParam;
    }
    rollUpCommits = sp.get("rollup_commits") === "1";
    hideDefaultBranchActivity = sp.get("hide_branch") === "1";
    showNotifications = sp.get("notif") !== "0";
    applyCollapsedFromURL();
    rebuildFilterTypes();
  }

  function syncToURL(): void {
    const sp = new URLSearchParams(window.location.search);
    rebuildFilterTypes();
    sp.delete("types");
    writeSelectedFilters(sp, ACTIVITY_ITEM_TYPES_PARAM, enabledItemTypes, DEFAULT_ACTIVITY_ITEM_TYPES);
    writeSelectedFilters(sp, ACTIVITY_EVENT_TYPES_PARAM, enabledEvents, DEFAULT_EVENT_TYPES);
    if (searchQuery) sp.set("search", searchQuery);
    else sp.delete("search");
    if (authorFilter) sp.set("author", authorFilter);
    else sp.delete("author");
    if (timeRange !== "7d") sp.set("range", timeRange);
    else sp.delete("range");
    if (viewMode !== "flat") sp.set("view", viewMode);
    else sp.delete("view");
    if (rollUpCommits) sp.set("rollup_commits", "1");
    else sp.delete("rollup_commits");
    if (hideDefaultBranchActivity) sp.set("hide_branch", "1");
    else sp.delete("hide_branch");
    if (!showNotifications) sp.set("notif", "0");
    else sp.delete("notif");
    if (collapseThreads !== collapseThreadsDefault) {
      sp.set("collapsed", collapseThreads ? "1" : "0");
    } else {
      sp.delete("collapsed");
    }
    const qs = sp.toString();
    const path = window.location.pathname || getBasePath();
    const url = path + (qs ? `?${qs}` : "");
    history.replaceState(null, "", url);
  }

  return {
    getActivityItems,
    getItemActivity,
    getWorkspaceActivity,
    isActivityLoading,
    getActivityError,
    getThreadLoadError,
    isActivityCapped,
    isItemActivityCapped,
    getActivityEventCursor,
    getActivityFilterTypes,
    getActivitySearch,
    getActivityAuthor,
    getActivityAuthors,
    isActivityAuthorsLoading,
    getActivityAuthorsError,
    getTimeRange,
    getViewMode,
    getCollapseThreads,
    getRollUpCommits,
    isThreadItemExpanded,
    getHideClosedMerged,
    getHideBots,
    getUseWorkspaceActivityForRecency,
    getHideDefaultBranchActivity,
    getEnabledItemTypes,
    getEnabledEvents,
    getShowNotifications,
    getInvolvesMe,
    isInitialized,
    setActivityFilterTypes,
    setActivitySearch,
    setActivityAuthor,
    setTimeRange,
    setViewMode,
    setFullEventProjectionRequired,
    setRollUpCommits,
    collapseAllThreads,
    expandAllThreads,
    toggleThreadItem,
    retryFailedThreadLoads,
    setHideClosedMerged,
    setHideBots,
    setHideDefaultBranchActivity,
    setEnabledItemTypes,
    setEnabledEvents,
    setShowNotifications,
    setInvolvesMe,
    hydrateDefaults,
    initializeFromMount,
    loadActivityAuthors,
    loadActivity,
    loadActivityEffect,
    reconcileActivityEffect,
    markNotificationSeen,
    startActivityPolling,
    stopActivityPolling,
    syncFromURL,
    syncToURL,
  };
}

export type ActivityStore = ReturnType<typeof createActivityStore>;
