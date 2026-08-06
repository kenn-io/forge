import { Clock, Context, Effect, Layer, Queue, Ref, Schedule, Schema, Stream } from "effect";
import type { Scope } from "effect/Scope";

const autoContinueMilliseconds = 2_500;
const countdownInterval = "250 millis";

export class SetupFlow extends Schema.Class<SetupFlow>("SetupFlow")({
  action: Schema.String,
  manifest: Schema.String,
  name: Schema.String,
  host: Schema.String,
}) {}

export class SetupManifestPermissions extends Schema.Class<SetupManifestPermissions>("SetupManifestPermissions")({
  default_permissions: Schema.optionalKey(Schema.Record(Schema.String, Schema.String)),
}) {}

export type SetupPermission = readonly [scope: string, level: string];
export interface SetupFlowView extends Pick<SetupFlow, "action" | "manifest" | "name" | "host"> {
  readonly permissions: ReadonlyArray<SetupPermission>;
}

export class SetupFlowFetchError extends Schema.TaggedErrorClass<SetupFlowFetchError>()("SetupFlowFetchError", {
  reason: Schema.String,
  cause: Schema.Defect(),
}) {}

export class SetupInvalidPayload extends Schema.TaggedErrorClass<SetupInvalidPayload>()("SetupInvalidPayload", {
  reason: Schema.String,
  cause: Schema.Defect(),
}) {}

export class SetupFormSubmitError extends Schema.TaggedErrorClass<SetupFormSubmitError>()("SetupFormSubmitError", {
  reason: Schema.String,
  cause: Schema.Defect(),
}) {}

export type SetupFlowError = SetupFlowFetchError | SetupInvalidPayload | SetupFormSubmitError;

export class SetupFlowFetch extends Context.Service<
  SetupFlowFetch,
  {
    readonly load: Effect.Effect<unknown, SetupFlowFetchError>;
  }
>()("kenn-forge/github-app/SetupFlowFetch") {}

export class SetupFormSubmit extends Context.Service<
  SetupFormSubmit,
  {
    readonly submit: (flow: SetupFlow) => Effect.Effect<void, SetupFormSubmitError>;
  }
>()("kenn-forge/github-app/SetupFormSubmit") {}

export type SetupEnvironment = SetupFlowFetch | SetupFormSubmit;

const SetupFlowFetchLive = Layer.succeed(SetupFlowFetch)({
  load: Effect.tryPromise({
    try: (signal) => globalThis.fetch("./flow.json", { signal }),
    catch: (cause) =>
      SetupFlowFetchError.make({
        reason: "Could not load flow.json",
        cause,
      }),
  }).pipe(
    Effect.flatMap((response) => {
      if (!response.ok) {
        return Effect.fail(
          SetupFlowFetchError.make({
            reason: `flow.json returned ${response.status}`,
            cause: new Error(response.statusText),
          }),
        );
      }
      return Effect.tryPromise({
        try: (): Promise<unknown> => response.json(),
        catch: (cause) =>
          SetupFlowFetchError.make({
            reason: "flow.json did not contain JSON",
            cause,
          }),
      });
    }),
  ),
});

const SetupFormSubmitLive = Layer.succeed(SetupFormSubmit)({
  submit: (flow) =>
    Effect.try({
      try: () => {
        const form = document.createElement("form");
        form.method = "post";
        form.action = flow.action;
        const input = document.createElement("input");
        input.type = "hidden";
        input.name = "manifest";
        input.value = flow.manifest;
        form.appendChild(input);
        document.body.appendChild(form);
        form.submit();
      },
      catch: (cause) =>
        SetupFormSubmitError.make({
          reason: "Could not continue to GitHub",
          cause,
        }),
    }),
});

export const SetupEnvironmentLive = Layer.mergeAll(SetupFlowFetchLive, SetupFormSubmitLive);

export interface SetupControllerOptions {
  readonly onFlow?: (flow: SetupFlowView) => void;
  readonly onSecondsLeft?: (seconds: number) => void;
  readonly onFailure?: (failure: SetupFlowError) => void;
  readonly onSubmit?: () => void;
}

export interface SetupController {
  readonly program: Effect.Effect<void, SetupFlowError, SetupEnvironment | Scope>;
  readonly continue: () => void;
}

type SetupCommand = "submit";

const notify = <A>(callback: ((value: A) => void) | undefined, value: A): Effect.Effect<void> =>
  callback === undefined ? Effect.void : Effect.sync(() => callback(value));

const notifyVoid = (callback: (() => void) | undefined): Effect.Effect<void> =>
  callback === undefined ? Effect.void : Effect.sync(callback);

const decodeSetupFlow = Effect.fn("SetupFlow.decode")(function* (payload: unknown) {
  const flow = yield* Schema.decodeUnknownEffect(SetupFlow)(payload).pipe(
    Effect.mapError((cause) =>
      SetupInvalidPayload.make({
        reason: "flow.json has an invalid shape",
        cause,
      }),
    ),
  );
  const encodedManifest = yield* Effect.try({
    try: (): unknown => JSON.parse(flow.manifest),
    catch: (cause) =>
      SetupInvalidPayload.make({
        reason: "The app manifest is not valid JSON",
        cause,
      }),
  });
  const manifest = yield* Schema.decodeUnknownEffect(SetupManifestPermissions)(encodedManifest).pipe(
    Effect.mapError((cause) =>
      SetupInvalidPayload.make({
        reason: "The app manifest has invalid repository permissions",
        cause,
      }),
    ),
  );
  const permissions = Object.entries(manifest.default_permissions ?? {}).sort(([left], [right]) =>
    left.localeCompare(right),
  );
  return {
    action: flow.action,
    manifest: flow.manifest,
    name: flow.name,
    host: flow.host,
    permissions,
  };
});

export function makeSetupController(options: SetupControllerOptions = {}): SetupController {
  let publish: (command: SetupCommand) => void = () => undefined;

  const program = Effect.gen(function* () {
    const commands = yield* Queue.bounded<SetupCommand>(16);
    yield* Effect.addFinalizer(() => Queue.shutdown(commands));
    yield* Effect.sync(() => {
      publish = (command) => {
        Queue.offerUnsafe(commands, command);
      };
    });
    yield* Effect.addFinalizer(() =>
      Effect.sync(() => {
        publish = () => undefined;
      }),
    );

    const fetchFlow = yield* SetupFlowFetch;
    const form = yield* SetupFormSubmit;
    const submitted = yield* Ref.make(false);
    const flow = yield* fetchFlow.load.pipe(Effect.flatMap(decodeSetupFlow));
    yield* notify(options.onFlow, flow);

    const submitOnce = Effect.gen(function* () {
      const shouldSubmit = yield* Ref.modify(submitted, (alreadySubmitted): readonly [boolean, boolean] => [
        !alreadySubmitted,
        true,
      ]);
      if (!shouldSubmit) return;
      yield* form.submit(flow).pipe(Effect.tapError(() => Ref.set(submitted, false)));
      yield* notifyVoid(options.onSubmit);
    });

    const consumeCommands = Effect.forever(
      Queue.take(commands).pipe(
        Effect.andThen(submitOnce),
        Effect.catchTag("SetupFormSubmitError", (failure) => notify(options.onFailure, failure)),
      ),
    );
    const countdown = Effect.gen(function* () {
      const startedAt = yield* Clock.currentTimeMillis;
      yield* notify(options.onSecondsLeft, Math.ceil(autoContinueMilliseconds / 1_000));
      yield* Stream.fromSchedule(Schedule.spaced(countdownInterval)).pipe(
        Stream.runForEachWhile(() =>
          Effect.gen(function* () {
            const now = yield* Clock.currentTimeMillis;
            const remaining = autoContinueMilliseconds - (now - startedAt);
            if (remaining <= 0) {
              yield* Queue.offer(commands, "submit");
              return false;
            }
            yield* notify(options.onSecondsLeft, Math.max(1, Math.ceil(remaining / 1_000)));
            return true;
          }),
        ),
      );
      return yield* Effect.never;
    });

    yield* Effect.all([consumeCommands, countdown], { concurrency: "unbounded", discard: true });
  }).pipe(
    Effect.tapError((failure) => notify(options.onFailure, failure)),
    Effect.withSpan("GitHubAppSetup.run"),
  );

  return {
    program,
    continue: () => publish("submit"),
  };
}
