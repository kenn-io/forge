import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import type { RuntimeSession } from "../../api/types.js";
import {
  loadMobileWorkspaceSession,
  saveMobileWorkspaceSession,
  selectMobileWorkspaceSession,
} from "./mobile-workspace-session.js";

function session(key: string): RuntimeSession {
  return {
    key,
    label: key === "agent" ? "Codex" : "Shell",
    target_key: key,
    kind: key === "agent" ? "agent" : "plain_shell",
    region: "workflow",
    status: "running",
    created_at: `2026-08-11T12:00:0${key.length}Z`,
    tmux_session: `tmux-${key}`,
  };
}

describe("mobile workspace session selection", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  it("keeps a live preference and falls back deterministically", () => {
    const sessions = [session("agent"), session("shell")];
    expect(selectMobileWorkspaceSession(sessions, "agent")).toBe("agent");
    expect(selectMobileWorkspaceSession(sessions, "gone")).toBe("agent");
    expect(selectMobileWorkspaceSession([], "gone")).toBeNull();
  });

  it("persists independently for local and Fleet workspaces", () => {
    saveMobileWorkspaceSession("ws-1", undefined, "agent");
    saveMobileWorkspaceSession("ws-1", "laptop", "shell");
    expect(loadMobileWorkspaceSession("ws-1")).toBe("agent");
    expect(loadMobileWorkspaceSession("ws-1", "laptop")).toBe("shell");
  });
});
