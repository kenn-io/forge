import { Effect, Stream } from "effect";
import { describe, expect, test, vi } from "vite-plus/test";

import { makeGeneratedApiLayer } from "../generated-api.js";
import { createRuntimeClient } from "../runtime.js";
import { KATA_DAEMON_HEADER } from "./daemons.js";
import {
  kataEventStream,
  KataEventStreamError,
  KataEventStreamParser,
  type KataEventStreamOptions,
  type KataTaskEventStreamFrame,
} from "./eventStream.js";
import type { KataEventStreamEvent } from "./schemas.js";

function compactFrame(
  id: number,
  event: "kata.tasks.reset" | "kata.tasks.invalidated",
  overrides: Record<string, unknown> = {},
): string {
  return [
    `id: ${id}`,
    `event: ${event}`,
    `data: ${JSON.stringify({
      server_instance_id: "server-a",
      daemon_id: "work",
      epoch: 3,
      cursor: id,
      ...overrides,
    })}`,
    "",
    "",
  ].join("\n");
}

function streamFromText(text: string): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(text));
      controller.close();
    },
  });
}

function runEventStream(
  fetchImpl: typeof fetch,
  options: KataEventStreamOptions = {},
  onEvent: (event: KataEventStreamEvent) => void = () => {},
): Promise<void> {
  return Effect.runPromise(
    kataEventStream(options).pipe(
      Stream.tap((event) => Effect.sync(() => onEvent(event))),
      Stream.runDrain,
      Effect.provide(makeGeneratedApiLayer(createRuntimeClient(fetchImpl))),
    ),
  );
}

describe("KataEventStreamParser", () => {
  test("parses compact reset frames", () => {
    const parser = new KataEventStreamParser();

    expect(parser.push(compactFrame(41, "kata.tasks.reset"))).toEqual([
      {
        kind: "reset",
        server_instance_id: "server-a",
        daemon_id: "work",
        epoch: 3,
        cursor: 41,
      },
    ] satisfies KataTaskEventStreamFrame[]);
  });

  test("parses compact invalidation frames across partial CRLF chunks", () => {
    const parser = new KataEventStreamParser();
    const frame = compactFrame(42, "kata.tasks.invalidated").replaceAll("\n", "\r\n");

    expect(parser.push(frame.slice(0, 35))).toEqual([]);
    expect(parser.push(frame.slice(35))).toEqual([
      {
        kind: "invalidation",
        server_instance_id: "server-a",
        daemon_id: "work",
        epoch: 3,
        cursor: 42,
      },
    ] satisfies KataTaskEventStreamFrame[]);
  });

  test("rejects frames whose JSON cursor differs from the SSE id", () => {
    const parser = new KataEventStreamParser();

    expect(parser.push(compactFrame(42, "kata.tasks.invalidated", { cursor: 41 }))).toEqual([]);
  });

  test("ignores unknown event names and raw Kata event payloads", () => {
    const parser = new KataEventStreamParser();
    const rawFrame = [
      "id: 43",
      "event: issue.updated",
      'data: {"event_id":43,"type":"issue.updated","payload":{"secret":"raw-upstream"}}',
      "",
      "",
    ].join("\n");
    const rawPayloadUnderForgeName = [
      "id: 44",
      "event: kata.tasks.invalidated",
      'data: {"event_id":44,"type":"issue.updated","payload":{"secret":"raw-upstream"}}',
      "",
      "",
    ].join("\n");

    expect(parser.push(rawFrame)).toEqual([]);
    expect(parser.push(rawPayloadUnderForgeName)).toEqual([]);
  });

  test("exposes no upstream fields from an accepted compact frame", () => {
    const parser = new KataEventStreamParser();

    const [message] = parser.push(
      compactFrame(45, "kata.tasks.invalidated", {
        event_id: 900,
        after_id: 899,
        payload: { secret: "raw-upstream" },
        type: "issue.updated",
      }),
    );

    expect(message).toEqual({
      kind: "invalidation",
      server_instance_id: "server-a",
      daemon_id: "work",
      epoch: 3,
      cursor: 45,
    });
  });

  test("ignores invalid JSON and incomplete compact identities", () => {
    const parser = new KataEventStreamParser();

    expect(parser.push("event: kata.tasks.reset\ndata: not-json\n\n")).toEqual([]);
    expect(parser.push('id: 46\nevent: kata.tasks.reset\ndata: {"cursor":46}\n\n')).toEqual([]);
  });
});

describe("kataEventStream", () => {
  test("opens the generated stream with daemon selection and snapshot cursor replay", async () => {
    let request = new Request(window.location.origin);
    const events: KataEventStreamEvent[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      request = input instanceof Request ? new Request(input, init) : new Request(input, init);
      return new Response(streamFromText(compactFrame(52, "kata.tasks.invalidated")), {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      });
    });

    await expect(
      runEventStream(fetchImpl, { daemonId: "work", lastEventID: 51 }, (event) => events.push(event)),
    ).rejects.toMatchObject({
      name: "KataEventStreamError",
      message: "Live updates disconnected",
      retryable: true,
    } satisfies Partial<KataEventStreamError>);

    expect(new URL(request.url).pathname).toBe("/api/v1/kata/tasks/events");
    expect(request.headers.get("Accept")).toBe("text/event-stream");
    expect(request.headers.get(KATA_DAEMON_HEADER)).toBe("work");
    expect(request.headers.get("Last-Event-ID")).toBe("51");
    expect(events).toEqual([
      { opened: true },
      {
        kind: "invalidation",
        server_instance_id: "server-a",
        daemon_id: "work",
        epoch: 3,
        cursor: 52,
      },
    ]);
  });

  test.each([
    [408, true],
    [429, true],
    [502, true],
    [400, false],
    [404, false],
  ])("classifies HTTP %i stream setup failures", async (status, retryable) => {
    const fetchImpl = vi.fn<typeof fetch>(async () => new Response("setup failed", { status }));

    await expect(runEventStream(fetchImpl)).rejects.toMatchObject({
      name: "KataEventStreamError",
      retryable,
    } satisfies Partial<KataEventStreamError>);
  });

  test("treats a successful response without a body as nonretryable", async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => new Response(null, { status: 200 }));

    await expect(runEventStream(fetchImpl)).rejects.toMatchObject({
      name: "KataEventStreamError",
      message: "Kata event stream response has no body",
      retryable: false,
    } satisfies Partial<KataEventStreamError>);
  });

  test("omits an empty Last-Event-ID", async () => {
    let requestHeaders = new Headers();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const request = input instanceof Request ? new Request(input, init) : new Request(input, init);
      requestHeaders = request.headers;
      return new Response(streamFromText(": connected\n\n"), { status: 200 });
    });

    await expect(runEventStream(fetchImpl, { lastEventID: 0 })).rejects.toThrow("Live updates disconnected");

    expect(requestHeaders.has("Last-Event-ID")).toBe(false);
  });
});
