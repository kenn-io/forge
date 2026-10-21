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

it("counts a warning flash when only one bulk workspace creation fails", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  let creates = 0;
  const handoff = vi.fn();
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method === "POST" && url.pathname === "/api/v1/workspaces") {
        creates++;
        return creates === 1
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
          expect.objectContaining({ tone: "warning", message: expect.stringContaining("started 1, failed 1") }),
        ]),
      ),
    );
    expect(creates).toBe(2);
    expect(handoff).toHaveBeenCalledOnce();
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
