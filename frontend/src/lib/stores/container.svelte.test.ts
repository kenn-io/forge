import { Effect } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../app/runtime.js";
import { getContainerSize, initContainerObserver } from "./container.svelte.js";

type ResizeCallback = (entries: ResizeObserverEntry[]) => void;

function resizeEntry(target: Element, width: number): ResizeObserverEntry {
  return {
    target,
    contentRect: DOMRectReadOnly.fromRect({ width }),
    borderBoxSize: [],
    contentBoxSize: [],
    devicePixelContentBoxSize: [],
  };
}

describe("container observer", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  afterEach(() => {
    vi.useRealTimers();
    globalThis.ResizeObserver = originalResizeObserver;
  });

  it("debounces resize publication and drops pending work when its owner disconnects", async () => {
    vi.useFakeTimers();
    let callback: ResizeCallback = () => {};
    let disconnectCount = 0;
    globalThis.ResizeObserver = class ResizeObserverStub {
      constructor(next: ResizeObserverCallback) {
        callback = next;
      }

      disconnect(): void {
        disconnectCount += 1;
      }

      observe(): void {}
      unobserve(): void {}
    };
    const runtime = makeAppRuntime();
    const element = document.createElement("div");
    Object.defineProperty(element, "clientWidth", { configurable: true, value: 1200 });
    const cleanup = initContainerObserver(runtime, element);

    callback([resizeEntry(element, 420)]);
    callback([resizeEntry(element, 780)]);
    await vi.advanceTimersByTimeAsync(100);

    expect(getContainerSize()).toBe("medium");
    callback([resizeEntry(element, 420)]);
    cleanup();
    await vi.advanceTimersByTimeAsync(100);
    expect(getContainerSize()).toBe("medium");

    callback([resizeEntry(element, 420)]);
    await vi.advanceTimersByTimeAsync(100);
    expect(getContainerSize()).toBe("medium");
    expect(disconnectCount).toBe(1);

    await Effect.runPromise(runtime.disposeEffect);
  });
});
