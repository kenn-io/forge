import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("design system page renders chip matrix with shared styles", async ({ page }) => {
  await page.goto("/design-system");

  await expect(
    page.getByRole("heading", { name: "Design system" }),
  ).toBeVisible();

  const smGreenChip = page.locator('[data-size="sm"] .chip--green', {
    hasText: "Green",
  }).first();
  const mdGreenChip = page.locator('[data-size="md"] .chip--green', {
    hasText: "Green",
  }).first();
  const mutedChip = page.locator(".chip--muted", {
    hasText: "Muted",
  }).first();
  const plainCaseChip = page.getByText("plain case", { exact: true }).first();
  const descenderChip = page.getByText("kenn-io/msgvault", { exact: true })
    .first();
  const interactiveChip = page.getByRole("button", {
    name: "Interactive",
  }).first();

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

test("chip descenders render without clipping", async ({ page }, testInfo) => {
  test.skip(
    process.env.MIDDLEMAN_VISUAL_E2E !== "1",
    "Set MIDDLEMAN_VISUAL_E2E=1 to run chip visual snapshots.",
  );
  test.skip(
    testInfo.project.name !== "chromium",
    "Chip visual snapshot is Chromium-only.",
  );

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
