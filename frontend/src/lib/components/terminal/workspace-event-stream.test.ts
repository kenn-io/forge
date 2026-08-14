import { Effect, Stream } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";

import type { WorkspaceEventsNotification } from "../../stores/events.svelte.js";
import { workspaceEventStream } from "./workspace-event-stream.js";

describe("workspaceEventStream", () => {
  it("subscribes before selecting the workspace and releases both together", async () => {
    let subscriber: ((event: WorkspaceEventsNotification) => void) | undefined;
    const unsubscribe = vi.fn();
    const releaseSelection = vi.fn();

    const signals = await Effect.runPromise(
      workspaceEventStream(
        (next) => {
          subscriber = next;
          return unsubscribe;
        },
        () => {
          subscriber?.({
            type: "workspace_diff_ready",
            payload: { workspace_id: "ws-1", version: "generation:ready" },
          });
          return releaseSelection;
        },
      ).pipe(Stream.take(1), Stream.runCollect, Effect.timeout("1 second")),
    );

    expect(signals).toEqual([{ _tag: "DiffReady", workspaceId: "ws-1", version: "generation:ready" }]);
    expect(unsubscribe).toHaveBeenCalledOnce();
    expect(releaseSelection).toHaveBeenCalledOnce();
  });

  it("delivers a burst without terminating the workspace stream", async () => {
    let subscriber: ((event: WorkspaceEventsNotification) => void) | undefined;

    const signals = await Effect.runPromise(
      workspaceEventStream(
        (next) => {
          subscriber = next;
          return () => {};
        },
        () => {
          for (let index = 0; index < 100; index += 1) {
            subscriber?.({
              type: "workspace_diff_changed",
              payload: { workspace_id: "ws-1", version: `generation:${index}` },
            });
          }
          return () => {};
        },
      ).pipe(Stream.take(100), Stream.runCollect, Effect.timeout("1 second")),
    );

    expect(signals).toHaveLength(100);
    expect(signals.at(-1)).toEqual({
      _tag: "DiffChanged",
      workspaceId: "ws-1",
      version: "generation:99",
    });
  });
});
