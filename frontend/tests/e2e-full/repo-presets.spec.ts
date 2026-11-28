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
    await expect(page.getByRole("dialog", { name: "Save repository preset" })).toBeHidden();
    await page.keyboard.press("Escape");

    await page.reload();
    await expect(page.getByRole("button", { name: "Select repository: Review queue" })).toBeVisible();
    let configText = await readFile(server.info.config_path, "utf8");
    expect(configText).toContain('name = "Review queue"');
    expect(configText).toMatch(/platform_repo_id = \d+/);

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
    await expect(page.getByRole("dialog", { name: "Save repository preset" })).toBeHidden();
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
    await expect(page.getByRole("dialog", { name: "Delete repository preset?" })).toBeHidden();
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

test("a GitHub team's configured repositories become an editable selection that saves as a preset", async ({
  browser,
}) => {
  const server = await startIsolatedE2EServer();
  const page = await browser.newPage();
  try {
    await page.goto(`${server.info.base_url}/issues`);
    await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });
    await page.getByRole("button", { name: /^Select repository:/ }).click();
    await page.getByRole("button", { name: "Select from GitHub team…" }).click();
    const dialog = page.getByRole("dialog", { name: "Select repositories from a GitHub team" });
    const organization = dialog.getByRole("combobox", { name: /^Organization/ });
    if (!(await organization.textContent())?.includes("acme")) {
      await organization.click();
      await page.getByRole("option", { name: /^acme/ }).first().click();
    }
    await dialog.getByRole("combobox", { name: /^Team/ }).click();
    await page.getByRole("option", { name: "Platform" }).click();
    await expect(dialog.getByText("2 configured repositories will be selected.")).toBeVisible();
    await dialog.getByRole("button", { name: "Use selection" }).click();
    await expect(dialog).toBeHidden();

    const repoList = page.getByRole("listbox", { name: "Repositories" });
    const widgets = repoList.getByRole("option", { name: "github/github.com/acme/widgets", exact: true });
    const tools = repoList.getByRole("option", { name: "github/github.com/acme/tools", exact: true });
    await expect(widgets).toHaveAttribute("aria-selected", "true");
    await expect(tools).toHaveAttribute("aria-selected", "true");
    await tools.click();
    await expect(tools).toHaveAttribute("aria-selected", "false");
    await page.getByRole("button", { name: "Save preset" }).click();
    await page.getByRole("textbox", { name: "Preset name" }).fill("Platform team");
    const created = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().endsWith("/api/v1/settings/repo-presets"),
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    expect((await created).ok()).toBe(true);
    await page.keyboard.press("Escape");

    await page.reload();
    await expect(page.getByRole("button", { name: "Select repository: Platform team" })).toBeVisible();
    const saved = await page.request.get(`${server.info.base_url}/api/v1/settings`);
    expect((await saved.json()).repo_presets).toEqual([
      expect.objectContaining({
        name: "Platform team",
        repos: [expect.objectContaining({ repo_path: "acme/widgets", platform_repo_id: expect.any(Number) })],
      }),
    ]);
  } finally {
    await page.close();
    await server.stop();
  }
});
