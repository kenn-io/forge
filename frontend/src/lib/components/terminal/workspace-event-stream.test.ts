import { Effect, Stream } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";

import type { WorkspaceEventsNotification } from "../../stores/events.svelte.js";
import { workspaceEventStream } from "./workspace-event-stream.js";

describe("workspaceEventStream", () => {
  it("maps workspace events and unsubscribes when the stream ends", async () => {
    const unsubscribe = vi.fn();

    const signals = await Effect.runPromise(
      workspaceEventStream((next) => {
        queueMicrotask(() =>
          next({
            type: "workspace_diff_ready",
            payload: { workspace_id: "ws-1", version: "generation:ready" },
          }),
        );
        return unsubscribe;
      }).pipe(Stream.take(1), Stream.runCollect, Effect.timeout("1 second")),
    );

    expect(signals).toEqual([{ _tag: "DiffReady", workspaceId: "ws-1", version: "generation:ready" }]);
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("delivers a burst without terminating the workspace stream", async () => {
    const signals = await Effect.runPromise(
      workspaceEventStream((next: (event: WorkspaceEventsNotification) => void) => {
        queueMicrotask(() => {
          for (let index = 0; index < 100; index += 1) {
            next({
              type: "workspace_diff_changed",
              payload: { workspace_id: "ws-1", version: `generation:${index}` },
            });
          }
        });
        return () => {};
      }).pipe(Stream.take(100), Stream.runCollect, Effect.timeout("1 second")),
    );

    expect(signals).toHaveLength(100);
    expect(signals.at(-1)).toEqual({
      _tag: "DiffChanged",
      workspaceId: "ws-1",
      version: "generation:99",
    });
  });
});
