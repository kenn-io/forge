import { execFileSync } from "node:child_process";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import {
  startIsolatedWorkspaceE2EServer,
  type IsolatedE2EServer,
} from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  worktree_path?: string;
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

async function waitForWorkspaceReady(
  api: APIRequestContext,
  workspaceId: string,
): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = await response.json() as WorkspaceStatusResponse;
    if (workspace.status === "ready") {
      return;
    }
    if (workspace.status === "error") {
      throw new Error(`workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  throw new Error(`workspace ${workspaceId} did not become ready`);
}

async function createIssueWorkspace(
  api: APIRequestContext,
  issueNumber: number,
): Promise<WorkspaceStatusResponse> {
  const createResponse = await api.post(
    `/api/v1/issues/github/acme/widgets/${issueNumber}/workspace`,
    {
      data: {},
    },
  );
  expect(createResponse.status()).toBe(202);
  const createdWorkspace = await createResponse.json() as WorkspaceStatusResponse;
  await waitForWorkspaceReady(api, createdWorkspace.id);
  return createdWorkspace;
}

test.describe("workspace tab persistence", () => {
  test.describe.configure({
    mode: "serial",
    timeout: lockedWorkspaceTestTimeoutMs,
  });

  test("opening tmux tab keeps Home pane mounted across tab switches", async ({ page }) => {
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

      const createResponse = await api.post(
        "/api/v1/issues/github/acme/widgets/10/workspace",
        {
          data: {},
        },
      );
      expect(createResponse.status()).toBe(202);
      const createdWorkspace = await createResponse.json() as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, createdWorkspace.id);

      await page.goto(
        `${isolatedServer.info.base_url}/terminal/${createdWorkspace.id}`,
      );

      const stage = page.locator(".workspace-stage");
      const panes = stage.locator(":scope > .stage-pane");
      const homeTab = page.locator('.workspace-tabs [role="tab"]', {
        hasText: "Home",
      });
      const tmuxTab = page.locator('.workspace-tabs [role="tab"]', {
        hasText: "tmux",
      });

      // Initial state: only the Home pane is in the stage.
      await expect(homeTab).toHaveAttribute("aria-selected", "true");
      await expect(panes).toHaveCount(1);

      // Open the tmux tab via the Launch menu. The tab is always
      // available because tmux is a built-in launch target.
      await page.locator(".launch-trigger").click();
      await page.locator(".launch-option", { hasText: "tmux" }).click();

      // After opening tmux, both Home and tmux panes should be in
      // the DOM, with tmux marked active.
      await expect(panes).toHaveCount(2);
      await expect(tmuxTab).toHaveAttribute("aria-selected", "true");
      const tmuxPane = stage.locator(":scope > .stage-pane.active");
      await expect(tmuxPane).toHaveCount(1);

      // Mark the tmux pane so we can later confirm it's the same
      // DOM element rather than a fresh remount.
      await tmuxPane.evaluate((el) => {
        el.setAttribute("data-test-tmux-id", "preserved");
      });

      // Switch to Home: tmux pane must remain mounted.
      await homeTab.click();
      await expect(homeTab).toHaveAttribute("aria-selected", "true");
      await expect(panes).toHaveCount(2);
      await expect(
        stage.locator(':scope > .stage-pane[data-test-tmux-id="preserved"]'),
      ).toHaveCount(1);

      // Switch back to tmux: must be the same DOM element, not a
      // freshly mounted one.
      await tmuxTab.click();
      await expect(panes).toHaveCount(2);
      const reactivated = stage.locator(":scope > .stage-pane.active");
      await expect(reactivated).toHaveAttribute("data-test-tmux-id", "preserved");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("returns to the most recently selected tab for each workspace", async ({ page }) => {
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

      const firstWorkspace = await createIssueWorkspace(api, 10);
      const secondWorkspace = await createIssueWorkspace(api, 11);

      await page.goto(
        `${isolatedServer.info.base_url}/terminal/${firstWorkspace.id}`,
      );

      const homeTab = page.locator('.workspace-tabs [role="tab"]', {
        hasText: "Home",
      });
      const tmuxTab = page.locator('.workspace-tabs [role="tab"]', {
        hasText: "tmux",
      });

      await expect(homeTab).toHaveAttribute("aria-selected", "true");

      await page.locator(".launch-trigger").click();
      await page.locator(".launch-option", { hasText: "tmux" }).click();
      await expect(tmuxTab).toHaveAttribute("aria-selected", "true");

      await page.goto(
        `${isolatedServer.info.base_url}/terminal/${secondWorkspace.id}`,
      );
      await expect(homeTab).toHaveAttribute("aria-selected", "true");

      await page.goto(
        `${isolatedServer.info.base_url}/terminal/${firstWorkspace.id}`,
      );
      await expect(tmuxTab).toHaveAttribute("aria-selected", "true");
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("shows workspace diff in the right sidebar without adding a stage pane", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );
    await page.setViewportSize({ width: 1033, height: 720 });

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const workspace = await createIssueWorkspace(api, 12);
      const workspaceResponse = await api.get(
        `/api/v1/workspaces/${workspace.id}`,
      );
      expect(workspaceResponse.ok()).toBe(true);
      const workspaceDetail = await workspaceResponse.json() as WorkspaceStatusResponse;
      expect(workspaceDetail.worktree_path).toBeTruthy();
      await writeFile(
        join(workspaceDetail.worktree_path!, "alpha.ts"),
        Array.from({ length: 120 }, (_, index) => `alpha ${index + 1}`)
          .join("\n") + "\n",
      );
      await writeFile(
        join(workspaceDetail.worktree_path!, "beta_test.go"),
        "beta\n",
      );

      await page.goto(
        `${isolatedServer.info.base_url}/terminal/${workspace.id}`,
      );

      const stage = page.locator(".workspace-stage");
      const panes = stage.locator(":scope > .stage-pane");
      const homeTab = page.locator('.workspace-tabs [role="tab"]', {
        hasText: "Home",
      });

      await expect(homeTab).toHaveAttribute("aria-selected", "true");
      await expect(page.locator('.workspace-tabs [role="tab"]', {
        hasText: "Diff",
      })).toHaveCount(0);
      await expect(panes).toHaveCount(1);

      const diffResponse = page.waitForResponse((response) =>
        response
          .url()
          .includes(`/api/v1/workspaces/${workspace.id}/diff`) &&
        response.request().method() === "GET",
      );
      await page.locator(".seg-control .seg-btn", { hasText: "Diff" }).click();
      await expect(page.locator(".right-sidebar .workspace-diff")).toBeVisible();
      await expect(page.locator(
        ".right-sidebar .workspace-diff-scope .diff-scope-picker__label",
      )).toBeHidden();
      const workspaceScopePicker = page.locator(
        ".right-sidebar .workspace-diff-scope .diff-scope-picker",
      );
      await expect(workspaceScopePicker.locator(".scope-pill")).toHaveCount(0);
      await expect(
        workspaceScopePicker.locator(".diff-scope-label"),
      ).toHaveText("HEAD");
      const scopeToggleMetrics = await page
        .locator(".right-sidebar .workspace-diff-scope .scope-toggle")
        .evaluate((toggle) => {
          const buttonRects = Array.from(
            toggle.querySelectorAll<HTMLElement>(".scope-btn"),
          ).map((button) => button.getBoundingClientRect());
          return {
            clientWidth: toggle.clientWidth,
            height: toggle.getBoundingClientRect().height,
            maxButtonTopDelta: Math.max(
              ...buttonRects.map((rect) =>
                Math.abs(rect.top - (buttonRects[0]?.top ?? rect.top)),
              ),
            ),
            scrollWidth: toggle.scrollWidth,
          };
        });
      expect(scopeToggleMetrics.height).toBeLessThanOrEqual(28);
      expect(scopeToggleMetrics.maxButtonTopDelta).toBeLessThanOrEqual(1);
      expect(scopeToggleMetrics.scrollWidth).toBeLessThanOrEqual(
        scopeToggleMetrics.clientWidth,
      );
      await page
        .locator(".right-sidebar .workspace-diff-scope")
        .getByRole("button", { name: "Select commit range" })
        .click();
      const commitMenu = page.locator(
        ".right-sidebar .diff-scope-picker__menu",
      );
      await expect(commitMenu).toBeVisible();
      const commitMenuTopElement = await commitMenu.evaluate((menu) => {
        const rect = menu.getBoundingClientRect();
        const topElement = document.elementFromPoint(
          rect.left + rect.width / 2,
          rect.top + 12,
        );
        return {
          className:
            typeof topElement?.className === "string"
              ? topElement.className
              : String(topElement?.className ?? ""),
          insideCommitMenu: Boolean(
            topElement?.closest(".diff-scope-picker__menu"),
          ),
        };
      });
      expect(commitMenuTopElement.insideCommitMenu).toBe(true);
      await page.keyboard.press("Escape");
      await expect(commitMenu).toBeHidden();
      expect((await diffResponse).ok()).toBe(true);
      const alphaDiffFile = page.locator(
        '.right-sidebar .diff-file[data-file-path="alpha.ts"]',
      );
      const betaDiffFile = page.locator(
        '.right-sidebar .diff-file[data-file-path="beta_test.go"]',
      );
      await expect(alphaDiffFile).toBeVisible();
      await expect(betaDiffFile).toHaveCount(1);
      const firstDiffLineGutterCount = await page
        .locator(".right-sidebar .diff-line")
        .first()
        .locator(".gutter")
        .count();
      expect(firstDiffLineGutterCount).toBe(2);
      const lineGutterWidths = await page
        .locator(".right-sidebar .diff-line .gutter")
        .evaluateAll((gutters) =>
          gutters.map((gutter) => gutter.getBoundingClientRect().width),
        );
      expect(Math.max(...lineGutterWidths)).toBeLessThanOrEqual(40);
      const diffToolbar = page.locator(".right-sidebar .diff-toolbar");
      await expect(diffToolbar.locator(".compact-more-btn")).toBeVisible();
      await expect(
        diffToolbar.getByRole("button", { name: "Jump to file" }),
      ).toBeVisible();
      await expect(
        page.locator(".right-sidebar .workspace-diff-scope .file-list-toggle"),
      ).toHaveCount(0);
      await expect(diffToolbar.locator(".file-list-toggle")).toHaveCount(0);
      await expect(diffToolbar.locator(".category-toggle")).toHaveCount(0);
      await expect(page.locator(".right-sidebar .workspace-diff-sidebar"))
        .toHaveCount(0);
      await expect(page.locator(".right-sidebar .workspace-diff-resize-handle"))
        .toHaveCount(0);
      const toolbarMetrics = await diffToolbar.evaluate((element) => ({
        clientWidth: element.clientWidth,
        scrollWidth: element.scrollWidth,
      }));
      expect(toolbarMetrics.scrollWidth).toBeLessThanOrEqual(
        toolbarMetrics.clientWidth,
      );
      await page.setViewportSize({ width: 1100, height: 720 });
      await diffToolbar.getByRole("button", { name: "Jump to file" }).click();
      const fileJump = page.locator(".right-sidebar .file-jump-menu");
      await expect(fileJump).toBeVisible();
      await expect(fileJump.getByRole("searchbox", { name: "Jump to file" }))
        .toBeFocused();
      await expect(fileJump.getByRole("option", { name: /alpha\.ts/ }))
        .toBeVisible();
      const jumpGeometry = await fileJump.evaluate((menu) => {
        const menuRect = menu.getBoundingClientRect();
        const sidebarRect = menu
          .closest(".right-sidebar")
          ?.getBoundingClientRect();
        return {
          position: getComputedStyle(menu).position,
          extendsLeftOfSidebar: sidebarRect
            ? menuRect.left < sidebarRect.left
            : false,
        };
      });
      expect(jumpGeometry.position).toBe("fixed");
      expect(jumpGeometry.extendsLeftOfSidebar).toBe(true);
      await fileJump.getByRole("option", { name: /beta_test\.go/ }).click();
      await expect(fileJump).toBeHidden();
      const betaJumpMetrics = await page
        .locator(".right-sidebar .diff-area")
        .evaluate((area) => {
          const beta = area.querySelector<HTMLElement>(
            '[data-file-path="beta_test.go"]',
          );
          const areaRect = area.getBoundingClientRect();
          const betaRect = beta?.getBoundingClientRect();
          return {
            hasBeta: Boolean(beta),
            scrollTop: area.scrollTop,
            betaInViewport: betaRect
              ? betaRect.top >= areaRect.top && betaRect.top < areaRect.bottom
              : false,
          };
        });
      expect(betaJumpMetrics.hasBeta).toBe(true);
      expect(betaJumpMetrics.scrollTop).toBeGreaterThan(0);
      expect(betaJumpMetrics.betaInViewport).toBe(true);
      await diffToolbar.locator(".compact-more-btn").click();
      const compactMenu = page.locator(".right-sidebar .compact-menu");
      await expect(compactMenu).toBeVisible();
      await expect(
        compactMenu.getByRole("switch", { name: "File list" }),
      ).toHaveCount(0);
      await compactMenu.getByRole("button", { name: "Code (1)" }).click();
      await expect(diffToolbar).toContainText("Code");
      await expect(alphaDiffFile).toBeVisible();
      await expect(betaDiffFile).toHaveCount(0);
      await page.keyboard.press("Escape");
      await expect(alphaDiffFile).toBeVisible();
      await expect(panes).toHaveCount(1);
      await expect(homeTab).toHaveAttribute("aria-selected", "true");

      await page.locator(".launch-trigger").click();
      await page.locator(".launch-option", { hasText: "tmux" }).click();
      await expect(page.locator('.workspace-tabs [role="tab"]', {
        hasText: "tmux",
      })).toHaveAttribute("aria-selected", "true");
      await expect(page.locator(".right-sidebar .workspace-diff")).toBeVisible();
      await expect(panes).toHaveCount(2);

      await page.locator(".stage-pane.active .terminal-container").click();
      for (const key of ["j", "k", "[", "]"]) {
        await page.keyboard.press(key);
      }
      await expect(page).toHaveURL(
        new RegExp(`/terminal/${workspace.id}$`),
      );
      await expect(page.locator('.workspace-tabs [role="tab"]', {
        hasText: "tmux",
      })).toHaveAttribute("aria-selected", "true");
      await expect(alphaDiffFile).toBeVisible();
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
