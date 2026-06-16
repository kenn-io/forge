import type { ActivityItem, ActivityParams, ActivitySettings } from "../api/types.js";
import type { MiddlemanClient } from "../types.js";

export type TimeRange = "24h" | "7d" | "30d" | "90d";
export type ViewMode = "flat" | "threaded";
export type ItemFilter = "all" | "prs" | "issues";

export const DEFAULT_EVENT_TYPES = ["comment", "review", "commit", "force_push"] as const;

// Default-branch activity rows render as "Commit"/"Force-pushed" just like
// their PR counterparts, so the event-type toggles must govern both kinds.
const BRANCH_TYPE_FOR_EVENT: Partial<Record<string, string>> = {
  commit: "default_branch_commit",
  force_push: "default_branch_force_push",
};

export function buildActivityFilterTypes(
  itemFilter: ItemFilter,
  enabledEvents: ReadonlySet<string>,
  hideDefaultBranchActivity: boolean,
  showNotifications = true,
): string[] {
  const allSelected =
    itemFilter === "all" &&
    enabledEvents.size === DEFAULT_EVENT_TYPES.length &&
    !hideDefaultBranchActivity &&
    showNotifications;
  // An empty list means "no type filter" — the backend returns every
  // activity_type, notifications included. Only short-circuit when the
  // notification toggle is also at its default, otherwise fall through
  // to build the explicit list that omits "notification".
  if (allSelected) return [];

  // Notifications-only inbox: every event type is deselected, the item
  // filter is unscoped, and notifications are on. Return just the
  // notification type so the PR/issue "Opened" anchor rows (new_pr /
  // new_issue) do not leak into a view the user narrowed to
  // notifications. A bare [] cannot express this (it means "everything").
  if (itemFilter === "all" && enabledEvents.size === 0 && showNotifications) {
    return ["notification"];
  }

  const types: string[] = [];
  if (itemFilter === "prs") types.push("new_pr");
  else if (itemFilter === "issues") types.push("new_issue");
  else {
    types.push("new_pr", "new_issue");
    if (!hideDefaultBranchActivity) {
      for (const evt of DEFAULT_EVENT_TYPES) {
        const branchType = BRANCH_TYPE_FOR_EVENT[evt];
        if (branchType && enabledEvents.has(evt)) types.push(branchType);
      }
    }
  }
  for (const evt of DEFAULT_EVENT_TYPES) {
    if (enabledEvents.has(evt)) types.push(evt);
  }
  if (showNotifications) types.push("notification");
  return types;
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

export interface ActivityStoreOptions {
  client: MiddlemanClient;
  getGlobalRepo?: () => string | undefined;
  getBasePath?: () => string;
}

function apiErrorMessage(error: { detail?: string; title?: string }, fallback: string): string {
  return error.detail ?? error.title ?? fallback;
}

export function createActivityStore(opts: ActivityStoreOptions) {
  const apiClient = opts.client;
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
  let pollHandle: ReturnType<typeof setInterval> | null = null;
  let pollInFlight = false;
  let requestVersion = 0;
  let pollCount = 0;
  const FULL_REFRESH_EVERY = 4;

  let hideClosedMerged = $state(false);
  let hideBots = $state(false);
  let hideDefaultBranchActivity = $state(false);
  let enabledEvents = $state<Set<string>>(new Set(DEFAULT_EVENT_TYPES));
  let showNotifications = $state(true);
  let itemFilter = $state<ItemFilter>("all");
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
  function getEnabledEvents(): Set<string> {
    return enabledEvents;
  }
  function getShowNotifications(): boolean {
    return showNotifications;
  }
  function getItemFilter(): ItemFilter {
    return itemFilter;
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
  function setEnabledEvents(events: Set<string>): void {
    enabledEvents = events;
  }
  function setShowNotifications(v: boolean): void {
    showNotifications = v;
  }
  function setItemFilter(f: ItemFilter): void {
    itemFilter = f;
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
    if (searchQuery) p.search = searchQuery;
    return p;
  }

  async function loadActivity(): Promise<void> {
    const version = ++requestVersion;
    loading = true;
    storeError = null;
    try {
      const { data, error: requestError } = await apiClient.GET("/activity", {
        params: { query: buildParams() },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to load activity"));
      }
      if (version !== requestVersion) return;
      items = data?.items ?? [];
      capped = data?.capped ?? false;
    } catch (err) {
      if (version !== requestVersion) return;
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      if (version === requestVersion) loading = false;
    }
  }

  async function refreshActivity(): Promise<void> {
    const versionAtStart = requestVersion;
    try {
      const { data, error: requestError } = await apiClient.GET("/activity", {
        params: { query: buildParams() },
      });
      if (requestError || versionAtStart !== requestVersion) return;
      const fresh = data?.items ?? [];
      if (fresh.length === 0) return;
      const freshById = new Map(fresh.map((it) => [it.id, it]));
      items = items.map((it) => {
        const updated = freshById.get(it.id);
        if (updated && updated.item_state !== it.item_state) {
          return { ...it, item_state: updated.item_state };
        }
        return it;
      });
    } catch {
      // silent
    }
  }

  // Mark a notification feed row as seen: queues the GitHub read
  // propagation backend-side and flips the row to read locally so the
  // unread affordance clears without waiting for the next sync. The
  // activity item id for a notification is "ntf:<db id>".
  async function markNotificationSeen(item: ActivityItem): Promise<void> {
    const id = notificationDbId(item.id);
    if (id === null) return;
    // Optimistically flip locally; QueueNotificationIDsRead persists
    // unread=0, so a later feed reload agrees.
    items = items.map((it) => (it.id === item.id ? { ...it, item_state: "read" } : it));
    const rollback = () => {
      items = items.map((it) => (it.id === item.id ? { ...it, item_state: "unread" } : it));
    };
    try {
      const { data, error: requestError } = await apiClient.POST("/notifications/read", {
        body: { ids: [id] },
      });
      // The endpoint is bulk-shaped and can return 200 while reporting
      // this id in `failed`. Only keep the optimistic flip when the id
      // was actually queued/acknowledged.
      const acked = !!data && [...(data.succeeded ?? []), ...(data.queued ?? [])].includes(id);
      if (requestError || !acked) {
        rollback();
      }
    } catch {
      rollback();
    }
  }

  async function pollNewItems(): Promise<void> {
    if (pollInFlight) return;
    pollInFlight = true;
    try {
      await doPoll();
    } finally {
      pollInFlight = false;
    }
  }

  async function doPoll(): Promise<void> {
    if (loading) return;
    pollCount++;
    if (items.length === 0) {
      await loadActivity();
      return;
    }
    if (pollCount % FULL_REFRESH_EVERY === 0) {
      await refreshActivity();
      return;
    }
    const versionAtStart = requestVersion;
    try {
      const params = buildParams();
      params.after = items[0]!.cursor;
      const { data, error: requestError } = await apiClient.GET("/activity", {
        params: { query: params },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to poll activity"));
      }
      if (versionAtStart !== requestVersion) return;
      const resp = data;
      if (!resp) return;
      if (resp.capped) {
        await loadActivity();
        return;
      }
      const nextItems = resp.items ?? [];
      if (nextItems.length > 0) {
        const existingIds = new Set(items.map((it) => it.id));
        const newItems = nextItems.filter((it) => !existingIds.has(it.id));
        if (newItems.length > 0) {
          items = [...newItems, ...items];
        }
      }
    } catch {
      // Silent poll failure
    }
    if (versionAtStart !== requestVersion) return;
    const cutoff = new Date(Date.now() - RANGE_MS[timeRange]);
    items = items.filter((it) => new Date(it.created_at) >= cutoff);
  }

  function startActivityPolling(): void {
    stopActivityPolling();
    pollHandle = setInterval(() => {
      void pollNewItems();
    }, 15_000);
  }

  function stopActivityPolling(): void {
    if (pollHandle !== null) {
      clearInterval(pollHandle);
      pollHandle = null;
    }
  }

  // deriveFiltersFromTypes reconstructs the dropdown state from the
  // persisted `types` list. The notification toggle is NOT inferred
  // from list membership: a legacy URL listing every event type but no
  // "notification" must still mean "show everything" rather than
  // "notifications hidden", so showNotifications is carried by its own
  // `notif` URL param (read in syncFromURL) instead.
  function deriveFiltersFromTypes(): void {
    if (filterTypes.length === 0) {
      itemFilter = "all";
      enabledEvents = new Set(DEFAULT_EVENT_TYPES);
    } else {
      const hasPR = filterTypes.includes("new_pr");
      const hasIssue = filterTypes.includes("new_issue");
      if (hasPR && !hasIssue) itemFilter = "prs";
      else if (hasIssue && !hasPR) itemFilter = "issues";
      else itemFilter = "all";
      enabledEvents = new Set(DEFAULT_EVENT_TYPES.filter((t) => filterTypes.includes(t)));
    }
    // Rebuild so the request matches the filter state the dropdown
    // shows: legacy URLs can list default_branch_commit while commit is
    // deselected, and an empty list with notifications hidden must
    // become the explicit exclusion list a bare `[]` cannot express.
    filterTypes = buildActivityFilterTypes(itemFilter, enabledEvents, hideDefaultBranchActivity, showNotifications);
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
    getEnabledEvents,
    getShowNotifications,
    getItemFilter,
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
    setEnabledEvents,
    setShowNotifications,
    setItemFilter,
    hydrateDefaults,
    initializeFromMount,
    loadActivity,
    markNotificationSeen,
    startActivityPolling,
    stopActivityPolling,
    syncFromURL,
    syncToURL,
  };
}

export type ActivityStore = ReturnType<typeof createActivityStore>;
