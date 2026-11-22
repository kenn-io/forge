import { expect, test } from "@playwright/test";
import { startIsolatedE2EServerWithOptions } from "./support/e2eServer";

// A fresh process keeps counts recorded by other specs out of this one's list.
test("suggests an opened untracked repository in Settings and adds it", async ({ page, request }) => {
  const server = await startIsolatedE2EServerWithOptions({ freshProcess: true });
  try {
    const base = server.info.base_url;
    await page.route("https://github.com/other/repo/pull/77", (route) =>
      route.fulfill({ contentType: "text/html", body: "Provider destination" }),
    );
    await page.goto(`${base}/pulls/github/acme/widgets/1`);
    const recorded = page.waitForResponse(
      (response) => response.url().endsWith("/api/v1/repos/untracked-opens") && response.request().method() === "POST",
    );
    await page.getByRole("link", { name: /External follow-up PR other\/repo#77/ }).click();
    expect((await recorded).ok()).toBe(true);

    for (let open = 0; open < 3; open++) {
      const response = await request.post(`${base}/api/v1/repos/untracked-opens`, {
        data: {
          provider: "github",
          host: "github.com",
          owner: "example",
          name: "project",
          item_type: "issue",
          number: 42,
        },
      });
      expect(response.ok()).toBe(true);
    }

    await page.goto(`${base}/settings?section=settings-repositories`);
    const suggestions = page.locator(".repo-suggestions");
    await expect(suggestions.getByText("Suggested from links you opened")).toBeVisible();
    const rows = suggestions.locator(".repo-row");
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0)).toContainText("example/project");
    await expect(rows.nth(1)).toContainText("other/repo");

    await suggestions.getByRole("button", { name: "Add other/repo" }).click();
    await expect(suggestions.getByRole("button", { name: "Add other/repo" })).toHaveCount(0);
    await expect(suggestions.getByRole("button", { name: "Add example/project" })).toBeVisible();
    await expect(page.locator(".repo-list .repo-name", { hasText: "other/repo" })).toBeVisible();

    await page.reload();
    await expect(page.locator(".repo-list .repo-name", { hasText: "other/repo" })).toBeVisible();
    await expect(rows).toHaveCount(1);
    await expect(rows.nth(0)).toContainText("example/project");
  } finally {
    await server.stop();
  }
});
