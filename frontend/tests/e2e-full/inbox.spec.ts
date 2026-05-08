import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer, startIsolatedE2EServerWithOptions } from "./support/e2eServer";

test.describe("inbox", () => {
  test("blocks direct inbox access and APIs when notifications are disabled", async ({ page }) => {
    const server = await startIsolatedE2EServerWithOptions({ notificationsEnabled: false });
    try {
      await page.goto(`${server.info.base_url}/inbox`);

      await expect(page.getByText("Notifications are disabled.")).toBeVisible();
      await expect(page.getByText("Draft UI")).toBeHidden();
      await expect(page.getByRole("heading", { name: "GitHub notifications" })).toBeHidden();

      const list = await page.request.get(`${server.info.base_url}/api/v1/notifications?state=unread`);
      expect(list.status()).toBe(403);
      const read = await page.request.post(`${server.info.base_url}/api/v1/notifications/read`, {
        data: { ids: [1] },
      });
      expect(read.status()).toBe(403);
    } finally {
      await server.stop();
    }
  });

  test("lists, filters, and refreshes notifications after sync", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/inbox`);

      await expect(page.getByText("Draft UI")).toBeVisible();
      await expect(page.getByRole("heading", { name: "GitHub notifications" })).toBeVisible();
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();

      await page.getByLabel("Notification reason").selectOption("mention");
      await expect(page).toHaveURL(/reason=mention/);
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeHidden();

      const typeOptions = await page.getByLabel("Notification type").evaluate((select) =>
        Array.from((select as HTMLSelectElement).options).map((option) => option.value)
      );
      expect(typeOptions).toEqual(["", "pr", "issue", "release", "commit", "other"]);
      await page.getByLabel("Notification reason").selectOption("");
      await page.getByLabel("Notification type").selectOption("pr");
      await expect(page).toHaveURL(/type=pr/);
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeHidden();

      await page.getByLabel("Notification type").selectOption("");
      await page.getByLabel("Notification repository").selectOption("github.com/acme/tools");
      await expect(page).toHaveURL(/repo=github\.com%2Facme%2Ftools/);
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeHidden();

      await page.goto(`${server.info.base_url}/inbox?state=unread&reason=mention&type=issue&repo=github.com%2Facme%2Ftools&q=tools&sort=repo`);
      await expect(page.getByLabel("Notification reason")).toHaveValue("mention");
      await expect(page.getByLabel("Notification type")).toHaveValue("issue");
      await expect(page.getByLabel("Notification repository")).toHaveValue("github.com/acme/tools");
      await expect(page.getByLabel("Notification sort")).toHaveValue("repo");
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();

      await page.getByPlaceholder("Search title, repo, author, number").fill("tools");
      await page.keyboard.press("Enter");
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeHidden();

      const addSyncedNotification = await page.request.post(`${server.info.base_url}/__e2e/notifications/add-synced`);
      expect(addSyncedNotification.status()).toBe(204);
      const syncResponse = page.waitForResponse((response) =>
        response.request().method() === "POST" && response.url().endsWith("/api/v1/notifications/sync")
      );
      await page.getByRole("button", { name: "Sync notifications" }).click();
      expect((await syncResponse).status()).toBe(202);
      await expect(page.getByRole("button", { name: /Synced tools notification/ })).toBeVisible();
      await expect(page.getByText(/Notifications synced/)).toBeVisible();
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("keeps the notification rows in an internal scroll region", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.setViewportSize({ width: 900, height: 180 });
      await page.goto(`${server.info.base_url}/inbox`);
      await expect(page.getByRole("heading", { name: "GitHub notifications" })).toBeVisible();
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeVisible();

      const layout = await page.evaluate(() => {
        const main = document.querySelector("main.app-main");
        const page = document.querySelector(".inbox-page");
        const list = document.querySelector(".notification-list");
        const listCard = document.querySelector(".list-card");
        if (!main || !page || !list || !listCard) {
          throw new Error("expected inbox layout elements to be present");
        }
        const listStyles = window.getComputedStyle(list);
        const pageStyles = window.getComputedStyle(page);
        const mainStyles = window.getComputedStyle(main);
        const listCardRect = listCard.getBoundingClientRect();
        const mainRect = main.getBoundingClientRect();
        return {
          listOverflowY: listStyles.overflowY,
          pageOverflowY: pageStyles.overflowY,
          mainOverflowY: mainStyles.overflowY,
          listClientHeight: list.clientHeight,
          listScrollHeight: list.scrollHeight,
          pageClientHeight: page.clientHeight,
          pageScrollHeight: page.scrollHeight,
          mainClientHeight: main.clientHeight,
          mainScrollHeight: main.scrollHeight,
          listCardBottom: Math.round(listCardRect.bottom),
          mainBottom: Math.round(mainRect.bottom),
        };
      });

      expect(layout.listOverflowY).toBe("auto");
      expect(layout.pageOverflowY).toBe("hidden");
      expect(layout.mainOverflowY).toBe("hidden");
      expect(layout.listScrollHeight).toBeGreaterThan(layout.listClientHeight);
      expect(layout.pageScrollHeight).toBe(layout.pageClientHeight);
      expect(layout.mainScrollHeight).toBe(layout.mainClientHeight);
      expect(layout.listCardBottom).toBeLessThanOrEqual(layout.mainBottom);
    } finally {
      await server.stop();
    }
  });

  test("selects notifications and bulk marks them read and done", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/inbox`);
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();

      await page.getByLabel("Select visible").check();
      await expect(page.getByText("2 selected")).toBeVisible();
      await page.locator(".bulk-bar").getByRole("button", { name: "Mark read" }).click();
      await expect(page.getByText("No notifications match this view.")).toBeVisible();

      await page.getByRole("button", { name: "Read", exact: true }).click();
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();

      await page.getByLabel("Select visible").check();
      await expect(page.getByText("2 selected")).toBeVisible();
      await page.locator(".bulk-bar").getByRole("button", { name: "Done", exact: true }).click();
      await expect(page.getByText("No notifications match this view.")).toBeVisible();

      await page.getByRole("button", { name: "Done", exact: true }).first().click();
      await expect(page.getByRole("button", { name: /Add widget caching layer/ })).toBeVisible();
      await expect(page.getByRole("button", { name: /Support config file loading/ })).toBeVisible();
    } finally {
      await server.stop();
    }
  });
});
