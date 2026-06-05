import { expect, test } from "@playwright/test";

test.describe("provider capabilities", () => {
  test("PR detail shows locked state only for providers that support it", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1");

    await expect(page.locator(".pull-detail")).toBeVisible();
    await expect(page.locator(".chips-row").getByText("Locked", { exact: true })).toBeVisible();
  });

  test("GitLab issue detail hides timeline edit controls when comments are read-only", async ({ page }) => {
    await page.goto("/host/gitlab.example.com/issues/gitlab/group/project/11");

    const detail = page.locator(".issue-detail");
    await expect(detail).toBeVisible();
    await expect(detail.getByText("GitLab read-only issue")).toBeVisible();
    await expect(detail.getByText("GitLab read-only timeline comment")).toBeVisible();
    await expect(detail.getByRole("button", { name: "Edit comment" })).toHaveCount(0);
    await expect(detail.getByRole("button", { name: "Copy comment" })).toBeVisible();

    const response = await page.request.get("/api/v1/host/gitlab.example.com/issues/gl/group/project/11");
    expect(response.ok()).toBeTruthy();
    const body = await response.json();
    expect(body.repo.capabilities).toMatchObject({
      read_repositories: false,
      read_merge_requests: false,
      read_issues: true,
      read_comments: true,
      read_releases: false,
      read_ci: false,
      comment_mutation: false,
    });
  });
});
