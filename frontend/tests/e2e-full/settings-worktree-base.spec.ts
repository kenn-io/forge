import { execFileSync } from "node:child_process";
import { mkdtempSync, realpathSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

let isolatedServer: IsolatedE2EServer | undefined;
let localRepo: string | undefined;

type SettingsResponse = {
  repos: Array<{
    owner: string;
    name: string;
    repo_path?: string;
    worktree_base_path?: string;
  }>;
};

test.beforeEach(async () => {
  isolatedServer = await startIsolatedE2EServer();
});

test.afterEach(async () => {
  await isolatedServer?.stop();
  isolatedServer = undefined;
  if (localRepo) {
    rmSync(localRepo, { recursive: true, force: true });
    localRepo = undefined;
  }
});

function git(dir: string, ...args: string[]): void {
  execFileSync("git", args, { cwd: dir, stdio: "ignore" });
}

test("local clone editor opens from the row gear and saves a valid path", async ({ page }) => {
  localRepo = realpathSync(mkdtempSync(path.join(os.tmpdir(), "mm-local-clone-")));
  git(localRepo, "init");
  git(localRepo, "remote", "add", "origin", "https://github.com/acme/widgets.git");

  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  const row = page.locator(".repo-row", { hasText: "acme/widgets" });
  const input = row.getByLabel("Local clone path for acme/widgets", { exact: true });
  await expect(row).toBeVisible();
  await expect(input).toBeHidden();

  await row.getByRole("button", { name: "Local clone for acme/widgets" }).click();
  await expect(input).toBeVisible();

  await input.fill(localRepo);
  const saveButton = row.getByRole("button", { name: "Save local clone path for acme/widgets" });
  const saveResponsePromise = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/repo/github/acme/widgets/worktree-base") &&
      response.request().method() === "PUT",
  );
  await saveButton.click();
  const saveResponse = await saveResponsePromise;
  const saveBody = await saveResponse.text();
  expect(saveResponse.status(), `PUT worktree-base failed: ${saveBody}`).toBe(200);

  await expect(row.locator(".row-error")).toHaveCount(0);
  await expect(input).toHaveValue(/mm-local-clone-/);
  await expect(saveButton).toBeDisabled();
  await expect(row.getByRole("button", { name: "Local clone for acme/widgets" })).toHaveAttribute(
    "title",
    `Local clone: ${localRepo}`,
  );

  await expect
    .poll(async () => {
      const response = await page.request.get(`${isolatedServer!.info.base_url}/api/v1/settings`);
      expect(response.ok()).toBe(true);
      const settings = (await response.json()) as SettingsResponse;
      const repo = settings.repos.find(
        (candidate) =>
          candidate.repo_path === "acme/widgets" || (candidate.owner === "acme" && candidate.name === "widgets"),
      );
      return repo?.worktree_base_path ?? "";
    })
    .toBe(localRepo);

  await page.reload();
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  const reloadedRow = page.locator(".repo-row", { hasText: "acme/widgets" });
  await expect(reloadedRow.getByRole("button", { name: "Local clone for acme/widgets" })).toHaveAttribute(
    "title",
    `Local clone: ${localRepo}`,
  );
  await reloadedRow.getByRole("button", { name: "Local clone for acme/widgets" }).click();
  const reloadedInput = reloadedRow.getByLabel("Local clone path for acme/widgets", { exact: true });
  await expect(reloadedInput).toHaveValue(localRepo);

  await reloadedRow.getByRole("button", { name: "Local clone for acme/widgets" }).click();
  await expect(reloadedInput).toBeHidden();
});

test("local clone editor surfaces validation errors inline", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  const row = page.locator(".repo-row", { hasText: "acme/widgets" });
  await row.getByRole("button", { name: "Local clone for acme/widgets" }).click();

  const input = row.getByLabel("Local clone path for acme/widgets", { exact: true });
  await input.fill("/nonexistent/clone/path");
  await row.getByRole("button", { name: "Save local clone path for acme/widgets" }).click();

  await expect(row.locator(".error-msg")).toBeVisible();
  await expect(row.locator(".error-msg")).not.toHaveText("");
  await expect(input).toBeVisible();
});
