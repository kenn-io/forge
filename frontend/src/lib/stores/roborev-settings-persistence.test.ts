import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../app/runtime.js";
import { createSettingsStore } from "./settings.svelte.js";
import {
  beginRoborevSettingsHydration,
  hydrateRoborevSettings,
  saveRoborevSettings,
} from "./roborev-settings-persistence.js";

describe("Roborev settings persistence", () => {
  let runtime: OwnedAppRuntime;
  let persisted = { init_managed_clones: false };
  const requests: Array<{ init_managed_clones?: boolean }> = [];

  beforeEach(() => {
    runtime = makeAppRuntime();
    persisted = { init_managed_clones: false };
    requests.length = 0;
    vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      const body = (await request.clone().json()) as { roborev: { init_managed_clones?: boolean } };
      requests.push(body.roborev);
      persisted = { ...persisted, ...body.roborev };
      return Response.json({ roborev: persisted });
    });
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
  });

  it("persists through the dedicated Roborev request object", async () => {
    const store = createSettingsStore();
    const execution = runtime.runCommand(
      saveRoborevSettings({
        store,
        baseline: store.getRoborevSettings(),
        changes: { init_managed_clones: true },
      }),
      { operation: "test Roborev settings persistence", safeContext: {}, onFailure: () => undefined },
    );
    await Effect.runPromise(execution.await.pipe(Effect.flatMap((exit) => exit)));

    expect(requests).toEqual([{ init_managed_clones: true }]);
    expect(store.getRoborevSettings()).toEqual({ init_managed_clones: true });
  });

  it("keeps a confirmed save when an older hydration finishes later", async () => {
    const store = createSettingsStore();
    const hydration = beginRoborevSettingsHydration(store);
    const execution = runtime.runCommand(
      saveRoborevSettings({
        store,
        baseline: store.getRoborevSettings(),
        changes: { init_managed_clones: true },
      }),
      { operation: "test Roborev settings persistence", safeContext: {}, onFailure: () => undefined },
    );
    await Effect.runPromise(execution.await.pipe(Effect.flatMap((exit) => exit)));
    hydrateRoborevSettings(hydration, { init_managed_clones: false });

    expect(store.getRoborevSettings()).toEqual({ init_managed_clones: true });
  });
});
