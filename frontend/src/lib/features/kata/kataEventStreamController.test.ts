import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import {
  KataEventStreamError,
  type KataTaskEventStreamFrame,
  type ReadKataEventStreamOptions,
} from "../../api/kata/eventStream.js";
import { createKataEventStreamController } from "./kataEventStreamController.js";

const resetFrame: KataTaskEventStreamFrame = {
  kind: "reset",
  server_instance_id: "server-a",
  daemon_id: "work",
  epoch: 4,
  cursor: 52,
};

function requireStreamOptions(options: ReadKataEventStreamOptions | null): ReadKataEventStreamOptions {
  expect(options).not.toBeNull();
  return options as ReadKataEventStreamOptions;
}

describe("kata event stream controller", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("consumes each compact frame before reporting a reset", async () => {
    let streamOptions: ReadKataEventStreamOptions | null = null;
    let releaseConsumer!: () => void;
    const consumerPending = new Promise<void>((resolve) => {
      releaseConsumer = resolve;
    });
    const calls: string[] = [];
    const readEventStream = vi.fn(async (options: ReadKataEventStreamOptions) => {
      streamOptions = options;
    });
    const controller = createKataEventStreamController({
      getDaemonId: () => "work",
      getLastEventID: () => 51,
      onOpen: vi.fn(),
      onMessage: vi.fn(async () => {
        calls.push("consume:start");
        await consumerPending;
        calls.push("consume:end");
      }),
      onReset: () => calls.push("reset"),
      onError: vi.fn(),
      readEventStream,
    });

    controller.start();
    const options = requireStreamOptions(streamOptions);
    expect(options.daemonId).toBe("work");
    expect(options.lastEventID).toBe(51);

    const delivery = options.onMessage(resetFrame);
    await vi.waitFor(() => expect(calls).toEqual(["consume:start"]));
    releaseConsumer();
    await delivery;

    expect(calls).toEqual(["consume:start", "consume:end", "reset"]);
  });

  it("suppresses reset notification when stopped mid-message", async () => {
    let streamOptions: ReadKataEventStreamOptions | null = null;
    let releaseConsumer!: () => void;
    const consumerPending = new Promise<void>((resolve) => {
      releaseConsumer = resolve;
    });
    const onReset = vi.fn();
    const controller = createKataEventStreamController({
      getDaemonId: () => undefined,
      getLastEventID: () => 0,
      onOpen: vi.fn(),
      onMessage: vi.fn(async () => consumerPending),
      onReset,
      onError: vi.fn(),
      readEventStream: vi.fn(async (options: ReadKataEventStreamOptions) => {
        streamOptions = options;
      }),
    });

    controller.start();
    const delivery = requireStreamOptions(streamOptions).onMessage(resetFrame);
    controller.stop();
    releaseConsumer();
    await delivery;

    expect(onReset).not.toHaveBeenCalled();
  });

  it("fences callbacks from superseded stream generations", async () => {
    const streams: ReadKataEventStreamOptions[] = [];
    const onOpen = vi.fn();
    const onMessage = vi.fn(async () => undefined);
    const controller = createKataEventStreamController({
      getDaemonId: () => "work",
      getLastEventID: () => 0,
      onOpen,
      onMessage,
      onError: vi.fn(),
      readEventStream: vi.fn(async (options: ReadKataEventStreamOptions) => {
        streams.push(options);
      }),
    });

    controller.start();
    controller.start();
    streams[0]?.onOpen?.();
    await streams[0]?.onMessage(resetFrame);
    streams[1]?.onOpen?.();
    await streams[1]?.onMessage({ ...resetFrame, kind: "invalidation" });

    expect(onOpen).toHaveBeenCalledOnce();
    expect(onMessage).toHaveBeenCalledOnce();
    expect(onMessage).toHaveBeenCalledWith({ ...resetFrame, kind: "invalidation" });
  });

  it("reconnects retryable failures with a fresh accepted snapshot cursor", async () => {
    vi.useFakeTimers();
    let cursor = 51;
    const seenCursors: number[] = [];
    const readEventStream = vi.fn(async (options: ReadKataEventStreamOptions) => {
      seenCursors.push(options.lastEventID ?? 0);
      throw new KataEventStreamError("temporary stream failure", { retryable: true });
    });
    const onError = vi.fn();
    const controller = createKataEventStreamController({
      getDaemonId: () => "work",
      getLastEventID: () => cursor,
      onOpen: vi.fn(),
      onMessage: vi.fn(),
      onError,
      readEventStream,
      reconnectDelayMS: 100,
      reconnectMaxDelayMS: 500,
    });

    controller.start();
    await vi.waitFor(() => expect(onError).toHaveBeenCalledWith("temporary stream failure"));
    cursor = 52;
    await vi.advanceTimersByTimeAsync(100);

    expect(seenCursors).toEqual([51, 52]);
  });

  it("does not reconnect nonretryable setup failures", async () => {
    vi.useFakeTimers();
    const readEventStream = vi.fn(async () => {
      throw new KataEventStreamError("invalid stream request", { retryable: false });
    });
    const controller = createKataEventStreamController({
      getDaemonId: () => undefined,
      getLastEventID: () => 0,
      onOpen: vi.fn(),
      onMessage: vi.fn(),
      onError: vi.fn(),
      readEventStream,
      reconnectDelayMS: 100,
    });

    controller.start();
    await vi.runAllTimersAsync();

    expect(readEventStream).toHaveBeenCalledOnce();
  });

  it("stop cancels a scheduled reconnect", async () => {
    vi.useFakeTimers();
    const readEventStream = vi.fn(async () => {
      throw new KataEventStreamError("temporary stream failure", { retryable: true });
    });
    const controller = createKataEventStreamController({
      getDaemonId: () => undefined,
      getLastEventID: () => 0,
      onOpen: vi.fn(),
      onMessage: vi.fn(),
      onError: vi.fn(),
      readEventStream,
      reconnectDelayMS: 100,
    });

    controller.start();
    await vi.waitFor(() => expect(readEventStream).toHaveBeenCalledOnce());
    controller.stop();
    await vi.advanceTimersByTimeAsync(100);

    expect(readEventStream).toHaveBeenCalledOnce();
  });
});
