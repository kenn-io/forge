import { expect, test, type Page } from "@playwright/test";

import { startIsolatedE2EServerWithOptions } from "./support/e2eServer.js";

async function waitForIssueList(page: Page): Promise<void> {
  await page.locator(".issue-item").first().waitFor({ state: "visible", timeout: 10_000 });
}

async function selectRepo(page: Page, name: string): Promise<void> {
  const option = page.getByRole("option", { name });
  await expect(option).toBeVisible();
  await option.click();
  await expect(option.locator("input[type='checkbox']")).toBeChecked();
}

test("repository selector filters dashboard lists by multiple selected repos", async ({ page }) => {
  await page.goto("/issues");
  await waitForIssueList(page);

  const selector = page.getByTitle("Select repository");
  await selector.click();

  await selectRepo(page, "github.com/acme/widgets");
  await selectRepo(page, "github.com/acme/tools");

  await page.keyboard.press("Escape");

  await expect(selector.locator(".typeahead-value")).toHaveText("2 repos");
  await expect(page.locator(".repo-header__name")).toHaveText(["acme/widgets", "acme/tools"]);

  await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();
  await expect(page.getByText("Add dark mode support")).toBeVisible();
  await expect(page.getByText("Support config file loading")).toBeVisible();
  await expect(page.getByText("GitLab read-only issue")).toHaveCount(0);

  await expect(page.evaluate(() => localStorage.getItem("middleman-filter-repo"))).resolves.toBe(
    "github.com/acme/widgets,github.com/acme/tools",
  );
});

test("repository selector normalizes stale provider-qualified persisted multi-select values", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("middleman-filter-repo", "github/github.com/acme/widgets,github.com/acme/tools");
  });

  await page.goto("/issues");
  await waitForIssueList(page);

  await expect
    .poll(() => page.evaluate(() => localStorage.getItem("middleman-filter-repo")))
    .toBe("github.com/acme/widgets,github.com/acme/tools");
  await expect(page.getByTitle("Select repository").locator(".typeahead-value")).toHaveText("2 repos");

  await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();
  await expect(page.getByText("Support config file loading")).toBeVisible();
  await expect(page.getByText("GitLab read-only issue")).toHaveCount(0);
});

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
      name: "github.com/acme/widgets",
      exact: true,
    })
    .locator("input[type='checkbox']")
    .click();
  await expect(
    page
      .getByRole("option", {
        name: "github.com/acme/widgets",
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

test("flattened single-repo owner shows the owner/repo path, not a bare repo name", async ({ page }) => {
  // gitlab.example.com/group has one repo "project", so it auto-flattens to a
  // single row. That row must read "group/project" (visible text) to stay
  // distinguishable from a same-named repo under another owner, while its
  // accessible name remains the full path.
  await page.goto("/issues");
  await waitForIssueList(page);

  await page.getByTitle("Select repository").click();

  const row = page.getByRole("option", {
    name: "gitlab.example.com/group/project",
    exact: true,
  });
  await expect(row).toBeVisible();
  // visible label is "group/project", not the bare "project"
  await expect(row.locator(".repo-tree-label")).toHaveText("group/project");
});

test("repository selector cascades an owner group to all its repos", async ({ page }) => {
  await page.goto("/issues");
  await waitForIssueList(page);

  const selector = page.getByTitle("Select repository");
  await selector.click();

  // The owner row's checkbox cascades selection to every repo under that
  // owner. The row body would only toggle expand/collapse, so the checkbox
  // is the deliberate target. Selection is wired to mousedown (see
  // RepoTreeNode.checkboxMouseDown), so dispatch that event directly rather
  // than a click, mirroring the component test's fireEvent.mouseDown.
  const ownerCheckbox = page
    .getByRole("option", { name: "github.com/acme", exact: true })
    .locator("input[type='checkbox']");
  await expect(ownerCheckbox).toBeVisible();
  await ownerCheckbox.dispatchEvent("mousedown");
  await expect(ownerCheckbox).toBeChecked();

  await page.keyboard.press("Escape");

  const stored = await page.evaluate(() => localStorage.getItem("middleman-filter-repo"));
  expect(stored).toContain("github.com/acme/widgets");
  expect(stored).toContain("github.com/acme/tools");
  expect(stored).toContain("github.com/acme/archived");

  // The group selection keeps acme's issues visible and excludes repos
  // outside the group, such as the GitLab read-only fixture.
  await expect(page.getByText("Widget rendering broken on Safari")).toBeVisible();
  await expect(page.getByText("Support config file loading")).toBeVisible();
  await expect(page.getByText("GitLab read-only issue")).toHaveCount(0);
});
