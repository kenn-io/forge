import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("renders mocked frontend data", async ({ page }) => {
  await page.goto("/pulls");

  await expect(page.getByText("Add browser regression coverage")).toBeVisible();
  await expect(page.getByText("acme/widgets")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("3 PRs")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("1 repos")).toBeVisible();

  await page.getByRole("button", { name: "Issues" }).click();

  await expect(page.getByText("Theme toggle does not stick")).toBeVisible();
  await expect(page.getByRole("contentinfo").getByText("1 issues")).toBeVisible();
});

test("toggles dark mode from the header control", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");

  const root = page.locator("html");
  const button = page.getByTitle("Toggle theme");
  const settingsButton = page.getByTitle("Settings");
  const icon = button.locator("svg");

  await expect(root).not.toHaveClass(/dark/);
  await expect(icon).toBeVisible();
  await expect(settingsButton.locator("svg")).toBeVisible();

  await expect(button).toHaveJSProperty("tagName", "BUTTON");
  await expect(settingsButton).toHaveJSProperty("tagName", "BUTTON");

  const themeLayout = await button.evaluate((node) => {
    const style = getComputedStyle(node);
    return {
      display: style.display,
      alignItems: style.alignItems,
      justifyContent: style.justifyContent,
    };
  });
  const settingsLayout = await settingsButton.evaluate((node) => {
    const style = getComputedStyle(node);
    return {
      display: style.display,
      alignItems: style.alignItems,
      justifyContent: style.justifyContent,
    };
  });

  expect(themeLayout).toEqual({
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
  });
  expect(settingsLayout).toEqual({
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
  });

  const moonPathFill = await button.evaluate((node) => {
    const path = node.querySelector("[data-filled-icon='moon'] svg path");
    return path ? getComputedStyle(path).fill : null;
  });
  const moonPathStroke = await button.evaluate((node) => {
    const path = node.querySelector("[data-filled-icon='moon'] svg path");
    return path ? getComputedStyle(path).stroke : null;
  });

  expect(moonPathFill).not.toBe("none");
  expect(moonPathStroke).toBe("none");

  const before = await icon.innerHTML();

  await button.click();
  await expect(root).toHaveClass(/dark/);

  await expect(button.locator("svg")).toBeVisible();

  const after = await button.locator("svg").innerHTML();

  expect(after).not.toBe(before);

  await button.click();
  await expect(root).not.toHaveClass(/dark/);
  await expect(button.locator("svg")).toBeVisible();
});
