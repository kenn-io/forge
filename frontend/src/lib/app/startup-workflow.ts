import { Cache, Context, Duration, Effect, Layer } from "effect";
import type { TimeoutError } from "effect/Cause";
import type { components } from "../api/generated/schema.js";
import { GeneratedApi } from "../api/generated-api.js";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { waitUntilBackendReady } from "../utils/backendReadiness.js";

export { waitUntilBackendReady } from "../utils/backendReadiness.js";

export type StartupSnapshot = components["schemas"]["SettingsResponse"];
export type StartupError = ApiProblemError | TransientTransportError | TimeoutError;

const STARTUP_CACHE_KEY = "settings";

const loadStartupSettings = Effect.fn("StartupWorkflow.loadSettings")(function* () {
  const api = yield* GeneratedApi;
  return yield* Effect.scoped(
    Effect.gen(function* () {
      const controller = yield* Effect.acquireRelease(
        Effect.sync(() => new AbortController()),
        (owned) => Effect.sync(() => owned.abort()),
      );
      return yield* api.execute("GET /settings", () => api.client.GET("/settings", { signal: controller.signal }));
    }),
  );
});

export class StartupWorkflow extends Context.Service<
  StartupWorkflow,
  {
    readonly start: Effect.Effect<StartupSnapshot, StartupError>;
    readonly invalidate: Effect.Effect<void>;
  }
>()("kenn-forge/StartupWorkflow") {}

export const StartupWorkflowLive = Layer.effect(StartupWorkflow)(
  Effect.gen(function* () {
    const cache = yield* Cache.make({
      capacity: 1,
      lookup: () => waitUntilBackendReady.pipe(Effect.andThen(loadStartupSettings()), Effect.timeout("8 seconds")),
      // Startup callers share only the active readiness/settings request.
      // Retaining a completed snapshot would require every settings and
      // repository writer to participate in one invalidation protocol.
      timeToLive: Duration.zero,
    });
    return {
      start: Cache.get(cache, STARTUP_CACHE_KEY),
      invalidate: Cache.invalidate(cache, STARTUP_CACHE_KEY),
    };
  }),
);

export function startupErrorMessage(failure: StartupError): string {
  switch (failure._tag) {
    case "ApiProblemError":
      return failure.problem.detail ?? failure.problem.title ?? "Could not load settings";
    case "TimeoutError":
      return "Timed out loading settings";
    case "TransientTransportError":
      return "Could not reach Kenn Forge";
  }
}
