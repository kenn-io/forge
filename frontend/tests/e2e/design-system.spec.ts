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

test("design system page renders chip matrix with shared styles", async ({ page }) => {
  await page.goto("/design-system");

  await expect(page.getByRole("heading", { name: "Design system" })).toBeVisible();

  const smGreenChip = page
    .locator('[data-size="sm"] .chip--green', {
      hasText: "Green",
    })
    .first();
  const mdGreenChip = page
    .locator('[data-size="md"] .chip--green', {
      hasText: "Green",
    })
    .first();
  const mutedChip = page
    .locator(".chip--muted", {
      hasText: "Muted",
    })
    .first();
  const plainCaseChip = page.getByText("plain case", { exact: true }).first();
  const descenderChip = page.getByText("team/inbox-view", { exact: true }).first();
  const interactiveChip = page
    .getByRole("button", {
      name: "Interactive",
    })
    .first();

  await expect(smGreenChip).toBeVisible();
  await expect(mdGreenChip).toBeVisible();
  await expect(mutedChip).toBeVisible();
  await expect(plainCaseChip).toBeVisible();
  await expect(descenderChip).toBeVisible();
  await expect(interactiveChip).toBeVisible();

  const styles = await Promise.all([
    smGreenChip.evaluate((node) => {
      const styles = getComputedStyle(node);
      return {
        minHeight: styles.minHeight,
        fontSize: styles.fontSize,
        paddingInline: `${styles.paddingLeft}/${styles.paddingRight}`,
      };
    }),
    mdGreenChip.evaluate((node) => {
      const styles = getComputedStyle(node);
      return {
        minHeight: styles.minHeight,
        fontSize: styles.fontSize,
        paddingInline: `${styles.paddingLeft}/${styles.paddingRight}`,
        backgroundColor: styles.backgroundColor,
        textTransform: styles.textTransform,
      };
    }),
    mutedChip.evaluate((node) => {
      const styles = getComputedStyle(node);
      return {
        backgroundColor: styles.backgroundColor,
      };
    }),
    plainCaseChip.evaluate((node) => {
      const styles = getComputedStyle(node);
      return {
        textTransform: styles.textTransform,
        letterSpacing: styles.letterSpacing,
      };
    }),
    descenderChip.evaluate((node) => {
      const chip = node.closest(".chip");
      const chipBox = chip?.getBoundingClientRect();
      return {
        chipHeight: chipBox?.height ?? 0,
      };
    }),
    interactiveChip.evaluate((node) => {
      const styles = getComputedStyle(node);
      return {
        cursor: styles.cursor,
      };
    }),
  ]);

  expect(styles[0].minHeight).toBe("18px");
  expect(styles[0].fontSize).toBe("10px");
  expect(styles[0].paddingInline).toBe("6px/6px");
  expect(styles[1].minHeight).toBe("22px");
  expect(styles[1].fontSize).toBe("11px");
  expect(styles[1].paddingInline).toBe("8px/8px");
  expect(styles[1].backgroundColor).not.toBe("rgba(0, 0, 0, 0)");
  expect(styles[1].textTransform).toBe("uppercase");
  expect(styles[2].backgroundColor).not.toBe("rgba(0, 0, 0, 0)");
  expect(styles[3].textTransform).toBe("none");
  expect(styles[3].letterSpacing).toBe("normal");
  expect(styles[4].chipHeight).toBe(18);
  expect(styles[5].cursor).toBe("pointer");
});

test("design system page renders panel and typeahead examples", async ({ page }) => {
  await page.goto("/design-system");

  await expect(page.getByRole("heading", { name: "Tabbed workspace panels" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Typeahead dropdown states" })).toBeVisible();

  const panelDemo = page.getByTestId("design-system-tabbed-panel-demo");
  await expect(panelDemo).toBeVisible();
  await expect(panelDemo.locator(".tabbed-panel-tab-tool")).toHaveCount(0);
  await expect(page.getByTestId("design-system-panel-overview")).toBeVisible();
  const selectedTabMetrics = await panelDemo.locator('[data-tabbed-panel-tab-key="overview"]').evaluate((tab) => {
    const styles = getComputedStyle(tab);
    const tabBar = tab.parentElement;
    if (!tabBar) {
      throw new Error("Missing tab bar for selected tab");
    }
    const tabBarStyles = getComputedStyle(tabBar);
    const tabBarDividerStyles = getComputedStyle(tabBar, "::after");
    const tabRect = tab.getBoundingClientRect();
    const tabBarRect = tabBar.getBoundingClientRect();
    return {
      borderBottomWidth: styles.borderBottomWidth,
      marginBottom: styles.marginBottom,
      tabAfterContent: getComputedStyle(tab, "::after").content,
      tabBarBorderBottomWidth: tabBarStyles.borderBottomWidth,
      tabBarDividerHeight: tabBarDividerStyles.height,
      tabExtendsBelowBar: Math.round(tabRect.bottom - tabBarRect.bottom),
      zIndex: styles.zIndex,
    };
  });
  expect(selectedTabMetrics).toEqual({
    borderBottomWidth: "0px",
    marginBottom: "-1px",
    tabAfterContent: "none",
    tabBarBorderBottomWidth: "0px",
    tabBarDividerHeight: "1px",
    tabExtendsBelowBar: 1,
    zIndex: "2",
  });
  const splitMetrics = await panelDemo
    .locator('[aria-label="Resize design system panel split"]')
    .evaluate((divider) => {
      const split = divider.closest(".tabbed-panel-split");
      const firstLeaf = split?.querySelector(".tabbed-panel-split-child.first > .tabbed-panel-leaf");
      const secondLeaf = split?.querySelector(".tabbed-panel-split-child.second > .tabbed-panel-leaf");
      const dividerBox = divider.getBoundingClientRect();
      const dividerStyles = getComputedStyle(divider);
      const firstLeafStyles = firstLeaf ? getComputedStyle(firstLeaf) : null;
      const secondLeafStyles = secondLeaf ? getComputedStyle(secondLeaf) : null;
      return {
        dividerWidth: Math.round(dividerBox.width),
        dividerPaddingInline: `${dividerStyles.paddingLeft}/${dividerStyles.paddingRight}`,
        firstLeafBorderTop: firstLeafStyles?.borderTopWidth,
        firstLeafBorderRight: firstLeafStyles?.borderRightWidth,
        secondLeafBorderTop: secondLeafStyles?.borderTopWidth,
        secondLeafBorderLeft: secondLeafStyles?.borderLeftWidth,
      };
    });
  expect(splitMetrics).toEqual({
    dividerWidth: 3,
    dividerPaddingInline: "0px/0px",
    firstLeafBorderTop: "0px",
    firstLeafBorderRight: "0px",
    secondLeafBorderTop: "0px",
    secondLeafBorderLeft: "0px",
  });

  await panelDemo.getByRole("tab", { name: /Activity/ }).click();
  await expect(page.getByTestId("design-system-panel-activity")).toBeVisible();
  const activityScrollMetrics = await page.getByTestId("design-system-panel-activity").evaluate((article) => {
    const panel = article.parentElement;
    if (!panel) {
      throw new Error("Missing tab panel for activity article");
    }
    panel.scrollTop = 0;
    const styles = getComputedStyle(panel);
    const before = panel.scrollTop;
    panel.scrollTop = 48;
    return {
      overflowY: styles.overflowY,
      scrollHeight: panel.scrollHeight,
      clientHeight: panel.clientHeight,
      scrollTopChanged: panel.scrollTop > before,
    };
  });
  expect(activityScrollMetrics.overflowY).toBe("auto");
  expect(activityScrollMetrics.scrollHeight).toBeGreaterThan(activityScrollMetrics.clientHeight);
  expect(activityScrollMetrics.scrollTopChanged).toBe(true);

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
