import { expect, test, type Locator } from "@playwright/test";

async function constrainScrollArea(scrollArea: Locator): Promise<void> {
  await scrollArea.locator("..").evaluate((node) => {
    const root = node as HTMLElement;
    root.style.flex = "0 0 160px";
    root.style.height = "160px";
  });
}

test("grouped rail scroll indicator floats above sticky headers", async ({ page }) => {
  await page.goto("/pulls");
  await expect(page.locator(".pull-item").first()).toBeVisible();

  const scrollArea = page.getByRole("region", { name: "Pull requests" });
  const scrollRoot = scrollArea.locator("..");
  const indicator = scrollRoot.locator(".sidebar-scroll-indicator");
  const thumb = indicator.locator(".sidebar-scroll-indicator__thumb");
  const stickyHeader = scrollArea.locator(".sidebar-group-header").first();
  await constrainScrollArea(scrollArea);

  const initialGeometry = await scrollArea.evaluate((node) => ({
    clientWidth: node.clientWidth,
    offsetWidth: (node as HTMLElement).offsetWidth,
    clientHeight: node.clientHeight,
    scrollHeight: node.scrollHeight,
  }));
  expect(initialGeometry.scrollHeight).toBeGreaterThan(initialGeometry.clientHeight);
  expect(initialGeometry.clientWidth).toBe(initialGeometry.offsetWidth);
  await expect(indicator).toHaveCSS("opacity", "0");

  await scrollArea.evaluate((node) => {
    node.scrollTop = 20;
  });
  await expect(indicator).toHaveCSS("opacity", "1");

  const stacking = await scrollArea.evaluate((node) => {
    const root = node.parentElement;
    const header = node.querySelector(".sidebar-group-header");
    const overlay = root?.querySelector(".sidebar-scroll-indicator");
    return {
      header: Number.parseInt(getComputedStyle(header!).zIndex, 10),
      overlay: Number.parseInt(getComputedStyle(overlay!).zIndex, 10),
    };
  });
  expect(stacking.overlay).toBeGreaterThan(stacking.header);

  const headerBox = await stickyHeader.boundingBox();
  const thumbBox = await thumb.boundingBox();
  expect(headerBox).not.toBeNull();
  expect(thumbBox).not.toBeNull();
  if (headerBox !== null && thumbBox !== null) {
    expect(thumbBox.y).toBeLessThan(headerBox.y + headerBox.height);
    expect(thumbBox.x).toBeGreaterThan(headerBox.x);
  }

  await scrollArea.focus();
  await page.keyboard.press("End");
  await expect.poll(() => scrollArea.evaluate((node) => node.scrollTop)).toBeGreaterThan(20);
  await expect(indicator).toHaveCSS("opacity", "1");
  await expect(indicator).toHaveCSS("opacity", "0", { timeout: 1_500 });
});

test("PR, issue, and workspace rails share labeled overlay scroll regions", async ({ page }) => {
  const rails = [
    { path: "/pulls", label: "Pull requests" },
    { path: "/issues", label: "Issues" },
    { path: "/workspaces", label: "Workspaces" },
  ];

  for (const rail of rails) {
    await page.goto(rail.path);
    const scrollArea = page.getByRole("region", { name: rail.label });
    await expect(scrollArea).toBeVisible();
    await expect(scrollArea).toHaveAttribute("tabindex", "0");
    await expect(scrollArea.locator("..").locator(".sidebar-scroll-indicator")).toHaveCount(1);
  }
});
