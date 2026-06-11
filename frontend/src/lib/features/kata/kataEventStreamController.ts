import { KataEventStreamError, readKataEventStream } from "../../api/kata/eventStream.js";
import type { KataTaskEventStreamMessage } from "../../api/kata/taskTypes.js";

type ReadKataEventStream = typeof readKataEventStream;

interface KataEventStreamControllerOptions {
  getDaemonId: () => string | undefined;
  getLastEventID: () => number;
  onOpen: () => void;
  onMessage: (message: KataTaskEventStreamMessage) => Promise<void>;
  onReset?: (() => void) | undefined;
  onError: (message: string) => void;
  readEventStream?: ReadKataEventStream | undefined;
  reconnectDelayMS?: number | undefined;
  reconnectMaxDelayMS?: number | undefined;
}

export interface KataEventStreamController {
  start: (reconnecting?: boolean) => void;
  stop: (resetReconnect?: boolean) => void;
}

export function createKataEventStreamController(options: KataEventStreamControllerOptions): KataEventStreamController {
  const readEventStream = options.readEventStream ?? readKataEventStream;
  const reconnectDelayMS = options.reconnectDelayMS ?? 100;
  const reconnectMaxDelayMS = options.reconnectMaxDelayMS ?? 5_000;
  let controller: AbortController | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;
  let reconnectAttempt = 0;

  function stop(resetReconnect = true): void {
    generation += 1;
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    controller?.abort();
    controller = null;
    if (resetReconnect) {
      reconnectAttempt = 0;
    }
  }

  function start(reconnecting = false): void {
    stop(!reconnecting);
    const activeController = new AbortController();
    const activeGeneration = generation;
    controller = activeController;

    void readEventStream({
      daemonId: options.getDaemonId(),
      lastEventID: options.getLastEventID(),
      signal: activeController.signal,
      onOpen: () => {
        if (activeGeneration !== generation) return;
        reconnectAttempt = 0;
        options.onOpen();
      },
      onMessage: async (message) => {
        if (activeGeneration !== generation || activeController.signal.aborted) return;
        await options.onMessage(message);
        if (activeGeneration !== generation || activeController.signal.aborted) return;
        if (message.kind === "reset") {
          options.onReset?.();
        }
      },
    }).catch((err: unknown) => {
      if (activeController.signal.aborted) return;
      if (activeGeneration !== generation) return;
      options.onError(err instanceof Error ? err.message : "Live updates disconnected");
      if (err instanceof KataEventStreamError && !err.retryable) return;
      const delay = Math.min(reconnectDelayMS * 2 ** reconnectAttempt, reconnectMaxDelayMS);
      reconnectAttempt += 1;
      reconnectTimer = setTimeout(() => {
        if (activeGeneration !== generation) return;
        reconnectTimer = null;
        start(true);
      }, delay);
    });
  }

  return { start, stop };
}
