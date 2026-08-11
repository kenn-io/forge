import { describe, expect, it, vi } from "vite-plus/test";

import { createKataDaemonsStore } from "./kata-daemons.svelte.js";

describe("Kata daemon roster store", () => {
  it("keeps roster metadata and reports the configured default without global selection", async () => {
    const store = createKataDaemonsStore({
      fetchDaemons: vi.fn().mockResolvedValue([
        { id: "home", default: false },
        { id: "work", default: true },
      ]),
    });

    await store.load();

    expect(store.daemons().map((daemon) => daemon.id)).toEqual(["home", "work"]);
    expect(store.defaultDaemonID()).toBe("work");
    expect(store.loading()).toBe(false);
    expect(store.error()).toBeNull();
  });

  it("does not let an older roster request replace a newer one", async () => {
    const resolvers: Array<(value: Array<{ id: string; default: boolean }>) => void> = [];
    const store = createKataDaemonsStore({
      fetchDaemons: vi.fn().mockImplementation(() => new Promise((resolve) => resolvers.push(resolve))),
    });

    const first = store.load();
    const second = store.load();
    resolvers[1]?.([{ id: "new", default: true }]);
    await second;
    resolvers[0]?.([{ id: "old", default: true }]);
    await first;

    expect(store.daemons().map((daemon) => daemon.id)).toEqual(["new"]);
  });

  it("clears stale roster data when loading fails", async () => {
    const fetchDaemons = vi
      .fn()
      .mockResolvedValueOnce([{ id: "home", default: true }])
      .mockRejectedValueOnce(new Error("catalog unavailable"));
    const store = createKataDaemonsStore({ fetchDaemons });

    await store.load();
    await store.load();

    expect(store.daemons()).toEqual([]);
    expect(store.error()).toBe("catalog unavailable");
  });
});
