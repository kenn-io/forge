import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../../app/runtime.js";
import { createAppStores } from "../../app-stores.svelte.js";
import { STORES_KEY, NAVIGATE_KEY, SIDEBAR_KEY, HOST_STATE_KEY } from "../../context.js";
import { startTestTelemetry } from "../../testing/telemetry.js";
import { createMockApiFetch } from "../../../test/mockApiFetch.js";
import Harness from "./SearchTelemetryTestHarness.svelte";

it.each([
  [false, "sidebar"],
  [true, "sidebar"],
  [false, "focus"],
  [true, "focus"],
  [false, "phone"],
  [true, "phone"],
] as const)("counts Enter through the real search, issues=%s, presentation=%s", async (issues, presentation) => {
  const api = createMockApiFetch([
    ({ url }) => (url.pathname === (issues ? "/api/v1/issues" : "/api/v1/pulls") ? Response.json([]) : undefined),
  ]);
  vi.stubGlobal("fetch", api.fetch);
  const runtime = makeAppRuntime();
  const { stores } = createAppStores({ runtime });
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  render(Harness, {
    props: { runtime, issues, presentation },
    context: new Map<symbol, unknown>([
      [STORES_KEY, stores],
      [NAVIGATE_KEY, vi.fn()],
      [SIDEBAR_KEY, { toggleSidebar: vi.fn() }],
      [HOST_STATE_KEY, {}],
    ]),
  });
  try {
    const input = screen.getByRole("searchbox", { name: issues ? "Search issues" : "Search PRs" });
    await fireEvent.input(input, { target: { value: "missing" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() =>
      expect(sent).toHaveBeenCalledExactlyOnceWith({ event: "search_empty", properties: { surface: "web" } }, {}),
    );
    await fireEvent.input(input, { target: { value: "different" } });
    await fireEvent.input(input, { target: { value: "missing" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(2));
  } finally {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
  }
});

it.each([false, true])("counts separate submissions sharing a pending read, issues=%s", async (issues) => {
  const response = Promise.withResolvers<Response>();
  const api = createMockApiFetch();
  let reads = 0;
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
    if (url.pathname === (issues ? "/api/v1/issues" : "/api/v1/pulls") && url.searchParams.get("q") === "missing") {
      reads++;
      return response.promise.then((value) => value.clone());
    }
    return api.fetch(input, init);
  });
  const runtime = makeAppRuntime();
  const { stores } = createAppStores({ runtime });
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  render(Harness, {
    props: { runtime, issues },
    context: new Map<symbol, unknown>([
      [STORES_KEY, stores],
      [NAVIGATE_KEY, vi.fn()],
      [SIDEBAR_KEY, { toggleSidebar: vi.fn() }],
      [HOST_STATE_KEY, {}],
    ]),
  });
  try {
    const input = screen.getByRole("searchbox", { name: issues ? "Search issues" : "Search PRs" });
    await fireEvent.input(input, { target: { value: "missing" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(reads).toBe(1));
    await fireEvent.input(input, { target: { value: "different" } });
    await fireEvent.input(input, { target: { value: "missing" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(reads).toBe(1);
    response.resolve(Response.json([]));
    await waitFor(() =>
      expect(sent.mock.calls.map(([event]) => event)).toEqual([
        { event: "search_empty", properties: { surface: "web" } },
        { event: "search_empty", properties: { surface: "web" } },
      ]),
    );
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(reads).toBe(2));
    await waitFor(() => expect(issues ? stores.issues.isIssuesLoading() : stores.pulls.isLoading()).toBe(false));
    expect(sent).toHaveBeenCalledTimes(2);
  } finally {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
  }
});
