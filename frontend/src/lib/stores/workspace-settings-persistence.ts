import { Effect, Exit } from "effect";
import type { Settings } from "../api/types.js";
import { SettingsWorkflow, type SettingsError } from "./settings-workflow.js";

type WorkspaceSettings = Settings["workspaces"];
type WorkspaceSettingsKey = keyof WorkspaceSettings;

export interface WorkspaceSettingsStore {
  getWorkspaceSettings: () => WorkspaceSettings;
  setWorkspaceSettings: (settings: WorkspaceSettings) => void;
}

interface WorkspaceSettingsQueue {
  confirmed: WorkspaceSettings;
  fieldConfirmedGenerations: Partial<Record<WorkspaceSettingsKey, number>>;
  fieldOptimisticGenerations: Partial<Record<WorkspaceSettingsKey, number>>;
  fieldPendingGenerations: Partial<Record<WorkspaceSettingsKey, Set<number>>>;
  hydrationGeneration: number;
  mutationGeneration: number;
}

export interface WorkspaceSettingsHydration {
  fieldConfirmedGenerations: Partial<Record<WorkspaceSettingsKey, number>>;
  hydrationGeneration: number;
  store: WorkspaceSettingsStore;
}

interface SaveWorkspaceSettingsOptions {
  baseline: WorkspaceSettings;
  changes: Partial<WorkspaceSettings>;
  store: WorkspaceSettingsStore;
}

const WORKSPACE_SETTINGS_KEYS = [
  "auto_assign_on_create",
  "default_sidebar_view",
] satisfies ReadonlyArray<WorkspaceSettingsKey>;
const saveQueues = new WeakMap<WorkspaceSettingsStore, WorkspaceSettingsQueue>();

function changedKeys(settings: Partial<WorkspaceSettings>): WorkspaceSettingsKey[] {
  return WORKSPACE_SETTINGS_KEYS.filter((key) => key in settings);
}

function getQueue(store: WorkspaceSettingsStore): WorkspaceSettingsQueue {
  let queue = saveQueues.get(store);
  if (!queue) {
    queue = {
      confirmed: { ...store.getWorkspaceSettings() },
      fieldConfirmedGenerations: {},
      fieldOptimisticGenerations: {},
      fieldPendingGenerations: {},
      hydrationGeneration: 0,
      mutationGeneration: 0,
    };
    saveQueues.set(store, queue);
  }
  return queue;
}

function addPending(queue: WorkspaceSettingsQueue, key: WorkspaceSettingsKey, generation: number): void {
  const pending = queue.fieldPendingGenerations[key] ?? new Set<number>();
  pending.add(generation);
  queue.fieldPendingGenerations[key] = pending;
}

function settlePending(queue: WorkspaceSettingsQueue, key: WorkspaceSettingsKey, generation: number): void {
  const pending = queue.fieldPendingGenerations[key];
  if (!pending) return;
  pending.delete(generation);
  if (pending.size === 0) delete queue.fieldPendingGenerations[key];
}

export function beginWorkspaceSettingsHydration(store: WorkspaceSettingsStore): WorkspaceSettingsHydration {
  const queue = getQueue(store);
  queue.hydrationGeneration += 1;
  return {
    fieldConfirmedGenerations: { ...queue.fieldConfirmedGenerations },
    hydrationGeneration: queue.hydrationGeneration,
    store,
  };
}

export function hydrateWorkspaceSettings(hydration: WorkspaceSettingsHydration, settings: WorkspaceSettings): void {
  const { store } = hydration;
  const queue = getQueue(store);
  if (hydration.hydrationGeneration !== queue.hydrationGeneration) return;

  const current = store.getWorkspaceSettings();
  const confirmed = { ...settings };
  const hydrated = { ...settings };
  for (const key of WORKSPACE_SETTINGS_KEYS) {
    const confirmedAtStart = hydration.fieldConfirmedGenerations[key] ?? 0;
    const confirmedNow = queue.fieldConfirmedGenerations[key] ?? 0;
    const hasPendingMutation = (queue.fieldPendingGenerations[key]?.size ?? 0) > 0;
    if (!hasPendingMutation && confirmedNow <= confirmedAtStart) continue;
    Object.assign(confirmed, { [key]: queue.confirmed[key] });
    Object.assign(hydrated, { [key]: current[key] });
  }
  queue.confirmed = confirmed;
  store.setWorkspaceSettings(hydrated);
}

export const saveWorkspaceSettings = Effect.fn("WorkspaceSettings.save")(function* ({
  baseline,
  changes,
  store,
}: SaveWorkspaceSettingsOptions) {
  const workflow = yield* SettingsWorkflow;
  const state = yield* Effect.sync(() => {
    const queue = getQueue(store);
    if (Object.keys(queue.fieldPendingGenerations).length === 0) {
      queue.confirmed = { ...baseline };
    }
    queue.mutationGeneration += 1;
    const generation = queue.mutationGeneration;
    for (const key of changedKeys(changes)) {
      addPending(queue, key, generation);
      queue.fieldOptimisticGenerations[key] = generation;
    }
    store.setWorkspaceSettings({ ...store.getWorkspaceSettings(), ...changes });
    return { generation, hydrationGeneration: queue.hydrationGeneration, queue };
  });

  const save = workflow
    .persist(() => ({ workspaces: { ...state.queue.confirmed, ...changes } }))
    .pipe(Effect.map((settings) => settings.workspaces));

  return yield* save.pipe(
    Effect.flatMap((saved) => {
      if (state.queue.hydrationGeneration === state.hydrationGeneration) return Effect.succeed(saved);
      const rebased = { ...state.queue.confirmed, ...changes };
      return workflow.persist(() => ({ workspaces: rebased })).pipe(Effect.map((settings) => settings.workspaces));
    }),
    Effect.onExit((exit) =>
      Effect.sync(() => {
        const current = store.getWorkspaceSettings();
        if (Exit.isSuccess(exit)) {
          const saved = exit.value;
          const confirmed = { ...state.queue.confirmed };
          for (const key of changedKeys(changes)) {
            Object.assign(confirmed, { [key]: saved[key] });
            state.queue.fieldConfirmedGenerations[key] = state.generation;
          }
          state.queue.confirmed = confirmed;
          const reconciled = { ...current };
          for (const key of changedKeys(changes)) {
            if (state.queue.fieldOptimisticGenerations[key] === state.generation) {
              Object.assign(reconciled, { [key]: saved[key] });
            }
          }
          store.setWorkspaceSettings(reconciled);
        } else {
          const rollback = { ...current };
          for (const key of changedKeys(changes)) {
            if (state.queue.fieldOptimisticGenerations[key] === state.generation) {
              Object.assign(rollback, { [key]: state.queue.confirmed[key] });
            }
          }
          store.setWorkspaceSettings(rollback);
        }

        for (const key of changedKeys(changes)) {
          settlePending(state.queue, key, state.generation);
          if (state.queue.fieldOptimisticGenerations[key] === state.generation) {
            delete state.queue.fieldOptimisticGenerations[key];
          }
        }
      }),
    ),
  );
}) satisfies (
  options: SaveWorkspaceSettingsOptions,
) => Effect.Effect<WorkspaceSettings, SettingsError, SettingsWorkflow>;
