import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

import { startIsolatedE2EServer } from "./support/e2eServer.js";

test("repository presets persist, overwrite atomically, and delete without clearing selection", async ({ browser }) => {
  const server = await startIsolatedE2EServer();
  const page = await browser.newPage();
  try {
    await page.goto(`${server.info.base_url}/issues`);
    await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const selector = page.getByRole("button", { name: /^Select repository:/ });
    await selector.click();
    const repoList = page.getByRole("listbox", { name: "Repositories" });
    await repoList.getByRole("option", { name: "github/github.com/acme/widgets", exact: true }).click();
    await page.getByRole("button", { name: "Save preset" }).click();
    await page.getByRole("textbox", { name: "Preset name" }).fill("Review queue");
    const created = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().endsWith("/api/v1/settings/repo-presets"),
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    expect((await created).ok()).toBe(true);
    await page.keyboard.press("Escape");

    await page.reload();
    await expect(page.getByRole("button", { name: "Select repository: Review queue" })).toBeVisible();
    let configText = await readFile(server.info.config_path, "utf8");
    expect(configText).toContain('name = "Review queue"');
    expect(configText).toContain('platform_repo_id = "');

    await page.getByRole("button", { name: "Select repository: Review queue" }).click();
    await page
      .getByRole("listbox", { name: "Repositories" })
      .getByRole("option", { name: "github/github.com/acme/tools", exact: true })
      .click();
    await page.getByRole("button", { name: "Save preset" }).click();
    await expect(page.getByRole("radio", { name: "Overwrite preset" })).toBeChecked();
    await expect(page.getByRole("combobox", { name: "Preset to overwrite: Review queue" })).toBeVisible();
    const updated = page.waitForResponse(
      (response) =>
        response.request().method() === "PUT" &&
        response.url().includes("/api/v1/settings/repo-presets/Review%20queue"),
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    expect((await updated).ok()).toBe(true);
    await page.keyboard.press("Escape");

    const savedSettings = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect(savedSettings.ok()).toBe(true);
    const savedBody = await savedSettings.json();
    expect(savedBody.repo_presets).toEqual([
      expect.objectContaining({
        name: "Review queue",
        repos: expect.arrayContaining([
          expect.objectContaining({ repo_path: "acme/widgets" }),
          expect.objectContaining({ repo_path: "acme/tools" }),
        ]),
      }),
    ]);

    await page.reload();
    await page.getByRole("button", { name: "Select repository: Review queue" }).click();
    await page.getByRole("button", { name: "Delete preset Review queue" }).click();
    const deleted = page.waitForResponse(
      (response) =>
        response.request().method() === "DELETE" &&
        response.url().includes("/api/v1/settings/repo-presets/Review%20queue"),
    );
    await page.getByRole("button", { name: "Delete preset", exact: true }).click();
    expect((await deleted).ok()).toBe(true);
    await page.keyboard.press("Escape");

    await expect(page.getByRole("button", { name: "Select repository: 2 repos" })).toBeVisible();
    const deletedSettings = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect((await deletedSettings.json()).repo_presets).toEqual([]);
    configText = await readFile(server.info.config_path, "utf8");
    expect(configText).not.toContain('name = "Review queue"');
  } finally {
    await page.close();
    await server.stop();
  }
});
