import { beforeEach, describe, expect, it } from "vite-plus/test";
import {
  getSessionSlotElement,
  isSessionMounted,
  isSessionSlotVisible,
  mountedSessions,
  noteSessionMounted,
  noteSessionUnmounted,
  registerSessionSlot,
  resetSessionHostForTest,
  sessionHostKey,
  setSessionSlotVisible,
} from "./session-host.svelte.ts";

const agentOnA = sessionHostKey("ws-1", undefined, "agent", "2026-01-01T00:00:00Z");

describe("session host registry", () => {
  beforeEach(() => resetSessionHostForTest());

  it("keys a session by workspace, host, session, and generation", () => {
    // Two fleet hosts can serve the same workspace id, and two workspaces can
    // both have a session called "agent": neither may share a live terminal.
    expect(agentOnA).not.toBe(sessionHostKey("ws-1", "build", "agent", "2026-01-01T00:00:00Z"));
    expect(agentOnA).not.toBe(sessionHostKey("ws-2", undefined, "agent", "2026-01-01T00:00:00Z"));
    // Nor may a relaunched session inherit the dead generation's subtree and its
    // closed socket.
    expect(agentOnA).not.toBe(sessionHostKey("ws-1", undefined, "agent", "2026-02-02T00:00:00Z"));
    expect(agentOnA).toBe(sessionHostKey("ws-1", undefined, "agent", "2026-01-01T00:00:00Z"));
  });

  it("keeps parts that contain the separator distinct", () => {
    // A workspace id is opaque; it must not be able to spell another key.
    expect(sessionHostKey("a/b", undefined, "agent", "g")).not.toBe(sessionHostKey("a", "b", "agent", "g"));
  });

  it("registers and clears one slot per session key", () => {
    const el = document.createElement("div");
    registerSessionSlot(agentOnA, el);
    expect(getSessionSlotElement(agentOnA)).toBe(el);
    expect(getSessionSlotElement(sessionHostKey("ws-1", undefined, "shell", "g"))).toBeNull();
    registerSessionSlot(agentOnA, null);
    expect(getSessionSlotElement(agentOnA)).toBeNull();
  });

  it("reports a mounted-but-hidden slot as not visible", () => {
    // An inactive tab panel keeps its slot in the DOM under visibility:hidden. A
    // terminal that stays active there fights the visible one for keystrokes.
    registerSessionSlot(agentOnA, document.createElement("div"));
    setSessionSlotVisible(agentOnA, false);
    expect(getSessionSlotElement(agentOnA)).not.toBeNull();
    expect(isSessionSlotVisible(agentOnA)).toBe(false);

    setSessionSlotVisible(agentOnA, true);
    expect(isSessionSlotVisible(agentOnA)).toBe(true);
  });

  it("reports a session with no slot as not visible", () => {
    expect(isSessionSlotVisible(agentOnA)).toBe(false);
    // A slot that unregisters while still marked visible must not leave the
    // terminal active in the parking area.
    setSessionSlotVisible(agentOnA, true);
    expect(isSessionSlotVisible(agentOnA)).toBe(false);
  });

  it("clears visibility when the slot unregisters", () => {
    registerSessionSlot(agentOnA, document.createElement("div"));
    setSessionSlotVisible(agentOnA, true);
    registerSessionSlot(agentOnA, null);
    registerSessionSlot(agentOnA, document.createElement("div"));
    // Re-registering must not resurrect the previous visibility: the new slot
    // says whether it is on screen.
    expect(isSessionSlotVisible(agentOnA)).toBe(false);
  });

  it("tracks mounted sessions and updates a changed status in place", () => {
    noteSessionMounted({ hostKey: agentOnA, websocketPath: "/ws/agent", status: "starting" });
    noteSessionMounted({ hostKey: agentOnA, websocketPath: "/ws/agent", status: "running" });
    expect(mountedSessions()).toHaveLength(1);
    expect(mountedSessions()[0]?.status).toBe("running");
    expect(isSessionMounted(agentOnA)).toBe(true);

    noteSessionUnmounted(agentOnA);
    expect(mountedSessions()).toHaveLength(0);
    expect(isSessionMounted(agentOnA)).toBe(false);
  });

  it("drops the slot of an unmounted session", () => {
    noteSessionMounted({ hostKey: agentOnA, websocketPath: "/ws/agent", status: "running" });
    registerSessionSlot(agentOnA, document.createElement("div"));
    noteSessionUnmounted(agentOnA);
    // The terminal is gone, so a stale slot would have the pool reparenting a
    // subtree that no longer exists.
    expect(getSessionSlotElement(agentOnA)).toBeNull();
  });
});
