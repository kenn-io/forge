import { expect, test } from "@playwright/test";

import { openSettingsPanel } from "./support/settingsPanel";

// The activity-selection-restore flows, the legacy /mail fallthrough, and the
// list-row -> detail-pane opens moved to the browser tier
// (frontend/src/App.navigation.browser.svelte.ts). What stays here depends on the
// external mode-shell backends (Kata/Docs) or on diff rendering, which
// are full-stack concerns best exercised against the live backend.

test.describe("view navigation", () => {
  test("header tabs switch between views", async ({ page }) => {
    await page.goto("/");

    // Wait for the app to be ready (activity feed visible).
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });

    // Click Kata tab -> URL should contain /kata, shell renders.
    await page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: "Kata" }).click();
    await expect(page).toHaveURL(/\/kata/);
    await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();

    // Click Docs tab -> URL should contain /docs, docs shell renders.
    await page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: "Docs" }).click();
    await expect(page).toHaveURL(/\/docs/);
    await page.locator(".docs-workspace").waitFor({ state: "visible", timeout: 10_000 });

    // Click PRs tab -> URL should contain /pulls, list renders.
    await page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: "PRs" }).click();
    await expect(page).toHaveURL(/\/pulls/);
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });

    // Click Issues tab -> URL should contain /issues, list renders.
    await page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: "Issues" }).click();
    await expect(page).toHaveURL(/\/issues/);
    await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });

    // Click Activity tab -> back to root, feed renders.
    await page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: "Activity" }).click();
    // Verify pathname is exactly the base path (default "/").
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);
    const basePath = new URL(page.url()).pathname.replace(/\?.*$/, "");
    expect(basePath).toBe("/");
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 5_000 });
  });

  test("Kata shell does not expose the repository selector", async ({ page }) => {
    await page.goto("/kata");

    await expect.poll(() => new URL(page.url()).pathname).toBe("/kata");
    await expect(page.getByRole("heading", { name: "Kata" })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Select repository:/ })).not.toBeAttached();
  });

  test("Docs route loads its mode shell directly", async ({ page }) => {
    await page.goto("/docs");
    await expect(page).toHaveURL(/\/docs$/);
    await page.locator(".docs-workspace").waitFor({ state: "visible", timeout: 10_000 });
  });

  test("removed Messages surfaces stay gone", async ({ page }) => {
    // The old /messages route is no longer parsed: it falls back to the
    // Activity feed instead of loading a Messages shell.
    await page.goto("/messages");
    await page.locator(".activity-feed").waitFor({ state: "visible", timeout: 10_000 });
    await expect(page.getByRole("heading", { name: "Messages" })).toHaveCount(0);

    // Even with the imported modes visible (this server enables Kata and
    // Docs), the top nav must not offer a Messages tab.
    await expect(page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: "Docs" })).toBeVisible();
    await expect(page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: "Messages" })).toHaveCount(0);

    // Settings no longer exposes a Messages visibility toggle.
    await page.goto("/settings");
    await openSettingsPanel(page, "Visible modes");
    await expect(page.getByLabel("Docs")).toBeVisible();
    await expect(page.getByLabel("Messages")).toHaveCount(0);
  });

  test("settings button toggles back to the previous route", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/1/files");
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1\/files$/);

    await page.getByTitle("Settings").click();
    await expect(page).toHaveURL(/\/settings$/);
    await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

    await page.getByTitle("Settings").click();
    await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1\/files$/);
    await page.locator(".diff-file").first().waitFor({ state: "visible", timeout: 10_000 });
  });
});
