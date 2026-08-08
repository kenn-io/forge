import { describe, expect, it } from "vitest";
import {
  isSessionPaneKey,
  parseSessionPaneKey,
  sessionPaneKey,
  sessionPaneKeyMatchesWorkspace,
} from "./session-pane-key.js";

describe("session pane keys", () => {
  it("round-trips a session key that contains the separator characters", () => {
    // Session keys are opaque and routinely contain colons; a colon-separated
    // pane key could not be parsed back.
    const key = sessionPaneKey("ws-1", undefined, "ws-1:helper");
    expect(parseSessionPaneKey(key)).toEqual({
      workspaceId: "ws-1",
      hostKey: undefined,
      sessionKey: "ws-1:helper",
    });
  });

  it("keeps parts that contain the separator distinct", () => {
    // Two different sessions must never spell one key: that would alias their
    // placements and their cleanup.
    expect(sessionPaneKey("a/b", undefined, "s")).not.toBe(sessionPaneKey("a", "b", "s"));
    expect(sessionPaneKey("ws-1", "build", "agent")).not.toBe(sessionPaneKey("ws-1", undefined, "agent"));
  });

  it("treats the empty host segment as the provider default", () => {
    const key = sessionPaneKey("ws-1", undefined, "agent");
    expect(parseSessionPaneKey(key)?.hostKey).toBeUndefined();
    expect(parseSessionPaneKey(sessionPaneKey("ws-1", "build", "agent"))?.hostKey).toBe("build");
  });

  it("rejects malformed keys rather than keeping them forever", () => {
    expect(parseSessionPaneKey("conversation")).toBeNull();
    expect(parseSessionPaneKey("session:only-two/parts")).toBeNull();
    expect(parseSessionPaneKey("session:a/b/c/d")).toBeNull();
    // Undecodable percent escape.
    expect(parseSessionPaneKey("session:ws-1//%zz")).toBeNull();
    // Semantically empty: a workspace or session segment that names nothing.
    // Only the host may be empty.
    expect(parseSessionPaneKey("session://agent")).toBeNull();
    expect(parseSessionPaneKey("session:ws-1//")).toBeNull();
    expect(isSessionPaneKey("session:bogus")).toBe(false);
  });

  it("matches a key against a workspace and host", () => {
    const key = sessionPaneKey("ws-1", "build", "agent");
    expect(sessionPaneKeyMatchesWorkspace(key, "ws-1", "build")).toBe(true);
    expect(sessionPaneKeyMatchesWorkspace(key, "ws-1", undefined)).toBe(false);
    expect(sessionPaneKeyMatchesWorkspace(key, "ws-2", "build")).toBe(false);
    expect(sessionPaneKeyMatchesWorkspace("conversation", "ws-1", "build")).toBe(false);
  });
});
