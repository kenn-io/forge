import { afterEach, describe, expect, it } from "vite-plus/test";

import { workspaceSessionWebSocketPath, workspaceTmuxWebSocketPath } from "./workspace-runtime.js";

describe("workspace runtime WebSocket paths", () => {
  afterEach(() => {
    delete window.__BASE_PATH__;
  });

  it("builds runtime WebSocket paths", () => {
    expect(workspaceSessionWebSocketPath("ws-1", "ws-1:helper")).toBe(
      "/ws/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper/terminal",
    );
    expect(workspaceTmuxWebSocketPath("ws-1")).toBe("/ws/v1/workspaces/ws-1/terminal");
  });

  it("includes the configured base path", () => {
    window.__BASE_PATH__ = "/kenn-forge/";

    expect(workspaceSessionWebSocketPath("ws-1", "ws-1:helper")).toBe(
      "/kenn-forge/ws/v1/workspaces/ws-1/runtime/sessions/ws-1%3Ahelper/terminal",
    );
    expect(workspaceTmuxWebSocketPath("ws-1")).toBe("/kenn-forge/ws/v1/workspaces/ws-1/terminal");
  });
});
