import { Effect } from "effect";
import type { AppRuntime } from "../app/runtime.js";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { retryIdempotentRead } from "../api/retry-policy.js";
import type {
  ActivityItem,
  ActivityParams,
  ActivityResponse,
  ActivitySettings,
  NotificationBulkResponse,
} from "../api/types.js";
import { ActivityWorkflow } from "./activity-workflow.js";
import { showFlash } from "./flash.svelte.js";
import { ProviderMutations, providerMutationFailureMessage } from "./ordered-mutations.js";

export type TimeRange = "24h" | "7d" | "30d" | "90d";
export type ViewMode = "flat" | "threaded";
export type ActivityItemType = "pr" | "issue";
export type ActivityAPIItemType = ActivityItemType | "repo";

export const DEFAULT_ACTIVITY_ITEM_TYPES = ["pr", "issue"] as const;
export const DEFAULT_EVENT_TYPES = ["comment", "review", "commit", "force_push"] as const;
const NO_ACTIVITY_FILTER_TYPE = "none";

// Default-branch activity rows render as "Commit"/"Force-pushed" just like
// their PR counterparts, so the event-type toggles must govern both kinds.
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

  // Keep the established notification-only representation free of opening
  // events when both item scopes remain at their default.
  if (enabledItemTypes.size === DEFAULT_ACTIVITY_ITEM_TYPES.length && enabledEvents.size === 0 && showNotifications) {
    return ["notification"];
  }

  const types: string[] = [];
  if (enabledItemTypes.size === 0) types.push(NO_ACTIVITY_FILTER_TYPE);
  if (enabledItemTypes.has("pr")) types.push("new_pr");
  if (enabledItemTypes.has("issue")) types.push("new_issue");
  if (!hideDefaultBranchActivity) {
    for (const evt of DEFAULT_EVENT_TYPES) {
      const branchType = BRANCH_TYPE_FOR_EVENT[evt];
      if (branchType && enabledEvents.has(evt)) types.push(branchType);
    }
  }
  for (const evt of DEFAULT_EVENT_TYPES) {
    if (enabledEvents.has(evt)) types.push(evt);
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

function readErrorMessage(error: ApiProblemError | TransientTransportError): string {
  if (error._tag === "ApiProblemError") {
    return apiErrorMessage(error.problem, "failed to load activity");
  }
  return "Could not reach Kenn Forge";
}

export function createActivityStore(opts: ActivityStoreOptions) {
  const runtime = opts.runtime;
  const getGlobalRepo = opts.getGlobalRepo ?? (() => undefined);
  const getBasePath = opts.getBasePath ?? (() => "/");

  // --- state ---

  let items = $state<ActivityItem[]>([]);
  let loading = $state(false);
  let storeError = $state<string | null>(null);
  let capped = $state(false);
  let filterTypes = $state<string[]>([]);
  let searchQuery = $state<string | undefined>(undefined);
  let timeRange = $state<TimeRange>("7d");
  let viewMode = $state<ViewMode>("flat");
  let collapseThreads = $state(false);
  let rollUpCommits = $state(false);
  let collapseThreadsDefault = false;
  let expandOverrides = $state<Set<string>>(new Set());
  let pollCount = 0;
  const FULL_REFRESH_EVERY = 4;
  let activityLifecycleTick = 0;
  const notificationStateOwnership = new Map<string, { readonly tick: number; readonly state: string }>();

  let hideClosedMerged = $state(false);
  let hideBots = $state(false);
  let hideDefaultBranchActivity = $state(false);
  let enabledItemTypes = $state<Set<ActivityItemType>>(new Set(DEFAULT_ACTIVITY_ITEM_TYPES));
  let enabledEvents = $state<Set<string>>(new Set(DEFAULT_EVENT_TYPES));
  let showNotifications = $state(true);
  let initialized = false;

  // --- reads ---

  function getActivityItems(): ActivityItem[] {
    return items;
  }
  function isActivityLoading(): boolean {
    return loading;
  }
  function getActivityError(): string | null {
    return storeError;
  }
  function isActivityCapped(): boolean {
    return capped;
  }
  function getActivityFilterTypes(): string[] {
    return filterTypes;
  }
  function getActivitySearch(): string | undefined {
    return searchQuery;
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
  function isInitialized(): boolean {
    return initialized;
  }

  // --- writes ---

  function setActivityFilterTypes(types: string[]): void {
    filterTypes = types;
  }
  function setActivitySearch(q: string | undefined): void {
    searchQuery = q;
  }
  function setTimeRange(range_: TimeRange): void {
    timeRange = range_;
  }
  function setViewMode(mode: ViewMode): void {
    viewMode = mode;
  }
  function setRollUpCommits(value: boolean): void {
    rollUpCommits = value;
  }
  function collapseAllThreads(): void {
    collapseThreads = true;
    expandOverrides = new Set();
    syncToURL();
  }
  function expandAllThreads(): void {
    collapseThreads = false;
    expandOverrides = new Set();
    syncToURL();
  }
  function toggleThreadItem(key: string): void {
    // Per-item overrides are session-only and intentionally not synced to the
    // URL; only collapse-all/expand-all persist via collapseThreads.
    const next = new Set(expandOverrides);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    expandOverrides = next;
  }
  function setHideClosedMerged(v: boolean): void {
    hideClosedMerged = v;
  }
  function setHideBots(v: boolean): void {
    hideBots = v;
  }
  function setHideDefaultBranchActivity(v: boolean): void {
    hideDefaultBranchActivity = v;
  }
  function setEnabledItemTypes(itemTypes: Set<ActivityItemType>): void {
    enabledItemTypes = itemTypes;
  }
  function setEnabledEvents(events: Set<string>): void {
    enabledEvents = events;
  }
  function setShowNotifications(v: boolean): void {
    showNotifications = v;
  }
  // --- hydration ---

  function hydrateDefaults(activity: ActivitySettings): void {
    viewMode = activity.view_mode;
    timeRange = activity.time_range;
    hideClosedMerged = activity.hide_closed;
    hideBots = activity.hide_bots;
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
    const repo = getGlobalRepo();
    if (repo) p.repo = repo;
    if (filterTypes.length > 0) p.types = filterTypes;
    const itemTypes = buildActivityItemTypeFilter(enabledItemTypes);
    if (itemTypes.length < DEFAULT_ACTIVITY_ITEM_TYPES.length + 1) {
      p.item_types = itemTypes;
    }
    if (searchQuery) p.search = searchQuery;
    return p;
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

  function loadActivityProgram(params: ActivityParams, owner: "foreground" | "poll" = "foreground") {
    return Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const read = activityRead(params);
      const project = (result: OwnedActivityResponse) =>
        Effect.sync(() => {
          items = projectOwnedNotificationStates(result);
          capped = result.response.capped;
          loading = false;
        });
      const clearLoading = Effect.sync(() => {
        loading = false;
      });
      yield* owner === "foreground"
        ? workflow.load(read, project, clearLoading)
        : workflow.pollRead(read, project, clearLoading);
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
      const read = activityRead(params);
      const project = (result: OwnedActivityResponse) =>
        Effect.sync(() => {
          items = projectOwnedNotificationStates(result);
          capped = result.response.capped;
          loading = false;
        });
      return Effect.gen(function* () {
        const workflow = yield* ActivityWorkflow;
        yield* workflow.reconcileRead(read, project);
      });
    });
  }

  function loadActivity(): void {
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
      const workflow = yield* ActivityWorkflow;
      yield* workflow.pollRead(activityRead(params), (result) => {
        const fresh = projectOwnedNotificationStates(result);
        if (fresh.length === 0) return Effect.void;
        return Effect.sync(() => {
          const freshById = new Map(fresh.map((item) => [item.id, item]));
          items = items.map((item) => {
            const updated = freshById.get(item.id);
            return updated && updated.item_state !== item.item_state
              ? { ...item, item_state: updated.item_state }
              : item;
          });
        });
      });
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
    if (items.length === 0) {
      loading = true;
      return loadActivityProgram(params, "poll");
    }
    if (pollCount % FULL_REFRESH_EVERY === 0) {
      return refreshActivityProgram(params);
    }
    const newestItem = items[0];
    if (newestItem === undefined) return Effect.void;
    params.after = newestItem.cursor;
    return Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const pollRead = Effect.gen(function* () {
        const result = yield* activityRead(params);
        if (!result.response.capped) {
          return { mode: "append", result } satisfies ActivityPollProjection;
        }
        const replacement = yield* activityRead(buildParams());
        return { mode: "replace", result: replacement } satisfies ActivityPollProjection;
      });
      yield* workflow.pollRead(
        pollRead,
        ({ mode, result }) =>
          Effect.sync(() => {
            if (mode === "replace") {
              items = projectOwnedNotificationStates(result);
              capped = result.response.capped;
              loading = false;
              return;
            }
            const existingIds = new Set(items.map((item) => item.id));
            const newItems = projectOwnedNotificationStates(result, false).filter((item) => !existingIds.has(item.id));
            if (newItems.length > 0) {
              items = [...newItems, ...items];
            }
            const cutoff = new Date(Date.now() - RANGE_MS[timeRange]);
            items = items.filter((item) => new Date(item.created_at) >= cutoff);
          }),
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

  // deriveFiltersFromTypes reconstructs the dropdown state from the
  // persisted `types` list. The notification toggle is NOT inferred
  // from list membership: a legacy URL listing every event type but no
  // "notification" must still mean "show everything" rather than
  // "notifications hidden", so showNotifications is carried by its own
  // `notif` URL param (read in syncFromURL) instead.
  function deriveFiltersFromTypes(): void {
    if (filterTypes.length === 0) {
      enabledItemTypes = new Set(DEFAULT_ACTIVITY_ITEM_TYPES);
      enabledEvents = new Set(DEFAULT_EVENT_TYPES);
    } else {
      const notificationOnly = filterTypes.length === 1 && filterTypes[0] === "notification";
      enabledItemTypes = notificationOnly
        ? new Set(DEFAULT_ACTIVITY_ITEM_TYPES)
        : new Set(
            DEFAULT_ACTIVITY_ITEM_TYPES.filter((itemType) =>
              filterTypes.includes(itemType === "pr" ? "new_pr" : "new_issue"),
            ),
          );
      enabledEvents = new Set(DEFAULT_EVENT_TYPES.filter((t) => filterTypes.includes(t)));
    }
    // Rebuild so the request matches the filter state the dropdown
    // shows: legacy URLs can list default_branch_commit while commit is
    // deselected, and an empty list with notifications hidden must
    // become the explicit exclusion list a bare `[]` cannot express.
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
    if (sp.has("types")) {
      const typesParam = sp.get("types");
      filterTypes = typesParam ? typesParam.split(",") : [];
    }
    if (sp.has("search")) searchQuery = sp.get("search") ?? undefined;
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
    deriveFiltersFromTypes();
  }

  function syncToURL(): void {
    const sp = new URLSearchParams(window.location.search);
    if (filterTypes.length > 0) sp.set("types", filterTypes.join(","));
    else sp.delete("types");
    if (searchQuery) sp.set("search", searchQuery);
    else sp.delete("search");
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
    isActivityLoading,
    getActivityError,
    isActivityCapped,
    getActivityFilterTypes,
    getActivitySearch,
    getTimeRange,
    getViewMode,
    getCollapseThreads,
    getRollUpCommits,
    isThreadItemExpanded,
    getHideClosedMerged,
    getHideBots,
    getHideDefaultBranchActivity,
    getEnabledItemTypes,
    getEnabledEvents,
    getShowNotifications,
    isInitialized,
    setActivityFilterTypes,
    setActivitySearch,
    setTimeRange,
    setViewMode,
    setRollUpCommits,
    collapseAllThreads,
    expandAllThreads,
    toggleThreadItem,
    setHideClosedMerged,
    setHideBots,
    setHideDefaultBranchActivity,
    setEnabledItemTypes,
    setEnabledEvents,
    setShowNotifications,
    hydrateDefaults,
    initializeFromMount,
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
