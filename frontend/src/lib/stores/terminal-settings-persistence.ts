import { Effect, Exit } from "effect";
import type { TerminalSettings } from "../api/types.js";
import { SettingsWorkflow, type SettingsError } from "./settings-workflow.js";

export interface TerminalSettingsStore {
  getTerminalSettings: () => TerminalSettings;
  setTerminalSettings: (settings: TerminalSettings) => void;
}

interface SaveQueue {
  confirmed: TerminalSettings;
  fieldConfirmedGenerations: Partial<Record<keyof TerminalSettings, number>>;
  fieldOptimisticGenerations: Partial<Record<keyof TerminalSettings, number>>;
  fieldPendingGenerations: Partial<Record<keyof TerminalSettings, Set<number>>>;
  fieldPreviewGenerations: Partial<Record<keyof TerminalSettings, number>>;
  hydrationGeneration: number;
  mutationGeneration: number;
  pending: number;
  previewGeneration: number;
}

interface SaveTerminalSettingsOptions {
  baseline: TerminalSettings;
  changes: Partial<TerminalSettings>;
  store: TerminalSettingsStore;
}

interface TerminalSettingsPreview {
  baseline: TerminalSettings;
  changes: Partial<TerminalSettings>;
  fieldGenerations: Partial<Record<keyof TerminalSettings, number>>;
}

export interface TerminalSettingsHydration {
  fieldConfirmedGenerations: Partial<Record<keyof TerminalSettings, number>>;
  hydrationGeneration: number;
  store: TerminalSettingsStore;
}

const saveQueues = new WeakMap<TerminalSettingsStore, SaveQueue>();
const previews = new WeakMap<TerminalSettingsStore, TerminalSettingsPreview>();
const TERMINAL_SETTINGS_KEYS = [
  "font_family",
  "font_size",
  "scrollback",
  "line_height",
  "letter_spacing",
  "cursor_blink",
  "font_ligatures",
  "hide_tmux_status",
  "graphics",
  "tmux_mouse",
  "retained_sessions",
] satisfies ReadonlyArray<keyof TerminalSettings>;

function changedKeys(settings: Partial<Record<keyof TerminalSettings, unknown>>): Array<keyof TerminalSettings> {
  return TERMINAL_SETTINGS_KEYS.filter((key) => key in settings);
}

function isDeferredSetting(key: keyof TerminalSettings): boolean {
  return key === "retained_sessions";
}

function immediateSettings(settings: Partial<TerminalSettings>): Partial<TerminalSettings> {
  const immediate: Partial<TerminalSettings> = {};
  for (const key of changedKeys(settings)) {
    if (!isDeferredSetting(key)) {
      Object.assign(immediate, { [key]: settings[key] });
    }
  }
  return immediate;
}

function previewOwnsField(queue: SaveQueue, preview: TerminalSettingsPreview, key: keyof TerminalSettings): boolean {
  const generation = preview.fieldGenerations[key];
  return generation !== undefined && queue.fieldPreviewGenerations[key] === generation;
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

function reconcileSavedSettings(
  store: TerminalSettingsStore,
  expected: Partial<TerminalSettings>,
  saved: TerminalSettings,
  ownsField: (key: keyof TerminalSettings) => boolean,
): void {
  const current = store.getTerminalSettings();
  const updates: Partial<TerminalSettings> = {};

  for (const key of changedKeys(expected)) {
    if (ownsField(key) && (isDeferredSetting(key) || Object.is(current[key], expected[key]))) {
      Object.assign(updates, { [key]: saved[key] });
    }
  }
  if (changedKeys(updates).length > 0) {
    store.setTerminalSettings({ ...current, ...updates });
  }
}

function settingsWithoutPreview(store: TerminalSettingsStore): TerminalSettings {
  const current = store.getTerminalSettings();
  const preview = previews.get(store);
  const queue = saveQueues.get(store);
  if (!preview || !queue) return { ...current };

  const settings = { ...current };
  for (const key of changedKeys(preview.changes)) {
    if (previewOwnsField(queue, preview, key) && Object.is(current[key], preview.changes[key])) {
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
      fieldPreviewGenerations: {},
      hydrationGeneration: 0,
      mutationGeneration: 0,
      pending: 0,
      previewGeneration: 0,
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
  const queue = saveQueues.get(store);
  if (!preview || !queue) return;

  const remaining = { ...preview.changes };
  const remainingGenerations = { ...preview.fieldGenerations };
  for (const key of changedKeys(changes)) {
    if (Object.is(changes[key], preview.changes[key])) {
      delete remaining[key];
      delete remainingGenerations[key];
      if (previewOwnsField(queue, preview, key)) {
        delete queue.fieldPreviewGenerations[key];
      }
    }
  }
  if (changedKeys(remaining).length > 0) {
    previews.set(store, {
      ...preview,
      changes: remaining,
      fieldGenerations: remainingGenerations,
    });
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
  const queue = getSaveQueue(store, baseline);
  const current = store.getTerminalSettings();
  const authoritativeBaseline = { ...queue.confirmed };
  for (const key of changedKeys(queue.fieldPendingGenerations)) {
    if ((queue.fieldPendingGenerations[key]?.size ?? 0) > 0) {
      Object.assign(authoritativeBaseline, { [key]: current[key] });
    }
  }
  const changes = immediateSettings(terminalSettingsChanges(authoritativeBaseline, next));
  queue.previewGeneration += 1;
  const previewGeneration = queue.previewGeneration;
  const previousPreview = previews.get(store);
  const previous = previousPreview?.changes ?? {};
  const updates: Partial<TerminalSettings> = {};
  const fieldGenerations: Partial<Record<keyof TerminalSettings, number>> = {};

  for (const key of changedKeys(previous)) {
    if (
      !(key in changes) &&
      previousPreview !== undefined &&
      previewOwnsField(queue, previousPreview, key) &&
      Object.is(current[key], previous[key])
    ) {
      Object.assign(updates, { [key]: authoritativeBaseline[key] });
      delete queue.fieldPreviewGenerations[key];
    }
  }
  for (const key of changedKeys(changes)) {
    Object.assign(updates, { [key]: changes[key] });
    fieldGenerations[key] = previewGeneration;
    queue.fieldPreviewGenerations[key] = previewGeneration;
  }

  if (changedKeys(updates).length > 0) {
    store.setTerminalSettings({ ...current, ...updates });
  }
  if (changedKeys(changes).length > 0) {
    previews.set(store, {
      baseline: authoritativeBaseline,
      changes,
      fieldGenerations,
    });
  } else {
    previews.delete(store);
  }
}

export function restoreTerminalSettingsPreview(store: TerminalSettingsStore): void {
  const preview = previews.get(store);
  const queue = saveQueues.get(store);
  if (!preview || !queue) return;
  reconcileSettings(store, preview.changes, preview.baseline, (key) => previewOwnsField(queue, preview, key));
  for (const key of changedKeys(preview.changes)) {
    if (previewOwnsField(queue, preview, key)) {
      delete queue.fieldPreviewGenerations[key];
    }
  }
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
  const preview = previews.get(store);
  if (preview) {
    const changes: Partial<TerminalSettings> = {};
    const fieldGenerations: Partial<Record<keyof TerminalSettings, number>> = {};
    for (const key of changedKeys(preview.changes)) {
      const previewGeneration = preview.fieldGenerations[key];
      if (previewGeneration === undefined || queue.fieldPreviewGenerations[key] !== previewGeneration) continue;
      if (Object.is(preview.changes[key], confirmed[key])) {
        delete queue.fieldPreviewGenerations[key];
        continue;
      }
      Object.assign(changes, { [key]: preview.changes[key] });
      Object.assign(hydrated, { [key]: preview.changes[key] });
      fieldGenerations[key] = previewGeneration;
    }
    if (changedKeys(changes).length > 0) {
      previews.set(store, {
        baseline: { ...confirmed },
        changes,
        fieldGenerations,
      });
    } else {
      previews.delete(store);
    }
  }
  queue.confirmed = confirmed;
  store.setTerminalSettings(hydrated);
}

export const saveTerminalSettings = Effect.fn("TerminalSettings.save")(function* ({
  baseline,
  changes,
  store,
}: SaveTerminalSettingsOptions) {
  const workflow = yield* SettingsWorkflow;
  const state = yield* Effect.sync(() => {
    const activeQueue = getSaveQueue(store, baseline);
    if (activeQueue.pending === 0) {
      activeQueue.confirmed = settingsWithoutPreview(store);
    }
    activeQueue.mutationGeneration += 1;
    const mutationGeneration = activeQueue.mutationGeneration;
    for (const key of changedKeys(changes)) {
      addPendingMutation(activeQueue, key, mutationGeneration);
      activeQueue.fieldOptimisticGenerations[key] = mutationGeneration;
      delete activeQueue.fieldPreviewGenerations[key];
    }
    activeQueue.pending += 1;
    const optimisticChanges = immediateSettings(changes);
    if (changedKeys(optimisticChanges).length > 0) {
      store.setTerminalSettings({
        ...store.getTerminalSettings(),
        ...optimisticChanges,
      });
    }
    return { activeQueue, mutationGeneration };
  });
  const save = workflow
    .persist(() => ({ terminal: { ...state.activeQueue.confirmed, ...changes } }))
    .pipe(Effect.map((settings) => settings.terminal));

  return yield* save.pipe(
    Effect.onExit((exit) =>
      Effect.sync(() => {
        if (Exit.isSuccess(exit)) {
          const saved = exit.value;
          const confirmed = { ...state.activeQueue.confirmed };
          for (const key of changedKeys(changes)) {
            Object.assign(confirmed, { [key]: saved[key] });
            state.activeQueue.fieldConfirmedGenerations[key] = state.mutationGeneration;
          }
          state.activeQueue.confirmed = confirmed;
          confirmPreviewChanges(store, changes);
          reconcileSavedSettings(
            store,
            changes,
            saved,
            (key) => state.activeQueue.fieldOptimisticGenerations[key] === state.mutationGeneration,
          );
        } else {
          reconcileSavedSettings(
            store,
            changes,
            state.activeQueue.confirmed,
            (key) => state.activeQueue.fieldOptimisticGenerations[key] === state.mutationGeneration,
          );
        }

        for (const key of changedKeys(changes)) {
          settlePendingMutation(state.activeQueue, key, state.mutationGeneration);
          if (state.activeQueue.fieldOptimisticGenerations[key] === state.mutationGeneration) {
            delete state.activeQueue.fieldOptimisticGenerations[key];
          }
        }
        state.activeQueue.pending -= 1;
      }),
    ),
  );
}) satisfies (options: SaveTerminalSettingsOptions) => Effect.Effect<TerminalSettings, SettingsError, SettingsWorkflow>;
