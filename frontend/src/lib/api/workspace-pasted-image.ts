import { Data, Effect, Schema } from "effect";

import { csrfFetch, type FetchFn } from "./csrf.js";
import { isProblem } from "./problems.js";
import { configuredAPIBaseURL } from "./runtime-base.js";
import { tracedFetch } from "./runtime.js";

export const MAX_PASTED_IMAGE_BYTES = 10 * 1024 * 1024;
export const MAX_PASTED_IMAGES_PER_PASTE = 4;

export interface WorkspaceImageUploadTarget {
  readonly workspaceId: string;
  readonly hostKey?: string | undefined;
}

export class WorkspacePastedImageUploadError extends Data.TaggedError("WorkspacePastedImageUploadError")<{
  readonly message: string;
  readonly cause?: unknown;
}> {}

const PastedImagePath = Schema.String.pipe(
  Schema.check(Schema.isPattern(/^\.kenn-forge\/pasted-images\/paste-[0-9a-f]{32}\.(png|jpg|gif|webp)$/)),
);
const PastedImageOutput = Schema.Struct({ path: PastedImagePath });

function pastedImageUploadPath(target: WorkspaceImageUploadTarget): string {
  const hostPrefix = target.hostKey ? `/fleet/hosts/${encodeURIComponent(target.hostKey)}` : "";
  return `${configuredAPIBaseURL()}${hostPrefix}/workspaces/${encodeURIComponent(target.workspaceId)}/pasted-images`;
}

function bytesToBase64(bytes: Uint8Array): string {
  const chunkSize = 0x8000;
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize));
  }
  return btoa(binary);
}

export function makeWorkspacePastedImageUploader(inner: FetchFn = fetch) {
  const request = csrfFetch(tracedFetch(inner));
  return Effect.fn("WorkspacePastedImage.upload")(function* (target: WorkspaceImageUploadTarget, file: File) {
    const buffer = yield* Effect.tryPromise({
      try: () => file.arrayBuffer(),
      catch: (cause) =>
        new WorkspacePastedImageUploadError({
          message: "Could not read the pasted image.",
          cause,
        }),
    });
    const response = yield* Effect.tryPromise({
      try: (signal) =>
        request(pastedImageUploadPath(target), {
          method: "POST",
          body: JSON.stringify({ data: bytesToBase64(new Uint8Array(buffer)) }),
          signal,
        }),
      catch: (cause) =>
        new WorkspacePastedImageUploadError({
          message: "Could not upload the pasted image.",
          cause,
        }),
    });
    const payload = yield* Effect.tryPromise({
      try: () => response.json() as Promise<unknown>,
      catch: (cause) =>
        new WorkspacePastedImageUploadError({
          message: `The pasted image upload returned invalid JSON (${response.status}).`,
          cause,
        }),
    });
    if (!response.ok) {
      return yield* Effect.fail(
        new WorkspacePastedImageUploadError({
          message:
            isProblem(payload) && payload.detail
              ? payload.detail
              : `Could not upload the pasted image (${response.status}).`,
        }),
      );
    }
    return yield* Schema.decodeUnknownEffect(PastedImageOutput)(payload).pipe(
      Effect.mapError(
        (cause) =>
          new WorkspacePastedImageUploadError({
            message: "The pasted image upload returned an invalid path.",
            cause,
          }),
      ),
      Effect.map((decoded) => decoded.path),
    );
  });
}

export function uploadWorkspacePastedImage(
  target: WorkspaceImageUploadTarget,
  file: File,
): Effect.Effect<string, WorkspacePastedImageUploadError> {
  return makeWorkspacePastedImageUploader()(target, file);
}
