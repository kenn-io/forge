import { expect, request, test } from "@playwright/test";
import { startIsolatedWorkspaceE2EServer } from "./support/e2eServer";

test("narrow workspace headers leave room for the terminal and keep controls reachable", async ({ page }) => {
  test.setTimeout(120_000);
  const server = await startIsolatedWorkspaceE2EServer();
  const api = await request.newContext({ baseURL: server.info.base_url });
  try {
    const response = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", { data: {} });
    expect(response.status()).toBe(202);
    const workspace = await response.json();
    await expect
      .poll(async () => (await (await api.get(`/api/v1/workspaces/${workspace.id}`)).json()).status)
      .toBe("ready");
    await page.setViewportSize({ width: 600, height: 850 });
    await page.goto(`${server.info.base_url}/terminal/${workspace.id}`);
    const header = page.locator(".header-bar");
    await expect(header).toBeVisible();
    const controls = header.getByRole("button", { name: "Workspace controls" });
    await expect(controls).toBeVisible();
    const bounds = await header.boundingBox();
    expect(bounds!.height).toBeLessThanOrEqual(56);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(600);
    await expect(header.getByRole("button", { name: "Launch", exact: true })).toBeVisible();
    await page.screenshot({ path: test.info().outputPath("compact-workspace-header.png") });
    await controls.click();
    const menu = page.getByRole("dialog", { name: "Workspace controls" });
    await expect(menu.getByRole("button", { name: "Terminal options" })).toBeVisible();
    await page.screenshot({ path: test.info().outputPath("compact-workspace-controls.png") });
    await page.keyboard.press("Escape");
    await expect(menu).not.toBeVisible();
    await expect(controls).toBeFocused();
    await controls.click();
    await menu.getByRole("button", { name: "Delete", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Delete workspace" })).toBeVisible();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.setViewportSize({ width: 1600, height: 850 });
    await expect(header.getByRole("button", { name: "Terminal options" })).toBeVisible();
    await expect(header.getByRole("button", { name: "Delete", exact: true })).toBeVisible();
    await page.screenshot({ path: test.info().outputPath("wide-workspace-header.png") });
    await page.setViewportSize({ width: 600, height: 850 });
    await expect(controls).toBeVisible();
  } finally {
    await api.dispose();
    await server.stop();
  }
});
