import { describe, expect, test, vi } from "vite-plus/test";

import { KATA_DAEMON_HEADER } from "./daemons.js";
import { KataEventStreamError, KataEventStreamParser, readKataEventStream } from "./eventStream.js";

const origin = "instance-1";

function eventFrame(id: number, type: string, data: Record<string, unknown>): string {
  return `id: ${id}\nevent: ${type}\ndata: ${JSON.stringify(data)}\n\n`;
}

function streamFromText(text: string): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(text));
      controller.close();
    },
  });
}

describe("KataEventStreamParser", () => {
  test("parses event frames with the stream cursor", () => {
    const parser = new KataEventStreamParser();

    expect(parser.push(": connected\n\n")).toEqual([]);
    const messages = parser.push(
      eventFrame(7, "issue.created", {
        event_id: 7,
        event_uid: "event-7",
        origin_instance_uid: origin,
        type: "issue.created",
        project_id: 2,
        project_uid: "project-inbox",
        project_name: "Inbox",
        issue_id: 9,
        issue_uid: "issue-created",
        issue_short_id: "created",
        actor: "middleman-test",
        created_at: "2026-05-15T12:00:00Z",
      }),
    );

    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatchObject({
      kind: "event",
      lastEventID: 7,
      event: {
        event_id: 7,
        origin_instance_uid: origin,
        type: "issue.created",
        issue_uid: "issue-created",
      },
    });
  });

  test("buffers partial chunks until a complete frame arrives", () => {
    const parser = new KataEventStreamParser();
    expect(parser.push("id: 8\nevent: issue.updated\n")).toEqual([]);

    const messages = parser.push(
      `data: ${JSON.stringify({
        event_id: 8,
        event_uid: "event-8",
        origin_instance_uid: origin,
        type: "issue.updated",
        project_id: 2,
        project_uid: "project-inbox",
        project_name: "Inbox",
        actor: "middleman-test",
        created_at: "2026-05-15T12:00:01Z",
      })}\n\n`,
    );

    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatchObject({
      kind: "event",
      lastEventID: 8,
      event: { type: "issue.updated" },
    });
  });

  test("parses reset frames without treating them as task events", () => {
    const parser = new KataEventStreamParser();
    const messages = parser.push(
      eventFrame(42, "sync.reset_required", {
        event_id: 42,
        reset_after_id: 42,
      }),
    );

    expect(messages).toEqual([
      {
        kind: "reset",
        event_id: 42,
        reset_after_id: 42,
        lastEventID: 42,
      },
    ]);
  });

  test("ignores comments, invalid JSON, and non-object data frames", () => {
    const parser = new KataEventStreamParser();

    expect(parser.push(": connected\r\n\r\n")).toEqual([]);
    expect(parser.push("event: issue.created\r\ndata: not-json\r\n\r\n")).toEqual([]);
    expect(parser.push('event: issue.created\r\ndata: ["not", "an", "object"]\r\n\r\n')).toEqual([]);
  });

  test("handles CRLF and multi-line data frames", () => {
    const parser = new KataEventStreamParser();

    const messages = parser.push(
      [
        "id: 12\r\n",
        "event: issue.updated\r\n",
        'data: {"event_uid":"event-12",\r\n',
        `data: "origin_instance_uid":"${origin}",\r\n`,
        'data: "type":"issue.updated",\r\n',
        'data: "project_id":2,\r\n',
        'data: "project_uid":"project-inbox",\r\n',
        'data: "project_name":"Inbox",\r\n',
        'data: "actor":"middleman-test",\r\n',
        'data: "created_at":"2026-05-15T12:00:03Z"}\r\n',
        "\r\n",
      ].join(""),
    );

    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatchObject({
      kind: "event",
      lastEventID: 12,
      event: {
        event_id: 12,
        type: "issue.updated",
      },
    });
  });

  test("uses the SSE id before body event ids for stream cursor position", () => {
    const parser = new KataEventStreamParser();

    const messages = parser.push(
      eventFrame(10, "sync.reset_required", {
        event_id: 11,
        reset_after_id: 12,
      }),
    );

    expect(messages).toEqual([
      {
        kind: "reset",
        event_id: 10,
        reset_after_id: 12,
        lastEventID: 10,
      },
    ]);
  });
});

describe("readKataEventStream", () => {
  test("marks transient stream setup failures as retryable", async () => {
    const fetchImpl = vi.fn(async () => new Response("bad gateway", { status: 502 }));

    await expect(
      readKataEventStream({
        fetchImpl,
        onMessage: vi.fn(),
      }),
    ).rejects.toMatchObject({
      name: "KataEventStreamError",
      message: "Kata event stream failed: HTTP 502",
      retryable: true,
    } satisfies Partial<KataEventStreamError>);
  });

  test("sets SSE headers and emits parsed messages through the proxy", async () => {
    let requestURL = "";
    let headers: Headers | undefined;
    const onMessage = vi.fn();
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      requestURL = input instanceof URL ? input.toString() : String(input);
      headers = new Headers(init?.headers);
      return new Response(
        streamFromText(
          eventFrame(9, "issue.created", {
            event_id: 9,
            event_uid: "event-9",
            origin_instance_uid: origin,
            type: "issue.created",
            project_id: 2,
            project_uid: "project-inbox",
            project_name: "Inbox",
            actor: "middleman-test",
            created_at: "2026-05-15T12:00:02Z",
          }),
        ),
        {
          status: 200,
          headers: { "Content-Type": "text/event-stream" },
        },
      );
    });

    await expect(
      readKataEventStream({
        daemonId: "work",
        fetchImpl,
        lastEventID: 8,
        onMessage,
      }),
    ).rejects.toThrow("Live updates disconnected");

    expect(new URL(requestURL, "http://localhost").pathname).toBe("/api/v1/kata/proxy/api/v1/events/stream");
    expect(headers?.get("Accept")).toBe("text/event-stream");
    expect(headers?.get(KATA_DAEMON_HEADER)).toBe("work");
    expect(headers?.get("Last-Event-ID")).toBe("8");
    expect(onMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "event",
        lastEventID: 9,
        event: expect.objectContaining({ type: "issue.created" }),
      }),
    );
  });

  test("adds project scope and calls onOpen after a stream is established", async () => {
    let requestURL = "";
    const onOpen = vi.fn();
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      requestURL = input instanceof URL ? input.toString() : String(input);
      return new Response(streamFromText(": connected\n\n"), {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      });
    });

    await expect(
      readKataEventStream({
        fetchImpl,
        projectID: 7,
        onOpen,
        onMessage: vi.fn(),
      }),
    ).rejects.toThrow("Live updates disconnected");

    const url = new URL(requestURL, "http://localhost");
    expect(url.pathname).toBe("/api/v1/kata/proxy/api/v1/events/stream");
    expect(url.searchParams.get("project_id")).toBe("7");
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  test("emits a final event frame when the stream closes without a trailing blank line", async () => {
    const onMessage = vi.fn();
    const fetchImpl = vi.fn(
      async () =>
        new Response(
          streamFromText(
            `id: 13\nevent: issue.updated\ndata: ${JSON.stringify({
              event_id: 13,
              event_uid: "event-13",
              origin_instance_uid: origin,
              type: "issue.updated",
              project_id: 2,
              project_uid: "project-inbox",
              project_name: "Inbox",
              actor: "middleman-test",
              created_at: "2026-05-15T12:00:04Z",
            })}`,
          ),
          {
            status: 200,
            headers: { "Content-Type": "text/event-stream" },
          },
        ),
    );

    await expect(
      readKataEventStream({
        fetchImpl,
        onMessage,
      }),
    ).rejects.toThrow("Live updates disconnected");

    expect(onMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "event",
        lastEventID: 13,
        event: expect.objectContaining({ type: "issue.updated" }),
      }),
    );
  });

  test("does not send Last-Event-ID for an empty cursor", async () => {
    let headers: Headers | undefined;
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      headers = new Headers(init?.headers);
      return new Response(streamFromText(": connected\n\n"), {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      });
    });

    await expect(
      readKataEventStream({
        fetchImpl,
        lastEventID: 0,
        onMessage: vi.fn(),
      }),
    ).rejects.toThrow("Live updates disconnected");

    expect(headers?.has("Last-Event-ID")).toBe(false);
  });

  test("treats a successful response without a body as a nonretryable stream error", async () => {
    const fetchImpl = vi.fn(async () => new Response(null, { status: 200 }));

    await expect(
      readKataEventStream({
        fetchImpl,
        onMessage: vi.fn(),
      }),
    ).rejects.toMatchObject({
      name: "KataEventStreamError",
      message: "Kata event stream response has no body",
      retryable: false,
    } satisfies Partial<KataEventStreamError>);
  });

  test("returns without surfacing an error when the stream is aborted", async () => {
    let cancelCalled = false;
    const controller = new AbortController();
    const fetchImpl = vi.fn(async () => {
      return new Response(
        new ReadableStream<Uint8Array>({
          start(streamController) {
            streamController.enqueue(new TextEncoder().encode(": connected\n\n"));
          },
          cancel() {
            cancelCalled = true;
          },
        }),
        {
          status: 200,
          headers: { "Content-Type": "text/event-stream" },
        },
      );
    });

    const stream = readKataEventStream({
      fetchImpl,
      signal: controller.signal,
      onMessage: vi.fn(),
    });

    await vi.waitFor(() => {
      expect(fetchImpl).toHaveBeenCalledTimes(1);
    });
    controller.abort();

    await expect(stream).resolves.toBeUndefined();
    expect(cancelCalled).toBe(true);
  });
});
