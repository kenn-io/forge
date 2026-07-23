import type { TerminalSettings } from "../api/types.js";

export interface TerminalSettingsStore {
  getTerminalSettings: () => TerminalSettings;
  setTerminalSettings: (settings: TerminalSettings) => void;
}

interface SaveQueue {
  confirmed: TerminalSettings;
  fieldConfirmedGenerations: Partial<Record<keyof TerminalSettings, number>>;
  fieldOptimisticGenerations: Partial<Record<keyof TerminalSettings, number>>;
  fieldPendingGenerations: Partial<Record<keyof TerminalSettings, Set<number>>>;
  hydrationGeneration: number;
  mutationGeneration: number;
  pending: number;
  tail: Promise<void>;
}

interface SaveTerminalSettingsOptions {
  baseline: TerminalSettings;
  changes: Partial<TerminalSettings>;
  persist: (settings: TerminalSettings) => Promise<TerminalSettings>;
  store: TerminalSettingsStore;
}

interface TerminalSettingsPreview {
  baseline: TerminalSettings;
  changes: Partial<TerminalSettings>;
}

export interface TerminalSettingsHydration {
  fieldConfirmedGenerations: Partial<Record<keyof TerminalSettings, number>>;
  hydrationGeneration: number;
  store: TerminalSettingsStore;
}

const saveQueues = new WeakMap<TerminalSettingsStore, SaveQueue>();
const previews = new WeakMap<TerminalSettingsStore, TerminalSettingsPreview>();

function changedKeys<T extends object>(settings: T): (keyof T)[] {
  return Object.keys(settings) as (keyof T)[];
}

function reconcileSettings(
  store: TerminalSettingsStore,
  expected: Partial<TerminalSettings>,
  replacement: TerminalSettings,
  ownsField: (key: keyof TerminalSettings) => boolean = () => true,
): void {
  const current = store.getTerminalSettings();
  const updates: Partial<TerminalSettings> = {};

  for (const key of changedKeys(expected)) {
    if (ownsField(key) && Object.is(current[key], expected[key])) {
      Object.assign(updates, { [key]: replacement[key] });
    }
  }
  if (changedKeys(updates).length > 0) {
    store.setTerminalSettings({ ...current, ...updates });
  }
}

function settingsWithoutPreview(store: TerminalSettingsStore): TerminalSettings {
  const current = store.getTerminalSettings();
  const preview = previews.get(store);
  if (!preview) return { ...current };

  const settings = { ...current };
  for (const key of changedKeys(preview.changes)) {
    if (Object.is(current[key], preview.changes[key])) {
      Object.assign(settings, { [key]: preview.baseline[key] });
    }
  }
  return settings;
}

function getSaveQueue(store: TerminalSettingsStore, baseline: TerminalSettings): SaveQueue {
  let queue = saveQueues.get(store);
  if (!queue) {
    queue = {
      confirmed: { ...baseline },
      fieldConfirmedGenerations: {},
      fieldOptimisticGenerations: {},
      fieldPendingGenerations: {},
      hydrationGeneration: 0,
      mutationGeneration: 0,
      pending: 0,
      tail: Promise.resolve(),
    };
    saveQueues.set(store, queue);
  }
  return queue;
}

function addPendingMutation(queue: SaveQueue, key: keyof TerminalSettings, generation: number): void {
  const pending = queue.fieldPendingGenerations[key] ?? new Set<number>();
  pending.add(generation);
  queue.fieldPendingGenerations[key] = pending;
}

function settlePendingMutation(queue: SaveQueue, key: keyof TerminalSettings, generation: number): void {
  const pending = queue.fieldPendingGenerations[key];
  if (!pending) return;
  pending.delete(generation);
  if (pending.size === 0) {
    delete queue.fieldPendingGenerations[key];
  }
}

function confirmPreviewChanges(store: TerminalSettingsStore, changes: Partial<TerminalSettings>): void {
  const preview = previews.get(store);
  if (!preview) return;

  const remaining = { ...preview.changes };
  for (const key of changedKeys(changes)) {
    if (Object.is(changes[key], preview.changes[key])) {
      delete remaining[key];
    }
  }
  if (changedKeys(remaining).length > 0) {
    previews.set(store, { ...preview, changes: remaining });
  } else {
    previews.delete(store);
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
  const previous = previews.get(store)?.changes ?? {};
  const current = store.getTerminalSettings();
  const updates: Partial<TerminalSettings> = {};

  for (const key of changedKeys(previous)) {
    if (!(key in changes) && Object.is(current[key], previous[key])) {
      Object.assign(updates, { [key]: baseline[key] });
    }
  }
  Object.assign(updates, changes);

  if (changedKeys(updates).length > 0) {
    store.setTerminalSettings({ ...current, ...updates });
  }
  if (changedKeys(changes).length > 0) {
    previews.set(store, {
      baseline: { ...baseline },
      changes,
    });
  } else {
    previews.delete(store);
  }
}

export function restoreTerminalSettingsPreview(store: TerminalSettingsStore, baseline: TerminalSettings): void {
  const preview = previews.get(store);
  if (!preview) return;
  reconcileSettings(store, preview.changes, baseline);
  previews.delete(store);
}

export function beginTerminalSettingsHydration(store: TerminalSettingsStore): TerminalSettingsHydration {
  const queue = getSaveQueue(store, settingsWithoutPreview(store));
  queue.hydrationGeneration += 1;
  return {
    fieldConfirmedGenerations: { ...queue.fieldConfirmedGenerations },
    hydrationGeneration: queue.hydrationGeneration,
    store,
  };
}

export function hydrateTerminalSettings(hydration: TerminalSettingsHydration, settings: TerminalSettings): void {
  const { store } = hydration;
  const queue = getSaveQueue(store, settingsWithoutPreview(store));
  if (hydration.hydrationGeneration !== queue.hydrationGeneration) return;

  const current = store.getTerminalSettings();
  const confirmed = { ...settings };
  const hydrated = { ...settings };
  for (const key of changedKeys(settings)) {
    const confirmedAtStart = hydration.fieldConfirmedGenerations[key] ?? 0;
    const confirmedNow = queue.fieldConfirmedGenerations[key] ?? 0;
    const hasPendingMutation = (queue.fieldPendingGenerations[key]?.size ?? 0) > 0;
    if (!hasPendingMutation && confirmedNow <= confirmedAtStart) continue;
    Object.assign(confirmed, { [key]: queue.confirmed[key] });
    Object.assign(hydrated, { [key]: current[key] });
  }
  queue.confirmed = confirmed;
  store.setTerminalSettings(hydrated);
}

export function saveTerminalSettings({
  baseline,
  changes,
  persist,
  store,
}: SaveTerminalSettingsOptions): Promise<TerminalSettings> {
  const activeQueue = getSaveQueue(store, baseline);
  if (activeQueue.pending === 0) {
    activeQueue.confirmed = settingsWithoutPreview(store);
  }
  activeQueue.mutationGeneration += 1;
  const mutationGeneration = activeQueue.mutationGeneration;
  for (const key of changedKeys(changes)) {
    addPendingMutation(activeQueue, key, mutationGeneration);
    activeQueue.fieldOptimisticGenerations[key] = mutationGeneration;
  }
  activeQueue.pending += 1;
  store.setTerminalSettings({
    ...store.getTerminalSettings(),
    ...changes,
  });

  const save = activeQueue.tail.then(async () => {
    const request = { ...activeQueue.confirmed, ...changes };
    try {
      const saved = await persist(request);
      const confirmed = { ...activeQueue.confirmed };
      for (const key of changedKeys(changes)) {
        Object.assign(confirmed, { [key]: saved[key] });
        activeQueue.fieldConfirmedGenerations[key] = mutationGeneration;
      }
      activeQueue.confirmed = confirmed;
      confirmPreviewChanges(store, changes);
      reconcileSettings(
        store,
        changes,
        saved,
        (key) => activeQueue.fieldOptimisticGenerations[key] === mutationGeneration,
      );
      return saved;
    } catch (error) {
      reconcileSettings(
        store,
        changes,
        activeQueue.confirmed,
        (key) => activeQueue.fieldOptimisticGenerations[key] === mutationGeneration,
      );
      throw error;
    }
  });
  const result = save.finally(() => {
    for (const key of changedKeys(changes)) {
      settlePendingMutation(activeQueue, key, mutationGeneration);
      if (activeQueue.fieldOptimisticGenerations[key] === mutationGeneration) {
        delete activeQueue.fieldOptimisticGenerations[key];
      }
    }
    activeQueue.pending -= 1;
  });
  activeQueue.tail = result.then(
    () => undefined,
    () => undefined,
  );

  return result;
}
