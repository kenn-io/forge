import { Effect } from "effect";
import { expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../api/types.js";
import { makeAppRuntime } from "../app/runtime.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { startTestTelemetry } from "../testing/telemetry.js";
import { getFlashes, dismissFlash } from "./flash.svelte.js";
import { runQuickActionOnItems } from "./bulk-quick-action.js";

it("counts a danger flash when creating workspaces for a bulk action fails", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const failing = createMockApiFetch([
    ({ method, url }) =>
      method === "POST" && url.pathname === "/api/v1/workspaces"
        ? Response.json({ code: "forbidden", title: "Create denied", status: 403 }, { status: 403 })
        : undefined,
  ]);
  vi.stubGlobal("fetch", failing.fetch);
  const runtime = makeAppRuntime();
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  try {
    runQuickActionOnItems(
      runtime,
      [{ kind: "pull", item: { ...pulls[0]!, workspace: undefined } }],
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "",
    );
    await vi.waitFor(() => expect(getFlashes().some((flash) => flash.tone === "danger")).toBe(true));
    expect(sent).toHaveBeenCalledExactlyOnceWith(
      { event: "error_shown", properties: { surface: "web", kind: "workspace_opened" } },
      {},
    );
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});
