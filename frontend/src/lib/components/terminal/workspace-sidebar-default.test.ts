import { describe, expect, it } from "vite-plus/test";
import { defaultWorkspaceSidebarTab } from "./workspace-sidebar-default.js";

describe("defaultWorkspaceSidebarTab", () => {
  it.each([
    ["pull_request", "pr"],
    ["issue", "issue"],
  ] as const)("opens the source item for a %s workspace", (itemType, expected) => {
    expect(defaultWorkspaceSidebarTab("item", itemType)).toBe(expected);
  });

  it.each(["adhoc", "kata_task"] as const)("keeps %s workspaces on Diff", (itemType) => {
    expect(defaultWorkspaceSidebarTab("item", itemType)).toBe("diff");
  });

  it("uses Diff when configured", () => {
    expect(defaultWorkspaceSidebarTab("diff", "pull_request")).toBe("diff");
  });
});
