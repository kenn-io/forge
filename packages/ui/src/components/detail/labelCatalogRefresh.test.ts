import { describe, expect, it, vi } from "vite-plus/test";

import { loadLabelCatalogWithRefresh } from "./labelCatalogRefresh.js";

function response(name: string, state: { stale?: boolean; syncing?: boolean } = {}) {
  return {
    labels: [{ name, color: "fbca04" }],
    stale: state.stale ?? false,
    syncing: state.syncing ?? false,
  };
}

describe("loadLabelCatalogWithRefresh", () => {
  it("reloads while the catalog response is stale or syncing", async () => {
    const loadOnce = vi
      .fn()
      .mockResolvedValueOnce(response("cached", { stale: true, syncing: true }))
      .mockResolvedValueOnce(response("fresh"));
    const updates: string[][] = [];

    await loadLabelCatalogWithRefresh({
      loadOnce,
      isActive: () => true,
      wait: async () => undefined,
      onUpdate: (catalog) => updates.push(catalog.labels.map((label) => label.name)),
    });

    expect(loadOnce).toHaveBeenCalledTimes(2);
    expect(updates).toEqual([["cached"], ["fresh"]]);
  });
});
