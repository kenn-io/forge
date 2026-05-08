import type {
  NotificationItem,
  NotificationParams,
  NotificationSummary,
  NotificationSyncStatus,
} from "../api/types.js";
import type { MiddlemanClient } from "../types.js";

export type NotificationState = "unread" | "active" | "read" | "done" | "all";
export type NotificationSort = "priority" | "updated_desc" | "updated_asc" | "repo";

export interface NotificationsStoreOptions {
  client: MiddlemanClient;
  getGlobalRepo?: () => string | undefined;
}

function apiErrorMessage(
  error: { detail?: string; title?: string },
  fallback: string,
): string {
  return error.detail ?? error.title ?? fallback;
}

function emptySummary(): NotificationSummary {
  return {
    total_active: 0,
    unread: 0,
    done: 0,
    by_reason: {},
    by_repo: {},
  };
}

function emptySyncStatus(): NotificationSyncStatus {
  return {
    running: false,
    last_error: "",
  };
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export function createNotificationsStore(
  opts: NotificationsStoreOptions,
) {
  const apiClient = opts.client;
  const getGlobalRepo = opts.getGlobalRepo ?? (() => undefined);

  let items = $state<NotificationItem[]>([]);
  let summary = $state<NotificationSummary>(emptySummary());
  let syncStatus = $state<NotificationSyncStatus>(emptySyncStatus());
  let loading = $state(false);
  let actionInFlight = $state(false);
  let storeError = $state<string | null>(null);
  let stateFilter = $state<NotificationState>("unread");
  let reasonFilter = $state<string[]>([]);
  let typeFilter = $state<string[]>([]);
  let repoFilter = $state<string | undefined>(undefined);
  let searchQuery = $state<string | undefined>(undefined);
  let sort = $state<NotificationSort>("priority");
  let selectedIDs = $state<Set<number>>(new Set());
  let requestVersion = 0;

  function getNotifications(): NotificationItem[] { return items; }
  function getSummary(): NotificationSummary { return summary; }
  function getSyncStatus(): NotificationSyncStatus { return syncStatus; }
  function isSyncRunning(): boolean { return syncStatus.running; }
  function isLoading(): boolean { return loading; }
  function isActionInFlight(): boolean { return actionInFlight; }
  function getError(): string | null { return storeError; }
  function getStateFilter(): NotificationState { return stateFilter; }
  function getReasonFilter(): string[] { return reasonFilter; }
  function getTypeFilter(): string[] { return typeFilter; }
  function getRepoFilter(): string | undefined { return repoFilter; }
  function getSearchQuery(): string | undefined { return searchQuery; }
  function getSort(): NotificationSort { return sort; }
  function getSelectedIDs(): Set<number> { return selectedIDs; }
  function getSelectedCount(): number { return selectedIDs.size; }

  function setStateFilter(next: NotificationState): void {
    stateFilter = next;
    clearSelection();
  }
  function setReasonFilter(next: string[]): void {
    reasonFilter = next;
    clearSelection();
  }
  function setTypeFilter(next: string[]): void {
    typeFilter = next;
    clearSelection();
  }
  function setRepoFilter(next: string | undefined): void {
    repoFilter = next && next.trim() ? next.trim() : undefined;
    clearSelection();
  }
  function setSearchQuery(next: string | undefined): void {
    searchQuery = next && next.trim() ? next.trim() : undefined;
    clearSelection();
  }
  function setSort(next: NotificationSort): void {
    sort = next;
    clearSelection();
  }
  function toggleSelected(id: number): void {
    const next = new Set(selectedIDs);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selectedIDs = next;
  }
  function selectVisible(): void {
    selectedIDs = new Set(items.map((item) => item.id));
  }
  function clearSelection(): void { selectedIDs = new Set(); }

  function buildParams(): NotificationParams {
    const params: NotificationParams = {
      state: stateFilter,
      limit: 100,
      sort,
    };
    const repo = repoFilter ?? getGlobalRepo();
    if (repo) params.repo = repo;
    if (reasonFilter.length > 0) params.reason = reasonFilter;
    if (typeFilter.length > 0) params.type = typeFilter;
    if (searchQuery) params.q = searchQuery;
    return params;
  }

  async function loadNotifications(): Promise<void> {
    const version = ++requestVersion;
    loading = true;
    storeError = null;
    try {
      const { data, error: requestError } = await apiClient.GET(
        "/notifications",
        { params: { query: buildParams() } },
      );
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to load inbox"));
      }
      if (version !== requestVersion) return;
      items = data?.items ?? [];
      summary = data?.summary ?? emptySummary();
      if (data?.sync) {
        syncStatus = data.sync;
        if (syncStatus.last_error) {
          storeError = syncStatus.last_error;
        }
      }
      selectedIDs = new Set(
        [...selectedIDs].filter((id) => items.some((item) => item.id === id)),
      );
    } catch (err) {
      if (version !== requestVersion) return;
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      if (version === requestVersion) loading = false;
    }
  }

  async function mutateSelected(
    path: "/notifications/done" | "/notifications/read" | "/notifications/undone",
  ): Promise<void> {
    const ids = [...selectedIDs];
    if (ids.length === 0 || actionInFlight) return;
    actionInFlight = true;
    storeError = null;
    try {
      const { error: requestError } = await apiClient.POST(path, {
        body: { ids },
      });
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to update inbox"));
      }
      clearSelection();
      await loadNotifications();
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
    } finally {
      actionInFlight = false;
    }
  }

  function markSelectedDone(): Promise<void> {
    return mutateSelected("/notifications/done");
  }
  function markSelectedRead(): Promise<void> {
    return mutateSelected("/notifications/read");
  }
  function markSelectedUndone(): Promise<void> {
    return mutateSelected("/notifications/undone");
  }

  async function triggerSync(): Promise<void> {
    storeError = null;
    const previousFinishedAt = syncStatus.last_finished_at ?? "";
    syncStatus = { ...syncStatus, running: true, last_error: "" };
    try {
      const { error: requestError } = await apiClient.POST(
        "/notifications/sync",
        { headers: { "Content-Type": "application/json" } },
      );
      if (requestError) {
        throw new Error(apiErrorMessage(requestError, "failed to sync inbox"));
      }
      await loadNotifications();
      void pollNotificationsAfterSync(previousFinishedAt);
    } catch (err) {
      storeError = err instanceof Error ? err.message : String(err);
      syncStatus = { ...syncStatus, running: false, last_error: storeError };
    }
  }

  async function pollNotificationsAfterSync(previousFinishedAt: string): Promise<void> {
    for (const waitMs of [500, 1_500, 3_000, 5_000]) {
      await delay(waitMs);
      await loadNotifications();
      if (syncStatus.last_error) return;
      const finishedAt = syncStatus.last_finished_at ?? "";
      if (finishedAt && finishedAt !== previousFinishedAt) return;
    }
  }

  return {
    getNotifications,
    getSummary,
    getSyncStatus,
    isSyncRunning,
    isLoading,
    isActionInFlight,
    getError,
    getStateFilter,
    getReasonFilter,
    getTypeFilter,
    getRepoFilter,
    getSearchQuery,
    getSort,
    getSelectedIDs,
    getSelectedCount,
    setStateFilter,
    setReasonFilter,
    setTypeFilter,
    setRepoFilter,
    setSearchQuery,
    setSort,
    toggleSelected,
    selectVisible,
    clearSelection,
    loadNotifications,
    markSelectedDone,
    markSelectedRead,
    markSelectedUndone,
    triggerSync,
  };
}

export type NotificationsStore = ReturnType<typeof createNotificationsStore>;
