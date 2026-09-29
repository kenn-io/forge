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
  ];

  for (const rail of rails) {
    await page.goto(rail.path);
    const scope = rail.path === "/workspaces" ? page.locator(".workspace-list-sidebar") : page;
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

for (const theme of ["dark", "light"] as const) {
  test(`${theme} grouped rows keep selected between the surface and hover`, async ({ page }) => {
    await page.addInitScript((name) => localStorage.setItem("kenn-forge-theme", name), theme);
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

    const [surface, selected, hover] = lightness as [number, number, number];
    expect(selected).toBeGreaterThan(Math.min(surface, hover));
    expect(selected).toBeLessThan(Math.max(surface, hover));
  });
}

test("light accents and muted text stay AA on hover and on their own tints", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("kenn-forge-theme", "light"));
  await page.goto("/pulls");

  const ratios = await page.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    const context = canvas.getContext("2d", { willReadFrequently: true })!;
    const luminance = (background: string) => {
      const sample = document.createElement("div");
      sample.style.background = background;
      document.body.append(sample);
      context.clearRect(0, 0, 1, 1);
      context.fillStyle = getComputedStyle(sample).backgroundColor;
      sample.remove();
      context.fillRect(0, 0, 1, 1);
      const [red, green, blue] = Array.from(context.getImageData(0, 0, 1, 1).data.slice(0, 3)).map((channel) => {
        const value = channel / 255;
        return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * red! + 0.7152 * green! + 0.0722 * blue!;
    };
    const contrast = (foreground: string, background: string) => {
      const [low, high] = [luminance(foreground), luminance(background)].sort((a, b) => a - b);
      return (high! + 0.05) / (low! + 0.05);
    };
    const results: Record<string, number> = {
      "text-muted on hover": contrast("var(--text-muted)", "var(--bg-surface-hover)"),
      "diff-stale-text on stale banner": contrast("var(--diff-stale-text)", "var(--diff-stale-bg)"),
    };
    for (const accent of ["blue", "amber", "purple", "green", "red", "teal"]) {
      const color = `var(--accent-${accent})`;
      results[`${accent} on hover`] = contrast(color, "var(--bg-surface-hover)");
      results[`${accent} on its 16% tint`] = contrast(color, `color-mix(in srgb, ${color} 16%, var(--bg-inset))`);
    }
    return results;
  });

  for (const [pair, ratio] of Object.entries(ratios)) {
    expect(ratio, pair).toBeGreaterThanOrEqual(4.5);
  }
});
