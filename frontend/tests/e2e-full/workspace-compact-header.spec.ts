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

test("PR terminal splits launch new tabs in their own pane", async ({ page }) => {
  test.setTimeout(120_000);
  const server = await startIsolatedWorkspaceE2EServer();
  const api = await request.newContext({ baseURL: server.info.base_url });
  try {
    const created = await api.post("/api/v1/workspaces", {
      data: { provider: "github", platform_host: "github.com", owner: "acme", name: "widgets", mr_number: 1 },
    });
    expect(created.status()).toBe(202);
    const workspace = await created.json();
    await expect
      .poll(async () => (await (await api.get(`/api/v1/workspaces/${workspace.id}`)).json()).status)
      .toBe("ready");
    const keys: string[] = [];
    for (let index = 0; index < 2; index++) {
      const launched = await api.post(`/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
        data: { target_key: "plain_shell", display_region: "workflow" },
      });
      expect(launched.status()).toBe(200);
      keys.push((await launched.json()).key);
    }
    await page.addInitScript(
      ({ workspaceId, keys }) => {
        localStorage.setItem(
          `kenn-forge-workspace-terminal-layout:${workspaceId}`,
          JSON.stringify({
            version: 1,
            open: false,
            dock: "bottom",
            height: 300,
            sessionRegions: Object.fromEntries(keys.map((key) => [key, "workflow"])),
            workflowMode: "tabs",
            workflowTree: {
              type: "split",
              id: "split",
              direction: "horizontal",
              ratio: 0.5,
              first: { type: "leaf", id: "left", tabs: [`session:${keys[0]}`], activeTabKey: `session:${keys[0]}` },
              second: { type: "leaf", id: "right", tabs: [`session:${keys[1]}`], activeTabKey: `session:${keys[1]}` },
            },
          }),
        );
      },
      { workspaceId: workspace.id, keys },
    );
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);
    const strips = page.getByRole("tablist", { name: "Workflow group tabs" });
    await expect(strips).toHaveCount(2);
    const right = strips.nth(1);
    const launch = right.getByRole("button", { name: "Launch", exact: true });
    await expect(launch).toBeVisible();
    await page.screenshot({ path: test.info().outputPath("pr-terminal-splits.png") });
    await launch.click();
    await page
      .getByRole("dialog", { name: "Run configurations" })
      .getByRole("button", { name: "Shell", exact: true })
      .click();
    await expect(right.getByRole("tab")).toHaveCount(2);
    await expect(right.getByRole("tab").last()).toHaveAttribute("aria-selected", "true");
    await expect(strips.first().getByRole("tab")).toHaveCount(1);
    await page.screenshot({ path: test.info().outputPath("pr-terminal-split-launched.png") });
  } finally {
    await api.dispose();
    await server.stop();
  }
});
