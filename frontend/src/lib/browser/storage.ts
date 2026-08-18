import { Context, Effect, Layer, Option, Schema } from "effect";
import { InvalidExternalPayload } from "../api/effect-errors.js";

export class BrowserStorageError extends Schema.TaggedError<BrowserStorageError>()("BrowserStorageError", {
  operation: Schema.String,
  cause: Schema.Defect(),
}) {}

export interface BrowserStorage {
  readonly getString: (key: string) => Effect.Effect<Option.Option<string>, BrowserStorageError>;
  readonly setString: (key: string, value: string) => Effect.Effect<void, BrowserStorageError>;
  readonly remove: (key: string) => Effect.Effect<void, BrowserStorageError>;
  readonly get: <S extends Schema.Constraint>(
    key: string,
    schema: S,
  ) => Effect.Effect<Option.Option<S["Type"]>, BrowserStorageError | InvalidExternalPayload, S["DecodingServices"]>;
}

export class LocalStorage extends Context.Service<LocalStorage, BrowserStorage>()("kenn-forge/browser/LocalStorage") {}
export class SessionStorage extends Context.Service<SessionStorage, BrowserStorage>()(
  "kenn-forge/browser/SessionStorage",
) {}

const makeStorage = (name: string, storage: () => Storage): BrowserStorage => {
  const getString = (key: string) =>
    Effect.try({
      try: () => Option.fromNullishOr(storage().getItem(key)),
      catch: (cause) => BrowserStorageError.make({ operation: `${name}.getItem`, cause }),
    });
  return {
    getString,
    setString: (key, value) =>
      Effect.try({
        try: () => storage().setItem(key, value),
        catch: (cause) => BrowserStorageError.make({ operation: `${name}.setItem`, cause }),
      }),
    remove: (key) =>
      Effect.try({
        try: () => storage().removeItem(key),
        catch: (cause) => BrowserStorageError.make({ operation: `${name}.removeItem`, cause }),
      }),
    get: (key, schema) =>
      getString(key).pipe(
        Effect.flatMap(
          Option.match({
            onNone: () => Effect.succeed(Option.none()),
            onSome: (encoded) =>
              Effect.try({
                try: () => JSON.parse(encoded),
                catch: (cause) => InvalidExternalPayload.make({ operation: `${name}.parse`, cause }),
              }).pipe(
                Effect.flatMap((parsed) =>
                  Schema.decodeUnknownEffect(schema)(parsed).pipe(
                    Effect.mapError((cause) => InvalidExternalPayload.make({ operation: `${name}.decode`, cause })),
                  ),
                ),
                Effect.map(Option.some),
              ),
          }),
        ),
      ),
  };
};

export const LocalStorageLive = Layer.succeed(LocalStorage)(makeStorage("localStorage", () => globalThis.localStorage));
export const SessionStorageLive = Layer.succeed(SessionStorage)(
  makeStorage("sessionStorage", () => globalThis.sessionStorage),
);
