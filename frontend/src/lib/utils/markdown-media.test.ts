import { Effect, Fiber } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { probeMarkdownMedia, resolveMarkdownMediaOutcomes } from "./markdown-media.js";

let counter = 0;
function uniqueCandidate() {
  counter++;
  const source = `https://github.com/user-attachments/assets/probe-${counter}-${Date.now()}`;
  return { source, mediaURL: `/repo/github/acme/widgets/markdown-media?source=${encodeURIComponent(source)}` };
}

function stubFetch(answer: (request: Request) => Response | Promise<Response>) {
  const requests: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      requests.push(request);
      return answer(request);
    }),
  );
  return requests;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("markdown media probe", () => {
  it("asks for one byte and remembers a confirmed video", async () => {
    const candidate = uniqueCandidate();
    const requests = stubFetch(
      () => new Response(new Uint8Array([0]), { status: 206, headers: { "Content-Type": "video/mp4" } }),
    );

    const first = await Effect.runPromise(resolveMarkdownMediaOutcomes([candidate]));
    const second = await Effect.runPromise(resolveMarkdownMediaOutcomes([candidate]));

    expect(first.get(candidate.source)).toBe("video");
    expect(second.get(candidate.source)).toBe("video");
    expect(requests).toHaveLength(1);
    expect(requests[0]?.headers.get("Range")).toBe("bytes=0-0");
    expect(new URL(requests[0]!.url).pathname).toBe("/api/v1/repo/github/acme/widgets/markdown-media");
  });

  it("cancels a whole-file answer without reading it", async () => {
    const candidate = uniqueCandidate();
    let pulled = 0;
    let cancelled = false;
    const body = new ReadableStream<Uint8Array>(
      {
        pull(controller) {
          pulled++;
          controller.enqueue(new Uint8Array(1024));
        },
        cancel() {
          cancelled = true;
        },
      },
      { highWaterMark: 0 },
    );
    stubFetch(() => new Response(body, { status: 200, headers: { "Content-Type": "video/webm" } }));

    const outcome = await Effect.runPromise(probeMarkdownMedia(candidate.mediaURL));

    expect(outcome).toBe("video");
    expect(cancelled).toBe(true);
    expect(pulled).toBe(0);
  });

  it("remembers only a not-a-video answer among failures", async () => {
    const notVideo = uniqueCandidate();
    stubFetch(() => Response.json({ status: 415, code: "unsupportedMediaType" }, { status: 415 }));
    expect((await Effect.runPromise(resolveMarkdownMediaOutcomes([notVideo]))).get(notVideo.source)).toBe("link");
    const afterNotVideo = stubFetch(() => new Response(null, { status: 500 }));
    expect((await Effect.runPromise(resolveMarkdownMediaOutcomes([notVideo]))).get(notVideo.source)).toBe("link");
    expect(afterNotVideo).toHaveLength(0);
  });

  it.each([
    ["denied credentials", () => Response.json({ code: "forbidden" }, { status: 403 })],
    [
      "a video answer with another type",
      () => new Response("x", { status: 200, headers: { "Content-Type": "text/html" } }),
    ],
    [
      "a network failure",
      () => {
        throw new TypeError("network down");
      },
    ],
  ])("probes again after %s", async (_label, failure) => {
    const candidate = uniqueCandidate();
    stubFetch(failure);
    expect((await Effect.runPromise(resolveMarkdownMediaOutcomes([candidate]))).get(candidate.source)).toBe("unknown");

    const retry = stubFetch(
      () => new Response(new Uint8Array([0]), { status: 206, headers: { "Content-Type": "video/mp4" } }),
    );
    expect((await Effect.runPromise(resolveMarkdownMediaOutcomes([candidate]))).get(candidate.source)).toBe("video");
    expect(retry).toHaveLength(1);
  });

  it("aborts the request when the render is interrupted", async () => {
    const candidate = uniqueCandidate();
    let started!: () => void;
    const requestStarted = new Promise<void>((resolve) => (started = resolve));
    const requests = stubFetch(
      (request) =>
        new Promise<Response>((_resolve, reject) => {
          started();
          request.signal.addEventListener("abort", () => reject(request.signal.reason));
        }),
    );

    const fiber = Effect.runFork(resolveMarkdownMediaOutcomes([candidate]));
    await requestStarted;
    await Effect.runPromise(Fiber.interrupt(fiber));

    expect(requests[0]?.signal.aborted).toBe(true);
  });

  it("keeps at most four probes in flight", async () => {
    const candidates = Array.from({ length: 6 }, uniqueCandidate);
    let inFlight = 0;
    let peak = 0;
    stubFetch(async () => {
      inFlight++;
      peak = Math.max(peak, inFlight);
      await new Promise((resolve) => setTimeout(resolve, 10));
      inFlight--;
      return new Response(new Uint8Array([0]), { status: 206, headers: { "Content-Type": "video/mp4" } });
    });

    const outcomes = await Effect.runPromise(resolveMarkdownMediaOutcomes(candidates));

    expect(peak).toBe(4);
    expect([...outcomes.values()]).toEqual(Array(6).fill("video"));
  });
});
