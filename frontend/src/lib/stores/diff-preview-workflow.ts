import { Cache, Context, Data, Effect, Fiber, FiberMap, Layer } from "effect";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import type { GeneratedApi } from "../api/generated-api.js";
import type { FilePreview } from "../api/types.js";

export class FilePreviewUnavailable extends Data.TaggedError("FilePreviewUnavailable")<{
  readonly message: string;
}> {}

export type FilePreviewReadError = ApiProblemError | FilePreviewUnavailable | TransientTransportError;

interface FilePreviewWorkflowShape {
  readonly read: (
    key: string,
    request: Effect.Effect<FilePreview, FilePreviewReadError, GeneratedApi>,
  ) => Effect.Effect<FilePreview, FilePreviewReadError>;
  readonly invalidateAll: Effect.Effect<void>;
  readonly clear: Effect.Effect<void>;
}

export class FilePreviewWorkflow extends Context.Service<FilePreviewWorkflow, FilePreviewWorkflowShape>()(
  "kenn-forge/FilePreviewWorkflow",
) {}

export const FilePreviewWorkflowLive = Layer.effect(FilePreviewWorkflow)(
  Effect.gen(function* () {
    const fibers = yield* FiberMap.make<string, FilePreview, FilePreviewReadError>();
    const pendingReads = new Map<string, Effect.Effect<FilePreview, FilePreviewReadError, GeneratedApi>>();
    const cache = yield* Cache.make({
      capacity: 64,
      timeToLive: "2 seconds",
      lookup: (key: string) =>
        Effect.suspend(() => {
          const request = pendingReads.get(key);
          if (request === undefined) return Effect.die(new Error(`missing file preview read for ${key}`));
          return FiberMap.run(fibers, key, request).pipe(Effect.flatMap(Fiber.join));
        }),
    });

    const read = Effect.fn("FilePreviewWorkflow.read")(function* (
      key: string,
      request: Effect.Effect<FilePreview, FilePreviewReadError, GeneratedApi>,
    ) {
      yield* Effect.sync(() => pendingReads.set(key, request));
      return yield* Cache.get(cache, key).pipe(Effect.ensuring(Effect.sync(() => pendingReads.delete(key))));
    });

    return {
      read,
      invalidateAll: Cache.invalidateAll(cache),
      clear: FiberMap.clear(fibers).pipe(
        Effect.andThen(Cache.invalidateAll(cache)),
        Effect.andThen(Effect.sync(() => pendingReads.clear())),
      ),
    };
  }),
);
