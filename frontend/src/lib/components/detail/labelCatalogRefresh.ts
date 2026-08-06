import { Effect } from "effect";
import type { Label } from "../../api/types.js";

export interface LabelCatalogLoadResult {
  labels: Label[];
  stale?: boolean;
  syncing?: boolean;
}

export interface LabelCatalogRefreshOptions<LoadE, LoadR, UpdateE, UpdateR> {
  loadOnce: Effect.Effect<LabelCatalogLoadResult, LoadE, LoadR>;
  onUpdate: (catalog: LabelCatalogLoadResult) => Effect.Effect<void, UpdateE, UpdateR>;
  isActive: () => boolean;
  intervalMs?: number;
}

export const loadLabelCatalogWithRefresh = Effect.fn("LabelCatalog.refresh")(function* <
  LoadE,
  LoadR,
  UpdateE,
  UpdateR,
>({ loadOnce, onUpdate, isActive, intervalMs = 1_000 }: LabelCatalogRefreshOptions<LoadE, LoadR, UpdateE, UpdateR>) {
  while (yield* Effect.sync(isActive)) {
    const catalog = yield* loadOnce;
    if (!(yield* Effect.sync(isActive))) return;
    yield* onUpdate(catalog);
    if (!catalog.stale && !catalog.syncing) return;
    yield* Effect.sleep(intervalMs);
  }
});
