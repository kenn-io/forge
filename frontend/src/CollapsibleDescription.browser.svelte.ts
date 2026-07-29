import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "./app.css";
import CollapsibleDescriptionBrowserFixture from "./test/CollapsibleDescriptionBrowserFixture.svelte";

describe("collapsible description browser layout", () => {
  it("creates a 320px vertical scroll container when collapsed", async () => {
    const { container, unmount } = render(CollapsibleDescriptionBrowserFixture);

    try {
      await page.getByRole("button", { name: "Collapse description" }).click();

      const card = container.querySelector(".detail-description-card--compact");
      expect(card).not.toBeNull();

      const style = getComputedStyle(card as Element);
      expect(style.maxHeight).toBe("320px");
      expect(style.overflowY).toBe("auto");
    } finally {
      unmount();
    }
  });
});
