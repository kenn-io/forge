import { describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { MCPSettings as MCPSettingsType } from "../../api/types.js";
import MCPSettingsBrowserHarness from "./MCPSettingsBrowserHarness.svelte";

describe("MCPSettings numeric inputs (browser)", () => {
  it("uses the number input constraints to gate saving", async () => {
    const mcp: MCPSettingsType = {
      enabled: false,
      port: 9092,
      restart_required: false,
      active_requires_auth: false,
    };
    render(MCPSettingsBrowserHarness, {
      props: { mcp, onUpdate: vi.fn() },
    });

    const port = page.getByRole("spinbutton", { name: "Port" });
    const save = page.getByRole("button", { name: "Save MCP companion" });
    await port.click();
    await userEvent.keyboard("-");

    await vi.waitFor(() => {
      const input = document.querySelector<HTMLInputElement>('input[max="65535"]');
      expect(input?.validity.badInput).toBe(true);
    });
    await expect.element(save).toBeDisabled();

    await port.fill("65536");
    await expect.element(save).toBeDisabled();

    await port.fill("65535");
    await expect.element(save).not.toBeDisabled();
  });
});
