import { devices, expect, test } from "@playwright/test";

// Seed (internal/testutil/fixtures.go): acme/tools is reported private,
// acme/widgets public, and acme/archived has no privacy report.
const lock = { name: "Private repository" } as const;

test("the pull list marks only the private repository's group", async ({ page }) => {
  await page.goto("/pulls");
  const tools = page.locator(".sidebar-group-header", { hasText: "acme/tools" });
  const widgets = page.locator(".sidebar-group-header", { hasText: "acme/widgets" });

  await expect(tools.getByRole("img", lock)).toBeVisible();
  await expect(widgets).toBeVisible();
  await expect(widgets.getByRole("img", lock)).toHaveCount(0);
});

test("the pull request view marks a private repository", async ({ page }) => {
  await page.goto("/pulls/github/acme/tools/1");
  await expect(page.locator(".pull-detail .meta-row").getByRole("img", lock)).toBeVisible();

  await page.goto("/pulls/github/acme/widgets/1");
  await expect(page.locator(".pull-detail .meta-row")).toContainText("acme/widgets");
  await expect(page.locator(".pull-detail .meta-row").getByRole("img", lock)).toHaveCount(0);
});

test("the repositories page marks only the private repository", async ({ page }) => {
  await page.goto("/repos");
  const cards = page.locator(".repo-card");

  await expect(cards.filter({ hasText: "tools" }).getByRole("img", lock)).toBeVisible();
  await expect(cards.filter({ hasText: "widgets" })).toBeVisible();
  await expect(cards.filter({ hasText: "widgets" }).getByRole("img", lock)).toHaveCount(0);
});

test("settings marks private repositories and shows nothing for unknown privacy", async ({ page }) => {
  await page.goto("/settings");
  const row = (name: string) => page.locator(".repo-row", { hasText: `acme/${name}` });

  await expect(row("tools").getByRole("img", lock)).toBeVisible();
  await expect(row("widgets")).toBeVisible();
  await expect(row("widgets").getByRole("img", lock)).toHaveCount(0);
  await expect(row("archived")).toBeVisible();
  await expect(row("archived").getByRole("img", lock)).toHaveCount(0);
});

test("the repository selector marks only the private repository", async ({ page }) => {
  await page.goto("/pulls");
  await page.getByRole("button", { name: /Select repository/ }).click();

  await expect(page.getByRole("option", { name: /acme\/tools/ }).getByRole("img", lock)).toBeVisible();
  await expect(page.getByRole("option", { name: /acme\/widgets/ })).toBeVisible();
  await expect(page.getByRole("option", { name: /acme\/widgets/ }).getByRole("img", lock)).toHaveCount(0);
});

test("the issue list and issue view mark a private repository", async ({ page }) => {
  await page.goto("/issues/github/acme/tools/5");

  await expect(page.locator(".sidebar-group-header", { hasText: "acme/tools" }).getByRole("img", lock)).toBeVisible();
  await expect(page.locator(".sidebar-group-header", { hasText: "acme/widgets" })).toBeVisible();
  await expect(page.locator(".sidebar-group-header", { hasText: "acme/widgets" }).getByRole("img", lock)).toHaveCount(
    0,
  );
  await expect(page.locator(".issue-detail .meta-row").getByRole("img", lock)).toBeVisible();
});

test("the activity table keeps the lock on the repository chip's line", async ({ page }) => {
  await page.goto("/");
  const toolsCell = page.locator(".cell--repo", { hasText: "acme/tools" }).first();
  const widgetsCell = page.locator(".cell--repo", { hasText: "acme/widgets" }).first();
  await expect(toolsCell.getByRole("img", lock)).toBeVisible();
  await expect(widgetsCell.getByRole("img", lock)).toHaveCount(0);

  const lockBox = await toolsCell.getByRole("img", lock).boundingBox();
  const chipBox = await toolsCell.locator(".repo-chip").boundingBox();
  const widgetsChipBox = await widgetsCell.locator(".repo-chip").boundingBox();
  expect(lockBox).not.toBeNull();
  expect(chipBox).not.toBeNull();
  expect(lockBox!.y).toBeGreaterThanOrEqual(chipBox!.y);
  expect(lockBox!.y + lockBox!.height).toBeLessThanOrEqual(chipBox!.y + chipBox!.height);
  expect(chipBox!.height).toBe(widgetsChipBox!.height);
});

test("the phone pull list marks only the private repository", async ({ browser }) => {
  const phone = await browser.newContext({ ...devices["iPhone 13"] });
  try {
    const page = await phone.newPage();
    await page.goto("/m/pulls");
    const rows = page.locator(".pull-item");

    await expect(rows.filter({ hasText: "acme/tools" }).first().getByRole("img", lock)).toBeVisible();
    await expect(rows.filter({ hasText: "acme/widgets" }).first()).toBeVisible();
    await expect(rows.filter({ hasText: "acme/widgets" }).first().getByRole("img", lock)).toHaveCount(0);
  } finally {
    await phone.close();
  }
});
