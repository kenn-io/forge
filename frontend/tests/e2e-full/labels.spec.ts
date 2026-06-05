import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

test.describe("label editing", () => {
  async function openLabelsFromPalette(page: import("@playwright/test").Page): Promise<void> {
    await page.keyboard.press(process.platform === "darwin" ? "Meta+K" : "Control+K");
    await expect(page.getByRole("dialog", { name: "Command palette" })).toBeVisible();
    await page.locator(".palette-input").fill("Edit labels");
    await expect(page.getByRole("button", { name: /Edit labels/ })).toHaveCount(1);
    await page.getByRole("button", { name: /Edit labels/ }).click();
    await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeVisible();
  }

  test("pull detail edits labels from repository catalog", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await expect(
        page.locator(".pull-detail .chips-row .label-pill", {
          hasText: "bug",
        }),
      ).toBeVisible();

      await page.getByRole("button", { name: "Labels" }).click();
      await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeVisible();
      await expect(page.getByLabel("Filter labels")).toBeFocused();
      await expect(page.getByRole("menuitemcheckbox", { name: /bug/i })).toHaveAttribute("aria-checked", "true");
      await expect(page.getByRole("menuitemcheckbox", { name: /triage/i })).toHaveAttribute("aria-checked", "false");
      await page.keyboard.type("tri");
      await expect(page.getByRole("menuitemcheckbox", { name: /bug/i })).toHaveCount(0);
      await expect(page.getByRole("menuitemcheckbox", { name: /triage/i })).toBeVisible();

      const updateResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/labels`,
      );
      await page.getByRole("menuitemcheckbox", { name: /triage/i }).click();
      expect((await updateResponse).status()).toBe(200);

      await expect(page.getByRole("menuitemcheckbox", { name: /triage/i })).toHaveAttribute("aria-checked", "true");
      await expect(
        page.locator(".pull-detail .chips-row .label-pill", {
          hasText: "triage",
        }),
      ).toBeVisible();

      await page.reload();
      await expect(
        page.locator(".pull-detail .chips-row .label-pill", {
          hasText: "triage",
        }),
      ).toBeVisible();
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("pull detail toggles the label picker from the Labels button", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();

      const labelsButton = page.locator(".pull-detail .chips-row").getByRole("button", { name: "Labels", exact: true });
      await labelsButton.click();
      await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeVisible();

      await labelsButton.click();
      await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeHidden();
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("keeps label editing in the header when no labels are assigned", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await page.getByRole("button", { name: "Labels" }).click();
      await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeVisible();

      const updateResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/labels`,
      );
      await page.getByRole("button", { name: "Clear selected labels" }).click();
      expect((await updateResponse).status()).toBe(200);

      await expect(page.locator(".pull-detail .label-editor-row")).toHaveCount(0);
      await expect(
        page.locator(".pull-detail .chips-row").getByRole("button", { name: "Labels", exact: true }),
      ).toBeVisible();
      await expect(page.locator(".pull-detail .label-editor-empty")).toHaveCount(0);
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("issue detail clears labels through the picker", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/issues/github/acme/widgets/10`);
      await expect(page.locator(".issue-detail")).toBeVisible();
      await expect(
        page.locator(".issue-detail .meta-row .label-pill", {
          hasText: "bug",
        }),
      ).toBeVisible();

      await page.getByRole("button", { name: "Labels" }).click();
      await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeVisible();
      await expect(page.getByLabel("Filter labels")).toBeFocused();

      const updateResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/issues/github/acme/widgets/10/labels`,
      );
      await page.getByRole("button", { name: "Clear selected labels" }).click();
      expect((await updateResponse).status()).toBe(200);

      await expect(page.locator(".issue-detail .meta-row .label-pill")).toHaveCount(0);
      await expect(
        page.locator(".issue-detail .meta-row").getByRole("button", { name: "Labels", exact: true }),
      ).toBeVisible();
      await page.reload();
      await expect(page.locator(".issue-detail .meta-row .label-pill")).toHaveCount(0);
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("issue detail toggles the label picker from the Labels button", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/issues/github/acme/widgets/10`);
      await expect(page.locator(".issue-detail")).toBeVisible();

      const labelsButton = page.locator(".issue-detail .meta-row").getByRole("button", { name: "Labels", exact: true });
      await labelsButton.click();
      await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeVisible();

      await labelsButton.click();
      await expect(page.getByRole("dialog", { name: "Edit labels" })).toBeHidden();
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("refreshes stale label catalog", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      const firstResponse = await page.request.get(`${baseURL}/api/v1/repo/github/acme/widgets/labels`);
      expect(firstResponse.ok()).toBe(true);
      const firstBody = await firstResponse.json();
      expect(firstBody.stale).toBe(true);
      expect(firstBody.syncing).toBe(true);

      await expect
        .poll(async () => {
          const response = await page.request.get(`${baseURL}/api/v1/repo/github/acme/widgets/labels`);
          const body = await response.json();
          return {
            stale: body.stale,
            syncing: body.syncing,
            count: body.labels.length,
          };
        })
        .toEqual({ stale: false, syncing: false, count: 3 });
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("command palette edits pull and issue labels", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await openLabelsFromPalette(page);
      await expect(page.getByRole("menuitemcheckbox", { name: /bug/i })).toHaveAttribute("aria-checked", "true");
      const pullUpdate = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/labels`,
      );
      await page.getByRole("menuitemcheckbox", { name: /docs/i }).click();
      expect((await pullUpdate).status()).toBe(200);
      await expect(
        page.locator(".pull-detail .chips-row .label-pill", {
          hasText: "docs",
        }),
      ).toBeVisible();
      await page.reload();
      await expect(
        page.locator(".pull-detail .chips-row .label-pill", {
          hasText: "docs",
        }),
      ).toBeVisible();

      await page.goto(`${baseURL}/issues/github/acme/widgets/10`);
      await expect(page.locator(".issue-detail")).toBeVisible();
      await openLabelsFromPalette(page);
      await expect(page.getByRole("menuitemcheckbox", { name: /bug/i })).toHaveAttribute("aria-checked", "true");
      const issueUpdate = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/issues/github/acme/widgets/10/labels`,
      );
      await page.getByRole("menuitemcheckbox", { name: /triage/i }).click();
      expect((await issueUpdate).status()).toBe(200);
      await expect(
        page.locator(".issue-detail .meta-row .label-pill", {
          hasText: "triage",
        }),
      ).toBeVisible();
      await page.reload();
      await expect(
        page.locator(".issue-detail .meta-row .label-pill", {
          hasText: "triage",
        }),
      ).toBeVisible();
    } finally {
      await isolatedServer?.stop();
    }
  });
});
