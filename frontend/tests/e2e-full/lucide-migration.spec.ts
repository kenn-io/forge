import { expect, test, type Page } from "@playwright/test";

async function waitForPRList(page: Page): Promise<void> {
  await page.locator(".pull-item").first()
    .waitFor({ state: "visible", timeout: 10_000 });
}

type WorkspaceFixture = {
  id: string;
  platform_host: string;
  repo_owner: string;
  repo_name: string;
  mr_number: number;
  mr_head_ref: string;
  worktree_path: string;
  tmux_session: string;
  status: string;
  error_message?: string | null;
  created_at: string;
  mr_title?: string | null;
  mr_state?: string | null;
  mr_is_draft?: boolean | null;
};

const baseWorkspace: WorkspaceFixture = {
  id: "ws-lucide",
  platform_host: "github.com",
  repo_owner: "acme",
  repo_name: "widgets",
  mr_number: 42,
  mr_head_ref: "feature/auth",
  worktree_path: "/tmp/worktrees/ws-lucide",
  tmux_session: "middleman-ws-lucide",
  status: "ready",
  created_at: "2026-04-10T12:00:00Z",
  mr_title: "Add auth middleware",
  mr_state: "open",
  mr_is_draft: false,
};

async function installWorkspaceRoutes(
  page: Page,
  opts?: {
    workspace?: Partial<WorkspaceFixture>;
    detailResponses?: Array<{
      status: number;
      body?: unknown;
    }>;
  },
): Promise<void> {
  const workspace = {
    ...baseWorkspace,
    ...opts?.workspace,
  };
  const detailResponses = [
    ...(opts?.detailResponses ?? []),
  ];

  await page.route("**/api/v1/events", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: "",
    });
  });

  await page.route("**/api/v1/workspaces", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ workspaces: [workspace] }),
    });
  });

  await page.route(
    `**/api/v1/workspaces/${workspace.id}`,
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }

      const nextResponse = detailResponses.shift();
      if (nextResponse) {
        await route.fulfill({
          status: nextResponse.status,
          contentType: "application/json",
          body: JSON.stringify(nextResponse.body ?? {}),
        });
        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(workspace),
      });
    },
  );

  await page.route(
    `**/api/v1/workspaces/${workspace.id}/retry`,
    async (route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }

      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify(workspace),
      });
    },
  );
}

test.describe("lucide migration", () => {
  test("startup loading state renders the live spinner icon", async ({ page }) => {
    let releaseSettings: () => void = () => {};
    const settingsGate = new Promise<void>((resolve) => {
      releaseSettings = resolve;
    });

    await page.route("**/api/v1/settings", async (route) => {
      const response = await route.fetch();
      await settingsGate;
      await route.fulfill({ response });
    });

    const gotoPromise = page.goto("/pulls");

    const loadingState = page.locator(".loading-state");
    await expect(loadingState).toBeVisible();
    await expect(loadingState.locator(".loading-spinner")).toBeVisible();

    releaseSettings();
    await gotoPromise;

    await waitForPRList(page);
    await expect(loadingState).toHaveCount(0);
  });

  test("repo selector keeps the live chevron icon while filtering repos", async ({ page }) => {
    await page.goto("/pulls");
    await waitForPRList(page);

    const selector = page.getByTitle("Select repository");
    await expect(selector).toBeVisible();
    await expect(selector.locator("svg")).toBeVisible();

    await selector.click();

    const input = page.getByLabel("Filter repos");
    await expect(input).toBeVisible();
    await input.fill("widg");

    const option = page.getByRole("option", {
      name: "acme/widgets",
    });
    await expect(option).toBeVisible();
    await option.click();
    await expect(option.locator("input[type='checkbox']")).toBeChecked();

    await page.keyboard.press("Escape");
    await expect(selector).toContainText("acme/widgets");
    await expect(selector.locator("svg")).toBeVisible();
  });

  test("light mode renders the live filled moon icon in the header", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await page.goto("/pulls");
    await waitForPRList(page);

    const button = page.getByTitle("Toggle theme");
    await expect(button.locator("svg")).toBeVisible();

    const moonStyles = await button.evaluate((node) => {
      const path = node.querySelector("[data-filled-icon='moon'] svg path");
      if (!path) {
        return null;
      }
      const style = getComputedStyle(path);
      return {
        fill: style.fill,
        stroke: style.stroke,
      };
    });

    expect(moonStyles).not.toBeNull();
    expect(moonStyles?.stroke).toBe("none");
    expect(moonStyles?.fill).not.toBe("none");
  });

  test("workspace creating state renders the terminal spinner icon", async ({ page }) => {
    await installWorkspaceRoutes(page, {
      workspace: { status: "creating" },
    });

    await page.goto("/terminal/ws-lucide");

    const stateMessage = page.locator(".state-message");
    await expect(stateMessage).toContainText("Setting up workspace...");
    await expect(stateMessage.locator(".spinner")).toBeVisible();
  });

  test("workspace load failure shows the alert icon and retry recovers", async ({ page }) => {
    await installWorkspaceRoutes(page, {
      detailResponses: [
        {
          status: 500,
          body: { error: "Internal error" },
        },
        {
          status: 200,
          body: baseWorkspace,
        },
      ],
    });

    await page.goto("/terminal/ws-lucide");

    const stateMessage = page.locator(".state-message.error");
    await expect(stateMessage).toContainText("Failed to load workspace (500)");
    await expect(
      stateMessage.getByLabel("Workspace load failed"),
    ).toBeVisible();

    await stateMessage.getByRole("button", { name: "Retry" }).click();
    await expect(page.locator(".header-name")).toContainText("Add auth middleware");
  });

  test("workspace setup error shows the alert icon and retry recovers", async ({ page }) => {
    await installWorkspaceRoutes(page, {
      detailResponses: [
        {
          status: 200,
          body: {
            ...baseWorkspace,
            status: "error",
            error_message: "tmux bootstrap failed",
          },
        },
        {
          status: 200,
          body: baseWorkspace,
        },
      ],
    });

    await page.goto("/terminal/ws-lucide");

    const stateMessage = page.locator(".state-message.error");
    await expect(stateMessage).toContainText("tmux bootstrap failed");
    await expect(
      stateMessage.getByLabel("Workspace setup failed"),
    ).toBeVisible();

    await stateMessage.getByRole("button", { name: "Retry" }).click();
    await expect(page.locator(".header-name")).toContainText("Add auth middleware");
  });
});
