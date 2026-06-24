import { expect, test, type Page } from "@playwright/test";

import { startIsolatedE2EServerWithOptions } from "./support/e2eServer.js";

// The repo-tree selector's component logic — multi-select with checkboxes, owner
// cascade, stale provider-qualified value normalization, provider-collision
// qualification, and single-repo-owner flatten labels — is covered by the jsdom
// component tests frontend/src/lib/components/{RepoTypeahead,RepoTreeNode,repoTree}.test.ts.
// The backend repo filter (lists narrowed by the selected repos) is covered by
// internal/server TestAPIRepoFilterAcceptsMultipleRepos against the real
// SQLite-backed server.
//
// What stays here needs the full stack: the provider-collision case persists a
// provider-qualified value end to end against a live server seeded with two
// hosts that collide by path, and the native-click focus case proves a real
// checkbox click does not steal focus from the filter input (a native event
// interaction the jsdom component test cannot reproduce).

async function waitForIssueList(page: Page): Promise<void> {
  await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

test("repository selector persists provider-qualified filters for provider collisions", async ({ browser }) => {
  const server = await startIsolatedE2EServerWithOptions({ providerCollision: true });
  const page = await browser.newPage();
  try {
    await page.goto(`${server.info.base_url}/issues`);
    await waitForIssueList(page);

    const selector = page.getByTitle("Select repository");
    await selector.click();

    const input = page.getByPlaceholder("Filter repos...");
    await input.fill("gitea/widgets");

    const giteaRow = page.getByRole("option", {
      name: "gitea/github.com/acme/widgets",
      exact: true,
    });
    await expect(giteaRow).toBeVisible();
    await expect(giteaRow.locator(".repo-tree-label")).toHaveText("gitea/widgets");
    await expect(page.getByRole("option", { name: "github/github.com/acme/widgets", exact: true })).toHaveCount(0);

    await giteaRow.click();
    await page.keyboard.press("Escape");

    await expect
      .poll(() => page.evaluate(() => localStorage.getItem("middleman-filter-repo")))
      .toBe("gitea|github.com/acme/widgets");
    await expect(page.getByText("Gitea provider collision issue")).toBeVisible();
    await expect(page.getByText("Widget rendering broken on Safari")).toHaveCount(0);
  } finally {
    await page.close();
    await server.stop();
  }
});

test("keyboard navigation survives a real checkbox click", async ({ page }) => {
  // A real click (not just mousedown) on a row checkbox must not steal focus
  // from the filter input. The checkbox is a focusable native input and its
  // mousedown stops propagation (skipping the list's preventBlur), so without
  // preventDefault the click would blur the input and kill keyboard handling,
  // which is bound only to that input.
  await page.goto("/issues");
  await waitForIssueList(page);

  const selector = page.getByTitle("Select repository");
  await selector.click();

  const input = page.getByPlaceholder("Filter repos...");
  await expect(input).toBeFocused();

  // Real click on a leaf repo's checkbox.
  await page
    .getByRole("option", {
      name: "github/github.com/acme/widgets",
      exact: true,
    })
    .locator("input[type='checkbox']")
    .click();
  await expect(
    page
      .getByRole("option", {
        name: "github/github.com/acme/widgets",
        exact: true,
      })
      .locator("input[type='checkbox']"),
  ).toBeChecked();

  // Focus must still be on the input, and keyboard handling must still work:
  // Escape closes the dropdown.
  await expect(input).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator(".typeahead-list")).toHaveCount(0);
});
