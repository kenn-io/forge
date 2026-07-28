import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { createTerminalClipboardWriter, type TerminalClipboardPort } from "./terminalClipboardWriter";

function deferred<T>(): {
  promise: Promise<T>;
  resolve(value: T): void;
  reject(reason: unknown): void;
} {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function createPort(): {
  port: TerminalClipboardPort;
  deferredWrites: Array<Promise<string>>;
  writeLocalText: ReturnType<typeof vi.fn>;
  writeText: ReturnType<typeof vi.fn>;
} {
  const deferredWrites: Array<Promise<string>> = [];
  const writeLocalText = vi.fn(async () => undefined);
  const writeText = vi.fn(async () => undefined);
  return {
    deferredWrites,
    writeLocalText,
    writeText,
    port: {
      beginDeferredWrite(text) {
        deferredWrites.push(text);
        return text.then(() => undefined);
      },
      writeLocalText,
      writeText,
    },
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("terminal clipboard writer", () => {
  it("keeps one pointer authorization alive through a long drag", async () => {
    vi.useFakeTimers();
    const { port, deferredWrites, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.beginPointerGesture();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(deferredWrites).toHaveLength(1);
    writer.endPointerGesture();
    const copied = writer.write("pointer selection");

    await expect(copied).resolves.toBe("written");
    await expect(deferredWrites[0]).resolves.toBe("pointer selection");
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();

    await expect(writer.write("second write")).resolves.toBe("unauthorized");
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("does not reauthorize a consumed pointer gesture on release", async () => {
    const { port, deferredWrites, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.beginPointerGesture();
    await expect(writer.write("first write")).resolves.toBe("written");
    writer.endPointerGesture();

    await expect(writer.write("second write")).resolves.toBe("unauthorized");
    await expect(deferredWrites[0]).resolves.toBe("first write");
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("revokes an active pointer authorization when the gesture is canceled", async () => {
    const { port, deferredWrites, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.beginPointerGesture();
    writer.cancelPointerGesture();

    await expect(writer.write("late write")).resolves.toBe("unauthorized");
    await expect(deferredWrites[0]).rejects.toMatchObject({ name: "AbortError" });
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("revokes a released pointer authorization when the gesture is canceled", async () => {
    const { port, deferredWrites, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.beginPointerGesture();
    writer.endPointerGesture();
    writer.cancelPointerGesture();

    await expect(writer.write("late write")).resolves.toBe("unauthorized");
    await expect(deferredWrites[0]).rejects.toMatchObject({ name: "AbortError" });
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("revokes keyboard authorization created inside a canceled pointer gesture", async () => {
    const { port, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.beginPointerGesture();
    await expect(writer.write("pointer write")).resolves.toBe("written");
    writer.authorizeKeyboardGesture();
    writer.cancelPointerGesture();

    await expect(writer.write("late write")).resolves.toBe("unauthorized");
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("revokes a pointer authorization when its watchdog expires", async () => {
    vi.useFakeTimers();
    const { port, deferredWrites, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.beginPointerGesture();
    await vi.advanceTimersByTimeAsync(60_001);

    await expect(writer.write("late write")).resolves.toBe("unauthorized");
    await expect(deferredWrites[0]).rejects.toMatchObject({ name: "AbortError" });
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("clears a rejected authorization so a later gesture can retry", async () => {
    const firstWrite = deferred<void>();
    const deferredWrites: Array<Promise<string>> = [];
    const writer = createTerminalClipboardWriter({
      beginDeferredWrite(text) {
        deferredWrites.push(text);
        return deferredWrites.length === 1 ? firstWrite.promise : text.then(() => undefined);
      },
      writeLocalText: vi.fn(async () => undefined),
      writeText: vi.fn(async () => undefined),
    });

    writer.authorizeKeyboardGesture();
    firstWrite.reject(new DOMException("denied", "NotAllowedError"));
    await firstWrite.promise.catch(() => undefined);
    writer.authorizeKeyboardGesture();

    await expect(writer.write("retried selection")).resolves.toBe("written");
    await expect(deferredWrites[1]).resolves.toBe("retried selection");
  });

  it("keeps keyboard authorization alive while trusted key gestures continue", async () => {
    vi.useFakeTimers();
    const { port, deferredWrites, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.authorizeKeyboardGesture();
    await vi.advanceTimersByTimeAsync(4_000);
    writer.authorizeKeyboardGesture();
    await vi.advanceTimersByTimeAsync(4_000);

    await expect(writer.write("keyboard selection")).resolves.toBe("written");
    await expect(deferredWrites[0]).resolves.toBe("keyboard selection");
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("does not shorten keyboard authorization for an unrelated pointer release", async () => {
    vi.useFakeTimers();
    const { port, deferredWrites, writeLocalText, writeText } = createPort();
    const writer = createTerminalClipboardWriter(port);

    writer.authorizeKeyboardGesture();
    await vi.advanceTimersByTimeAsync(2_000);
    writer.endPointerGesture();
    await vi.advanceTimersByTimeAsync(2_000);

    await expect(writer.write("keyboard selection")).resolves.toBe("written");
    await expect(deferredWrites[0]).resolves.toBe("keyboard selection");
    expect(writeText).not.toHaveBeenCalled();
    expect(writeLocalText).not.toHaveBeenCalled();
  });

  it("expires an idle keyboard authorization before a later OSC 52 write", async () => {
    vi.useFakeTimers();
    const pending = deferred<void>();
    const writeText = vi.fn(async () => undefined);
    const port: TerminalClipboardPort = {
      beginDeferredWrite(text) {
        void text.catch(() => undefined);
        return pending.promise;
      },
      writeLocalText: vi.fn(async () => undefined),
      writeText,
    };
    const writer = createTerminalClipboardWriter(port);

    writer.authorizeKeyboardGesture();
    await vi.advanceTimersByTimeAsync(10_001);

    await expect(writer.write("late write")).resolves.toBe("unauthorized");
    expect(writeText).not.toHaveBeenCalled();
    pending.resolve();
  });

  it("falls back to writeText when deferred clipboard setup is unavailable", async () => {
    const writeText = vi.fn(async () => undefined);
    const writer = createTerminalClipboardWriter({
      beginDeferredWrite() {
        throw new DOMException("unsupported", "NotSupportedError");
      },
      writeLocalText: vi.fn(async () => undefined),
      writeText,
    });

    writer.beginPointerGesture();

    await expect(writer.write("fallback")).resolves.toBe("written");
    expect(writeText).toHaveBeenCalledWith("fallback");
  });

  it("falls back to the local Middleman clipboard when browser writes fail", async () => {
    const writeLocalText = vi.fn(async () => undefined);
    const writer = createTerminalClipboardWriter({
      beginDeferredWrite() {
        throw new DOMException("unsupported", "NotSupportedError");
      },
      writeLocalText,
      writeText: vi.fn(async () => {
        throw new DOMException("denied", "NotAllowedError");
      }),
    });

    writer.authorizeKeyboardGesture();

    await expect(writer.write("firefox selection")).resolves.toBe("written");
    expect(writeLocalText).toHaveBeenCalledWith("firefox selection");
  });

  it("reports failure only after browser and local writes fail", async () => {
    const deferredWrite = deferred<void>();
    const writer = createTerminalClipboardWriter({
      beginDeferredWrite(text) {
        void text.catch(() => undefined);
        return deferredWrite.promise;
      },
      writeLocalText: vi.fn(async () => {
        throw new Error("local clipboard unavailable");
      }),
      writeText: vi.fn(async () => {
        throw new DOMException("denied", "NotAllowedError");
      }),
    });

    writer.beginPointerGesture();
    const copied = writer.write("blocked");
    deferredWrite.reject(new DOMException("denied", "NotAllowedError"));

    await expect(copied).resolves.toBe("blocked");
  });
});
