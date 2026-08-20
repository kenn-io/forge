import { describe, expect, it } from "vite-plus/test";
import { SETTINGS_PANELS } from "./settingsPanels.js";

describe("settings panels", () => {
  it("keeps Kata mappings available without a full Kata mode", () => {
    expect(SETTINGS_PANELS.some((panel) => panel.id === "settings-kata-projects")).toBe(true);
  });

  it("includes the MCP companion", () => {
    expect(SETTINGS_PANELS.some((panel) => panel.id === "settings-mcp")).toBe(true);
  });
});
