import { Effect, Option } from "effect";
import type { AppRuntime, AppServices } from "../app/runtime.js";
import { StartupWorkflow } from "../app/startup-workflow.js";
import type { StoreInstances } from "../types.js";
import { applySettingsHydration } from "../stores/settings-hydration.js";
import { beginTerminalSettingsHydration } from "../stores/terminal-settings-persistence.js";
import { beginWorkspaceSettingsHydration } from "../stores/workspace-settings-persistence.js";

export interface AppStartupDeps {
  readonly stores: StoreInstances;
  readonly afterBackendReady?: Effect.Effect<void, never, AppServices>;
  readonly onReady: () => void;
  readonly beforeInitialLoad?: () => void;
}

export const appStartupProgram = Effect.fn("AppStartup.run")(function* (deps: AppStartupDeps) {
  const startup = yield* StartupWorkflow;
  const terminalHydration = yield* Effect.sync(() => beginTerminalSettingsHydration(deps.stores.settings));
  const workspaceHydration = yield* Effect.sync(() => beginWorkspaceSettingsHydration(deps.stores.settings));
  const snapshot = yield* startup.start.pipe(
    Effect.match({
      onFailure: (failure) => {
        console.warn("Failed to load settings, using defaults:", failure);
        return Option.none();
      },
      onSuccess: Option.some,
    }),
  );

  if (Option.isSome(snapshot)) {
    yield* Effect.sync(() => {
      applySettingsHydration(
        {
          settings: deps.stores.settings,
          activity: deps.stores.activity,
          issues: deps.stores.issues,
        },
        snapshot.value,
        terminalHydration,
        workspaceHydration,
      );
    });
  }

  if (deps.afterBackendReady) {
    yield* Effect.forkChild(deps.afterBackendReady);
  }
  yield* Effect.sync(() => deps.beforeInitialLoad?.());
  yield* Effect.sync(deps.onReady);
  yield* Effect.sync(() => {
    deps.stores.pulls.loadPulls();
    deps.stores.issues.loadIssues();
  });
  yield* Effect.forkChild(deps.stores.sync.pollingEffect, { startImmediately: true });
  yield* Effect.forkChild(deps.stores.events.streamEffect, { startImmediately: true });
  return yield* Effect.never;
});

export function runAppStartup(runtime: AppRuntime, deps: AppStartupDeps): () => void {
  const execution = runtime.runCommand(appStartupProgram(deps), {
    operation: "start application shell",
    safeContext: {},
    onFailure: (failure) => {
      console.warn("Application startup stopped unexpectedly:", failure);
    },
  });
  return execution.interrupt;
}
