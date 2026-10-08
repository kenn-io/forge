import { Effect } from "effect";
import type { ACPSettings } from "../api/types.js";
import type { SettingsStore } from "./settings.svelte.js";
import { SettingsWorkflow } from "./settings-workflow.js";

type ACPSettingsStore = Pick<SettingsStore, "setACPSettings">;
const mutations = new WeakMap<ACPSettingsStore, { revision: number; pending: number; hydration: number }>();

function stateFor(store: ACPSettingsStore) {
  let state = mutations.get(store);
  if (!state) {
    state = { revision: 0, pending: 0, hydration: 0 };
    mutations.set(store, state);
  }
  return state;
}

export function beginACPSettingsHydration(store: ACPSettingsStore) {
  const state = stateFor(store);
  return { store, revision: state.revision, hydration: ++state.hydration };
}

export type ACPSettingsHydration = ReturnType<typeof beginACPSettingsHydration>;

export function hydrateACPSettings(read: ACPSettingsHydration, settings: ACPSettings): void {
  const state = stateFor(read.store);
  if (state.pending || state.revision !== read.revision || state.hydration !== read.hydration) return;
  read.store.setACPSettings(settings);
}

export const saveACPSettings = Effect.fn("ACPSettings.save")(function* (
  store: ACPSettingsStore,
  changes: Partial<ACPSettings>,
) {
  const workflow = yield* SettingsWorkflow;
  return yield* Effect.acquireUseRelease(
    Effect.sync(() => {
      const state = stateFor(store);
      state.revision++;
      state.pending++;
      return state;
    }),
    () =>
      workflow
        .persist(() => ({ acp: changes }))
        .pipe(Effect.tap((settings) => Effect.sync(() => store.setACPSettings(settings.acp)))),
    (state) =>
      Effect.sync(() => {
        state.pending--;
        state.revision++;
      }),
  );
});
