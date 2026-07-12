import { describe, expect, it } from "vite-plus/test";
import { settingsPanelsForModes } from "./settingsPanels.js";

describe("settingsPanelsForModes", () => {
  it("includes Kata mappings only while Kata mode is enabled", () => {
    expect(settingsPanelsForModes(false).some((panel) => panel.id === "settings-kata-projects")).toBe(false);
    expect(settingsPanelsForModes(true).some((panel) => panel.id === "settings-kata-projects")).toBe(true);
  });
});
