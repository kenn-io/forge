import { Effect, Option, Schema, Stream } from "effect";
import { responseByteStream } from "../../browser/streaming-fetch.js";
import { GeneratedApi } from "../generated-api.js";
import { KATA_DAEMON_HEADER } from "./daemons.js";
import { KataEventStreamOpened, type KataEventStreamEvent, KataTaskEventStreamFrame } from "./schemas.js";

interface FrameState {
  id?: number;
  event?: string;
  data: string[];
}

export { KataTaskEventStreamFrame } from "./schemas.js";

export class KataEventStreamError extends Error {
  readonly retryable: boolean;

  constructor(message: string, options: { retryable: boolean; cause?: unknown }) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = "KataEventStreamError";
    this.retryable = options.retryable;
  }
}

function isRetryableStreamSetupStatus(status: number): boolean {
  return status === 408 || status === 429 || status >= 500;
}

function parseData(data: string): unknown {
  try {
    return JSON.parse(data);
  } catch {
    return undefined;
  }
}

export class KataEventStreamParser {
  private buffer = "";
  private frame: FrameState = { data: [] };

  push(chunk: string): KataTaskEventStreamFrame[] {
    this.buffer += chunk;
    const messages: KataTaskEventStreamFrame[] = [];

    for (;;) {
      const newline = this.buffer.indexOf("\n");
      if (newline === -1) break;
      const rawLine = this.buffer.slice(0, newline);
      this.buffer = this.buffer.slice(newline + 1);
      const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
      const message = this.consumeLine(line);
      if (message) messages.push(message);
    }

    return messages;
  }

  flush(): KataTaskEventStreamFrame[] {
    const messages: KataTaskEventStreamFrame[] = [];
    if (this.buffer.length > 0) {
      const line = this.buffer.endsWith("\r") ? this.buffer.slice(0, -1) : this.buffer;
      this.buffer = "";
      const message = this.consumeLine(line);
      if (message) messages.push(message);
    }
    const message = this.commitFrame();
    if (message) messages.push(message);
    return messages;
  }

  private consumeLine(line: string): KataTaskEventStreamFrame | undefined {
    if (line === "") {
      return this.commitFrame();
    }
    if (line.startsWith(":")) {
      return undefined;
    }

    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    const rawValue = colon === -1 ? "" : line.slice(colon + 1);
    const value = rawValue.startsWith(" ") ? rawValue.slice(1) : rawValue;

    switch (field) {
      case "id": {
        const id = Number(value);
        if (Number.isSafeInteger(id) && id >= 0) this.frame.id = id;
        break;
      }
      case "event":
        this.frame.event = value;
        break;
      case "data":
        this.frame.data.push(value);
        break;
      default:
        break;
    }
    return undefined;
  }

  private commitFrame(): KataTaskEventStreamFrame | undefined {
    const frame = this.frame;
    this.frame = { data: [] };
    if (frame.id === undefined || frame.data.length === 0) return undefined;
    let kind: KataTaskEventStreamFrame["kind"];
    if (frame.event === "kata.tasks.reset") {
      kind = "reset";
    } else if (frame.event === "kata.tasks.invalidated") {
      kind = "invalidation";
    } else {
      return undefined;
    }

    const body = parseData(frame.data.join("\n"));
    if (typeof body !== "object" || body === null || Array.isArray(body)) return undefined;
    const decoded = Schema.decodeUnknownOption(KataTaskEventStreamFrame)({
      kind,
      ...body,
    });
    if (Option.isNone(decoded) || decoded.value.cursor !== frame.id) return undefined;
    return decoded.value;
  }
}

export interface KataEventStreamOptions {
  readonly daemonId?: string | undefined;
  readonly lastEventID?: number | undefined;
}

function streamHeaders(options: KataEventStreamOptions): Headers {
  const headers = new Headers({ Accept: "text/event-stream" });
  if (options.daemonId) headers.set(KATA_DAEMON_HEADER, options.daemonId);
  if (options.lastEventID && options.lastEventID > 0) headers.set("Last-Event-ID", String(options.lastEventID));
  return headers;
}

function releaseResponseBody(response: Response): Effect.Effect<void> {
  const body = response.body;
  if (body === null) return Effect.void;
  return Effect.tryPromise({
    try: () => body.cancel(),
    catch: (cause) => new Error("Could not cancel Kata event response body", { cause }),
  }).pipe(Effect.ignore);
}

function parsedFrames(response: Response): Stream.Stream<KataTaskEventStreamFrame, KataEventStreamError> {
  return Stream.suspend(() => {
    const parser = new KataEventStreamParser();
    const frames = responseByteStream(response, "read Kata event stream").pipe(
      Stream.mapError((cause) => new KataEventStreamError("Live updates disconnected", { retryable: true, cause })),
      Stream.decodeText(),
      Stream.flatMap((chunk) => Stream.fromIterable(parser.push(chunk))),
    );
    return Stream.concat(frames, Stream.fromIterable(parser.flush()));
  });
}

export function kataEventStream(
  options: KataEventStreamOptions,
): Stream.Stream<KataEventStreamEvent, KataEventStreamError, GeneratedApi> {
  return Stream.unwrap(
    Effect.gen(function* () {
      const { client } = yield* GeneratedApi;
      const result = yield* Effect.acquireRelease(
        Effect.tryPromise({
          try: (signal) =>
            client.GET("/kata/tasks/events", {
              headers: streamHeaders(options),
              parseAs: "stream",
              signal,
            }),
          catch: (cause) => new KataEventStreamError("Live updates disconnected", { retryable: true, cause }),
        }),
        ({ response }) => releaseResponseBody(response),
        { interruptible: true },
      );
      if (!result.response.ok) {
        return Stream.fail(
          new KataEventStreamError(`Kata event stream failed: HTTP ${result.response.status}`, {
            retryable: isRetryableStreamSetupStatus(result.response.status),
          }),
        );
      }
      if (!result.data) {
        return Stream.fail(new KataEventStreamError("Kata event stream response has no body", { retryable: false }));
      }
      return Stream.concat(
        Stream.succeed<KataEventStreamEvent>(KataEventStreamOpened.make({ opened: true })),
        Stream.concat(
          parsedFrames(result.response),
          Stream.fail(new KataEventStreamError("Live updates disconnected", { retryable: true })),
        ),
      );
    }),
  );
}
