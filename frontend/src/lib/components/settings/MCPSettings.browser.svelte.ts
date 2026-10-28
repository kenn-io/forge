import { describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { MCPSettings as MCPSettingsType } from "../../api/types.js";
import MCPSettingsBrowserHarness from "./MCPSettingsBrowserHarness.svelte";

describe("MCPSettings numeric inputs (browser)", () => {
  it("refuses a port outside 0 to 65535 and accepts the top of that range", async () => {
    const mcp: MCPSettingsType = {
      enabled: false,
      port: 9092,
      restart_required: false,
      active_requires_auth: false,
    };
    await render(MCPSettingsBrowserHarness, {
      props: { mcp, onUpdate: vi.fn() },
    });

    const port = page.getByRole("spinbutton", { name: "Port", exact: true });
    const save = page.getByRole("button", { name: "Save MCP companion" });
    await port.click();
    await userEvent.keyboard("-");

    await expect.element(port).toHaveValue("9092");
    await expect.element(save).toBeDisabled();

    await port.fill("65536");
    await expect.element(save).toBeDisabled();

    await port.fill("65535");
    await expect.element(save).not.toBeDisabled();
  });
});
