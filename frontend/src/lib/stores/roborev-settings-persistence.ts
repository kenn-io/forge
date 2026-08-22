import { Effect, Exit } from "effect";
import type { Settings } from "../api/types.js";
import { SettingsWorkflow, type SettingsError } from "./settings-workflow.js";

type RoborevSettings = Settings["roborev"];

export interface RoborevSettingsStore {
  getRoborevSettings: () => RoborevSettings;
  setRoborevSettings: (settings: RoborevSettings) => void;
}

interface RoborevSettingsQueue {
  confirmed: RoborevSettings;
  confirmedGeneration: number;
  hydrationGeneration: number;
  mutationGeneration: number;
  optimisticGeneration: number;
  pendingGenerations: Set<number>;
}

export interface RoborevSettingsHydration {
  confirmedGeneration: number;
  hydrationGeneration: number;
  store: RoborevSettingsStore;
}

interface SaveRoborevSettingsOptions {
  baseline: RoborevSettings;
  changes: Partial<RoborevSettings>;
  store: RoborevSettingsStore;
}

const saveQueues = new WeakMap<RoborevSettingsStore, RoborevSettingsQueue>();

function getQueue(store: RoborevSettingsStore): RoborevSettingsQueue {
  let queue = saveQueues.get(store);
  if (!queue) {
    queue = {
      confirmed: { ...store.getRoborevSettings() },
      confirmedGeneration: 0,
      hydrationGeneration: 0,
      mutationGeneration: 0,
      optimisticGeneration: 0,
      pendingGenerations: new Set<number>(),
    };
    saveQueues.set(store, queue);
  }
  return queue;
}

export function beginRoborevSettingsHydration(store: RoborevSettingsStore): RoborevSettingsHydration {
  const queue = getQueue(store);
  queue.hydrationGeneration += 1;
  return {
    confirmedGeneration: queue.confirmedGeneration,
    hydrationGeneration: queue.hydrationGeneration,
    store,
  };
}

export function hydrateRoborevSettings(hydration: RoborevSettingsHydration, settings: RoborevSettings): void {
  const queue = getQueue(hydration.store);
  if (hydration.hydrationGeneration !== queue.hydrationGeneration) return;
  if (queue.pendingGenerations.size === 0 && queue.confirmedGeneration <= hydration.confirmedGeneration) {
    queue.confirmed = { ...settings };
    hydration.store.setRoborevSettings({ ...settings });
    return;
  }
  hydration.store.setRoborevSettings({ ...hydration.store.getRoborevSettings() });
}

export const saveRoborevSettings = Effect.fn("RoborevSettings.save")(function* ({
  baseline,
  changes,
  store,
}: SaveRoborevSettingsOptions) {
  const workflow = yield* SettingsWorkflow;
  const state = yield* Effect.sync(() => {
    const queue = getQueue(store);
    if (queue.pendingGenerations.size === 0) queue.confirmed = { ...baseline };
    queue.mutationGeneration += 1;
    const generation = queue.mutationGeneration;
    queue.optimisticGeneration = generation;
    queue.pendingGenerations.add(generation);
    store.setRoborevSettings({ ...store.getRoborevSettings(), ...changes });
    return { generation, queue };
  });

  return yield* workflow
    .persist(() => ({ roborev: changes }))
    .pipe(
      Effect.map((settings) => settings.roborev),
      Effect.onExit((exit) =>
        Effect.sync(() => {
          if (Exit.isSuccess(exit)) {
            state.queue.confirmed = { ...exit.value };
            state.queue.confirmedGeneration = state.generation;
            if (state.queue.optimisticGeneration === state.generation) {
              store.setRoborevSettings({ ...exit.value });
            }
          } else if (state.queue.optimisticGeneration === state.generation) {
            store.setRoborevSettings({ ...state.queue.confirmed });
          }
          state.queue.pendingGenerations.delete(state.generation);
          if (state.queue.optimisticGeneration === state.generation) {
            state.queue.optimisticGeneration = 0;
          }
        }),
      ),
    );
}) satisfies (options: SaveRoborevSettingsOptions) => Effect.Effect<RoborevSettings, SettingsError, SettingsWorkflow>;
