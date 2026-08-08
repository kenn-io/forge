import { describe, expect, it, vi } from "@effect/vitest";
import { Effect } from "effect";

import { loadLabelCatalogWithRefresh } from "./labelCatalogRefresh.js";

function response(name: string, state: { stale?: boolean; syncing?: boolean } = {}) {
  return {
    labels: [{ name, color: "fbca04" }],
    stale: state.stale ?? false,
    syncing: state.syncing ?? false,
  };
}

describe("loadLabelCatalogWithRefresh", () => {
  it.effect("reloads while the catalog response is stale or syncing", () =>
    Effect.gen(function* () {
      const loadOnce = vi
        .fn()
        .mockReturnValueOnce(response("cached", { stale: true, syncing: true }))
        .mockReturnValueOnce(response("fresh"));
      const updates: string[][] = [];

      yield* loadLabelCatalogWithRefresh({
        loadOnce: Effect.sync(loadOnce),
        isActive: () => true,
        intervalMs: 0,
        onUpdate: (catalog) =>
          Effect.sync(() => {
            updates.push(catalog.labels.map((label) => label.name));
          }),
      });

      expect(loadOnce).toHaveBeenCalledTimes(2);
      expect(updates).toEqual([["cached"], ["fresh"]]);
    }),
  );
});
