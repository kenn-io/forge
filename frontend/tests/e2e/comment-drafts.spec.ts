import { expect, test } from "@playwright/test";

import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

for (const [kind, path] of [
  ["pull request", "/pulls/github/acme/widgets/42"],
  ["issue", "/issues/github/acme/widgets/7"],
] as const) {
  test(`does not restore a ${kind} draft in a replacement repository at the same route`, async ({ page }) => {
    const api = createMockApiHandler();
    let repositoryId = 1104;
    await mockApi(page);
    await page.route(`**/api/v1${path}`, async (route) => {
      const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
      const detail = (await response.json()) as { repo: { platform_repo_id: number } };
      detail.repo.platform_repo_id = repositoryId;
      await route.fulfill({ json: detail });
    });
    await page.goto(path);
    const editor = page.locator(".comment-box .comment-editor-input");
    await expect(editor).toBeEditable();
    await editor.fill("Draft for the original repository");

    repositoryId = 1103;
    await page.reload();
    await expect(editor).toBeEditable();
    await expect(editor).toHaveText("");

    repositoryId = 1104;
    await page.reload();
    await expect(editor).toHaveText("Draft for the original repository");
  });

  for (const restoreFrom of ["reload", "a closed tab"] as const) {
    test(`keeps an unsent ${kind} comment through ${restoreFrom} until explicitly posted`, async ({
      page,
      context,
    }) => {
      const submittedBodies: string[] = [];
      context.on("request", (request) => {
        if (request.method() === "POST" && new URL(request.url()).pathname.endsWith("/comments")) {
          submittedBodies.push((request.postDataJSON() as { body: string }).body);
        }
      });
      await mockApi(page);
      await page.goto(path);
      const editor = page.locator(".comment-box .comment-editor-input");
      await expect(editor).toBeEditable();
      await editor.fill("Unfinished comment saved for later");

      let restoredPage = page;
      if (restoreFrom === "reload") {
        await page.reload();
      } else {
        await page.close();
        restoredPage = await context.newPage();
        await mockApi(restoredPage);
        await restoredPage.goto(path);
      }
      const restoredEditor = restoredPage.locator(".comment-box .comment-editor-input");
      await expect(restoredEditor).toHaveText("Unfinished comment saved for later");
      expect(submittedBodies).toEqual([]);

      await restoredPage.route(`**/api/v1${path}/comments`, (route) =>
        route.fulfill({ status: 201, contentType: "application/json", body: "{}" }),
      );
      await restoredPage.getByRole("button", { name: "Comment", exact: true }).click();
      await expect(restoredEditor).toHaveText("");
      expect(submittedBodies).toEqual(["Unfinished comment saved for later"]);
    });
  }

  test(`clears a ${kind} comment draft through reload after explicitly posting it`, async ({ page }) => {
    const submittedBodies: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "POST" && new URL(request.url()).pathname.endsWith("/comments")) {
        submittedBodies.push((request.postDataJSON() as { body: string }).body);
      }
    });
    await mockApi(page);
    await page.route(`**/api/v1${path}/comments`, (route) =>
      route.fulfill({ status: 201, contentType: "application/json", body: "{}" }),
    );
    await page.goto(path);
    const editor = page.locator(".comment-box .comment-editor-input");
    await expect(editor).toBeEditable();
    await editor.fill("Unfinished comment saved for later");
    expect(submittedBodies).toEqual([]);

    await page.getByRole("button", { name: "Comment", exact: true }).click();
    await expect(editor).toHaveText("");
    expect(submittedBodies).toEqual(["Unfinished comment saved for later"]);

    await page.reload();
    await expect(editor).toHaveText("");
  });
}
