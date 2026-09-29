import { Effect } from "effect";
import { expect, it, vi } from "vite-plus/test";
import { makeGeneratedApiLayer } from "../../api/generated-api.js";
import { makeGeneratedClient } from "../../testing/generated-client.js";
import { loadWorkspaceViewState, saveWorkspaceViewState } from "./workspace-view-state.js";

it.each([
  { workspaceId: "ws-2", hostKey: "peer" },
  { workspaceId: "ws-1", hostKey: undefined },
])("does not block $hostKey/$workspaceId behind another workspace's save", async ({ workspaceId, hostKey }) => {
  const delayed = Promise.withResolvers<{ active_tab: string }>();
  const started = Promise.withResolvers<void>();
  const calls: string[] = [];
  const read = vi.fn(async () => ({ active_tab: "home" }));
  const client = makeGeneratedClient({
    FleetService: {
      updateFleetWorkspaceViewState: async (_params, body) => {
        const tab = body.active_tab as string;
        calls.push(tab);
        if (tab === "terminal") {
          started.resolve();
          return delayed.promise;
        }
        return { active_tab: tab };
      },
      getFleetWorkspaceViewState: async (params) => {
        if (params.id === "ws-1") {
          calls.push("read");
          return { active_tab: "home" };
        }
        return read();
      },
    },
    WorkspacesService: { getWorkspaceViewState: read },
  });
  const layer = makeGeneratedApiLayer(client);
  const first = Effect.runPromise(saveWorkspaceViewState("ws-1", "peer", "terminal").pipe(Effect.provide(layer)));
  await started.promise;
  const second = Effect.runPromise(saveWorkspaceViewState("ws-1", "peer", "home").pipe(Effect.provide(layer)));
  const sameWorkspace = Effect.runPromise(loadWorkspaceViewState("ws-1", "peer").pipe(Effect.provide(layer)));
  const otherWorkspace = Effect.runPromise(loadWorkspaceViewState(workspaceId, hostKey).pipe(Effect.provide(layer)));
  try {
    await vi.waitFor(() => expect(read).toHaveBeenCalled());
    expect(await otherWorkspace).toEqual({ active_tab: "home" });
    expect(calls).toEqual(["terminal"]);
  } finally {
    delayed.resolve({ active_tab: "terminal" });
    await Promise.all([first, second, sameWorkspace, otherWorkspace]);
  }
  expect(calls).toEqual(["terminal", "home", "read"]);
});
