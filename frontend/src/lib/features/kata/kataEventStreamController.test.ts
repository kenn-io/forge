import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { KataEventStreamError, type ReadKataEventStreamOptions } from "../../api/kata/eventStream.js";
import type { KataTaskEventStreamMessage } from "../../api/kata/taskTypes.js";
import { createKataEventStreamController } from "./kataEventStreamController.js";

const resetMessage: KataTaskEventStreamMessage = {
  kind: "reset",
  event_id: 12,
  reset_after_id: 12,
  lastEventID: 12,
};

function requireStreamOptions(options: ReadKataEventStreamOptions | null): ReadKataEventStreamOptions {
  expect(options).not.toBeNull();
  return options as ReadKataEventStreamOptions;
}

describe("kata event stream controller", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("passes stream options and reports resets after applying messages", async () => {
    let streamOptions: ReadKataEventStreamOptions | null = null;
    const onOpen = vi.fn();
    const onMessage = vi.fn(async () => undefined);
    const onReset = vi.fn();
    const readEventStream = vi.fn(async (options: ReadKataEventStreamOptions) => {
      streamOptions = options;
    });

    const controller = createKataEventStreamController({
      getDaemonId: () => "daemon-a",
      getLastEventID: () => 41,
      onOpen,
      onMessage,
      onReset,
      onError: vi.fn(),
      readEventStream,
    });

    controller.start();

    expect(readEventStream).toHaveBeenCalledOnce();
    const options = requireStreamOptions(streamOptions);
    expect(options.daemonId).toBe("daemon-a");
    expect(options.lastEventID).toBe(41);

    options.onOpen?.();
    await options.onMessage(resetMessage);

    expect(onOpen).toHaveBeenCalledOnce();
    expect(onMessage).toHaveBeenCalledWith(resetMessage);
    expect(onReset).toHaveBeenCalledOnce();
  });

  it("ignores reset side effects after the stream is stopped mid-message", async () => {
    let streamOptions: ReadKataEventStreamOptions | null = null;
    let releaseMessage!: () => void;
    const messageApplied = new Promise<void>((resolve) => {
      releaseMessage = resolve;
    });
    const onReset = vi.fn();
    const readEventStream = vi.fn(async (options: ReadKataEventStreamOptions) => {
      streamOptions = options;
    });

    const controller = createKataEventStreamController({
      getDaemonId: () => undefined,
      getLastEventID: () => 0,
      onOpen: vi.fn(),
      onMessage: vi.fn(async () => messageApplied),
      onReset,
      onError: vi.fn(),
      readEventStream,
    });

    controller.start();
    const pendingMessage = requireStreamOptions(streamOptions).onMessage(resetMessage);
    controller.stop();
    releaseMessage();
    await pendingMessage;

    expect(onReset).not.toHaveBeenCalled();
  });

  it("reconnects retryable stream failures with backoff", async () => {
    vi.useFakeTimers();
    const readEventStream = vi.fn(async () => {
      throw new KataEventStreamError("temporary stream failure", { retryable: true });
    });
    const onError = vi.fn();

    const controller = createKataEventStreamController({
      getDaemonId: () => undefined,
      getLastEventID: () => 0,
      onOpen: vi.fn(),
      onMessage: vi.fn(),
      onError,
      readEventStream,
      reconnectDelayMS: 100,
      reconnectMaxDelayMS: 500,
    });

    controller.start();
    await vi.waitFor(() => {
      expect(onError).toHaveBeenCalledWith("temporary stream failure");
    });

    expect(readEventStream).toHaveBeenCalledOnce();

    await vi.advanceTimersByTimeAsync(100);

    expect(readEventStream).toHaveBeenCalledTimes(2);
  });
});
