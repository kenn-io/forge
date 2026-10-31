import { afterEach, expect, test, vi } from "vite-plus/test";
import { Effect, ManagedRuntime } from "effect";
import { makeTraceExportLayer } from "./traceExport.js";

afterEach(() => {
  vi.unstubAllGlobals();
});

async function exportedRequests(enabled: boolean) {
  const requests: Array<{ url: string; body: string }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      requests.push({ url: request.url, body: await request.text() });
      return new Response(null, { status: 204 });
    }),
  );
  const runtime = ManagedRuntime.make(makeTraceExportLayer(enabled));
  await runtime.runPromise(Effect.void.pipe(Effect.withSpan("WorkspaceRuntime.refresh")));
  // Disposing the runtime flushes buffered spans.
  await runtime.dispose();
  return requests;
}

test("exports finished spans to the daemon's trace relay", async () => {
  const [request] = await exportedRequests(true);

  expect(new URL(request!.url).pathname).toBe("/api/v1/telemetry/traces");
  expect(request!.body).toContain('"WorkspaceRuntime.refresh"');
  expect(request!.body).toContain('"kenn-forge-web"');
});

test("exports nothing unless the daemon exports traces", async () => {
  expect(await exportedRequests(false)).toEqual([]);
});
