const storageKey = "kenn-forge:vite-reload";

interface FrontendReload {
  source: string;
  target: string;
}

function readFrontendReload(storage: Storage): FrontendReload | null {
  const raw = storage.getItem(storageKey);
  if (!raw) return null;

  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    storage.removeItem(storageKey);
    return null;
  }

  if (
    typeof value !== "object" ||
    value === null ||
    !("source" in value) ||
    typeof value.source !== "string" ||
    !("target" in value) ||
    typeof value.target !== "string"
  ) {
    storage.removeItem(storageKey);
    return null;
  }

  return { source: value.source, target: value.target };
}

export function prepareFrontendReload(storage: Storage, source: string, target: string): boolean {
  const pending = readFrontendReload(storage);
  if (pending?.source === source && pending.target === target) return false;

  storage.setItem(storageKey, JSON.stringify({ source, target } satisfies FrontendReload));
  return true;
}

export function retireFrontendReload(storage: Storage, loadedEntrypoint: string): void {
  const pending = readFrontendReload(storage);
  if (!pending || loadedEntrypoint === pending.source) return;

  storage.removeItem(storageKey);
}
