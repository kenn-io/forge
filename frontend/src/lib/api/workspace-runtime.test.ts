import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import {
  getWorkspaceRuntime,
  launchWorkspaceSession,
  renameWorkspaceSession,
  stopWorkspaceSession,
  workspaceSessionWebSocketPath,
  workspaceTmuxWebSocketPath,
} from "./workspace-runtime.js";

describe("workspace-runtime api", () => {
  afterEach(() => {
    delete window.__BASE_PATH__;
  });

  it("loads runtime state and normalizes nullable arrays", async () => {
    const fetchMock = vi.fn(
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

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/workspaces/ws-1/runtime");
    expect(runtime.launch_targets).toEqual([]);
    expect(runtime.sessions).toEqual([]);
  });

  it("launches and stops sessions with JSON mutation requests", async () => {
    const fetchMock = vi
      .fn()
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

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/v1/workspaces/ws-1/runtime/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target_key: "helper" }),
    });
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ label: "Review helper" }),
    });
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper", {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
    });
  });

  it("builds runtime websocket paths", () => {
    expect(workspaceSessionWebSocketPath("ws-1", "ws-1:helper")).toBe(
      "/ws/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper/terminal",
    );
    expect(workspaceTmuxWebSocketPath("ws-1")).toBe("/ws/v1/workspaces/ws-1/terminal");
  });

  it("includes the configured base path in runtime and websocket paths", async () => {
    window.__BASE_PATH__ = "/middleman/";
    const fetchMock = vi.fn(
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

    expect(fetchMock).toHaveBeenCalledWith("/middleman/api/v1/workspaces/ws-1/runtime");
    expect(workspaceSessionWebSocketPath("ws-1", "ws-1:helper")).toBe(
      "/middleman/ws/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper/terminal",
    );
    expect(workspaceTmuxWebSocketPath("ws-1")).toBe("/middleman/ws/v1/workspaces/ws-1/terminal");
  });
});
