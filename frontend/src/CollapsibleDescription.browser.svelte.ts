import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "./app.css";
import CollapsibleDescriptionBrowserFixture from "./test/CollapsibleDescriptionBrowserFixture.svelte";

describe("collapsible description browser layout", () => {
  it("creates a 320px vertical scroll container when collapsed", async () => {
    await page.viewport(1280, 900);
    const { container, unmount } = render(CollapsibleDescriptionBrowserFixture);

    try {
      const expandedCard = container.querySelector(".detail-description-card");
      expect(expandedCard).not.toBeNull();
      const expandedHeight = (expandedCard as Element).clientHeight;

      await page.getByRole("button", { name: "Collapse description" }).click();

      const card = container.querySelector(".detail-description-card--compact");
      expect(card).not.toBeNull();

      const style = getComputedStyle(card as Element);
      expect(style.maxHeight).toBe("320px");
      expect(style.overflowY).toBe("auto");
      expect((card as Element).scrollHeight).toBeGreaterThan((card as Element).clientHeight);

      (card as Element).scrollTop = 100;
      expect((card as Element).scrollTop).toBeGreaterThan(0);

      const collapsedHeight = (card as Element).clientHeight;
      await page.getByRole("button", { name: "Expand description" }).click();

      const restoredCard = container.querySelector(".detail-description-card");
      expect(restoredCard).not.toBeNull();
      expect((restoredCard as Element).clientHeight).toBe(expandedHeight);
      expect((restoredCard as Element).clientHeight).toBeGreaterThan(collapsedHeight);
    } finally {
      unmount();
    }
  });

  it("aligns the copy control with the card's right edge on mobile", async () => {
    await page.viewport(390, 844);
    const { container, unmount } = render(CollapsibleDescriptionBrowserFixture);

    try {
      const copyButton = container.querySelector(".kit-copy-btn.body-copy");
      const card = container.querySelector(".detail-description-card");
      expect(copyButton).not.toBeNull();
      expect(card).not.toBeNull();

      const copyRight = (copyButton as Element).getBoundingClientRect().right;
      const cardRight = (card as Element).getBoundingClientRect().right;
      expect(Math.abs(copyRight - cardRight)).toBeLessThanOrEqual(1);
    } finally {
      unmount();
    }
  });
});
