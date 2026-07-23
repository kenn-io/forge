import type { TerminalSettings } from "@middleman/ui/api/types";

export interface TerminalSettingsStore {
  getTerminalSettings: () => TerminalSettings;
  setTerminalSettings: (settings: TerminalSettings) => void;
}

interface SaveQueue {
  confirmed: TerminalSettings;
  pending: number;
  tail: Promise<void>;
}

interface SaveTerminalSettingsOptions {
  baseline: TerminalSettings;
  changes: Partial<TerminalSettings>;
  persist: (settings: TerminalSettings) => Promise<TerminalSettings>;
  store: TerminalSettingsStore;
}

const saveQueues = new WeakMap<TerminalSettingsStore, SaveQueue>();

function changedKeys(settings: Partial<TerminalSettings>): (keyof TerminalSettings)[] {
  return Object.keys(settings) as (keyof TerminalSettings)[];
}

function reconcileSettings(
  store: TerminalSettingsStore,
  expected: Partial<TerminalSettings>,
  replacement: TerminalSettings,
): void {
  const current = store.getTerminalSettings();
  const updates: Partial<TerminalSettings> = {};

  for (const key of changedKeys(expected)) {
    if (Object.is(current[key], expected[key])) {
      Object.assign(updates, { [key]: replacement[key] });
    }
  }
  if (changedKeys(updates).length > 0) {
    store.setTerminalSettings({ ...current, ...updates });
  }
}

export function terminalSettingsChanges(baseline: TerminalSettings, next: TerminalSettings): Partial<TerminalSettings> {
  const changes: Partial<TerminalSettings> = {};
  for (const key of changedKeys(next)) {
    if (!Object.is(baseline[key], next[key])) {
      Object.assign(changes, { [key]: next[key] });
    }
  }
  return changes;
}

export function previewTerminalSettings(
  store: TerminalSettingsStore,
  baseline: TerminalSettings,
  next: TerminalSettings,
): void {
  const changes = terminalSettingsChanges(baseline, next);
  store.setTerminalSettings({
    ...store.getTerminalSettings(),
    ...changes,
  });
}

export function restoreTerminalSettingsPreview(
  store: TerminalSettingsStore,
  baseline: TerminalSettings,
  preview: TerminalSettings,
): void {
  reconcileSettings(store, terminalSettingsChanges(baseline, preview), baseline);
}

export function saveTerminalSettings({
  baseline,
  changes,
  persist,
  store,
}: SaveTerminalSettingsOptions): Promise<TerminalSettings> {
  let queue = saveQueues.get(store);
  if (!queue) {
    queue = {
      confirmed: { ...baseline },
      pending: 0,
      tail: Promise.resolve(),
    };
    saveQueues.set(store, queue);
  }

  const activeQueue = queue;
  activeQueue.pending += 1;
  store.setTerminalSettings({
    ...store.getTerminalSettings(),
    ...changes,
  });

  const save = activeQueue.tail.then(async () => {
    const request = { ...activeQueue.confirmed, ...changes };
    try {
      const saved = await persist(request);
      activeQueue.confirmed = saved;
      reconcileSettings(store, changes, saved);
      return saved;
    } catch (error) {
      reconcileSettings(store, changes, baseline);
      throw error;
    }
  });
  activeQueue.tail = save.then(
    () => undefined,
    () => undefined,
  );

  return save.finally(() => {
    activeQueue.pending -= 1;
    if (activeQueue.pending === 0 && saveQueues.get(store) === activeQueue) {
      saveQueues.delete(store);
    }
  });
}
