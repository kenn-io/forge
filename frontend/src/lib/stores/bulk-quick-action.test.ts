import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "../api/types.js";
import { makeAppRuntime } from "../app/runtime.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { startTestTelemetry } from "../testing/telemetry.js";
import { getFlashes, dismissFlash } from "./flash.svelte.js";
import { runQuickActionOnItems } from "./bulk-quick-action.js";
import { resetWorkspaceCreatePendingForTest } from "./workspace-create-pending.svelte.js";

afterEach(resetWorkspaceCreatePendingForTest);

it.each(["partial", "all"])("counts one error flash for %s bulk workspace creation failures", async (failure) => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  let creates = 0;
  const handoff = vi.fn();
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method === "POST" && url.pathname === "/api/v1/workspaces") {
        creates++;
        return failure === "partial" && creates === 1
          ? Response.json({ id: "ws-partial", status: "ready" })
          : Response.json({ code: "forbidden", title: "Create denied", status: 403 }, { status: 403 });
      }
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-partial/runtime/agent-handoffs") {
        handoff();
        return Response.json({
          session: { key: "runtime-1", target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 18 },
        });
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", mixed.fetch);
  const runtime = makeAppRuntime();
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  try {
    runQuickActionOnItems(
      runtime,
      pulls.slice(0, 2).map((item) => ({ kind: "pull", item: { ...item, workspace: undefined } })),
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "",
    );
    await vi.waitFor(() =>
      expect(getFlashes()).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            tone: failure === "partial" ? "warning" : "danger",
            message: expect.stringContaining(failure === "partial" ? "started 1, failed 1" : "failed 2"),
          }),
        ]),
      ),
    );
    expect(creates).toBe(2);
    expect(handoff).toHaveBeenCalledTimes(failure === "partial" ? 1 : 0);
    await vi.waitFor(() =>
      expect(sent).toHaveBeenCalledExactlyOnceWith(
        { event: "error_shown", properties: { surface: "web", kind: "workspace_opened" } },
        {},
      ),
    );
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});
