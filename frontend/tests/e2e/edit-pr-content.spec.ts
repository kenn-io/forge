import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

const mockCapabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_labels: true,
  comment_mutation: true,
  state_mutation: true,
  merge_mutation: true,
  label_mutation: true,
  review_mutation: true,
  workflow_approval: true,
  ready_for_review: true,
  issue_mutation: true,
  review_draft_mutation: false,
  review_thread_resolution: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  thread_reply: false,
  thread_resolve: false,
  supported_review_actions: [],
};

const mockRepo = {
  provider: "github",
  platform_host: "github.com",
  repo_path: "acme/widgets",
  owner: "acme",
  name: "widgets",
  capabilities: mockCapabilities,
};

async function routeMockDashboardImage(page: Page): Promise<void> {
  await page.route("**/mock-dashboard.svg", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "image/svg+xml",
      body: [
        '<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="1400" viewBox="0 0 1600 1400">',
        '<rect width="1600" height="1400" fill="#f6f8fa"/>',
        '<rect x="120" y="120" width="1360" height="1160" rx="18" fill="#ffffff" stroke="#d0d7de" stroke-width="6"/>',
        '<rect x="210" y="220" width="420" height="80" rx="10" fill="#2563eb"/>',
        '<rect x="210" y="410" width="1180" height="48" rx="12" fill="#8fb0f4"/>',
        '<rect x="210" y="540" width="890" height="48" rx="12" fill="#8fb0f4"/>',
        '<rect x="210" y="780" width="1180" height="360" rx="18" fill="#e9eefc"/>',
        "</svg>",
      ].join(""),
    });
  });
}

async function renderMockDashboardMarkdownImage(page: Page) {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-body-btn").click();

  await page.locator(".body-edit-textarea").fill("Screenshots\n\n![Quality dashboard](/mock-dashboard.svg)");
  await page.locator(".body-edit .title-edit-save").click();

  const image = page.getByRole("img", { name: "Quality dashboard" }).first();
  const zoomButton = page.getByRole("button", { name: "Open image in expanded view" });
  await expect(image).toBeVisible();
  return { image, zoomButton };
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("edit title: click Edit, type, save", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await expect(page.locator(".detail-title")).toContainText("Add browser regression coverage");
  await page.locator(".edit-title-btn").click();

  const input = page.locator(".title-edit-input");
  await expect(input).toBeVisible();
  await input.fill("Updated title text");
  await page.locator(".title-edit-save").click();

  await expect(page.locator(".detail-title")).toContainText("Updated title text");
});

test("edit title: cancel with Escape", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-title-btn").click();

  const input = page.locator(".title-edit-input");
  await input.fill("should not persist");
  await input.press("Escape");

  await expect(page.locator(".detail-title")).toContainText("Add browser regression coverage");
});

test("edit title: save disabled when blank", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-title-btn").click();

  const input = page.locator(".title-edit-input");
  await input.fill("");
  await expect(page.locator(".title-edit-save")).toBeDisabled();
});

test("edit body: click Edit, type, save", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-body-btn").click();

  const textarea = page.locator(".body-edit-textarea");
  await expect(textarea).toBeVisible();
  await textarea.fill("New description content");
  await page.locator(".body-edit .title-edit-save").click();

  await expect(page.locator(".markdown-body")).toContainText("New description content");
});

test("edit body: cancel preserves original", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-body-btn").click();

  await page.locator(".body-edit-textarea").fill("discarded");
  await page.locator(".body-edit .title-edit-cancel").click();

  await expect(page.locator(".markdown-body")).toContainText("Adds Playwright smoke tests");
});

test("markdown mermaid fences render as diagrams", async ({ page }) => {
  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-body-btn").click();

  await page.locator(".body-edit-textarea").fill("```mermaid\ngraph TD\n  A --> B\n```");
  await page.locator(".body-edit .title-edit-save").click();

  await expect(page.locator(".markdown-body code.language-mermaid")).toHaveCount(0);
  await expect(page.locator(".markdown-body pre.mermaid.mermaid-viewer svg")).toBeVisible();
  await expect(page.getByRole("button", { name: "Zoom in diagram" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Zoom out diagram" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Copy Mermaid source" })).toBeVisible();
  await expect(page.locator(".markdown-body").getByRole("button", { name: "Reset diagram view" })).toBeVisible();
  await expect(page.locator(".markdown-body").getByRole("button", { name: /Pan diagram/ })).toHaveCount(0);

  const diagramViewport = page.locator(".markdown-body .mermaid-viewer__viewport");
  const diagramPan = page.locator(".markdown-body .mermaid-viewer__pan");
  const initialTransform = await diagramPan.evaluate((element) => getComputedStyle(element).transform);
  await diagramViewport.hover();
  await page.mouse.wheel(0, -240);
  await expect
    .poll(async () => diagramPan.evaluate((element) => getComputedStyle(element).transform))
    .not.toBe(initialTransform);

  await page.getByRole("button", { name: "Open diagram in expanded view" }).click();
  const expandedDiagram = page.getByRole("dialog", { name: "Expanded Mermaid diagram" });
  await expect(expandedDiagram).toBeVisible();
  await expect(expandedDiagram.getByRole("button", { name: "Close expanded diagram" })).toBeVisible();
  await expect(expandedDiagram.getByRole("button", { name: "Reset diagram view" })).toBeVisible();
  await expect(expandedDiagram.getByRole("button", { name: /Pan diagram/ })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Expanded Mermaid diagram" })).toBeHidden();
});

test("markdown images open in an expanded overlay", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 700 });
  await routeMockDashboardImage(page);

  const { image, zoomButton } = await renderMockDashboardMarkdownImage(page);

  const imageBox = await image.boundingBox();
  const buttonBox = await zoomButton.boundingBox();
  expect(imageBox).not.toBeNull();
  expect(buttonBox).not.toBeNull();
  expect(buttonBox!.x).toBeGreaterThan(imageBox!.x + imageBox!.width - 44);
  expect(buttonBox!.y).toBeLessThan(imageBox!.y + 16);
  await expect(zoomButton).toHaveCSS("opacity", "0");
  await expect(zoomButton).toHaveCSS("pointer-events", "none");

  await image.hover();
  await expect(zoomButton).toHaveCSS("opacity", "1");
  await expect(zoomButton).toHaveCSS("pointer-events", "auto");

  await zoomButton.click();
  const dialog = page.getByRole("dialog", { name: "Expanded image" });
  await expect(dialog).toBeVisible();
  const panel = dialog.locator(".markdown-image-lightbox__panel");
  const expandedImage = dialog.getByRole("img", { name: "Quality dashboard" });
  const closeButton = dialog.getByRole("button", { name: "Close expanded image" });
  await expect(expandedImage).toBeVisible();
  await expect(dialog).toBeFocused();

  const viewport = page.viewportSize();
  expect(viewport).not.toBeNull();
  const panelBox = await panel.boundingBox();
  const expandedBox = await expandedImage.boundingBox();
  expect(panelBox).not.toBeNull();
  expect(expandedBox).not.toBeNull();
  expect(panelBox!.width).toBeLessThanOrEqual(viewport!.width - 56 + 1);
  expect(panelBox!.height).toBeLessThanOrEqual(viewport!.height - 56 + 1);
  expect(expandedBox!.width).toBeLessThanOrEqual(viewport!.width - 56 + 1);
  expect(expandedBox!.height).toBeLessThanOrEqual(viewport!.height - 56 + 1);

  await expect(panel).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
  await expect(panel).toHaveCSS("border-top-width", "0px");
  await expect(panel).toHaveCSS("border-right-width", "0px");
  await expect(panel).toHaveCSS("border-bottom-width", "0px");
  await expect(panel).toHaveCSS("border-left-width", "0px");
  await expect(panel).toHaveCSS("border-radius", "0px");
  await expect(closeButton).toHaveCSS("opacity", "0");
  await expect(closeButton).toHaveCSS("pointer-events", "none");

  await expandedImage.hover();
  await expect(closeButton).toHaveCSS("opacity", "1");
  await expect(closeButton).toHaveCSS("pointer-events", "auto");
  await closeButton.click();
  await expect(dialog).toBeHidden();

  await image.hover();
  await zoomButton.click();
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(closeButton).toBeFocused();
  await expect(closeButton).toHaveCSS("opacity", "1");

  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
});

test("markdown image lightbox opens above drawer layers", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 700 });
  await routeMockDashboardImage(page);

  const { image, zoomButton } = await renderMockDashboardMarkdownImage(page);
  await page.evaluate(() => {
    const expander = document.querySelector(".markdown-image-expander");
    if (!expander) throw new Error("missing markdown image expander");

    const drawer = document.createElement("div");
    drawer.className = "test-drawer-layer";
    Object.assign(drawer.style, {
      alignItems: "flex-start",
      background: "rgba(0, 0, 0, 0.01)",
      display: "flex",
      inset: "0",
      justifyContent: "center",
      paddingTop: "80px",
      position: "fixed",
      zIndex: "100",
    });

    expander.replaceWith(drawer);
    drawer.append(expander);
    document.body.append(drawer);
  });

  await image.hover();
  await zoomButton.click();

  const dialog = page.getByRole("dialog", { name: "Expanded image" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveCSS("z-index", "130");
  await expect
    .poll(async () =>
      page.evaluate(() =>
        Boolean(
          document.elementFromPoint(window.innerWidth / 2, window.innerHeight / 2)?.closest(".markdown-image-lightbox"),
        ),
      ),
    )
    .toBe(true);
});

test.describe("touch markdown image zoom", () => {
  test.skip(({ browserName }) => browserName === "firefox", "Firefox does not support Playwright mobile emulation");
  test.use({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 844 },
  });

  test("keeps the zoom affordance tappable without hover", async ({ page }) => {
    await routeMockDashboardImage(page);

    const { zoomButton } = await renderMockDashboardMarkdownImage(page);
    await expect(zoomButton).toHaveCSS("opacity", "1");
    await expect(zoomButton).toHaveCSS("pointer-events", "auto");

    await zoomButton.tap();
    await expect(page.getByRole("dialog", { name: "Expanded image" })).toBeVisible();
  });
});

test("markdown tables keep compact columns readable", async ({ page }) => {
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        merge_request: {
          ID: 1,
          RepoID: 1,
          GitHubID: 101,
          Number: 42,
          URL: "https://github.com/acme/widgets/pull/42",
          Title: "Add browser regression coverage",
          Author: "marius",
          State: "open",
          IsDraft: false,
          Body: [
            "| Task | Commit | Description |",
            "| --- | --- | --- |",
            "| 1 | b2af4711 | Add the generated client shape without flattening the response. |",
          ].join("\n"),
          HeadBranch: "feature/playwright",
          BaseBranch: "main",
          Additions: 120,
          Deletions: 12,
          CommentCount: 3,
          ReviewDecision: "APPROVED",
          CIStatus: "success",
          CIChecksJSON: "[]",
          CreatedAt: "2026-03-29T14:00:00Z",
          UpdatedAt: "2026-03-30T14:00:00Z",
          LastActivityAt: "2026-03-30T14:00:00Z",
          MergedAt: null,
          ClosedAt: null,
          KanbanStatus: "reviewing",
          Starred: false,
          repo_owner: "acme",
          repo_name: "widgets",
          platform_host: "github.com",
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  const taskHeader = page.locator(".markdown-body th").filter({ hasText: "Task" });
  const commitCell = page.locator(".markdown-body td").filter({ hasText: "b2af4711" });
  await expect(taskHeader).toBeVisible();
  await expect(commitCell).toBeVisible();

  // Auto table layout gives the compact columns their content width, so
  // the commit hash renders on a single line without nowrap overrides.
  const commitLines = await commitCell.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    return Array.from(range.getClientRects()).filter((rect) => rect.width > 0).length;
  });
  expect(commitLines).toBe(1);

  // The long description column absorbs the remaining width; the compact
  // columns stay narrow instead of splitting the table evenly.
  const taskBox = await taskHeader.boundingBox();
  const tableBox = await page.locator(".markdown-body table").boundingBox();
  const bodyBox = await page.locator(".markdown-body").first().boundingBox();
  expect(taskBox).not.toBeNull();
  expect(tableBox).not.toBeNull();
  expect(bodyBox).not.toBeNull();
  expect(taskBox!.width).toBeLessThan(tableBox!.width * 0.2);
  expect(tableBox!.width).toBeLessThanOrEqual(bodyBox!.width + 1);

  await expect(page.locator(".markdown-body table")).toHaveCSS("border-collapse", "collapse");
});

test("markdown tables keep long unbroken values on one line and scroll", async ({ page }) => {
  const longToken = "deadbeef".repeat(30);
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        merge_request: {
          ID: 1,
          RepoID: 1,
          GitHubID: 101,
          Number: 42,
          URL: "https://github.com/acme/widgets/pull/42",
          Title: "Add browser regression coverage",
          Author: "marius",
          State: "open",
          IsDraft: false,
          Body: ["| Name | Digest |", "| --- | --- |", `| image | ${longToken} |`].join("\n"),
          HeadBranch: "feature/playwright",
          BaseBranch: "main",
          Additions: 120,
          Deletions: 12,
          CommentCount: 3,
          ReviewDecision: "APPROVED",
          CIStatus: "success",
          CIChecksJSON: "[]",
          CreatedAt: "2026-03-29T14:00:00Z",
          UpdatedAt: "2026-03-30T14:00:00Z",
          LastActivityAt: "2026-03-30T14:00:00Z",
          MergedAt: null,
          ClosedAt: null,
          KanbanStatus: "reviewing",
          Starred: false,
          repo_owner: "acme",
          repo_name: "widgets",
          platform_host: "github.com",
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  // The PR body container sets word-break: break-word; cells must reset it
  // so an unbroken token keeps its intrinsic width instead of splitting.
  const digestCell = page.locator(".markdown-body td").filter({ hasText: longToken.slice(0, 24) });
  await expect(digestCell).toBeVisible();
  const digestLines = await digestCell.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    return Array.from(range.getClientRects()).filter((rect) => rect.width > 0).length;
  });
  expect(digestLines).toBe(1);

  // The too-wide table scrolls horizontally within itself like GitHub.
  const overflow = await page
    .locator(".markdown-body table")
    .evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }));
  expect(overflow.scrollWidth).toBeGreaterThan(overflow.clientWidth);
});

test("markdown tables with images share width evenly like GitHub", async ({ page }) => {
  // Serve a fixed-size image for every screenshot cell so auto table layout
  // has real intrinsic widths to distribute.
  await page.route("https://images.test/**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "image/svg+xml",
      body: '<svg xmlns="http://www.w3.org/2000/svg" width="600" height="300"><rect width="600" height="300" fill="#7a7"/></svg>',
    });
  });

  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        merge_request: {
          ID: 1,
          RepoID: 1,
          GitHubID: 101,
          Number: 42,
          URL: "https://github.com/acme/widgets/pull/42",
          Title: "Add browser regression coverage",
          Author: "marius",
          State: "open",
          IsDraft: false,
          Body: [
            "| Default | Sort dropdown | Activity sort |",
            "| --- | --- | --- |",
            "| ![default](https://images.test/shot-1.svg) | ![dropdown](https://images.test/shot-2.svg) | ![activity](https://images.test/shot-3.svg) |",
          ].join("\n"),
          HeadBranch: "feature/playwright",
          BaseBranch: "main",
          Additions: 120,
          Deletions: 12,
          CommentCount: 3,
          ReviewDecision: "APPROVED",
          CIStatus: "success",
          CIChecksJSON: "[]",
          CreatedAt: "2026-03-29T14:00:00Z",
          UpdatedAt: "2026-03-30T14:00:00Z",
          LastActivityAt: "2026-03-30T14:00:00Z",
          MergedAt: null,
          ClosedAt: null,
          KanbanStatus: "reviewing",
          Starred: false,
          repo_owner: "acme",
          repo_name: "widgets",
          platform_host: "github.com",
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  const images = page.locator(".markdown-body table img");
  await expect(images).toHaveCount(3);

  const widths: number[] = [];
  for (let i = 0; i < 3; i++) {
    const box = await images.nth(i).boundingBox();
    expect(box).not.toBeNull();
    widths.push(box!.width);
  }

  // Every screenshot gets a real share of the row instead of the first
  // columns collapsing to slivers while the last column takes everything.
  for (const width of widths) {
    expect(width).toBeGreaterThan(100);
  }
  expect(Math.max(...widths) / Math.min(...widths)).toBeLessThan(1.25);

  // The table stays within the description container instead of overflowing.
  const tableBox = await page.locator(".markdown-body table").boundingBox();
  const bodyBox = await page.locator(".markdown-body").first().boundingBox();
  expect(tableBox).not.toBeNull();
  expect(bodyBox).not.toBeNull();
  expect(tableBox!.width).toBeLessThanOrEqual(bodyBox!.width + 1);
});

test("add description to empty-body PR shows add-description-btn", async ({ page }) => {
  // Override the GET route to return a PR with empty body.
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        merge_request: {
          ID: 1,
          RepoID: 1,
          GitHubID: 101,
          Number: 42,
          URL: "https://github.com/acme/widgets/pull/42",
          Title: "Add browser regression coverage",
          Author: "marius",
          State: "open",
          IsDraft: false,
          Body: "",
          HeadBranch: "feature/playwright",
          BaseBranch: "main",
          Additions: 120,
          Deletions: 12,
          CommentCount: 3,
          ReviewDecision: "APPROVED",
          CIStatus: "success",
          CIChecksJSON: "[]",
          CreatedAt: "2026-03-29T14:00:00Z",
          UpdatedAt: "2026-03-30T14:00:00Z",
          LastActivityAt: "2026-03-30T14:00:00Z",
          MergedAt: null,
          ClosedAt: null,
          KanbanStatus: "reviewing",
          Starred: false,
          repo_owner: "acme",
          repo_name: "widgets",
          platform_host: "github.com",
          repo: mockRepo,
          worktree_links: [],
        },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        repo: mockRepo,
        detail_loaded: true,
        detail_fetched_at: "2026-03-30T14:00:00Z",
        worktree_links: [],
      }),
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");

  const addBtn = page.locator(".add-description-btn");
  await expect(addBtn).toBeVisible();
  await addBtn.click();

  const textarea = page.locator(".body-edit-textarea");
  await expect(textarea).toBeVisible();
  await textarea.fill("Added a new description");
  await page.locator(".body-edit .title-edit-save").click();

  await expect(page.locator(".markdown-body")).toContainText("Added a new description");
});
