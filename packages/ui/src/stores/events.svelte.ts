import type { SyncStatus } from "../api/types.js";

export interface ConfigChangedEvent {
  valid: boolean;
  error?: string;
  restart_required: boolean;
}

export interface WorkspacePushedHeadChangedEvent {
  workspace_id: string;
  provider: string;
  platform_host: string;
  repo_path: string;
  owner: string;
  name: string;
  number: number;
  old_sha: string;
  new_sha: string;
  remote: string;
  branch: string;
  tracking_ref: string;
  observed_at: string;
}

export interface WorkspacePRAssociatedEvent {
  workspace_id: string;
  provider: string;
  platform_host: string;
  repo_path: string;
  owner: string;
  name: string;
  issue_number: number;
  pr_number: number;
  associated_at: string;
}

export interface WorkspacePRRefreshQueuedEvent {
  workspace_id: string;
  provider: string;
  platform_host: string;
  repo_path: string;
  owner: string;
  name: string;
  number: number;
  head_sha: string;
  priority: string;
  queued_at: string;
}

export interface PRDetailRefreshedEvent {
  provider: string;
  platform_host: string;
  repo_path: string;
  owner: string;
  name: string;
  number: number;
  head_sha: string;
  synced_at: string;
  warnings: string[];
}

export interface PRCIRefreshQueuedEvent {
  provider: string;
  platform_host: string;
  repo_path: string;
  owner: string;
  name: string;
  number: number;
  head_sha: string;
  priority: string;
  queued_at: string;
}

export interface PRCIRefreshedEvent {
  provider: string;
  platform_host: string;
  repo_path: string;
  owner: string;
  name: string;
  number: number;
  head_sha: string;
  refreshed_at: string;
  warnings: string[];
}

export interface EventsStoreOptions {
  /**
   * Base URL path (typically from config.basePath). Trailing
   * slash tolerated. Used to build the EventSource URL.
   */
  getBasePath?: () => string;
  /** Called on each `data_changed` SSE frame. */
  onDataChanged?: () => void;
  /** Called on each `sync_status` SSE frame. */
  onSyncStatus?: (status: SyncStatus) => void;
  /** Called on each `config.changed` SSE frame. */
  onConfigChanged?: (event: ConfigChangedEvent) => void;
  /**
   * Called on a `reconnect.stale` SSE frame. The server emits this
   * when the client's Last-Event-ID cursor predates its replay ring,
   * meaning the gap between disconnect and reconnect was too large to
   * bridge from the in-memory buffer. The handler should refetch view
   * state from scratch (pulls, issues, sync status) the same way it
   * would after a hard refresh.
   */
  onReconnectStale?: () => void;
  onWorkspacePushedHeadChanged?: (
    event: WorkspacePushedHeadChangedEvent,
  ) => void;
  onWorkspacePRAssociated?: (
    event: WorkspacePRAssociatedEvent,
  ) => void;
  onWorkspacePRRefreshQueued?: (
    event: WorkspacePRRefreshQueuedEvent,
  ) => void;
  onPRDetailRefreshed?: (event: PRDetailRefreshedEvent) => void;
  onPRCIRefreshQueued?: (event: PRCIRefreshQueuedEvent) => void;
  onPRCIRefreshed?: (event: PRCIRefreshedEvent) => void;
}

/**
 * createEventsStore wraps a single EventSource that streams from
 * /api/v1/events. It exposes connect/disconnect and forwards
 * data_changed / sync_status frames to the callbacks supplied at
 * construction time.
 */
export function createEventsStore(opts: EventsStoreOptions = {}) {
  const getBasePath = opts.getBasePath ?? (() => "/");
  let source: EventSource | null = null;
  let connected = $state(false);

  function buildURL(): string {
    const base = getBasePath().replace(/\/$/, "");
    return `${base}/api/v1/events`;
  }

  function connect(): void {
    if (source !== null) return;
    try {
      source = new EventSource(buildURL());
    } catch {
      return;
    }
    source.addEventListener("open", () => {
      connected = true;
    });
    source.addEventListener("error", () => {
      connected = false;
    });
    source.addEventListener("data_changed", () => {
      opts.onDataChanged?.();
    });
    source.addEventListener("sync_status", (ev) => {
      try {
        const status = JSON.parse(
          (ev as MessageEvent).data,
        ) as SyncStatus;
        opts.onSyncStatus?.(status);
      } catch {
        // ignore malformed frames
      }
    });
    source.addEventListener("config.changed", (ev) => {
      try {
        const event = JSON.parse(
          (ev as MessageEvent).data,
        ) as ConfigChangedEvent;
        opts.onConfigChanged?.(event);
      } catch {
        // ignore malformed frames
      }
    });
    source.addEventListener("reconnect.stale", () => {
      opts.onReconnectStale?.();
    });
    addJSONListener<WorkspacePushedHeadChangedEvent>(
      source,
      "workspace_pushed_head_changed",
      opts.onWorkspacePushedHeadChanged,
    );
    addJSONListener<WorkspacePRAssociatedEvent>(
      source,
      "workspace_pr_associated",
      opts.onWorkspacePRAssociated,
    );
    addJSONListener<WorkspacePRRefreshQueuedEvent>(
      source,
      "workspace_pr_refresh_queued",
      opts.onWorkspacePRRefreshQueued,
    );
    addJSONListener<PRDetailRefreshedEvent>(
      source,
      "pr_detail_refreshed",
      opts.onPRDetailRefreshed,
    );
    addJSONListener<PRCIRefreshQueuedEvent>(
      source,
      "pr_ci_refresh_queued",
      opts.onPRCIRefreshQueued,
    );
    addJSONListener<PRCIRefreshedEvent>(
      source,
      "pr_ci_refreshed",
      opts.onPRCIRefreshed,
    );
  }

  function disconnect(): void {
    if (source === null) return;
    source.close();
    source = null;
    connected = false;
  }

  function isConnected(): boolean {
    return connected;
  }

  return { connect, disconnect, isConnected };
}

function addJSONListener<T>(
  source: EventSource,
  eventName: string,
  callback: ((event: T) => void) | undefined,
): void {
  source.addEventListener(eventName, (ev) => {
    if (!callback) return;
    try {
      callback(JSON.parse((ev as MessageEvent).data) as T);
    } catch {
      // ignore malformed frames
    }
  });
}

export type EventsStore = ReturnType<typeof createEventsStore>;
