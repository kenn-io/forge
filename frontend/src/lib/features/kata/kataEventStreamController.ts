import { Effect } from "effect";
import type { GeneratedApi } from "../../api/generated-api.js";
import type { KataTaskEventStreamFrame } from "../../api/kata/schemas.js";
import { KataWorkflow, type KataWorkflowService } from "./kata-workflow.js";

interface KataEventStreamControllerOptions {
  owner: string;
  getDaemonId: () => string | undefined;
  getLastEventID: () => number;
  onOpen: () => void;
  onMessage: (
    message: KataTaskEventStreamFrame,
    workflow: KataWorkflowService,
  ) => Effect.Effect<boolean, never, GeneratedApi>;
  onReset?: (() => void) | undefined;
  onError: (message: string) => void;
}

export interface KataEventStreamController {
  readonly start: Effect.Effect<void, never, GeneratedApi | KataWorkflow>;
  readonly stop: Effect.Effect<void, never, KataWorkflow>;
}

export function createKataEventStreamController(options: KataEventStreamControllerOptions): KataEventStreamController {
  return {
    start: Effect.gen(function* () {
      const workflow = yield* KataWorkflow;
      yield* workflow.connectEvents({
        owner: options.owner,
        daemonId: options.getDaemonId(),
        checkpoint: options.getLastEventID(),
        onOpen: Effect.sync(options.onOpen),
        onFrame: (message) =>
          options.onMessage(message, workflow).pipe(
            Effect.tap((accepted) =>
              accepted && message.kind === "reset" ? Effect.sync(() => options.onReset?.()) : Effect.void,
            ),
            Effect.andThen(workflow.updateEventSource(options.owner, options.getDaemonId(), options.getLastEventID())),
            Effect.asVoid,
          ),
        onError: (error) => Effect.sync(() => options.onError(error.message)),
      });
    }),
    stop: Effect.gen(function* () {
      const workflow = yield* KataWorkflow;
      yield* workflow.disconnectEvents(options.owner);
    }),
  };
}
