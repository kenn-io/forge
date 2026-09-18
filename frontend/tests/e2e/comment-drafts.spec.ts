import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

for (const [kind, path] of [
  ["pull request", "/pulls/github/acme/widgets/42"],
  ["issue", "/issues/github/acme/widgets/7"],
] as const) {
  test(`keeps an unsent ${kind} comment through reload and a closed tab until explicitly posted`, async ({
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

    await page.reload();
    await expect(editor).toHaveText("Unfinished comment saved for later");
    expect(submittedBodies).toEqual([]);

    await page.close();
    const reopened = await context.newPage();
    await mockApi(reopened);
    await reopened.route(`**/api/v1${path}/comments`, (route) =>
      route.fulfill({ status: 201, contentType: "application/json", body: "{}" }),
    );
    await reopened.goto(path);
    const restoredEditor = reopened.locator(".comment-box .comment-editor-input");
    await expect(restoredEditor).toHaveText("Unfinished comment saved for later");
    expect(submittedBodies).toEqual([]);

    await reopened.getByRole("button", { name: "Comment", exact: true }).click();
    await expect(restoredEditor).toHaveText("");
    expect(submittedBodies).toEqual(["Unfinished comment saved for later"]);

    await reopened.reload();
    await expect(restoredEditor).toHaveText("");
  });
}
