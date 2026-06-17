// The 00- filename prefix schedules this long-running spec first:
// Playwright dispatches files in path order, and multi-second tests
// that start near the end of the run stretch the suite tail.

import { execFileSync } from "node:child_process";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  error_message?: string | null;
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

function runGit(worktreePath: string, args: string[]): void {
  execFileSync("git", args, { cwd: worktreePath, stdio: "ignore" });
}

async function waitForWorkspaceReady(api: APIRequestContext, workspaceId: string): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") {
      return;
    }
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  throw new Error(`workspace ${workspaceId} did not become ready`);
}

async function createIssueWorkspace(api: APIRequestContext, issueNumber: number): Promise<WorkspaceStatusResponse> {
  const createResponse = await api.post(`/api/v1/issues/github/acme/widgets/${issueNumber}/workspace`, {
    data: {},
  });
  expect(createResponse.status()).toBe(202);
  const createdWorkspace = (await createResponse.json()) as WorkspaceStatusResponse;
  await waitForWorkspaceReady(api, createdWorkspace.id);
  return createdWorkspace;
}

async function clickPierreExpander(file: Locator, separatorIndex: number): Promise<void> {
  const expander = file
    .locator(".pierre-diff [data-separator][data-expand-index]")
    .filter({ visible: true })
    .nth(separatorIndex)
    .locator("[data-expand-button]")
    .filter({ visible: true })
    .first();
  await expect(expander).toBeVisible();
  await expander.evaluate((button: HTMLElement) => {
    button.dispatchEvent(
      new MouseEvent("click", {
        bubbles: true,
        cancelable: true,
        composed: true,
        shiftKey: true,
      }),
    );
  });
}

async function expandedContextStats(file: Locator): Promise<{ blank: number; texts: string[] }> {
  return await file.locator(".pierre-diff").evaluate((host) => {
    const rows = Array.from(
      host.shadowRoot?.querySelectorAll("[data-content] [data-line-type='context-expanded']") ?? [],
    ).map((element) => element.textContent?.trim() ?? "");
    return {
      blank: rows.filter((text) => text.length === 0).length,
      texts: rows.filter((text) => text.length > 0),
    };
  });
}

async function pierreDiffText(file: Locator): Promise<string> {
  return await file.locator(".pierre-diff").evaluate((host) => host.shadowRoot?.textContent ?? "");
}

async function openTerminalWorkflowTab(page: Page): Promise<void> {
  const terminalPanel = page.getByRole("region", { name: "Terminal panel" });
  await terminalPanel.getByRole("button", { name: "New terminal" }).click();
  await expect(terminalPanel.getByRole("button", { name: "Move terminal panel to workflow" })).toBeVisible();
  await terminalPanel.getByRole("button", { name: "Move terminal panel to workflow" }).click();
}

test.describe("workspace tab persistence", () => {
  test.describe.configure({
    mode: "serial",
    timeout: lockedWorkspaceTestTimeoutMs,
  });

  test("opening terminal tab keeps Home pane mounted across tab switches", async ({ page }) => {
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

      const createResponse = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", {
        data: {},
      });
      expect(createResponse.status()).toBe(202);
      const createdWorkspace = (await createResponse.json()) as WorkspaceStatusResponse;
      await waitForWorkspaceReady(api, createdWorkspace.id);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${createdWorkspace.id}`);

      const workflow = page.getByRole("region", { name: "Workflow panes" });
      const panes = workflow.locator(".tabbed-panel-tab-panel");
      const homeTab = workflow.getByRole("tab", { name: "Home" });
      const terminalTab = workflow.getByRole("tab", { name: "Terminal" });

      // Initial state: only the Home pane is in the stage.
      await expect(homeTab).toHaveAttribute("aria-selected", "true");
      await expect(panes).toHaveCount(1);

      await openTerminalWorkflowTab(page);

      // After opening Terminal, both Home and Terminal panes should be
      // in the DOM, with Terminal marked active.
      await expect(panes).toHaveCount(2);
      await expect(terminalTab).toHaveAttribute("aria-selected", "true");
      const terminalPane = workflow.locator(".tabbed-panel-tab-panel.active");
      await expect(terminalPane).toHaveCount(1);

      // Mark the terminal pane so we can later confirm it's the same
      // DOM element rather than a fresh remount.
      await terminalPane.evaluate((el) => {
        el.setAttribute("data-test-terminal-id", "preserved");
      });

      // Switch to Home: terminal pane must remain mounted.
      await homeTab.click();
      await expect(homeTab).toHaveAttribute("aria-selected", "true");
      await expect(panes).toHaveCount(2);
      await expect(workflow.locator('.tabbed-panel-tab-panel[data-test-terminal-id="preserved"]')).toHaveCount(1);

      // Switch back to Terminal: must be the same DOM element, not a
      // freshly mounted one.
      await terminalTab.click();
      await expect(panes).toHaveCount(2);
      const reactivated = workflow.locator(".tabbed-panel-tab-panel.active");
      await expect(reactivated).toHaveAttribute("data-test-terminal-id", "preserved");
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

      await page.goto(`${isolatedServer.info.base_url}/terminal/${firstWorkspace.id}`);

      const workflow = page.getByRole("region", {
        name: "Workflow panes",
      });
      const homeTab = workflow.getByRole("tab", { name: "Home" });
      const terminalTab = workflow.getByRole("tab", { name: "Terminal" });

      await expect(homeTab).toHaveAttribute("aria-selected", "true");

      await openTerminalWorkflowTab(page);
      await expect(terminalTab).toHaveAttribute("aria-selected", "true");

      await page.goto(`${isolatedServer.info.base_url}/terminal/${secondWorkspace.id}`);
      await expect(homeTab).toHaveAttribute("aria-selected", "true");

      await page.goto(`${isolatedServer.info.base_url}/terminal/${firstWorkspace.id}`);
      await expect(terminalTab).toHaveAttribute("aria-selected", "true");
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
      const workspaceResponse = await api.get(`/api/v1/workspaces/${workspace.id}`);
      expect(workspaceResponse.ok()).toBe(true);
      const workspaceDetail = (await workspaceResponse.json()) as WorkspaceStatusResponse;
      expect(workspaceDetail.worktree_path).toBeTruthy();
      await writeFile(
        join(workspaceDetail.worktree_path!, "alpha.ts"),
        Array.from({ length: 360 }, (_, index) => `alpha ${index + 1}`).join("\n") + "\n",
      );
      await writeFile(join(workspaceDetail.worktree_path!, "beta_test.go"), "beta\n");

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);

      const workflow = page.getByRole("region", { name: "Workflow panes" });
      const panes = workflow.locator(".tabbed-panel-tab-panel");
      const homeTab = workflow.getByRole("tab", { name: "Home" });

      await expect(homeTab).toHaveAttribute("aria-selected", "true");
      await expect(workflow.getByRole("tab", { name: "Diff" })).toHaveCount(0);
      await expect(panes).toHaveCount(1);

      const diffResponse = page.waitForResponse(
        (response) =>
          response.url().includes(`/api/v1/workspaces/${workspace.id}/diff`) && response.request().method() === "GET",
      );
      await page.locator(".seg-control .seg-btn", { hasText: "Diff" }).click();
      await expect(page.locator(".right-sidebar .workspace-diff")).toBeVisible();
      await expect(page.locator(".right-sidebar .workspace-diff-scope .diff-scope-picker__label")).toBeHidden();
      const workspaceScopePicker = page.locator(".right-sidebar .workspace-diff-scope .diff-scope-picker");
      await expect(workspaceScopePicker.locator(".scope-pill")).toHaveCount(0);
      await expect(workspaceScopePicker.locator(".diff-scope-label")).toHaveText("HEAD");
      const scopeToggleMetrics = await page
        .locator(".right-sidebar .workspace-diff-scope .scope-toggle")
        .evaluate((toggle) => {
          const buttonRects = Array.from(toggle.querySelectorAll<HTMLElement>(".scope-btn")).map((button) =>
            button.getBoundingClientRect(),
          );
          return {
            clientWidth: toggle.clientWidth,
            height: toggle.getBoundingClientRect().height,
            maxButtonTopDelta: Math.max(
              ...buttonRects.map((rect) => Math.abs(rect.top - (buttonRects[0]?.top ?? rect.top))),
            ),
            scrollWidth: toggle.scrollWidth,
          };
        });
      expect(scopeToggleMetrics.height).toBeLessThanOrEqual(28);
      expect(scopeToggleMetrics.maxButtonTopDelta).toBeLessThanOrEqual(1);
      expect(scopeToggleMetrics.scrollWidth).toBeLessThanOrEqual(scopeToggleMetrics.clientWidth);
      await page
        .locator(".right-sidebar .workspace-diff-scope")
        .getByRole("button", { name: "Select commit range" })
        .click();
      const commitMenu = page.locator(".right-sidebar .diff-scope-picker__menu");
      await expect(commitMenu).toBeVisible();
      const commitMenuTopElement = await commitMenu.evaluate((menu) => {
        const rect = menu.getBoundingClientRect();
        const topElement = document.elementFromPoint(rect.left + rect.width / 2, rect.top + 12);
        return {
          className:
            typeof topElement?.className === "string" ? topElement.className : String(topElement?.className ?? ""),
          insideCommitMenu: Boolean(topElement?.closest(".diff-scope-picker__menu")),
        };
      });
      expect(commitMenuTopElement.insideCommitMenu).toBe(true);
      await page.keyboard.press("Escape");
      await expect(commitMenu).toBeHidden();
      expect((await diffResponse).ok()).toBe(true);
      const alphaDiffFile = page.locator('.right-sidebar .diff-file[data-file-path="alpha.ts"]');
      const betaDiffFile = page.locator('.right-sidebar .diff-file[data-file-path="beta_test.go"]');
      await expect(alphaDiffFile).toBeVisible();
      await expect(betaDiffFile).toHaveCount(1);
      const rightDiffHost = page.locator(".right-sidebar .pierre-diff").first();
      await expect
        .poll(async () => {
          return await rightDiffHost.evaluate((host) => {
            return host.shadowRoot?.querySelectorAll("[data-gutter] [data-line-type]").length ?? 0;
          });
        })
        .toBeGreaterThan(0);
      await expect
        .poll(async () => {
          return await rightDiffHost.evaluate((host) => {
            return host.shadowRoot?.querySelector("[data-gutter]")?.getBoundingClientRect().width ?? 0;
          });
        })
        .toBeLessThanOrEqual(56);
      const rightDiffArea = page.locator(".right-sidebar .diff-area");
      await expect.poll(async () => rightDiffArea.evaluate((area) => area.scrollHeight > area.clientHeight)).toBe(true);
      const beforePageDownScrollTop = await rightDiffArea.evaluate((area) => area.scrollTop);
      await rightDiffHost.click();
      await rightDiffHost.press("PageDown");
      await expect
        .poll(async () => rightDiffArea.evaluate((area) => area.scrollTop))
        .toBeGreaterThan(beforePageDownScrollTop);
      await rightDiffHost.press("j");
      await rightDiffHost.press("k");
      await expect(workflow.getByRole("tab", { name: "Diff" })).toHaveCount(0);
      await expect(panes).toHaveCount(1);
      await expect(page.locator(".right-sidebar .workspace-diff")).toBeVisible();
      const diffToolbar = page.locator(".right-sidebar .diff-toolbar");
      await expect(diffToolbar.locator(".compact-more-btn")).toBeVisible();
      await expect(diffToolbar.getByRole("button", { name: "Jump to file" })).toBeVisible();
      await expect(page.locator(".right-sidebar .workspace-diff-scope .file-list-toggle")).toHaveCount(0);
      await expect(diffToolbar.locator(".file-list-toggle")).toHaveCount(0);
      await expect(diffToolbar.locator(".category-toggle")).toHaveCount(0);
      await expect(page.locator(".right-sidebar .workspace-diff-sidebar")).toHaveCount(0);
      await expect(page.locator(".right-sidebar .workspace-diff-resize-handle")).toHaveCount(0);
      const toolbarMetrics = await diffToolbar.evaluate((element) => ({
        clientWidth: element.clientWidth,
        scrollWidth: element.scrollWidth,
      }));
      expect(toolbarMetrics.scrollWidth).toBeLessThanOrEqual(toolbarMetrics.clientWidth);
      await page.setViewportSize({ width: 1100, height: 720 });
      await diffToolbar.getByRole("button", { name: "Jump to file" }).click();
      const fileJump = page.locator(".right-sidebar .file-jump-menu");
      await expect(fileJump).toBeVisible();
      await expect(fileJump.getByRole("searchbox", { name: "Jump to file" })).toBeFocused();
      await expect(fileJump.getByRole("option", { name: /alpha\.ts/ })).toBeVisible();
      const jumpGeometry = await fileJump.evaluate((menu) => {
        const menuRect = menu.getBoundingClientRect();
        const sidebarRect = menu.closest(".right-sidebar")?.getBoundingClientRect();
        return {
          position: getComputedStyle(menu).position,
          extendsLeftOfSidebar: sidebarRect ? menuRect.left < sidebarRect.left : false,
        };
      });
      expect(jumpGeometry.position).toBe("fixed");
      expect(jumpGeometry.extendsLeftOfSidebar).toBe(true);
      await fileJump.getByRole("option", { name: /beta_test\.go/ }).click();
      await expect(fileJump).toBeHidden();
      await expect
        .poll(async () =>
          page.locator(".right-sidebar .diff-area").evaluate((area) => {
            const beta = area.querySelector<HTMLElement>('[data-file-path="beta_test.go"]');
            const areaRect = area.getBoundingClientRect();
            const betaRect = beta?.getBoundingClientRect();
            return Boolean(betaRect && betaRect.top >= areaRect.top && betaRect.top < areaRect.bottom);
          }),
        )
        .toBe(true);
      await diffToolbar.locator(".compact-more-btn").click();
      const compactMenu = page.locator(".right-sidebar .compact-menu");
      await expect(compactMenu).toBeVisible();
      await expect(compactMenu.getByRole("switch", { name: "File list" })).toHaveCount(0);
      await compactMenu.getByRole("button", { name: "Code (1)" }).click();
      await expect(diffToolbar).toContainText("Code");
      await expect(alphaDiffFile).toBeVisible();
      await expect(betaDiffFile).toHaveCount(0);
      await page.keyboard.press("Escape");
      await expect(alphaDiffFile).toBeVisible();
      await expect(panes).toHaveCount(1);
      await expect(homeTab).toHaveAttribute("aria-selected", "true");

      await openTerminalWorkflowTab(page);
      await expect(workflow.getByRole("tab", { name: "Terminal" })).toHaveAttribute("aria-selected", "true");
      await expect(page.locator(".right-sidebar .workspace-diff")).toBeVisible();
      await expect(panes).toHaveCount(2);

      await workflow.locator(".tabbed-panel-tab-panel.active .terminal-container").click();
      for (const key of ["j", "k", "[", "]"]) {
        await page.keyboard.press(key);
      }
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspace.id}$`));
      await expect(workflow.getByRole("tab", { name: "Terminal" })).toHaveAttribute("aria-selected", "true");
      await expect(alphaDiffFile).toBeVisible();
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });

  test("workspace diff context expansion renders text from the real preview API", async ({ page }) => {
    test.skip(
      !hasCommand("git") || !hasCommand("tmux", ["-V"]),
      "git and tmux are required for the real workspace flow",
    );
    await page.setViewportSize({ width: 1400, height: 860 });

    let isolatedServer: IsolatedE2EServer | null = null;
    let api: APIRequestContext | null = null;
    try {
      isolatedServer = await startIsolatedWorkspaceE2EServer();
      api = await playwrightRequest.newContext({
        baseURL: isolatedServer.info.base_url,
      });

      const workspace = await createIssueWorkspace(api, 12);
      const workspaceResponse = await api.get(`/api/v1/workspaces/${workspace.id}`);
      expect(workspaceResponse.ok()).toBe(true);
      const workspaceDetail = (await workspaceResponse.json()) as WorkspaceStatusResponse;
      expect(workspaceDetail.worktree_path).toBeTruthy();
      const worktreePath = workspaceDetail.worktree_path!;

      runGit(worktreePath, ["config", "user.email", "test@test.com"]);
      runGit(worktreePath, ["config", "user.name", "Test"]);
      const baseLines = Array.from({ length: 120 }, (_, index) => `workspace hidden line ${index + 1}`);
      await writeFile(join(worktreePath, "expand-context.ts"), `${baseLines.join("\n")}\n`);
      runGit(worktreePath, ["add", "expand-context.ts"]);
      runGit(worktreePath, ["commit", "-m", "add expandable context fixture"]);
      const changedLines = [...baseLines];
      changedLines[9] = "workspace changed line 10";
      changedLines[89] = "workspace changed line 90";
      await writeFile(join(worktreePath, "expand-context.ts"), `${changedLines.join("\n")}\n`);

      await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
      await page.locator(".seg-control .seg-btn", { hasText: "Diff" }).click();
      const previewResponses = Promise.all([
        page.waitForResponse((response) => {
          const url = response.url();
          return (
            response.request().method() === "GET" &&
            url.includes(`/api/v1/workspaces/${workspace.id}/file-preview`) &&
            url.includes("path=expand-context.ts") &&
            url.includes("side=old")
          );
        }),
        page.waitForResponse((response) => {
          const url = response.url();
          return (
            response.request().method() === "GET" &&
            url.includes(`/api/v1/workspaces/${workspace.id}/file-preview`) &&
            url.includes("path=expand-context.ts") &&
            url.includes("side=new")
          );
        }),
      ]);

      const file = page.locator('.right-sidebar .diff-file[data-file-path="expand-context.ts"]');
      await expect(file).toBeVisible();
      await expect
        .poll(async () => {
          return await file.locator(".pierre-diff").evaluate((host) => {
            return host.shadowRoot?.querySelectorAll("[data-separator][data-expand-index]").length ?? 0;
          });
        })
        .toBeGreaterThanOrEqual(2);
      await clickPierreExpander(file, 1);
      const responses = await previewResponses;
      expect(responses.every((response) => response.ok())).toBe(true);

      await expect
        .poll(async () => {
          const stats = await expandedContextStats(file);
          return {
            blank: stats.blank,
            hasMiddleLine: stats.texts.some((text) => text.includes("workspace hidden line 50")),
          };
        })
        .toEqual({
          blank: 0,
          hasMiddleLine: true,
        });

      const refreshedLines = [...changedLines];
      refreshedLines[49] = "workspace refreshed hidden line 50";
      await writeFile(join(worktreePath, "expand-context.ts"), `${refreshedLines.join("\n")}\n`);

      const refreshResponse = page.waitForResponse((response) => {
        return (
          response.request().method() === "POST" &&
          response.url().includes(`/api/v1/workspaces/${workspace.id}/refresh`)
        );
      });
      await page.getByRole("button", { name: "Refresh workspace details" }).click();
      expect((await refreshResponse).ok()).toBe(true);
      await expect
        .poll(async () => (await pierreDiffText(file)).includes("workspace refreshed hidden line 50"))
        .toBe(true);

      const refreshedPreviewResponses = Promise.all([
        page.waitForResponse((response) => {
          const url = response.url();
          return (
            response.request().method() === "GET" &&
            url.includes(`/api/v1/workspaces/${workspace.id}/file-preview`) &&
            url.includes("path=expand-context.ts") &&
            url.includes("side=old")
          );
        }),
        page.waitForResponse((response) => {
          const url = response.url();
          return (
            response.request().method() === "GET" &&
            url.includes(`/api/v1/workspaces/${workspace.id}/file-preview`) &&
            url.includes("path=expand-context.ts") &&
            url.includes("side=new")
          );
        }),
      ]);
      await expect
        .poll(async () => {
          return await file.locator(".pierre-diff").evaluate((host) => {
            return host.shadowRoot?.querySelectorAll("[data-separator][data-expand-index]").length ?? 0;
          });
        })
        .toBeGreaterThanOrEqual(3);
      await clickPierreExpander(file, 1);
      const refreshedResponses = await refreshedPreviewResponses;
      expect(refreshedResponses.every((response) => response.ok())).toBe(true);
      await expect
        .poll(async () => {
          const stats = await expandedContextStats(file);
          return {
            blank: stats.blank,
            hasStaleHiddenLine: stats.texts.some((text) => text.includes("workspace hidden line 50")),
            hasUnchangedContext: stats.texts.some((text) => text.includes("workspace hidden line 40")),
          };
        })
        .toEqual({
          blank: 0,
          hasStaleHiddenLine: false,
          hasUnchangedContext: true,
        });
    } finally {
      await api?.dispose();
      await isolatedServer?.stop();
    }
  });
});
