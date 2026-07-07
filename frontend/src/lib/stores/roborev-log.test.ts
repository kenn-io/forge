import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { createLogStore } from "@middleman/ui";

const encoder = new TextEncoder();

function ndjsonResponse(lines: unknown[]): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const line of lines) {
        controller.enqueue(encoder.encode(`${JSON.stringify(line)}\n`));
      }
      controller.close();
    },
  });
  return new Response(body, {
    status: 200,
    headers: { "Content-Type": "application/x-ndjson" },
  });
}

describe("createLogStore", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("loads completed job output snapshots from the JSON response", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          lines: [
            {
              ts: "2026-04-11T11:00:00Z",
              text: "finished review",
              line_type: "text",
            },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    const store = createLogStore({ client: {} as never, baseUrl: "http://roborev.test" });

    await store.loadSnapshot(42);

    expect(fetchMock).toHaveBeenCalledWith("http://roborev.test/api/job/output?job_id=42");
    expect(store.getLines()).toEqual([
      {
        ts: "2026-04-11T11:00:00Z",
        text: "finished review",
        lineType: "text",
      },
    ]);
  });

  it("streams live job output from the NDJSON job output endpoint", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(ndjsonResponse([{ ts: "2026-04-11T11:00:01Z", text: "running review", line_type: "tool" }]));
    const store = createLogStore({ client: {} as never, baseUrl: "http://roborev.test" });

    await store.startStreaming(77);

    expect(fetchMock).toHaveBeenCalledWith("http://roborev.test/api/job/output?job_id=77&stream=1", {
      signal: expect.any(AbortSignal),
    });
    expect(store.getLines()).toEqual([
      {
        ts: "2026-04-11T11:00:01Z",
        text: "running review",
        lineType: "tool",
      },
    ]);
  });
});
