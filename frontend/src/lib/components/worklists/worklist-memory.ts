// Browser-local memory of the last open worklist and, per list, the last open
// item, so returning to Worklists reopens where the maintainer left off.

const STORAGE_KEY = "kenn-forge:worklists:last";

interface WorklistMemory {
  listId: number | null;
  /** Last open item key per list ID, by `worklistItemKey`. */
  items: Record<string, string>;
}

function readMemory(): WorklistMemory {
  try {
    const raw = globalThis.localStorage?.getItem(STORAGE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : null;
    if (typeof parsed !== "object" || parsed === null) return { listId: null, items: {} };
    const { listId, items } = parsed as Partial<WorklistMemory>;
    return {
      listId: typeof listId === "number" ? listId : null,
      items: typeof items === "object" && items !== null ? items : {},
    };
  } catch {
    return { listId: null, items: {} };
  }
}

export function lastWorklistId(): number | null {
  return readMemory().listId;
}

export function lastWorklistItemKey(listId: number): string | null {
  const key = readMemory().items[String(listId)];
  return typeof key === "string" ? key : null;
}

/**
 * Records the open list and, when one is open, its item. Lists missing from
 * `liveListIds` are forgotten so the stored map stays bounded.
 */
export function rememberWorklist(listId: number, itemKey: string | null, liveListIds: readonly number[]): void {
  const memory = readMemory();
  const live = new Set(liveListIds.map(String));
  live.add(String(listId));
  const items = Object.fromEntries(Object.entries(memory.items).filter(([id]) => live.has(id)));
  if (itemKey !== null) items[String(listId)] = itemKey;
  try {
    globalThis.localStorage?.setItem(STORAGE_KEY, JSON.stringify({ listId, items }));
  } catch {
    // localStorage can be unavailable in restricted browser contexts.
  }
}
