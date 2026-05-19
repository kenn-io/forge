import type { SyncStatus } from "../api/types.js";

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
  /**
   * Called on a `reconnect.stale` SSE frame. The server emits this
   * when the client's Last-Event-ID cursor predates its replay ring,
   * meaning the gap between disconnect and reconnect was too large to
   * bridge from the in-memory buffer. The handler should refetch view
   * state from scratch (pulls, issues, sync status) the same way it
   * would after a hard refresh.
   */
  onReconnectStale?: () => void;
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
    source.addEventListener("reconnect.stale", () => {
      opts.onReconnectStale?.();
    });
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

export type EventsStore = ReturnType<typeof createEventsStore>;
