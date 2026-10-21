import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../../app/runtime.js";
import { createAppStores } from "../../app-stores.svelte.js";
import { STORES_KEY, NAVIGATE_KEY, SIDEBAR_KEY, HOST_STATE_KEY } from "../../context.js";
import { startTestTelemetry } from "../../testing/telemetry.js";
import { createMockApiFetch } from "../../../test/mockApiFetch.js";
import Harness from "./SearchTelemetryTestHarness.svelte";

it.each([false, true])("counts Enter through the real sidebar search, issues=%s", async (issues) => {
  const api = createMockApiFetch([
    ({ url }) => (url.pathname === (issues ? "/api/v1/issues" : "/api/v1/pulls") ? Response.json([]) : undefined),
  ]);
  vi.stubGlobal("fetch", api.fetch);
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
    await waitFor(() =>
      expect(sent).toHaveBeenCalledExactlyOnceWith({ event: "search_empty", properties: { surface: "web" } }, {}),
    );
    await fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(issues ? stores.issues.isIssuesLoading() : stores.pulls.isLoading()).toBe(false));
    expect(sent).toHaveBeenCalledTimes(1);
  } finally {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
  }
});
