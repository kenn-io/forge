import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

async function dragDesignSystemPanelTab(page: Page, sourceTabKey: string, targetTabKey: string): Promise<void> {
  await page.evaluate(
    ({ sourceTabKey, targetTabKey }) => {
      const demo = document.querySelector('[data-testid="design-system-tabbed-panel-demo"]');
      const source = demo?.querySelector(`[data-tabbed-panel-tab-key="${sourceTabKey}"] [role="tab"]`);
      const target = demo?.querySelector(`[data-tabbed-panel-tab-key="${targetTabKey}"]`);
      if (!(source instanceof HTMLElement)) {
        throw new Error(`Missing panel tab: ${sourceTabKey}`);
      }
      if (!(target instanceof HTMLElement)) {
        throw new Error(`Missing panel tab: ${targetTabKey}`);
      }

      const transfer = new DataTransfer();
      source.dispatchEvent(
        new DragEvent("dragstart", {
          bubbles: true,
          cancelable: true,
          dataTransfer: transfer,
        }),
      );

      const rect = target.getBoundingClientRect();
      const clientX = rect.left + Math.max(1, rect.width * 0.25);
      const clientY = rect.top + rect.height / 2;
      target.dispatchEvent(
        new DragEvent("dragover", {
          bubbles: true,
          cancelable: true,
          clientX,
          clientY,
          dataTransfer: transfer,
        }),
      );
      target.dispatchEvent(
        new DragEvent("drop", {
          bubbles: true,
          cancelable: true,
          clientX,
          clientY,
          dataTransfer: transfer,
        }),
      );
      source.dispatchEvent(
        new DragEvent("dragend", {
          bubbles: true,
          cancelable: true,
          dataTransfer: transfer,
        }),
      );
    },
    { sourceTabKey, targetTabKey },
  );
}

async function dragDesignSystemPanelTabToPanelEdge(
  page: Page,
  sourceTabKey: string,
  targetPanelTestID: string,
  edge: "right" | "bottom",
): Promise<void> {
  await page.evaluate(
    ({ sourceTabKey, targetPanelTestID, edge }) => {
      const demo = document.querySelector('[data-testid="design-system-tabbed-panel-demo"]');
      const source = demo?.querySelector(`[data-tabbed-panel-tab-key="${sourceTabKey}"] [role="tab"]`);
      const targetPanel = demo?.querySelector(`[data-testid="${targetPanelTestID}"]`);
      const target = targetPanel?.closest(".tabbed-panel-body");
      if (!(source instanceof HTMLElement)) {
        throw new Error(`Missing panel tab: ${sourceTabKey}`);
      }
      if (!(target instanceof HTMLElement)) {
        throw new Error(`Missing panel body: ${targetPanelTestID}`);
      }

      const transfer = new DataTransfer();
      source.dispatchEvent(
        new DragEvent("dragstart", {
          bubbles: true,
          cancelable: true,
          dataTransfer: transfer,
        }),
      );

      const rect = target.getBoundingClientRect();
      const clientX = edge === "right" ? rect.right - 1 : rect.left + rect.width / 2;
      const clientY = edge === "bottom" ? rect.bottom - 1 : rect.top + rect.height / 2;
      target.dispatchEvent(
        new DragEvent("dragover", {
          bubbles: true,
          cancelable: true,
          clientX,
          clientY,
          dataTransfer: transfer,
        }),
      );
      target.dispatchEvent(
        new DragEvent("drop", {
          bubbles: true,
          cancelable: true,
          clientX,
          clientY,
          dataTransfer: transfer,
        }),
      );
      source.dispatchEvent(
        new DragEvent("dragend", {
          bubbles: true,
          cancelable: true,
          dataTransfer: transfer,
        }),
      );
    },
    { sourceTabKey, targetPanelTestID, edge },
  );
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

// The chip-matrix render facts and the tabbed-panel selected-tab / split-divider
// / scroll-overflow layout metrics that used to live here now run at the browser
// test tier, which mounts the real Chip and TabbedPanelTree primitives directly
// in Chromium:
//   src/lib/components/design-system/DesignSystemChips.browser.svelte.ts
//   src/lib/components/design-system/DesignSystemPanel.browser.svelte.ts
// Those facts need no /design-system route or app shell, so they no longer run
// as full e2e. This spec keeps only what genuinely needs the live route: the
// drag-to-split / drag-to-reorder mutations, the store-backed RepoTypeahead
// dropdown, the chip visual snapshot, and route-level keyboard handling.

test("design system panel supports drag mutations and typeahead dropdown states", async ({ page }) => {
  await page.goto("/design-system");

  await expect(page.getByRole("heading", { name: "Tabbed workspace panels" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Typeahead dropdown states" })).toBeVisible();

  const panelDemo = page.getByTestId("design-system-tabbed-panel-demo");
  await expect(panelDemo).toBeVisible();
  await expect(panelDemo.locator(".tabbed-panel-tab-tool")).toHaveCount(0);
  await expect(page.getByTestId("design-system-panel-overview")).toBeVisible();

  await panelDemo.getByRole("tab", { name: /Activity/ }).click();
  await expect(page.getByTestId("design-system-panel-activity")).toBeVisible();

  const rootFirstChild = panelDemo.locator(".tabbed-panel-split-child.first").first();
  const rootDivider = panelDemo.locator('[aria-label="Resize design system panel split"]').first();
  const beforeResize = await rootFirstChild.boundingBox();
  const dividerBox = await rootDivider.boundingBox();
  if (!beforeResize || !dividerBox) {
    throw new Error("Missing split geometry for resize assertion");
  }
  await page.mouse.move(dividerBox.x + dividerBox.width / 2, dividerBox.y + dividerBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(dividerBox.x - 90, dividerBox.y + dividerBox.height / 2);
  await page.mouse.up();
  await expect.poll(async () => (await rootFirstChild.boundingBox())?.width ?? 0).toBeLessThan(beforeResize.width - 24);

  await dragDesignSystemPanelTabToPanelEdge(page, "activity", "design-system-panel-overview", "right");
  await expect(panelDemo.locator(".tabbed-panel-leaf")).toHaveCount(3);
  await expect(page.getByTestId("design-system-panel-activity")).toBeVisible();
  const nestedSplitMetrics = await page.getByTestId("design-system-panel-activity").evaluate((article) => {
    const leaf = article.closest(".tabbed-panel-leaf");
    if (!leaf) {
      throw new Error("Missing nested split leaf");
    }
    const styles = getComputedStyle(leaf);
    return {
      borderLeftWidth: styles.borderLeftWidth,
      borderRightWidth: styles.borderRightWidth,
    };
  });
  expect(nestedSplitMetrics).toEqual({
    borderLeftWidth: "0px",
    borderRightWidth: "0px",
  });

  await dragDesignSystemPanelTab(page, "terminal", "overview");
  await expect(panelDemo.locator(".tabbed-panel-leaf")).toHaveCount(2);
  await expect(page.getByTestId("design-system-panel-terminal")).toBeVisible();
  await expect(panelDemo.locator('[data-tabbed-panel-tab-key="terminal"] [role="tab"]')).toHaveAttribute(
    "aria-selected",
    "true",
  );
  const tabOrder = await panelDemo
    .locator("[data-tabbed-panel-tab-key]")
    .evaluateAll((tabs) => tabs.map((tab) => tab.getAttribute("data-tabbed-panel-tab-key")));
  expect(tabOrder).toEqual(["terminal", "overview", "activity"]);

  const typeaheadDemo = page.getByTestId("design-system-typeahead-demo");
  await expect(typeaheadDemo).toBeVisible();
  const openTypeahead = typeaheadDemo.getByTestId("typeahead-open");
  await expect(openTypeahead.getByRole("textbox", { name: "Filter repos" })).toBeVisible();
  await expect(openTypeahead.getByRole("option", { name: /acme\/widgets/ })).toBeVisible();

  const defaultTypeahead = typeaheadDemo.getByTestId("typeahead-default");
  await defaultTypeahead.getByRole("button", { name: /All repos/ }).click();
  const input = defaultTypeahead.getByRole("textbox", { name: "Filter repos" });
  await expect(input).toBeVisible();
  await input.fill("widgets");
  await expect(defaultTypeahead.getByRole("option", { name: /acme\/widgets/ })).toBeVisible();
  await input.fill("does-not-exist");
  await expect(defaultTypeahead.getByText("No matching repos")).toBeVisible();
});

test("chip descenders render without clipping", async ({ page }, testInfo) => {
  test.skip(process.env.MIDDLEMAN_VISUAL_E2E !== "1", "Set MIDDLEMAN_VISUAL_E2E=1 to run chip visual snapshots.");
  test.skip(testInfo.project.name !== "chromium", "Chip visual snapshot is Chromium-only.");

  await page.goto("/design-system");
  const descenderChip = page.getByTestId("descender-chip");

  await expect(descenderChip).toBeVisible();
  await expect(descenderChip).toHaveScreenshot("chip-descenders.png");
});

test("design system page ignores list keyboard navigation shortcuts", async ({ page }) => {
  await page.goto("/design-system");
  await expect(page.getByRole("heading", { name: "Design system" })).toBeVisible();

  await page.keyboard.press("j");
  await expect(page).toHaveURL(/\/design-system$/);

  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/design-system$/);
});
