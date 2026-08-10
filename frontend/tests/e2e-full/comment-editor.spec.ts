import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

test.describe("comment editor autocomplete", () => {
  test("PR comment editor preserves IME composition without accepting a suggestion", async ({ page }) => {
    await page.goto("/pulls");
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
    await page.locator(".pull-item").first().click();
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    const detail = page.locator(".pull-detail");
    const editor = detail.locator(".comment-editor-input").first();
    await editor.click();
    await page.keyboard.type("@a");

    await expect(page.locator(".comment-editor-option").first()).toBeVisible({
      timeout: 10_000,
    });

    await editor.dispatchEvent("compositionstart");

    await editor.evaluate((node) => {
      const event = new KeyboardEvent("keydown", {
        key: "Enter",
        bubbles: true,
        cancelable: true,
      });
      Object.defineProperty(event, "isComposing", { value: true });
      node.dispatchEvent(event);
    });

    await expect(editor).toContainText("@a");
    await expect(detail.locator(".submit-btn")).toHaveText("Comment");

    await editor.dispatchEvent("compositionend");
  });

  test("PR comment editor inserts autocomplete at the active caret position", async ({ page }) => {
    await page.goto("/pulls");
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
    await page.locator(".pull-item").first().click();
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    const detail = page.locator(".pull-detail");
    const editor = detail.locator(".comment-editor-input").first();
    await editor.click();
    await page.keyboard.type("hello world");

    await editor.evaluate((node) => {
      const textNode = node.querySelector("p")?.firstChild;
      if (!(textNode instanceof Text)) {
        throw new Error("expected text node in editor");
      }
      const selection = window.getSelection();
      if (!selection) {
        throw new Error("expected browser selection");
      }
      const range = document.createRange();
      range.setStart(textNode, 6);
      range.collapse(true);
      selection.removeAllRanges();
      selection.addRange(range);
      node.focus();
    });

    await page.keyboard.type("@a");
    // Wait for the specific "@alice" option to appear. @tiptap/suggestion
    // binds the commit range into props at update time, so accepting while
    // a stale props object is still current (items fetch still in-flight)
    // commits against an out-of-date range and leaves the trigger chars
    // behind. Waiting for the filtered option ensures currentProps has been
    // swapped to the latest update before we commit.
    await expect(page.locator(".comment-editor-option").filter({ hasText: "@alice" })).toBeVisible({
      timeout: 10_000,
    });
    await editor.press("Enter");

    await expect(editor).toContainText("hello @alice world");
  });

  test("focused comment editors suppress global view shortcuts", async ({ page }) => {
    await page.goto("/pulls");
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
    await page.locator(".pull-item").first().click();
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });

    const detail = page.locator(".pull-detail");
    const editor = detail.locator(".comment-editor-input").first();
    await editor.click();
    await expect(editor).toHaveClass(/ProseMirror-focused/);
    await editor.evaluate((node) => {
      node.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "f",
          bubbles: true,
          cancelable: true,
        }),
      );
    });

    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/\d+$/);
    await expect(detail).toBeVisible();
  });

  test("PR comment editor accepts @ mention and submits end-to-end", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/5");
    await page.locator(".pull-detail").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByText("Detail not yet loaded")).toHaveCount(0, {
      timeout: 10_000,
    });

    const detail = page.locator(".pull-detail");
    const shell = detail.locator(".comment-editor-shell").first();
    const editor = shell.locator(".comment-editor-input");
    const submit = shell.getByRole("button", { name: "Comment" });
    await expect(editor).toBeVisible();
    await expect(submit).toBeVisible();
    await editor.click();
    await page.keyboard.type("<script>alert('x')</script> @a");

    const aliceOption = page.locator(".comment-editor-option").filter({ hasText: "@alice" });
    await expect(aliceOption).toBeVisible({ timeout: 10_000 });
    await aliceOption.dispatchEvent("pointerdown");
    await expect(editor).toContainText("@alice");

    await expect(submit).toBeEnabled();
    await submit.click();

    await expect(
      detail
        .locator(".event-body")
        .filter({ hasText: /@alice/ })
        .first(),
    ).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText("Select a PR")).toHaveCount(0);
  });

  test("issue comment editor accepts # reference and submits end-to-end", async ({ page }) => {
    await page.goto("/issues/github/acme/widgets/12");
    await page.locator(".issue-detail").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByText("Detail not yet loaded")).toHaveCount(0, {
      timeout: 10_000,
    });

    const detail = page.locator(".issue-detail");
    const shell = detail.locator(".comment-editor-shell").first();
    const editor = shell.locator(".comment-editor-input");
    const submit = shell.getByRole("button", { name: "Comment" });
    await expect(editor).toBeVisible();
    await expect(submit).toBeVisible();
    await editor.fill("See #1");

    await expect(page.locator(".comment-editor-option").first()).toBeVisible({
      timeout: 10_000,
    });
    await editor.press("Enter");

    await expect(submit).toBeEnabled();
    await submit.click();

    await expect(
      detail
        .locator(".event-body")
        .filter({ hasText: /See #1/ })
        .first(),
    ).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText("Select a PR")).toHaveCount(0);
  });

  test("PR comment edits and deletions persist through the real detail API", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);
      const detail = page.locator(".pull-detail");
      await expect(detail).toBeVisible();

      const shell = detail.locator(".comment-editor-shell").first();
      const editor = shell.locator(".comment-editor-input");
      const originalBody = "Comment mutation full-stack baseline";
      const editedBody = "Comment mutation full-stack edited";
      await editor.fill(originalBody);
      const postResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          new URL(response.url()).pathname === "/api/v1/pulls/github/acme/widgets/1/comments",
      );
      await shell.getByRole("button", { name: "Comment" }).click();
      expect((await postResponse).status()).toBe(201);

      const originalBodyNode = detail.locator(".event-body", { hasText: originalBody }).last();
      await expect(originalBodyNode).toBeVisible();
      const originalCard = originalBodyNode.locator("xpath=ancestor::*[.//button[@aria-label='Edit comment']][1]");
      await originalCard.hover();
      await originalCard.getByRole("button", { name: "Edit comment" }).click();
      const editPanel = detail.locator(".edit-panel");
      await editPanel.locator(".comment-editor-input").fill(editedBody);
      const patchResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PATCH" &&
          /\/api\/v1\/pulls\/github\/acme\/widgets\/1\/comments\/\d+$/.test(new URL(response.url()).pathname),
      );
      await editPanel.getByRole("button", { name: "Save" }).click();
      expect((await patchResponse).status()).toBe(200);
      await expect(detail.locator(".event-body", { hasText: editedBody }).last()).toBeVisible();

      await expect
        .poll(async () => {
          const persisted = await page.request.get(`${server.info.base_url}/api/v1/pulls/github/acme/widgets/1`);
          expect(persisted.ok()).toBe(true);
          const editedDetail: { events?: Array<{ Body?: string }> } = await persisted.json();
          return editedDetail.events?.some((event) => event.Body === editedBody) ?? false;
        })
        .toBe(true);

      const editedBodyNode = detail.locator(".event-body", { hasText: editedBody }).last();
      const editedCard = editedBodyNode.locator("xpath=ancestor::*[.//button[@aria-label='Delete comment']][1]");
      await editedCard.hover();
      await editedCard.getByRole("button", { name: "Delete comment" }).click();
      const dialog = page.getByRole("dialog", { name: "Delete comment?" });
      await expect(dialog).toBeVisible();
      const deleteResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "DELETE" &&
          /\/api\/v1\/pulls\/github\/acme\/widgets\/1\/comments\/\d+$/.test(new URL(response.url()).pathname),
      );
      await dialog.getByRole("button", { name: "Delete", exact: true }).click();
      expect((await deleteResponse).status()).toBe(204);
      await expect(dialog).toBeHidden();
      await expect(detail.locator(".event-body", { hasText: editedBody })).toHaveCount(0);

      const reconciled = await page.request.post(`${server.info.base_url}/__e2e/activity/pr-comments/sync?number=1`);
      expect(reconciled.status(), await reconciled.text()).toBe(204);

      const persistedAfterDelete = await page.request.get(`${server.info.base_url}/api/v1/pulls/github/acme/widgets/1`);
      expect(persistedAfterDelete.ok()).toBe(true);
      const deletedDetail: { events?: Array<{ Body?: string }> } = await persistedAfterDelete.json();
      expect(deletedDetail.events?.some((event) => event.Body === editedBody)).toBe(false);
    } finally {
      await server.stop();
    }
  });
});
