import { Effect, Schema } from "effect";

class ClipboardWriteError extends Schema.TaggedErrorClass<ClipboardWriteError>()("ClipboardWriteError", {
  cause: Schema.Defect(),
}) {}

export interface TransientClipboardFeedbackOptions {
  readonly text: string;
  readonly write: (text: string) => Promise<boolean>;
  readonly isActive: () => boolean;
  readonly onCopied: () => void;
  readonly onExpired: () => void;
}

export const transientClipboardFeedback = Effect.fn("Clipboard.transientFeedback")(function* (
  options: TransientClipboardFeedbackOptions,
) {
  let published = false;
  yield* Effect.tryPromise({
    try: () => options.write(options.text),
    catch: (cause) => ClipboardWriteError.make({ cause }),
  }).pipe(
    Effect.catchTag("ClipboardWriteError", () => Effect.succeed(false)),
    Effect.flatMap((copied) => {
      if (!copied || !options.isActive()) return Effect.void;
      return Effect.sync(() => {
        published = true;
        options.onCopied();
      }).pipe(Effect.andThen(Effect.sleep("1500 millis")));
    }),
    Effect.ensuring(
      Effect.sync(() => {
        if (!published || !options.isActive()) return;
        published = false;
        options.onExpired();
      }),
    ),
  );
});
