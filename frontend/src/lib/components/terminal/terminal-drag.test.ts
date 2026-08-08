import { clearActiveTabbedPanelDrag } from "../shared/tabbed-panel-drag.js";
import { beforeEach, describe, expect, it } from "vite-plus/test";
import {
  RUNTIME_SESSION_DRAG_MIME,
  WORKFLOW_TAB_DRAG_MIME,
  clearActiveTerminalDrag,
  readRuntimeSessionDrag,
  readWorkflowTabDrag,
  startRuntimeSessionDrag,
  startWorkflowTabDrag,
} from "./terminal-drag";

describe("terminal drag payloads", () => {
  beforeEach(() => {
    clearActiveTerminalDrag();
  });

  it("reads active session drag state when Chrome hides custom drag data", () => {
    const dragStart = fakeDragEvent();
    startRuntimeSessionDrag(dragStart, {
      workspaceId: "workspace-1",
      sessionKey: "session-1",
    });
    expect(dragStart.dataTransfer?.getData("text/plain")).toBe("Kenn Forge terminal session");
    expect(dragStart.dataTransfer?.getData(RUNTIME_SESSION_DRAG_MIME)).not.toContain("session-1");
    expect(dragStart.dataTransfer?.getData(RUNTIME_SESSION_DRAG_MIME)).not.toContain("workspace-1");

    expect(readRuntimeSessionDrag(fakeDragEvent({ exposeGetData: false }), "workspace-1")).toBe("session-1");
  });

  it("expires token-backed drop payloads after active drag state clears", () => {
    const dragStart = fakeDragEvent();
    startRuntimeSessionDrag(dragStart, {
      workspaceId: "workspace-1",
      sessionKey: "session-1",
    });
    clearActiveTerminalDrag();

    expect(readRuntimeSessionDrag(dragStart, "workspace-1")).toBeNull();
  });

  it("maps workflow session tabs to runtime session drags", () => {
    const dragStart = fakeDragEvent();
    startWorkflowTabDrag(dragStart, {
      workspaceId: "workspace-1",
      tabKey: "session:session-1",
    });
    expect(dragStart.dataTransfer?.getData("text/plain")).toBe("Kenn Forge workflow tab");
    expect(dragStart.dataTransfer?.getData(WORKFLOW_TAB_DRAG_MIME)).not.toContain("session-1");
    expect(dragStart.dataTransfer?.getData(RUNTIME_SESSION_DRAG_MIME)).not.toContain("workspace-1");
    const chromeDragOver = fakeDragEvent({ exposeGetData: false });

    expect(readWorkflowTabDrag(chromeDragOver, "workspace-1")).toBe("session:session-1");
    expect(readRuntimeSessionDrag(chromeDragOver, "workspace-1")).toBe("session-1");
  });

  it("rejects active drags from another workspace", () => {
    startRuntimeSessionDrag(fakeDragEvent(), {
      workspaceId: "workspace-1",
      sessionKey: "session-1",
    });

    expect(readRuntimeSessionDrag(fakeDragEvent({ exposeGetData: false }), "workspace-2")).toBeNull();
  });

  it("clears its payloads when the shared tabbed-panel drag ends elsewhere", () => {
    // A workflow tab drag starts two payloads: this module's and the shared
    // tabbed-panel one. A drop in a detail pane clears only the shared payload
    // and destroys the source tab, so its dragend - the only other clear - never
    // fires, and the next unrelated drag would read the stale session through
    // the active-payload fallback.
    startWorkflowTabDrag(fakeDragEvent(), {
      workspaceId: "workspace-1",
      tabKey: "session:session-1",
    });
    expect(readRuntimeSessionDrag(fakeDragEvent({ exposeGetData: false }), "workspace-1")).toBe("session-1");

    clearActiveTabbedPanelDrag();

    expect(readRuntimeSessionDrag(fakeDragEvent({ exposeGetData: false }), "workspace-1")).toBeNull();
    expect(readWorkflowTabDrag(fakeDragEvent({ exposeGetData: false }), "workspace-1")).toBeNull();
  });
});

function fakeDragEvent(options: { exposeGetData?: boolean } = {}): DragEvent {
  const data = new Map<string, string>();
  const exposeGetData = options.exposeGetData ?? true;
  return {
    dataTransfer: {
      dropEffect: "none",
      effectAllowed: "none",
      getData: (type: string) => (exposeGetData ? (data.get(type) ?? "") : ""),
      setData: (type: string, value: string) => {
        data.set(type, value);
      },
    },
  } as unknown as DragEvent;
}
