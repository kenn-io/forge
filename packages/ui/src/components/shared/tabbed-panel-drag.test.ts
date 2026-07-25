import { beforeEach, describe, expect, it } from "vitest";
import { clearActiveTabbedPanelDrag, readTabbedPanelTabDrag, startTabbedPanelTabDrag } from "./tabbed-panel-drag.js";

/**
 * jsdom has no DataTransfer, and the real one is write-only during dragstart
 * anyway, so both sides share this store the way a browser's drag session does.
 */
function dragEvent(store: Map<string, string>): DragEvent {
  return {
    dataTransfer: {
      effectAllowed: "none",
      setData: (mime: string, value: string) => store.set(mime, value),
      getData: (mime: string) => store.get(mime) ?? "",
    },
  } as unknown as DragEvent;
}

describe("tabbed panel tab drag scope", () => {
  beforeEach(() => {
    clearActiveTabbedPanelDrag();
  });

  it("accepts a drop only in the scope the drag started in", () => {
    const store = new Map<string, string>();
    startTabbedPanelTabDrag(dragEvent(store), { scope: "detail:prs", tabKey: "files" });

    expect(readTabbedPanelTabDrag(dragEvent(store), "detail:prs")).toBe("files");
    // Scope comparison is plain string equality, so every scope has to be
    // namespaced: a workspace id equal to a bare surface key would let a detail
    // pane land in the Workspaces tree.
    expect(readTabbedPanelTabDrag(dragEvent(store), "workspace:prs")).toBeNull();
    expect(readTabbedPanelTabDrag(dragEvent(store), "prs")).toBeNull();
    expect(readTabbedPanelTabDrag(dragEvent(store), "detail:issues")).toBeNull();
  });

  it("rejects a workspace-scoped drag in every detail surface", () => {
    const store = new Map<string, string>();
    startTabbedPanelTabDrag(dragEvent(store), { scope: "workspace:ws-1", tabKey: "home" });

    expect(readTabbedPanelTabDrag(dragEvent(store), "workspace:ws-1")).toBe("home");
    expect(readTabbedPanelTabDrag(dragEvent(store), "workspace:ws-2")).toBeNull();
    expect(readTabbedPanelTabDrag(dragEvent(store), "detail:prs")).toBeNull();
  });

  it("rejects a stale drag whose token no longer matches the active one", () => {
    const first = new Map<string, string>();
    startTabbedPanelTabDrag(dragEvent(first), { scope: "detail:prs", tabKey: "files" });
    const second = new Map<string, string>();
    startTabbedPanelTabDrag(dragEvent(second), { scope: "detail:prs", tabKey: "workspace" });

    // The first drag's own event still carries its old token; honoring it would
    // move the tab from a drag the user already abandoned.
    expect(readTabbedPanelTabDrag(dragEvent(first), "detail:prs")).toBeNull();
    expect(readTabbedPanelTabDrag(dragEvent(second), "detail:prs")).toBe("workspace");
  });
});
