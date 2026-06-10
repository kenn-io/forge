import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

let isolatedServer: IsolatedE2EServer | undefined;
let localRepo: string | undefined;

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
  localRepo = mkdtempSync(path.join(os.tmpdir(), "mm-local-clone-"));
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
  await row.getByRole("button", { name: "Save local clone path for acme/widgets" }).click();

  await expect(input).toHaveValue(/mm-local-clone-/);
  await row.getByRole("button", { name: "Local clone for acme/widgets" }).click();
  await expect(input).toBeHidden();
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
