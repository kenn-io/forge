import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import {
  getWorkspaceRuntime,
  launchWorkspaceSession,
  renameWorkspaceSession,
  stopWorkspaceSession,
  type RuntimeFetch,
  workspaceSessionWebSocketPath,
  workspaceTmuxWebSocketPath,
} from "./workspace-runtime.js";
import { beginInteractionTrace, endInteractionTrace } from "../instrumentation/traceContext.js";

describe("workspace-runtime api", () => {
  afterEach(() => {
    delete window.__BASE_PATH__;
    endInteractionTrace();
  });

  it("loads runtime state and normalizes nullable arrays", async () => {
    const fetchMock = vi.fn<RuntimeFetch>(
      async () =>
        new Response(
          JSON.stringify({
            launch_targets: null,
            sessions: null,
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
    );

    const runtime = await getWorkspaceRuntime("ws-1", fetchMock);

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/workspaces/ws-1/runtime");
    expect(runtime.launch_targets).toEqual([]);
    expect(runtime.sessions).toEqual([]);
  });

  it("launches and stops sessions with JSON mutation requests", async () => {
    const fetchMock = vi
      .fn<RuntimeFetch>()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            key: "ws-1:helper",
            workspace_id: "ws-1",
            target_key: "helper",
            label: "Helper",
            kind: "agent",
            status: "running",
            created_at: "2026-04-25T00:00:00Z",
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            key: "ws-1:helper",
            workspace_id: "ws-1",
            target_key: "helper",
            label: "Review helper",
            kind: "agent",
            status: "running",
            created_at: "2026-04-25T00:00:00Z",
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));

    await launchWorkspaceSession("ws-1", "helper", fetchMock);
    await renameWorkspaceSession("ws-1", "ws-1:helper", "Review helper", fetchMock);
    await stopWorkspaceSession("ws-1", "ws-1:helper", fetchMock);

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/workspaces/ws-1/runtime/sessions");
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({ target_key: "helper" }),
    });
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("Content-Type")).toBe("application/json");
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/api/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper");
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({
      method: "PATCH",
      body: JSON.stringify({ label: "Review helper" }),
    });
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get("Content-Type")).toBe("application/json");
    expect(fetchMock.mock.calls[2]?.[0]).toBe("/api/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper");
    expect(fetchMock.mock.calls[2]?.[1]).toMatchObject({
      method: "DELETE",
    });
    expect(new Headers(fetchMock.mock.calls[2]?.[1]?.headers).get("Content-Type")).toBe("application/json");
  });

  it("includes display region when launching a workspace session", async () => {
    const fetchMock = vi.fn<RuntimeFetch>(
      async () =>
        new Response(
          JSON.stringify({
            key: "ws-1:shell",
            workspace_id: "ws-1",
            target_key: "plain_shell",
            label: "Shell",
            kind: "plain_shell",
            status: "running",
            display_region: "workflow",
            created_at: "2026-04-25T00:00:00Z",
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
    );

    await launchWorkspaceSession("ws-1", "plain_shell", undefined, "workflow", fetchMock);

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/workspaces/ws-1/runtime/sessions");
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({
        target_key: "plain_shell",
        display_region: "workflow",
      }),
    });
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("Content-Type")).toBe("application/json");
  });

  it("adds W3C trace headers to runtime reads and mutations", async () => {
    const fetchMock = vi
      .fn<RuntimeFetch>()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ launch_targets: [], sessions: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ key: "ws-1:helper" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ key: "ws-1:helper", label: "Review helper" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));

    const traceId = beginInteractionTrace("workspace-switch", { "workspace.id": "ws-1" });
    await getWorkspaceRuntime("ws-1", fetchMock);
    await launchWorkspaceSession("ws-1", "helper", fetchMock);
    await renameWorkspaceSession("ws-1", "ws-1:helper", "Review helper", fetchMock);
    await stopWorkspaceSession("ws-1", "ws-1:helper", fetchMock);

    const requests = fetchMock.mock.calls.map(([input, init]) =>
      input instanceof Request ? input : new Request(new URL(String(input), "http://localhost"), init),
    );
    expect(requests).toHaveLength(4);
    for (const request of requests) {
      expect(request.headers.get("traceparent")).toMatch(new RegExp(`^00-${traceId}-[0-9a-f]{16}-01$`));
      expect(request.headers.get("baggage")).toBe("interaction=workspace-switch,workspace.id=ws-1");
    }
  });

  it("builds runtime websocket paths", () => {
    expect(workspaceSessionWebSocketPath("ws-1", "ws-1:helper")).toBe(
      "/ws/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper/terminal",
    );
    expect(workspaceTmuxWebSocketPath("ws-1")).toBe("/ws/v1/workspaces/ws-1/terminal");
  });

  it("includes the configured base path in runtime and websocket paths", async () => {
    window.__BASE_PATH__ = "/middleman/";
    const fetchMock = vi.fn<RuntimeFetch>(
      async () =>
        new Response(
          JSON.stringify({
            launch_targets: [],
            sessions: [],
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
    );

    await getWorkspaceRuntime("ws-1", fetchMock);

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/middleman/api/v1/workspaces/ws-1/runtime");
    expect(workspaceSessionWebSocketPath("ws-1", "ws-1:helper")).toBe(
      "/middleman/ws/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper/terminal",
    );
    expect(workspaceTmuxWebSocketPath("ws-1")).toBe("/middleman/ws/v1/workspaces/ws-1/terminal");
  });
});
