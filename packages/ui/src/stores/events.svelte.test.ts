import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createEventsStore } from "./events.svelte.js";

type StoredListener = (event: Event) => void;

class FakeEventSource {
  static instances: FakeEventSource[] = [];

  readonly url: string;
  private listeners = new Map<string, StoredListener[]>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(
    type: string,
    listener: EventListenerOrEventListenerObject,
  ): void {
    const stored =
      typeof listener === "function"
        ? listener
        : (event: Event) => listener.handleEvent(event);
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), stored]);
  }

  close(): void {}

  listenerCount(type: string): number {
    return this.listeners.get(type)?.length ?? 0;
  }

  emit(type: string, data: string): void {
    const event = { data } as MessageEvent;
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event);
    }
  }
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal(
    "EventSource",
    FakeEventSource as unknown as typeof EventSource,
  );
  vi.stubGlobal("$state", <T>(value: T) => value);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("events store JSON listeners", () => {
  it("swallows malformed JSON without swallowing callback exceptions", () => {
    const callbackError = new Error("callback failed");
    let shouldThrow = false;
    const onPRCIRefreshed = vi.fn(() => {
      if (shouldThrow) throw callbackError;
    });
    const store = createEventsStore({ onPRCIRefreshed });
    store.connect();

    const source = FakeEventSource.instances[0];
    expect(source).toBeDefined();
    expect(source?.listenerCount("pr_ci_refreshed")).toBe(1);
    source?.emit("pr_ci_refreshed", "{");
    expect(onPRCIRefreshed).not.toHaveBeenCalled();

    source?.emit("pr_ci_refreshed", JSON.stringify({ number: 7 }));
    expect(onPRCIRefreshed).toHaveBeenCalledWith({ number: 7 });
    onPRCIRefreshed.mockClear();

    shouldThrow = true;

    let thrown: unknown;
    try {
      source?.emit("pr_ci_refreshed", JSON.stringify({ number: 8 }));
    } catch (error) {
      thrown = error;
    }
    expect(onPRCIRefreshed).toHaveBeenCalledWith({ number: 8 });
    expect(thrown).toBe(callbackError);
  });
});
