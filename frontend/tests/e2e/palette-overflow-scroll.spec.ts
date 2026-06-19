import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

// Covers the two UX fixes in commit 7e89ac7: the palette is centered
// vertically (was previously stuck at top: 80px) and arrow-key navigation
// scrolls the highlighted row into view when the list overflows. Both
// behaviors depend on real browser layout that jsdom cannot reproduce.

test.beforeEach(async ({ page }) => {
  await mockApi(page);
  // Inject a long PR list so the palette's pr: results overflow the
  // 480px-tall list pane. This route is registered AFTER mockApi, so it
  // takes precedence for /api/v1/pulls.
  await page.route("**/api/v1/pulls", async (route) => {
    const many = Array.from({ length: 40 }, (_, i) => ({
      ID: 1000 + i,
      RepoID: 1,
      GitHubID: 9000 + i,
      Number: 1000 + i,
      URL: `https://github.com/acme/widgets/pull/${1000 + i}`,
      Title: `Overflow row ${String(i).padStart(2, "0")}`,
      Author: "marius",
      State: "open",
      IsDraft: false,
      Body: "",
      HeadBranch: `feature/overflow-${i}`,
      BaseBranch: "main",
      Additions: 1,
      Deletions: 0,
      CommentCount: 0,
      ReviewDecision: "",
      CIStatus: "success",
      CIChecksJSON: "[]",
      CreatedAt: "2026-03-29T14:00:00Z",
      UpdatedAt: "2026-03-30T14:00:00Z",
      LastActivityAt: "2026-03-30T14:00:00Z",
      MergedAt: null,
      ClosedAt: null,
      KanbanStatus: "new",
      Starred: false,
      repo_owner: "acme",
      repo_name: "widgets",
      platform_host: "github.com",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
        capabilities: {
          read_repositories: true,
          read_merge_requests: true,
          read_issues: true,
          read_comments: true,
          read_releases: true,
          read_ci: true,
          comment_mutation: true,
          state_mutation: true,
          merge_mutation: true,
          ready_for_review: true,
          draft_mutation: true,
          review_mutation: true,
          workflow_approval: true,
        },
      },
      worktree_links: [],
    }));
    await route.fulfill({ json: many });
  });
});

test("palette is vertically centered in the viewport", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");

  const dialog = page.getByRole("dialog", { name: "Command palette" });
  await expect(dialog).toBeVisible();

  const box = await dialog.boundingBox();
  expect(box).not.toBeNull();
  const viewport = page.viewportSize();
  expect(viewport).not.toBeNull();
  const dialogCenterY = box!.y + box!.height / 2;
  const viewportCenterY = viewport!.height / 2;
  // Allow a small tolerance for browser rounding; the previous
  // top: 80px implementation would put the dialog center near
  // 80 + 240 = 320, far above the viewport's 450 center.
  expect(Math.abs(dialogCenterY - viewportCenterY)).toBeLessThan(8);
});

test("arrow-down past the visible window scrolls the highlighted row into view", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");

  const dialog = page.getByRole("dialog", { name: "Command palette" });
  await expect(dialog).toBeVisible();

  // pr: filters to the injected long list. groupResults caps at 10
  // entries per group, so the list shows 10 PR rows. The .palette-list
  // pane is short enough that arrow-down past visible rows must scroll.
  await page.locator(".palette-input").fill("pr:");

  const rows = page.locator(".palette-list .palette-row");
  await expect(rows.first()).toBeVisible();
  const rowCount = await rows.count();
  expect(rowCount).toBeGreaterThan(0);

  // Arrow-down to the last row. After each press, the highlighted row
  // must be inside the .palette-list bounds (i.e. scrolled into view).
  for (let i = 1; i < rowCount; i++) {
    await page.keyboard.press("ArrowDown");
  }

  const inView = await page.evaluate(() => {
    const listEl = document.querySelector<HTMLElement>(".palette-list");
    const highlight = document.querySelector<HTMLElement>(".palette-row-highlight");
    if (!listEl || !highlight) return null;
    const lr = listEl.getBoundingClientRect();
    const hr = highlight.getBoundingClientRect();
    // scrollIntoView({ block: "nearest" }) keeps the row's top and
    // bottom within the container's visible area.
    return hr.top >= lr.top - 1 && hr.bottom <= lr.bottom + 1;
  });
  expect(inView).toBe(true);
});
