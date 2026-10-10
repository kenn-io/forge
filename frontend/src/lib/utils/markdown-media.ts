import { Effect } from "effect";
import { orvalRequest } from "../api/runtime.js";
import type { MarkdownMediaOutcome } from "./markdown.js";

export interface MarkdownMediaCandidate {
  readonly source: string;
  // API-relative media route for the source, as the generated URL helpers
  // build it.
  readonly mediaURL: string;
}

const PROBE_CONCURRENCY = 4;

// Settled answers that describe the asset itself. Failed probes say nothing
// about the asset type, so they are never stored and the next render asks
// again.
const settledOutcomes = new Map<string, Exclude<MarkdownMediaOutcome, "unknown">>();

// The answers already known for candidates, so a render can show a confirmed
// video as a player before any probe runs.
export function settledMarkdownMediaOutcomes(
  candidates: ReadonlyArray<MarkdownMediaCandidate>,
): ReadonlyMap<string, MarkdownMediaOutcome> {
  return new Map(
    candidates.flatMap(({ source, mediaURL }) => {
      const settled = settledOutcomes.get(mediaURL);
      return settled ? [[source, settled] as const] : [];
    }),
  );
}

// Asks the media route for one byte. The request stays interruptible so a
// torn-down render aborts it. A host that ignores Range answers with the
// whole file, so the body is cancelled instead of read.
export const probeMarkdownMedia = (mediaURL: string): Effect.Effect<MarkdownMediaOutcome> =>
  Effect.tryPromise({
    try: (signal) => orvalRequest(mediaURL, { signal, headers: { Range: "bytes=0-0" } }),
    catch: (cause) => cause,
  }).pipe(
    Effect.flatMap((response) => Effect.sync(() => probeOutcome(response)).pipe(Effect.ensuring(cancelBody(response)))),
    Effect.orElseSucceed((): MarkdownMediaOutcome => "unknown"),
  );

const cancelBody = (response: Response): Effect.Effect<void> =>
  Effect.tryPromise({ try: () => response.body?.cancel() ?? Promise.resolve(), catch: (cause) => cause }).pipe(
    Effect.ignore,
  );

function probeOutcome(response: Response): MarkdownMediaOutcome {
  const contentType = response.headers.get("Content-Type")?.split(";")[0]?.trim().toLowerCase() ?? "";
  if (response.ok && contentType.startsWith("video/")) return "video";
  // Only the not-a-video answer proves the asset type.
  if (response.status === 415) return "link";
  return "unknown";
}

export const resolveMarkdownMediaOutcomes = (
  candidates: ReadonlyArray<MarkdownMediaCandidate>,
): Effect.Effect<ReadonlyMap<string, MarkdownMediaOutcome>> =>
  Effect.forEach(
    candidates,
    ({ source, mediaURL }) => {
      const settled = settledOutcomes.get(mediaURL);
      if (settled) return Effect.succeed([source, settled] as const);
      return probeMarkdownMedia(mediaURL).pipe(
        Effect.tap((outcome) =>
          Effect.sync(() => {
            if (outcome !== "unknown") settledOutcomes.set(mediaURL, outcome);
          }),
        ),
        Effect.map((outcome) => [source, outcome] as const),
      );
    },
    { concurrency: PROBE_CONCURRENCY },
  ).pipe(Effect.map((entries) => new Map(entries)));
