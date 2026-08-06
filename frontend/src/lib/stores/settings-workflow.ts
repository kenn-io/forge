import { Context, Effect, Layer } from "effect";
import type { components } from "../api/generated/schema.js";
import { GeneratedApi } from "../api/generated-api.js";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { StartupWorkflow } from "../app/startup-workflow.js";
import { CommandQueueClosed, makeOrderedCommandQueue } from "../effect/ordered-command-queue.js";

export type UpdateSettingsRequest = components["schemas"]["UpdateSettingsRequest"];
export type SettingsSnapshot = components["schemas"]["SettingsResponse"];
export interface SettingsCommand {
  readonly request: Effect.Effect<UpdateSettingsRequest>;
}
export type SettingsError = ApiProblemError | TransientTransportError | CommandQueueClosed;

export class SettingsWorkflow extends Context.Service<
  SettingsWorkflow,
  {
    readonly enqueue: (command: SettingsCommand) => Effect.Effect<SettingsSnapshot, SettingsError>;
  }
>()("kenn-forge/SettingsWorkflow") {}

export const SettingsWorkflowLive = Layer.effect(SettingsWorkflow)(
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const startup = yield* StartupWorkflow;
    const persist = Effect.fn("SettingsWorkflow.persist")(function* (command: SettingsCommand) {
      const request = yield* command.request;
      const settings = yield* api.execute("PUT /settings", () => api.client.PUT("/settings", { body: request }));
      yield* startup.invalidate;
      return settings;
    });
    const queue = yield* makeOrderedCommandQueue("settings writes", persist);
    return {
      enqueue: queue.submit,
    };
  }),
);

export function settingsErrorMessage(failure: SettingsError): string {
  switch (failure._tag) {
    case "ApiProblemError":
      return failure.problem.detail ?? failure.problem.title ?? "Failed to save settings";
    case "CommandQueueClosed":
      return "Settings are no longer available";
    case "TransientTransportError":
      return "Could not reach Kenn Forge";
  }
}
