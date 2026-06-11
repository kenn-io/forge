import { describe, expect, it } from "vite-plus/test";

import { shouldUseFullAppShell } from "./appShell.js";

describe("app shell selection", () => {
  it("does not use full app startup for workspace embed pages", () => {
    expect(shouldUseFullAppShell("embed-workspace-list")).toBe(false);
    expect(shouldUseFullAppShell("embed-workspace-terminal")).toBe(false);
    expect(shouldUseFullAppShell("embed-workspace-detail")).toBe(false);
    expect(shouldUseFullAppShell("embed-workspace-empty")).toBe(false);
    expect(shouldUseFullAppShell("embed-workspace-first-run")).toBe(false);
    expect(shouldUseFullAppShell("embed-workspace-project")).toBe(false);
  });

  it("uses the full app shell for standalone pages", () => {
    expect(shouldUseFullAppShell("activity")).toBe(true);
    expect(shouldUseFullAppShell("kata")).toBe(true);
    expect(shouldUseFullAppShell("docs")).toBe(true);
    expect(shouldUseFullAppShell("messages")).toBe(true);
    expect(shouldUseFullAppShell("workspaces")).toBe(true);
    expect(shouldUseFullAppShell("terminal")).toBe(true);
  });
});
