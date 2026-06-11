import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

test.describe("assignee and reviewer editing", () => {
  test("pull detail edits assignees and persists across reload", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await expect(page.locator("[data-user-list-editor='assignees']", { hasText: "alice" })).toBeVisible();

      await page.getByRole("button", { name: "Edit assignees" }).click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();
      await expect(page.getByRole("menuitemcheckbox", { name: /alice/i })).toHaveAttribute("aria-checked", "true");
      await expect(page.getByRole("menuitemcheckbox", { name: /bob/i })).toHaveAttribute("aria-checked", "false");

      const updateResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/assignees`,
      );
      await page.getByRole("menuitemcheckbox", { name: /bob/i }).click();
      expect((await updateResponse).status()).toBe(200);

      await expect(page.getByRole("menuitemcheckbox", { name: /bob/i })).toHaveAttribute("aria-checked", "true");
      await expect(page.locator("[data-user-list-editor='assignees']", { hasText: "alice, bob" })).toBeVisible();

      await page.reload();
      await expect(page.locator("[data-user-list-editor='assignees']", { hasText: "alice, bob" })).toBeVisible();
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("pull detail requests and removes reviewers", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await expect(page.locator("[data-user-list-editor='reviewers']", { hasText: "carol" })).toBeVisible();

      await page.getByRole("button", { name: "Edit reviewers" }).click();
      await expect(page.getByRole("dialog", { name: "Edit reviewers" })).toBeVisible();
      await expect(page.getByRole("menuitemcheckbox", { name: /carol/i })).toHaveAttribute("aria-checked", "true");

      // Removing the only requested reviewer issues a PUT with an empty set.
      const removeResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/reviewers`,
      );
      await page.getByRole("menuitemcheckbox", { name: /carol/i }).click();
      expect((await removeResponse).status()).toBe(200);
      await expect(page.getByRole("menuitemcheckbox", { name: /carol/i })).toHaveAttribute("aria-checked", "false");

      // Requesting a fresh reviewer adds them to the set.
      const requestResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/reviewers`,
      );
      await page.getByRole("menuitemcheckbox", { name: /bob/i }).click();
      expect((await requestResponse).status()).toBe(200);
      await expect(page.locator("[data-user-list-editor='reviewers']", { hasText: "bob" })).toBeVisible();
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("issue detail edits assignees", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/issues/github/acme/widgets/10`);
      await expect(page.locator(".issue-detail")).toBeVisible();

      await page.getByRole("button", { name: "Edit assignees" }).click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();

      const updateResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/issues/github/acme/widgets/10/assignees`,
      );
      await page.getByRole("menuitemcheckbox", { name: /alice/i }).click();
      expect((await updateResponse).status()).toBe(200);

      await expect(page.locator("[data-user-list-editor='assignees']", { hasText: "alice" })).toBeVisible();

      await page.reload();
      await expect(page.locator("[data-user-list-editor='assignees']", { hasText: "alice" })).toBeVisible();
    } finally {
      await isolatedServer?.stop();
    }
  });
});
