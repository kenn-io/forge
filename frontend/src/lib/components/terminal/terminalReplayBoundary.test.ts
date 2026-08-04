import { describe, expect, it } from "vite-plus/test";

import { supportsReplayBoundary } from "./terminalReplayBoundary.js";

describe("supportsReplayBoundary", () => {
  it.each([
    [false, "/ws/v1/workspaces/ws-1/terminal"],
    [false, "/api/v1/workspaces/ws-1/terminal?cols=80"],
    [true, "/ws/v1/workspaces/ws-1/runtime/sessions/shell/terminal"],
    [false, "/ws/v1/fleet/hosts/peer/workspaces/ws-1/terminal"],
    [false, "/ws/v1/fleet/hosts/peer/workspaces/ws-1/runtime/sessions/shell/terminal"],
    [false, "/ws/v1/workspaces/ws-1/files"],
  ] as const)("returns %s for %s", (expected, path) => {
    expect(supportsReplayBoundary(path)).toBe(expected);
  });

  it("returns false for a missing or malformed path", () => {
    expect(supportsReplayBoundary(undefined)).toBe(false);
    expect(supportsReplayBoundary(null)).toBe(false);
    expect(supportsReplayBoundary("http://[invalid")).toBe(false);
  });
});
