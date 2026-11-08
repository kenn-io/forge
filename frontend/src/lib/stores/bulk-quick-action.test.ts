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

it("reports a rejected workspace retry and still runs the other selected items", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const handoffs: string[] = [];
  const mixed = createMockApiFetch([
    ({ method, url }) => {
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-failed/retry") {
        return Response.json(
          { code: "workspaceNotFound", title: "Not Found", detail: "workspace not found", status: 404 },
          { status: 404 },
        );
      }
      if (method === "POST" && url.pathname === "/api/v1/workspaces") {
        return Response.json({ id: "ws-new", status: "creating" });
      }
      const handoff = url.pathname.match(/^\/api\/v1\/workspaces\/([^/]+)\/runtime\/agent-handoffs$/);
      if (method === "POST" && handoff) {
        handoffs.push(handoff[1] ?? "");
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
  const [failed, fresh] = pulls;
  if (!failed || !fresh) throw new Error("fixture needs two pull requests");
  try {
    runQuickActionOnItems(
      runtime,
      [
        { kind: "pull", item: { ...failed, workspace: { id: "ws-failed", status: "error" } } },
        { kind: "pull", item: { ...fresh, workspace: undefined } },
      ],
      { label: "Review", agent: "helper", prompt: "Review this change" },
      "",
    );
    await vi.waitFor(() =>
      expect(getFlashes()).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            tone: "warning",
            message: expect.stringContaining("started 1, failed 1"),
          }),
        ]),
      ),
    );
    expect(
      getFlashes()
        .map((flash) => flash.message)
        .join(" "),
    ).toContain("workspace not found");
    expect(handoffs).toEqual(["ws-new"]);
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

it("launches in a retried workspace in the order the actions were run", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const prompts: string[] = [];
  // Like the server, a retry starts only from the error state and is
  // rejected once the workspace is ready again.
  let status = "error";
  const mixed = createMockApiFetch([
    ({ method, url, bodyText }) => {
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-failed/retry") {
        if (status === "ready") {
          return Response.json(
            { code: "conflict", title: "Conflict", detail: "workspace is not in error status", status: 409 },
            { status: 409 },
          );
        }
        status = "creating";
        return Response.json({ id: "ws-failed", status });
      }
      if (method === "POST" && url.pathname === "/api/v1/workspaces/ws-failed/runtime/agent-handoffs") {
        status = "ready";
        prompts.push((JSON.parse(bodyText) as { message: string }).message);
        return Response.json({
          session: { key: "runtime-1", target_key: "helper" },
          initial_message: { state: "delivered", target_key: "helper", message_bytes: 5 },
        });
      }
      return undefined;
    },
  ]);
  // The first run's retry answers only after the second run's retry would have.
  const firstRetry = Promise.withResolvers<void>();
  let retries = 0;
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input), window.location.href);
    if (url.pathname.endsWith("/retry") && ++retries === 1) await firstRetry.promise;
    return mixed.fetch(input, init);
  });
  const runtime = makeAppRuntime();
  const [pull] = pulls;
  if (!pull) throw new Error("fixture needs a pull request");
  const target = { kind: "pull" as const, item: { ...pull, workspace: { id: "ws-failed", status: "error" } } };
  try {
    runQuickActionOnItems(runtime, [target], { label: "First", agent: "helper", prompt: "first" }, "");
    runQuickActionOnItems(runtime, [target], { label: "Second", agent: "helper", prompt: "second" }, "");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(prompts).toEqual([]);
    firstRetry.resolve();
    await vi.waitFor(() => expect(prompts).toEqual(["first", "second"]));
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
    for (const flash of getFlashes()) dismissFlash(flash.id);
    vi.unstubAllGlobals();
  }
});

it("creates on the fleet member the saved default names, or reports it unavailable", async () => {
  const api = createMockApiFetch();
  const pulls: PullRequest[] = await (await api.fetch("/api/v1/pulls")).json();
  const node = "b".repeat(32);
  for (const available of [true, false]) {
    const creates: string[] = [];
    const handoffs: string[] = [];
    const mixed = createMockApiFetch([
      ({ method, url }) => {
        if (method === "GET" && url.pathname === "/api/v1/snapshot") {
          return Response.json({
            hosts: [
              {
                configKey: node,
                kind: "remote",
                name: "build-b",
                baseURL: "https://build-b.example.test",
                operationAvailability: {
                  workspaceWrite: available ? { available } : { available, unavailableReason: "Host is offline." },
                },
              },
            ],
          });
        }
        if (method === "POST" && url.pathname.endsWith("/workspaces")) {
          creates.push(url.pathname);
          return Response.json({ id: "ws-remote", status: "ready" });
        }
        if (method === "POST" && url.pathname.endsWith("/runtime/agent-handoffs")) {
          handoffs.push(url.pathname);
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
    try {
      runQuickActionOnItems(
        runtime,
        [{ kind: "pull", item: { ...pulls[0]!, workspace: undefined } }],
        { label: "Review", agent: "helper", prompt: "Review this change" },
        "https://build-b.example.test",
      );
      await vi.waitFor(() =>
        expect(getFlashes()).toEqual([
          expect.objectContaining({
            message: available
              ? expect.stringContaining('Started "Review" in 1 workspace')
              : expect.stringContaining("your default machine, build-b, is unavailable: Host is offline."),
          }),
        ]),
      );
      expect(creates).toEqual(available ? [`/api/v1/fleet/hosts/${node}/workspaces`] : []);
      expect(handoffs).toEqual(
        available ? [`/api/v1/fleet/hosts/${node}/workspaces/ws-remote/runtime/agent-handoffs`] : [],
      );
    } finally {
      await Effect.runPromise(runtime.disposeEffect);
      for (const flash of getFlashes()) dismissFlash(flash.id);
      vi.unstubAllGlobals();
    }
  }
});
