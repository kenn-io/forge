import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import { pressKey } from "../../../test/browserAppHarness.js";
import DatePicker from "./DatePicker.svelte";

describe("DatePicker (browser)", () => {
  it("restores focus to the trigger when Escape closes the calendar", async () => {
    render(DatePicker, {
      props: {
        value: "2026-07-15",
        onchange: vi.fn(),
        ariaLabel: "Due date",
      },
    });

    const trigger = page.getByRole("button", { name: /Due date:/ });
    await trigger.click();

    const dialog = page.getByRole("dialog", { name: "Due date" });
    await expect.element(dialog).toBeVisible();

    const dialogElement = dialog.element() as HTMLElement;
    dialogElement.focus();
    expect(document.activeElement).toBe(dialogElement);

    pressKey("Escape", {}, dialogElement);

    await expect.element(dialog).not.toBeInTheDocument();
    expect(document.activeElement).toBe(trigger.element());
  });
});
