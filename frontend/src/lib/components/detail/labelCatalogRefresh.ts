import type { Label } from "../../api/types.js";

export interface LabelCatalogLoadResult {
  labels: Label[];
  stale?: boolean;
  syncing?: boolean;
}

export interface LabelCatalogRefreshOptions {
  loadOnce: () => Promise<LabelCatalogLoadResult>;
  onUpdate: (catalog: LabelCatalogLoadResult) => void;
  isActive: () => boolean;
  wait?: (ms: number) => Promise<void>;
  intervalMs?: number;
}

function defaultWait(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export async function loadLabelCatalogWithRefresh({
  loadOnce,
  onUpdate,
  isActive,
  wait = defaultWait,
  intervalMs = 1_000,
}: LabelCatalogRefreshOptions): Promise<void> {
  while (isActive()) {
    const catalog = await loadOnce();
    if (!isActive()) return;
    onUpdate(catalog);
    if (!catalog.stale && !catalog.syncing) return;
    await wait(intervalMs);
  }
}
