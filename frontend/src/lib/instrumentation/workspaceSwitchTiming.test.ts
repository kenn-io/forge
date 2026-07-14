import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";

import {
  beginWorkspaceSwitch,
  cancelWorkspaceSwitch,
  createWorkspaceSwitchPaneTimer,
  recordWorkspaceSwitchPhase,
} from "./workspaceSwitchTiming.js";

function measures(phase: string): PerformanceEntry[] {
  return performance.getEntriesByName(`workspace-switch:${phase}`, "measure");
}

describe("workspace switch timing", () => {
  beforeEach(() => {
    cancelWorkspaceSwitch();
    performance.clearMarks();
    performance.clearMeasures();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  test("records phases for the live switch with the workspace in the detail", () => {
    beginWorkspaceSwitch("ws-1", undefined);

    recordWorkspaceSwitchPhase("workspace-request-start", "ws-1", undefined);
    recordWorkspaceSwitchPhase("workspace-request-end", "ws-1", undefined, { ok: true });

    const start = measures("workspace-request-start");
    const end = measures("workspace-request-end");
    expect(start).toHaveLength(1);
    expect(end).toHaveLength(1);
    expect((start[0] as PerformanceMeasure).detail).toMatchObject({ workspaceId: "ws-1" });
    expect((end[0] as PerformanceMeasure).detail).toMatchObject({ workspaceId: "ws-1", ok: true });
  });

  test("a phase records at most once per switch", () => {
    beginWorkspaceSwitch("ws-1", undefined);

    recordWorkspaceSwitchPhase("runtime-request-start", "ws-1", undefined);
    recordWorkspaceSwitchPhase("runtime-request-start", "ws-1", undefined);

    expect(measures("runtime-request-start")).toHaveLength(1);
  });

  test("a response for a different workspace or host records nothing", () => {
    beginWorkspaceSwitch("ws-2", "fleet-a");

    recordWorkspaceSwitchPhase("workspace-request-end", "ws-1", "fleet-a");
    recordWorkspaceSwitchPhase("workspace-request-end", "ws-2", undefined);

    expect(measures("workspace-request-end")).toHaveLength(0);
  });

  test("beginning a new switch supersedes the previous one", () => {
    beginWorkspaceSwitch("ws-1", undefined);
    const staleTimer = createWorkspaceSwitchPaneTimer();

    beginWorkspaceSwitch("ws-2", undefined);
    expect(staleTimer.record("first-bytes")).toBe(false);
    recordWorkspaceSwitchPhase("workspace-request-end", "ws-1", undefined);

    expect(measures("first-bytes")).toHaveLength(0);
    expect(measures("workspace-request-end")).toHaveLength(0);

    expect(createWorkspaceSwitchPaneTimer().record("first-bytes")).toBe(true);
    expect(measures("first-bytes")).toHaveLength(1);
    expect((measures("first-bytes")[0] as PerformanceMeasure).detail).toMatchObject({
      workspaceId: "ws-2",
    });
  });

  test("pane timers share the per-switch one-shot guard and identify the winning pane", () => {
    beginWorkspaceSwitch("ws-1", undefined);

    const first = createWorkspaceSwitchPaneTimer();
    const second = createWorkspaceSwitchPaneTimer();
    expect(first.record("socket-open")).toBe(true);
    expect(second.record("socket-open")).toBe(false);

    const entries = measures("socket-open");
    expect(entries).toHaveLength(1);
    const detail = (entries[0] as PerformanceMeasure).detail as { paneId?: unknown };
    expect(typeof detail.paneId).toBe("number");

    // The losing pane can still win a different phase, and its paneId
    // differs from the first pane's.
    expect(second.record("first-bytes")).toBe(true);
    const firstBytesDetail = (measures("first-bytes")[0] as PerformanceMeasure).detail as {
      paneId?: unknown;
    };
    expect(firstBytesDetail.paneId).not.toBe(detail.paneId);
  });

  test("cancelling the switch stops all further recording", () => {
    beginWorkspaceSwitch("ws-1", undefined);
    const timer = createWorkspaceSwitchPaneTimer();
    cancelWorkspaceSwitch();

    expect(timer.record("first-paint")).toBe(false);
    recordWorkspaceSwitchPhase("workspace-request-start", "ws-1", undefined);

    expect(measures("first-paint")).toHaveLength(0);
    expect(measures("workspace-request-start")).toHaveLength(0);
  });

  test("a stale token cannot cancel a newer switch", () => {
    const staleToken = beginWorkspaceSwitch("ws-1", undefined);
    beginWorkspaceSwitch("ws-2", undefined);

    cancelWorkspaceSwitch(staleToken);
    recordWorkspaceSwitchPhase("workspace-request-start", "ws-2", undefined);

    expect(measures("workspace-request-start")).toHaveLength(1);
  });

  test("a matching token cancels its own switch", () => {
    const token = beginWorkspaceSwitch("ws-1", undefined);

    cancelWorkspaceSwitch(token);
    recordWorkspaceSwitchPhase("workspace-request-start", "ws-1", undefined);

    expect(measures("workspace-request-start")).toHaveLength(0);
  });

  test("phases arriving after the recording window are dropped", () => {
    beginWorkspaceSwitch("ws-1", undefined);
    const timer = createWorkspaceSwitchPaneTimer();
    const realNow = performance.now();
    vi.spyOn(performance, "now").mockReturnValue(realNow + 31_000);

    expect(timer.record("terminal-constructed")).toBe(false);
    recordWorkspaceSwitchPhase("runtime-request-end", "ws-1", undefined);

    expect(measures("terminal-constructed")).toHaveLength(0);
    expect(measures("runtime-request-end")).toHaveLength(0);
  });

  test("a pane timer created with no live switch records nothing", () => {
    const timer = createWorkspaceSwitchPaneTimer();
    beginWorkspaceSwitch("ws-1", undefined);

    expect(timer.record("terminal-constructed")).toBe(false);

    expect(measures("terminal-constructed")).toHaveLength(0);
  });
});
