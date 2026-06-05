import { execFileSync } from "node:child_process";
import { access } from "node:fs/promises";
import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import { startIsolatedE2EServer, startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  platform_host: string;
  repo_owner: string;
  repo_name: string;
  item_type: string;
  item_number: number;
  git_head_ref: string;
  worktree_path: string;
  tmux_session: string;
  status: string;
  error_message?: string | null;
};

type PullDetailResponse = {
  events: Array<{
    EventType: string;
    Author: string;
    Body: string;
    Summary: string;
  }>;
};

const lockedWorkspaceTestTimeoutMs = 120_000;

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function gitOutput(dir: string, args: string[]): string {
  return execFileSync("git", args, {
    cwd: dir,
    encoding: "utf8",
  }).trim();
}

async function waitForWorkspaceReady(api: APIRequestContext, workspaceId: string): Promise<WorkspaceStatusResponse> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") {
      return workspace;
    }
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  throw new Error(`workspace ${workspaceId} did not become ready`);
}

test.describe("detail action buttons", () => {
  test.describe.configure({ timeout: lockedWorkspaceTestTimeoutMs });

  test("issue detail creates a middleman workspace and opens its terminal", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const server = isolatedServer;
      const apiContext = api;

      await page.goto(`${server.info.base_url}/issues/github/acme/widgets/10`);
      await expect(page.locator(".issue-detail")).toBeVisible();

      const createResponsePromise = page.waitForResponse((response) => {
        const url = response.url();
        return (
          response.request().method() === "POST" &&
          url === `${server.info.base_url}/api/v1/issues/github/acme/widgets/10/workspace`
        );
      });

      await page.locator(".btn--workspace").click();

      const createResponse = await createResponsePromise;
      expect(createResponse.status()).toBe(202);

      const createdWorkspace = (await createResponse.json()) as WorkspaceStatusResponse;
      expect(createdWorkspace.platform_host).toBe("github.com");
      expect(createdWorkspace.item_type).toBe("issue");
      expect(createdWorkspace.item_number).toBe(10);
      expect(createdWorkspace.git_head_ref).toBe("middleman/issue-10-widget-rendering-broken-on-safari");

      await expect(page).toHaveURL(new RegExp(`/terminal/${createdWorkspace.id}$`));

      const readyWorkspace = await waitForWorkspaceReady(apiContext, createdWorkspace.id);
      await access(readyWorkspace.worktree_path);
      expect(gitOutput(readyWorkspace.worktree_path, ["branch", "--show-current"])).toBe(
        "middleman/issue-10-widget-rendering-broken-on-safari",
      );
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("PR detail shows approve workflows after real pending workflow sync", async ({ page }) => {
    const server = await startIsolatedE2EServer();
    try {
      const seedResponse = await page.request.post(`${server.info.base_url}/__e2e/pr-workflow-approval/required`);
      expect(seedResponse.ok()).toBe(true);

      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);

      await expect(page.getByRole("button", { name: "Approve workflows" })).toBeVisible({
        timeout: 10_000,
      });
    } finally {
      await server.stop();
    }
  });

  test("issue workspace button still creates a middleman workspace after detail sync refresh", async ({ page }) => {
    const createdWorkspace = {
      id: "ws-issue-10",
      platform_host: "github.com",
      repo_owner: "acme",
      repo_name: "widgets",
      item_type: "issue",
      item_number: 10,
      git_head_ref: "middleman/issue-10",
      worktree_path: "/tmp/workspaces/issue-10",
      tmux_session: "middleman-ws-issue-10",
      status: "ready",
      created_at: "2026-04-20T12:00:00Z",
      mr_title: "Add keyboard shortcut docs",
      mr_state: "open",
    };
    let createCalls = 0;
    await page.route("**/api/v1/issues/github/acme/widgets/10/workspace", async (route) => {
      createCalls += 1;
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify(createdWorkspace),
      });
    });
    await page.route("**/api/v1/workspaces/ws-issue-10", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(createdWorkspace),
      });
    });
    await page.route("**/api/v1/workspaces", async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ workspaces: [createdWorkspace] }),
      });
    });
    await page.route("**/api/v1/events", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
    });

    const syncResponsePromise = page.waitForResponse((response) => {
      const url = response.url();
      return response.request().method() === "POST" && url.endsWith("/api/v1/issues/github/acme/widgets/10/sync/async");
    });
    const createResponsePromise = page.waitForResponse((response) => {
      const url = response.url();
      return response.request().method() === "POST" && url.endsWith("/api/v1/issues/github/acme/widgets/10/workspace");
    });

    await page.goto("/issues/github/acme/widgets/10");
    await expect(page.locator(".issue-detail")).toBeVisible();

    // Background sync enqueues asynchronously; the server returns 202
    // with no body. The platform host defaults to github.com server-side
    // when the URL has no platform_host query parameter.
    const syncResponse = await syncResponsePromise;
    expect(syncResponse.status()).toBe(202);
    expect(new URL(syncResponse.url()).searchParams.has("platform_host")).toBe(false);

    await page.locator(".btn--workspace").click();
    const createResponse = await createResponsePromise;
    expect(createResponse.status()).toBe(202);
    expect(createCalls).toBe(1);
    await expect(page).toHaveURL(/\/terminal\/ws-issue-10$/);
  });

  test("issue workspace conflict dialog can reuse the existing branch", async ({ page }) => {
    const createdWorkspace = {
      id: "ws-issue-10",
      platform_host: "github.com",
      repo_owner: "acme",
      repo_name: "widgets",
      item_type: "issue",
      item_number: 10,
      git_head_ref: "middleman/issue-10",
      worktree_path: "/tmp/workspaces/issue-10",
      tmux_session: "middleman-ws-issue-10",
      status: "ready",
      created_at: "2026-04-20T12:00:00Z",
      mr_title: "Add keyboard shortcut docs",
      mr_state: "open",
    };
    const conflict = {
      type: "urn:middleman:error:issue-workspace-branch-conflict",
      title: "Issue workspace branch conflict",
      status: 409,
      detail: "A local branch with the requested name already exists.",
      errors: [
        {
          message: "Requested branch already exists",
          location: "body.git_head_ref",
          value: "middleman/issue-10",
        },
        {
          message: "Suggested alternative branch name",
          location: "body.suggested_git_head_ref",
          value: "middleman/issue-10-2",
        },
      ],
    };

    const payloads: Record<string, unknown>[] = [];
    await page.route("**/api/v1/issues/github/acme/widgets/10/workspace", async (route) => {
      payloads.push(JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>);
      if (payloads.length === 1) {
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify(conflict),
        });
        return;
      }
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify(createdWorkspace),
      });
    });
    await page.route("**/api/v1/workspaces/ws-issue-10", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(createdWorkspace),
      });
    });
    await page.route("**/api/v1/workspaces", async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ workspaces: [createdWorkspace] }),
      });
    });
    await page.route("**/api/v1/events", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
    });

    await page.goto("/issues/github/acme/widgets/10");
    await expect(page.locator(".issue-detail")).toBeVisible();

    await page.locator(".btn--workspace").click();

    const dialog = page.getByRole("dialog", {
      name: "Branch Name Conflict",
    });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("middleman/issue-10");
    await expect(dialog.locator("#issue-workspace-branch-name")).toHaveValue("middleman/issue-10-2");

    await dialog.getByRole("button", { name: "Use Existing Branch" }).click();

    await expect
      .poll(() => payloads)
      .toEqual([
        {},
        {
          git_head_ref: "middleman/issue-10",
          reuse_existing_branch: true,
        },
      ]);
    await expect(page).toHaveURL(/\/terminal\/ws-issue-10$/);
  });

  test("issue workspace conflict dialog can create a new suggested branch", async ({ page }) => {
    const createdWorkspace = {
      id: "ws-issue-10",
      platform_host: "github.com",
      repo_owner: "acme",
      repo_name: "widgets",
      item_type: "issue",
      item_number: 10,
      git_head_ref: "middleman/issue-10-2",
      worktree_path: "/tmp/workspaces/issue-10-2",
      tmux_session: "middleman-ws-issue-10",
      status: "ready",
      created_at: "2026-04-20T12:00:00Z",
      mr_title: "Add keyboard shortcut docs",
      mr_state: "open",
    };
    const conflict = {
      type: "urn:middleman:error:issue-workspace-branch-conflict",
      title: "Issue workspace branch conflict",
      status: 409,
      detail: "A local branch with the requested name already exists.",
      errors: [
        {
          message: "Requested branch already exists",
          location: "body.git_head_ref",
          value: "middleman/issue-10",
        },
        {
          message: "Suggested alternative branch name",
          location: "body.suggested_git_head_ref",
          value: "middleman/issue-10-2",
        },
      ],
    };

    const payloads: Record<string, unknown>[] = [];
    await page.route("**/api/v1/issues/github/acme/widgets/10/workspace", async (route) => {
      payloads.push(JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>);
      if (payloads.length === 1) {
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify(conflict),
        });
        return;
      }
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify(createdWorkspace),
      });
    });
    await page.route("**/api/v1/workspaces/ws-issue-10", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(createdWorkspace),
      });
    });
    await page.route("**/api/v1/workspaces", async (route) => {
      if (route.request().method() !== "GET") {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ workspaces: [createdWorkspace] }),
      });
    });
    await page.route("**/api/v1/events", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: "",
      });
    });

    await page.goto("/issues/github/acme/widgets/10");
    await expect(page.locator(".issue-detail")).toBeVisible();

    await page.locator(".btn--workspace").click();

    const dialog = page.getByRole("dialog", {
      name: "Branch Name Conflict",
    });
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Create New Branch" }).click();

    await expect
      .poll(() => payloads)
      .toEqual([
        {},
        {
          git_head_ref: "middleman/issue-10-2",
        },
      ]);
    await expect(page).toHaveURL(/\/terminal\/ws-issue-10$/);
  });

  test("pull request actions use shared ActionButton component", async ({ page }) => {
    await page.goto("/pulls");
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });
    await page.locator(".pull-item").filter({ hasText: "Add widget caching layer" }).first().click();
    await expect(page.locator(".pull-detail")).toBeVisible();

    const approve = page.locator(".btn--approve");
    const merge = page.locator(".btn--merge");
    const close = page.locator(".btn--close");

    await expect(approve).toBeVisible();
    await expect(merge).toBeVisible();
    await expect(close).toBeVisible();

    // All action buttons use the shared action-button base class
    for (const btn of [approve, merge, close]) {
      const classes = await btn.getAttribute("class");
      expect(classes).toContain("action-button");
    }
  });

  test("repo merge permission disables merge action with reason end-to-end", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      const seedResponse = await page.request.post(`${baseURL}/__e2e/repo-settings/viewer-can-merge/deny`);
      expect(seedResponse.ok()).toBe(true);

      const settingsResponse = await page.request.get(`${baseURL}/api/v1/repo/github/acme/widgets`);
      expect(settingsResponse.ok()).toBe(true);
      const settings = (await settingsResponse.json()) as {
        ViewerCanMerge: boolean;
        operations: {
          merge_pr: { available: boolean; code?: string };
        };
      };
      expect(settings.ViewerCanMerge).toBe(false);
      expect(settings.operations.merge_pr.available).toBe(false);
      expect(settings.operations.merge_pr.code).toBe("viewer_cannot_merge");

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await expect(page.locator(".detail-title")).toContainText("Add widget caching layer");
      const merge = page.locator(".btn--merge").first();
      await expect(merge).toBeVisible();
      await expect(merge).toBeDisabled();
      await expect(merge).toHaveAttribute("title", "You do not have permission to merge in this repository");
      // Clicking a disabled merge button must not open the merge modal.
      await merge.click({ force: true });
      await expect(page.locator(".modal-title", { hasText: "Merge Pull Request" })).toHaveCount(0);
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("conflicted pull request disables the merge action", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/2`);
      await expect(page.locator(".pull-detail")).toBeVisible();
      await expect(page.locator(".detail-title")).toContainText("Fix race condition in event loop");
      await expect(page.getByText("This branch has conflicts")).toBeVisible();

      const merge = page.locator(".btn--merge").first();
      await expect(merge).toBeDisabled();
      await merge.click({ force: true });
      await expect(page.locator(".modal-title", { hasText: "Merge Pull Request" })).toHaveCount(0);
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("narrow actions menu closes when clicking outside", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.goto("/pulls/github/acme/widgets/1");
    await expect(page.locator(".pull-detail")).toBeVisible();

    await page.locator(".actions-menu-trigger").click();
    await expect(page.locator(".actions-menu-popover")).toBeVisible();

    await page.locator(".detail-title").click();
    await expect(page.locator(".actions-menu-popover")).toHaveCount(0);
  });

  test("narrow actions menu shows state change failures after closing", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.route("**/api/v1/pulls/github/acme/widgets/1/github-state", async (route) => {
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ detail: "backend down" }),
      });
    });

    await page.goto("/pulls/github/acme/widgets/1");
    await expect(page.locator(".pull-detail")).toBeVisible();

    await page.locator(".actions-menu-trigger").click();
    await page.locator(".actions-menu-popover .btn--close").click();

    await expect(page.locator(".actions-menu-popover")).toHaveCount(0);
    await expect(page.locator(".primary-actions-wrap .action-error")).toHaveText("backend down");
    await expect(page.locator(".primary-actions-wrap .action-error")).toBeVisible();
  });

  test("approve form stays visible in the narrow actions menu", async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 720 });
    await page.goto("/pulls/github/acme/widgets/1");
    await expect(page.locator(".pull-detail")).toBeVisible();

    await page.locator(".actions-menu-trigger").click();
    await expect(page.locator(".actions-menu-popover")).toBeVisible();

    await page.locator(".actions-menu-popover .btn--approve").click();
    await expect(page.locator(".actions-menu-popover")).toBeVisible();
    await expect(page.locator(".actions-menu-popover .approve-comment")).toBeVisible();
    await expect(page.locator(".actions-menu-popover .btn--green")).toHaveText("Approve");
  });

  test("approve action submits through API, persists review, and refreshes detail and list data", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const baseURL = isolatedServer.info.base_url;
      const approvalBody = "LGTM from approve e2e";
      const detailURL = `${baseURL}/api/v1/pulls/github/acme/widgets/1`;

      await page.goto(`${baseURL}/pulls/github/acme/widgets/1`);
      await expect(page.locator(".pull-detail")).toBeVisible();

      await page.locator(".btn--approve").first().click();
      await page.locator(".approve-comment").fill(approvalBody);

      const approveResponsePromise = page.waitForResponse(
        (response) => response.request().method() === "POST" && response.url() === `${detailURL}/approve`,
      );
      const detailRefreshPromise = page.waitForResponse(
        (response) => response.request().method() === "GET" && response.url() === detailURL,
      );
      const listRefreshPromise = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "GET" && url.origin === baseURL && url.pathname === "/api/v1/pulls";
      });

      await page.locator(".approve-actions .btn--green").click();

      const approveResponse = await approveResponsePromise;
      expect(approveResponse.status()).toBe(200);
      expect((await approveResponse.json()).status).toBe("approved");
      expect((await detailRefreshPromise).ok()).toBe(true);
      expect((await listRefreshPromise).ok()).toBe(true);

      await expect(page.locator(".approve-popover")).toHaveCount(0);
      await expect(page.locator(".event-card", { hasText: approvalBody })).toBeVisible();

      await expect
        .poll(async () => {
          const response = await api!.get("/api/v1/pulls/github/acme/widgets/1");
          expect(response.ok()).toBe(true);
          const detail = (await response.json()) as PullDetailResponse;
          return detail.events.some(
            (event) =>
              event.EventType === "review" &&
              event.Author === "fixture-bot" &&
              event.Summary === "APPROVED" &&
              event.Body === approvalBody,
          );
        })
        .toBe(true);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("self-contained actions close the narrow actions menu after success", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      const baseURL = isolatedServer.info.base_url;

      await page.setViewportSize({ width: 320, height: 720 });
      await page.goto(`${baseURL}/pulls/github/acme/widgets/6`);
      await expect(page.locator(".pull-detail")).toBeVisible();

      await page.locator(".actions-menu-trigger").click();
      await expect(page.locator(".actions-menu-popover")).toBeVisible();

      const readyResponse = page.waitForResponse((response) => {
        return (
          response.request().method() === "POST" &&
          response.url() === `${baseURL}/api/v1/pulls/github/acme/widgets/6/ready-for-review`
        );
      });
      await page.locator(".actions-menu-popover .btn--ready").click();
      expect((await readyResponse).status()).toBe(200);
      await expect(page.locator(".actions-menu-popover")).toHaveCount(0);
    } finally {
      await isolatedServer?.stop();
    }
  });

  test("draft pull request actions keep exactly the same height", async ({ page }) => {
    await page.goto("/pulls/github/acme/widgets/6");
    await expect(page.locator(".pull-detail")).toBeVisible();

    const ready = page.locator(".btn--ready");
    const approve = page.locator(".btn--approve");
    const merge = page.locator(".btn--merge");
    const close = page.locator(".btn--close");

    for (const btn of [ready, approve, merge, close]) {
      await expect(btn).toBeVisible();
    }

    const metrics = await page.evaluate(() => {
      const selectors = [".btn--ready", ".btn--approve", ".btn--merge", ".btn--close"];
      return selectors.map((selector) => {
        const element = document.querySelector(selector);
        if (!(element instanceof HTMLElement)) {
          throw new Error(`missing action button: ${selector}`);
        }
        const rect = element.getBoundingClientRect();
        return {
          selector,
          height: element.offsetHeight,
          top: Math.round(rect.top),
          left: Math.round(rect.left),
          right: Math.round(rect.right),
        };
      });
    });

    expect(metrics.map((metric) => metric.height)).toEqual(Array(metrics.length).fill(metrics[0]?.height));
    expect(new Set(metrics.map((metric) => metric.top)).size).toBe(1);
    expect(metrics.slice(0, -1).map((metric, index) => metrics[index + 1]!.left - metric.right)).toEqual(
      Array(metrics.length - 1).fill(metrics[1] ? metrics[1].left - metrics[0]!.right : 0),
    );
  });

  test("ready for review updates API state and removes the draft action", async ({ page }) => {
    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const server = isolatedServer;
      const apiContext = api;

      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/6`);
      await expect(page.locator(".pull-detail")).toBeVisible();

      const readyResponsePromise = page.waitForResponse((response) => {
        const url = response.url();
        return (
          response.request().method() === "POST" &&
          url === `${server.info.base_url}/api/v1/pulls/github/acme/widgets/6/ready-for-review`
        );
      });

      await page.locator(".btn--ready").click();

      const readyResponse = await readyResponsePromise;
      expect(readyResponse.status()).toBe(200);
      expect((await readyResponse.json()).status).toBe("ready_for_review");

      await expect(page.locator(".btn--ready")).toHaveCount(0);
      await expect(page.locator(".btn--approve")).toBeVisible();

      await expect
        .poll(async () => {
          const response = await apiContext.get("/api/v1/pulls/github/acme/widgets/6");
          const detail = await response.json();
          return detail.merge_request.IsDraft;
        })
        .toBe(false);
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
