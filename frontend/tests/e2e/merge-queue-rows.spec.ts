import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const repo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  platform_repo_id: 1001,
};

function pull(number: number, title: string, overrides: Record<string, unknown>) {
  return {
    ID: number,
    RepoID: 1,
    GitHubID: 1000 + number,
    Number: number,
    URL: `https://github.com/acme/widgets/pull/${number}`,
    Title: title,
    Author: "octo",
    State: "open",
    IsDraft: false,
    Body: "",
    HeadBranch: `feature/${number}`,
    BaseBranch: "main",
    Additions: 10,
    Deletions: 1,
    CommentCount: 0,
    ReviewDecision: "",
    CIStatus: "success",
    CIChecksJSON: "[]",
    CreatedAt: "2026-03-29T14:00:00Z",
    UpdatedAt: "2026-03-30T16:00:00Z",
    LastActivityAt: "2026-03-30T16:00:00Z",
    MergedAt: null,
    ClosedAt: null,
    MergeableState: "clean",
    KanbanStatus: "new",
    Starred: false,
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    platform_head_sha: `${number}`.padStart(40, "a"),
    repo,
    worktree_links: [],
    ...overrides,
  };
}

function checks(count: number, conclusion: string, status = "completed") {
  return Array.from({ length: count }, (_, i) => ({ name: `${conclusion || status}-${i}`, status, conclusion }));
}

// Every status icon a row can show, with two-digit CI counts.
const crowded = {
  ReviewDecision: "CHANGES_REQUESTED",
  MergeableState: "dirty",
  workspace: { id: "ws-1", status: "ready" },
  stack: { position: 12, size: 14, stack_id: 1 },
  CIChecksJSON: JSON.stringify([
    ...checks(12, "failure"),
    ...checks(15, "", "in_progress"),
    ...checks(48, "success"),
    ...checks(11, "skipped"),
  ]),
};

const entry = (position: number, state: string) => ({
  merge_queue: {
    position,
    state,
    enqueued_at: "2026-03-30T16:00:00Z",
    observed_at: "2026-03-30T16:00:00Z",
    estimated_time_to_merge_seconds: 1300,
  },
});

async function openQueues(page: Page, sidebarWidth: number): Promise<void> {
  await mockApi(page);
  await page.route("**/api/v1/pulls**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname !== "/api/v1/pulls" || url.searchParams.get("attributes") !== "merge_queue") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        pull(21, "Crowded queued change", { ...crowded, ...entry(1, "awaiting_checks") }),
        pull(22, "Quiet queued change", {
          ReviewDecision: "APPROVED",
          CIChecksJSON: JSON.stringify(checks(2, "success")),
          ...entry(2, "mergeable"),
        }),
        pull(23, "Crowded removed change", {
          ...crowded,
          merge_queue_removal: { reason: "merge_conflict", removed_at: "2026-03-30T13:30:00Z" },
        }),
      ]),
    });
  });
  await page.addInitScript((width) => localStorage.setItem("kenn-forge-sidebar-width", String(width)), sidebarWidth);
  await page.setViewportSize({ width: 1200, height: 700 });
  await page.goto("/queues");
  await expect(page.locator(".merge-queue__row")).toHaveCount(3);
}

// The status box is measured from the icons themselves: a shrunken
// container can report a box its icons overflow.
async function rowGeometry(page: Page, title: string) {
  const row = page.locator(".merge-queue__row").filter({ hasText: title });
  const [rowBox, chipBox, statusBox] = await Promise.all([
    row.boundingBox(),
    row.locator(".merge-queue__line--meta > :first-child").boundingBox(),
    row.locator(".merge-queue__status").evaluate((status) => {
      const boxes = [...status.children]
        .map((icon) => icon.getBoundingClientRect())
        .filter((box) => box.width > 0 && box.height > 0);
      const x = Math.min(...boxes.map((box) => box.left));
      const y = Math.min(...boxes.map((box) => box.top));
      return {
        x,
        y,
        width: Math.max(...boxes.map((box) => box.right)) - x,
        height: Math.max(...boxes.map((box) => box.bottom)) - y,
      };
    }),
  ]);
  if (!rowBox || !chipBox) throw new Error(`row ${title} is not rendered`);
  return { rowBox, chipBox, statusBox };
}

for (const sidebarWidth of [200, 340]) {
  test(`merge queue status icons stay inside their rows in a ${sidebarWidth}px sidebar`, async ({ page }) => {
    await openQueues(page, sidebarWidth);

    const list = page.locator(".merge-queues__body");
    const { scrollWidth, clientWidth } = await list.evaluate((el) => ({
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
    }));
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth);

    for (const title of ["Crowded queued change", "Quiet queued change", "Crowded removed change"]) {
      const { rowBox, chipBox, statusBox } = await rowGeometry(page, title);
      expect(statusBox.x + statusBox.width, title).toBeLessThanOrEqual(rowBox.x + rowBox.width);
      const besideChip = statusBox.y < chipBox.y + chipBox.height && chipBox.y < statusBox.y + statusBox.height;
      if (besideChip) expect(statusBox.x, title).toBeGreaterThanOrEqual(chipBox.x + chipBox.width);
    }
  });
}

test("a merge queue row with few status icons keeps them on the state chip's line", async ({ page }) => {
  await openQueues(page, 340);

  const { chipBox, statusBox } = await rowGeometry(page, "Quiet queued change");
  expect(statusBox.y).toBeLessThan(chipBox.y + chipBox.height);
});
