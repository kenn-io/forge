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

      // The picker is a compact dropdown: start-aligned under its
      // trigger chip and height-capped so long candidate lists scroll.
      const triggerBox = await page.getByRole("button", { name: "Edit assignees" }).boundingBox();
      const dialogBox = await page.getByRole("dialog", { name: "Edit assignees" }).boundingBox();
      expect(triggerBox).not.toBeNull();
      expect(dialogBox).not.toBeNull();
      expect(Math.abs(dialogBox!.x - triggerBox!.x)).toBeLessThanOrEqual(12);
      const dropGap = dialogBox!.y - (triggerBox!.y + triggerBox!.height);
      expect(dropGap).toBeGreaterThanOrEqual(0);
      expect(dropGap).toBeLessThanOrEqual(12);
      expect(dialogBox!.height).toBeLessThanOrEqual(320);

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

  test("pull detail searches candidates server-side and assigns a typed username", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await page.getByRole("button", { name: "Edit assignees" }).click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();

      // Typing requeries the autocomplete endpoint with the filter so
      // candidates beyond the first page stay reachable.
      const queryResponse = page.waitForResponse(
        (response) => response.url().includes("/comment-autocomplete") && response.url().includes("q=zed"),
      );
      await page.getByLabel("Filter users").fill("zed");
      expect((await queryResponse).status()).toBe(200);

      // No synced user matches, so the picker offers exact-username
      // entry; the provider accepts it and the chip reflects it.
      const updateResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/assignees`,
      );
      await page.getByRole("menuitemcheckbox", { name: /add .zed./i }).click();
      expect((await updateResponse).status()).toBe(200);
      await expect(page.locator("[data-user-list-editor='assignees']", { hasText: "zed" })).toBeVisible();
      await page.getByRole("button", { name: "Close user picker" }).click();

      // Reviewer free-entry goes through the request/remove mutation
      // path, not the assignee replace-set path, so cover it too.
      await page.getByRole("button", { name: "Edit reviewers" }).click();
      await expect(page.getByRole("dialog", { name: "Edit reviewers" })).toBeVisible();
      const reviewerQueryResponse = page.waitForResponse(
        (response) => response.url().includes("/comment-autocomplete") && response.url().includes("q=quill"),
      );
      await page.getByLabel("Filter users").fill("quill");
      expect((await reviewerQueryResponse).status()).toBe(200);

      const reviewerUpdateResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/reviewers`,
      );
      await page.getByRole("menuitemcheckbox", { name: /add .quill./i }).click();
      expect((await reviewerUpdateResponse).status()).toBe(200);
      await expect(page.locator("[data-user-list-editor='reviewers']", { hasText: "quill" })).toBeVisible();
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("a failed save stays visible after a later successful candidate search", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await page.getByRole("button", { name: "Edit assignees" }).click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();

      // The fixture provider rejects the user "ghost", like a real
      // provider rejecting a username that does not exist.
      const ghostQueryResponse = page.waitForResponse(
        (response) => response.url().includes("/comment-autocomplete") && response.url().includes("q=ghost"),
      );
      await page.getByLabel("Filter users").fill("ghost");
      expect((await ghostQueryResponse).status()).toBe(200);

      const failedUpdate = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/1/assignees`,
      );
      await page.getByRole("menuitemcheckbox", { name: /add .ghost./i }).click();
      expect((await failedUpdate).status()).toBeGreaterThanOrEqual(400);
      await expect(page.getByRole("alert")).toBeVisible();

      // A subsequent successful candidate search must not clear the
      // mutation error: the save still has not happened.
      const retryQueryResponse = page.waitForResponse(
        (response) => response.url().includes("/comment-autocomplete") && response.url().includes("q=bo"),
      );
      await page.getByLabel("Filter users").fill("bo");
      expect((await retryQueryResponse).status()).toBe(200);
      await expect(page.getByRole("menuitemcheckbox", { name: /bob/i })).toBeVisible();
      await expect(page.getByRole("alert")).toBeVisible();
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("navigating away closes an open picker without sending a mutation", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      const mutationRequests: string[] = [];
      page.on("request", (request) => {
        if (request.method() === "PUT" && /\/(assignees|reviewers)$/.test(request.url())) {
          mutationRequests.push(request.url());
        }
      });

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await page.getByRole("button", { name: "Edit assignees" }).click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();

      // Route to a different PR while the picker is open. The picker
      // must not survive the transition: a leftover panel would aim
      // its mutations at whatever item the handlers now target.
      await page.getByText("Bump lodash from 4.17.20 to 4.17.21").click();
      await expect(page.locator(".pull-detail .detail-title")).toContainText("Bump lodash");
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeHidden();
      expect(mutationRequests).toEqual([]);
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("an outside press dismisses the picker and pickers never stack", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();

      await page.getByRole("button", { name: "Edit assignees" }).click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();

      // Clicking anywhere outside the chip and panel dismisses it.
      await page.locator(".pull-detail .detail-title").click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeHidden();

      // Opening the reviewers picker while assignees is open swaps
      // them; the two panels must never be on screen together.
      await page.getByRole("button", { name: "Edit assignees" }).click();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();
      await page.getByRole("button", { name: "Edit reviewers" }).click();
      await expect(page.getByRole("dialog", { name: "Edit reviewers" })).toBeVisible();
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeHidden();

      // Keyboard activation fires no mousedown, so the swap must not
      // depend on the document-mousedown dismissal path.
      await page.getByRole("button", { name: "Edit assignees" }).press("Enter");
      await expect(page.getByRole("dialog", { name: "Edit assignees" })).toBeVisible();
      await expect(page.getByRole("dialog", { name: "Edit reviewers" })).toBeHidden();
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
