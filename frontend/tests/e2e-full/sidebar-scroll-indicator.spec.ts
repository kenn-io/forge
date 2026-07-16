import { expect, test, type Locator } from "@playwright/test";

import { authoredScrollbarWidths } from "../support/scrollbarStyles";

async function constrainScrollArea(scrollArea: Locator): Promise<void> {
  await scrollArea.locator("..").evaluate((node) => {
    const root = node as HTMLElement;
    root.style.flex = "0 0 160px";
    root.style.height = "160px";
  });
}

test("grouped rail uses native scrollbars with sticky content", async ({ page, browserName }) => {
  await page.goto("/pulls");
  await expect(page.locator(".pull-item").first()).toBeVisible();

  const scrollArea = page.getByRole("region", { name: "Pull requests" });
  const stickyHeader = scrollArea.locator(".sidebar-group-header").first();
  await constrainScrollArea(scrollArea);

  const initialGeometry = await scrollArea.evaluate((node) => ({
    clientHeight: node.clientHeight,
    scrollHeight: node.scrollHeight,
    scrollbarColor: getComputedStyle(node).scrollbarColor,
    scrollbarWidth: getComputedStyle(node).scrollbarWidth,
    webkitWidth: getComputedStyle(node, "::-webkit-scrollbar").width,
  }));
  expect(initialGeometry.scrollHeight).toBeGreaterThan(initialGeometry.clientHeight);
  expect(initialGeometry.scrollbarColor).toBe("auto");
  expect(await authoredScrollbarWidths(scrollArea)).toEqual([]);
  if (browserName === "chromium") {
    expect(initialGeometry.scrollbarWidth).toBe("auto");
    expect(initialGeometry.webkitWidth).toBe("auto");
  }
  await expect(stickyHeader).toBeVisible();

  await scrollArea.focus();
  await page.keyboard.press("End");
  await expect.poll(() => scrollArea.evaluate((node) => node.scrollTop)).toBeGreaterThan(0);
});

test("grouped rails share labeled native scroll regions", async ({ page, browserName }) => {
  const rails = [
    { path: "/pulls", label: "Pull requests" },
    { path: "/issues", label: "Issues" },
    { path: "/workspaces", label: "Workspaces" },
    { path: "/kata", label: "Kata navigation" },
  ];

  for (const rail of rails) {
    await page.goto(rail.path);
    const scope =
      rail.path === "/workspaces"
        ? page.locator(".workspace-list-sidebar")
        : rail.path === "/kata"
          ? page.locator(".kata-sidebar")
          : page;
    const scrollArea = scope.getByRole("region", { name: rail.label, exact: true });
    await expect(scrollArea).toBeVisible();
    await expect(scrollArea).toHaveAttribute("tabindex", "0");
    await expect(scrollArea.locator("..").locator(".kit-scrollbox__indicator")).toHaveCount(0);
    const scrollbarStyles = await scrollArea.evaluate((node) => ({
      color: getComputedStyle(node).scrollbarColor,
      width: getComputedStyle(node).scrollbarWidth,
    }));
    expect(scrollbarStyles.color).toBe("auto");
    expect(await authoredScrollbarWidths(scrollArea)).toEqual([]);
    if (browserName === "chromium") expect(scrollbarStyles.width).toBe("auto");
  }
});

test("dark grouped rows keep selected between the surface and hover", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("middleman-theme", "dark"));
  await page.goto("/pulls");

  const lightness = await page.evaluate(() => {
    const colors = ["--sidebar-row-bg", "--bg-row-selected", "--sidebar-row-hover-bg"].map((token) => {
      const sample = document.createElement("div");
      sample.style.background = `var(${token})`;
      document.body.append(sample);
      const color = getComputedStyle(sample).backgroundColor;
      sample.remove();
      return color;
    });
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    const context = canvas.getContext("2d", { willReadFrequently: true })!;
    return colors.map((color) => {
      context.clearRect(0, 0, 1, 1);
      context.fillStyle = color;
      context.fillRect(0, 0, 1, 1);
      const [red, green, blue] = context.getImageData(0, 0, 1, 1).data;
      return 0.2126 * red! + 0.7152 * green! + 0.0722 * blue!;
    });
  });

  expect(lightness[1]).toBeGreaterThan(lightness[0]!);
  expect(lightness[1]).toBeLessThan(lightness[2]!);
});
