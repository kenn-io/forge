import { beforeEach, describe, expect, test } from "vite-plus/test";

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
    staleTimer.record("first-bytes");
    recordWorkspaceSwitchPhase("workspace-request-end", "ws-1", undefined);

    expect(measures("first-bytes")).toHaveLength(0);
    expect(measures("workspace-request-end")).toHaveLength(0);

    createWorkspaceSwitchPaneTimer().record("first-bytes");
    expect(measures("first-bytes")).toHaveLength(1);
    expect((measures("first-bytes")[0] as PerformanceMeasure).detail).toMatchObject({
      workspaceId: "ws-2",
    });
  });

  test("a pane timer shares the per-switch one-shot guard with other panes", () => {
    beginWorkspaceSwitch("ws-1", undefined);

    createWorkspaceSwitchPaneTimer().record("socket-open");
    createWorkspaceSwitchPaneTimer().record("socket-open");

    expect(measures("socket-open")).toHaveLength(1);
  });

  test("cancelling the switch stops all further recording", () => {
    beginWorkspaceSwitch("ws-1", undefined);
    const timer = createWorkspaceSwitchPaneTimer();
    cancelWorkspaceSwitch();

    timer.record("first-paint");
    recordWorkspaceSwitchPhase("workspace-request-start", "ws-1", undefined);

    expect(measures("first-paint")).toHaveLength(0);
    expect(measures("workspace-request-start")).toHaveLength(0);
  });

  test("a pane timer created with no live switch records nothing", () => {
    const timer = createWorkspaceSwitchPaneTimer();
    beginWorkspaceSwitch("ws-1", undefined);

    timer.record("terminal-constructed");

    expect(measures("terminal-constructed")).toHaveLength(0);
  });
});
