import { expect, test, type Locator, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

import {
  startIsolatedE2EServerWithOptions,
  startIsolatedWorkspaceE2EServer,
} from "../../frontend/tests/e2e-full/support/e2eServer";

const outputDir = process.env.KENN_FORGE_DOCS_SCREENSHOT_DIR;
const execFileAsync = promisify(execFile);

type ThemeName = "light" | "dark";

const syntheticCodexTranscript = [
  "› Implement in-flight request coalescing for the widget cache.",
  "",
  "• Inspected",
  "  └ cache.mjs",
  "  └ cache.test.mjs",
  "",
  "• Implemented in-flight request coalescing.",
  "  └ Concurrent loads share one request.",
  "  └ Failed requests clear so later calls retry.",
  "",
  "Test result: node --test — 3 passed, 0 failed.",
].join("\n");

const syntheticCodexPalettes = {
  light: {
    background: "oklch(97.5% 0.008 250)",
    foreground: "oklch(30% 0.025 255)",
    promptBackground: "oklch(93.5% 0.012 250)",
    promptBorder: "oklch(83% 0.02 250)",
    promptMarker: "oklch(27% 0.025 255)",
    promptText: "oklch(52% 0.025 250)",
    model: "oklch(47% 0.105 80)",
    separator: "oklch(63% 0.02 250)",
    workingDirectory: "oklch(48% 0.085 150)",
  },
  dark: {
    background: "#0d1117",
    foreground: "#c9d1d9",
    promptBackground: "#343941",
    promptBorder: "#444b55",
    promptMarker: "#f0f3f6",
    promptText: "#9da4ad",
    model: "#f6e2b7",
    separator: "#6e7681",
    workingDirectory: "#abdfa7",
  },
} as const;

type CaptureCase = {
  name:
    | "maintainer-overview"
    | "issue-triager"
    | "code-reviewer"
    | "code-reviewer-agent-launch"
    | "workspace-codex-session"
    | "first-run";
  theme: ThemeName;
  path: string;
  readySelector: string;
  readyText: string;
  requiredSelector?: string;
  requiredButtonName?: string;
  loadingText?: RegExp;
  waitForSync?: boolean;
  prepare?: (page: Page, baseURL: string) => Promise<void>;
  afterReady?: (page: Page, theme: ThemeName) => Promise<void>;
  description: string;
};

async function openCodexLaunchMenu(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Create Workspace options" }).click();
  await expect(page.getByRole("menuitem", { name: "Codex" })).toBeVisible();
}

async function embedSyntheticCodexTranscript(workspace: Locator, theme: ThemeName): Promise<void> {
  const terminal = workspace.locator(".terminal-container:visible").first();
  await expect(terminal).toBeVisible();
  await terminal.evaluate((element, { transcript, palette }) => {
    const existing = element.querySelector(".docs-codex-transcript");
    if (existing) existing.remove();

    const content = document.createElement("div");
    content.className = "docs-codex-transcript";
    content.setAttribute("aria-label", "Synthetic Codex session transcript");
    content.style.cssText = [
      "position: absolute",
      "inset: 0",
      "z-index: 3",
      "box-sizing: border-box",
      "overflow: hidden",
      `background: ${palette.background}`,
      `color: ${palette.foreground}`,
      'font-family: "JetBrains Mono", "SF Mono", Menlo, Consolas, monospace',
      "font-size: 12.5px",
      "line-height: 1.4",
      "display: flex",
      "flex-direction: column",
    ].join("; ");

    const output = document.createElement("pre");
    output.textContent = transcript;
    output.style.cssText = [
      "box-sizing: border-box",
      "flex: 1 1 auto",
      "min-height: 0",
      "margin: 0",
      "padding: 14px 22px 6px",
      "overflow: hidden",
      "color: inherit",
      "font: inherit",
      "white-space: pre-wrap",
    ].join("; ");

    const composer = document.createElement("div");
    composer.setAttribute("aria-label", "Codex prompt composer");
    composer.style.cssText = ["box-sizing: border-box", "flex: 0 0 auto", "margin: 0 12px 10px"].join("; ");

    const prompt = document.createElement("div");
    prompt.style.cssText = [
      "box-sizing: border-box",
      "display: flex",
      "align-items: center",
      "gap: 10px",
      "min-height: 44px",
      "padding: 10px 14px",
      `border: 1px solid ${palette.promptBorder}`,
      `background: ${palette.promptBackground}`,
    ].join("; ");

    const promptMarker = document.createElement("span");
    promptMarker.textContent = "›";
    promptMarker.style.cssText = `color: ${palette.promptMarker}; font-weight: 700`;

    const promptText = document.createElement("span");
    promptText.textContent = "Summarize recent commits";
    promptText.style.cssText = `color: ${palette.promptText}`;
    prompt.append(promptMarker, promptText);

    const status = document.createElement("div");
    status.setAttribute("aria-label", "Codex model and workspace status");
    status.style.cssText = [
      "display: flex",
      "align-items: center",
      "gap: 8px",
      "padding: 3px 14px 0",
      "font-size: 12.5px",
      "line-height: 1.4",
    ].join("; ");

    const model = document.createElement("span");
    model.textContent = "gpt-5.6-sol high";
    model.style.cssText = `color: ${palette.model}`;

    const separator = document.createElement("span");
    separator.textContent = "·";
    separator.style.cssText = `color: ${palette.separator}`;

    const workingDirectory = document.createElement("span");
    workingDirectory.textContent = "~/src/kenn-io/forge";
    workingDirectory.style.cssText = `color: ${palette.workingDirectory}`;
    status.append(model, separator, workingDirectory);

    composer.append(prompt, status);
    content.append(output, composer);
    element.appendChild(content);
  }, { transcript: syntheticCodexTranscript, palette: syntheticCodexPalettes[theme] });
}

async function showCodexWorkspace(page: Page, theme: ThemeName): Promise<void> {
  const row = page.locator(".workspace-list-sidebar .ws-row", { hasText: "Add widget caching layer" });
  await row.click();
  await expect(row).toHaveClass(/\bselected\b/);
  const workspace = page.locator(".terminal-view");
  const codexTab = workspace.getByRole("region", { name: "Workflow panes" }).getByRole("tab", { name: "Codex" });
  await expect(codexTab).toBeVisible();
  await codexTab.click();
  await expect(codexTab).toHaveAttribute("aria-selected", "true");
  await embedSyntheticCodexTranscript(workspace, theme);

  const syntheticPath = "/worktrees/github/github.com/acme/widgets/pr-1";
  await page.locator(".meta-chip.mono.path").evaluate((element, replacement) => {
    const pathText = Array.from(element.childNodes).find((node) => node.nodeType === Node.TEXT_NODE);
    if (!pathText) throw new Error("workspace path text node was not found");
    pathText.textContent = replacement;
    element.setAttribute("title", replacement);
  }, syntheticPath);
}

async function showActivityCodexWorkspace(page: Page, theme: ThemeName): Promise<void> {
  const prRow = page
    .locator(".activity-row")
    .filter({ has: page.locator(".badge", { hasText: "PR" }) })
    .filter({ hasText: "Add widget caching layer" })
    .first();
  await prRow.click();

  const detail = page.locator(".activity-detail");
  await expect(page.locator(".activity-shell.activity-shell--split")).toBeVisible();
  await expect(detail.locator(".detail-title")).toContainText("Add widget caching layer");

  const workspace = page.locator(".detail-pane-workspace-slot .workspace-host-wrapper");
  await expect(workspace).toBeVisible();
  const workflow = workspace.getByRole("region", { name: "Workflow panes" });
  const codexTab = workflow.getByRole("tab", { name: "Codex" });
  await expect(codexTab).toBeVisible();
  await codexTab.click();
  await expect(codexTab).toHaveAttribute("aria-selected", "true");
  await expect(workspace.locator(".terminal-view")).toBeVisible();
  await embedSyntheticCodexTranscript(workspace, theme);
  await waitForIdleSync(page);
}

const cases: CaptureCase[] = [
  {
    name: "issue-triager",
    theme: "light",
    path: "/issues/github/acme/widgets/10",
    readySelector: ".issue-detail .detail-title",
    readyText: "Widget rendering broken on Safari",
    requiredButtonName: "Create Workspace",
    loadingText: /Loading comments/i,
    waitForSync: true,
    description: "Issue triage view with the newest issue context first and a workspace action in the detail pane.",
  },
  {
    name: "issue-triager",
    theme: "dark",
    path: "/issues/github/acme/widgets/10",
    readySelector: ".issue-detail .detail-title",
    readyText: "Widget rendering broken on Safari",
    requiredButtonName: "Create Workspace",
    loadingText: /Loading comments/i,
    waitForSync: true,
    description:
      "Issue triage view in dark mode with the newest issue context first and a workspace action in the detail pane.",
  },
  {
    name: "code-reviewer",
    theme: "light",
    path: "/pulls/github/acme/widgets/1",
    readySelector: ".pull-detail .detail-title",
    readyText: "Add widget caching layer",
    requiredButtonName: "Create Workspace",
    loadingText: /Loading discussion/i,
    waitForSync: true,
    description:
      "Code review view with recent PR activity, review state, CI context, and workspace creation in one pane.",
  },
  {
    name: "code-reviewer",
    theme: "dark",
    path: "/pulls/github/acme/widgets/1",
    readySelector: ".pull-detail .detail-title",
    readyText: "Add widget caching layer",
    requiredButtonName: "Create Workspace",
    loadingText: /Loading discussion/i,
    waitForSync: true,
    description:
      "Code review view in dark mode with recent PR activity, review state, CI context, and workspace creation in one pane.",
  },
  {
    name: "code-reviewer-agent-launch",
    theme: "light",
    path: "/pulls/github/acme/widgets/1",
    readySelector: ".pull-detail .detail-title",
    readyText: "Add widget caching layer",
    loadingText: /Loading discussion/i,
    waitForSync: true,
    prepare: configureSyntheticCodexAgent,
    afterReady: openCodexLaunchMenu,
    description: "Code review view with the Create Workspace menu open to launch a configured Codex agent.",
  },
  {
    name: "code-reviewer-agent-launch",
    theme: "dark",
    path: "/pulls/github/acme/widgets/1",
    readySelector: ".pull-detail .detail-title",
    readyText: "Add widget caching layer",
    loadingText: /Loading discussion/i,
    waitForSync: true,
    prepare: configureSyntheticCodexAgent,
    afterReady: openCodexLaunchMenu,
    description:
      "Code review view in dark mode with the Create Workspace menu open to launch a configured Codex agent.",
  },
  {
    name: "workspace-codex-session",
    theme: "light",
    path: "/workspaces",
    readySelector: ".workspace-list-sidebar",
    readyText: "Add widget caching layer",
    waitForSync: true,
    prepare: ensureSyntheticCodexWorkspace,
    afterReady: showCodexWorkspace,
    description: "Workspaces view with the pull request worktree selected and its Codex session available.",
  },
  {
    name: "workspace-codex-session",
    theme: "dark",
    path: "/workspaces",
    readySelector: ".workspace-list-sidebar",
    readyText: "Add widget caching layer",
    waitForSync: true,
    prepare: ensureSyntheticCodexWorkspace,
    afterReady: showCodexWorkspace,
    description:
      "Workspaces view in dark mode with the pull request worktree selected and its Codex session available.",
  },
  {
    name: "maintainer-overview",
    theme: "light",
    path: "/",
    readySelector: ".activity-feed",
    readyText: "Add widget caching layer",
    loadingText: /Loading activity/i,
    waitForSync: true,
    prepare: ensureSyntheticCodexWorkspace,
    afterReady: showActivityCodexWorkspace,
    description:
      "Activity with a selected pull request, its live workspace, and the workspace's selected Codex session.",
  },
  {
    name: "maintainer-overview",
    theme: "dark",
    path: "/",
    readySelector: ".activity-feed",
    readyText: "Add widget caching layer",
    loadingText: /Loading activity/i,
    waitForSync: true,
    prepare: ensureSyntheticCodexWorkspace,
    afterReady: showActivityCodexWorkspace,
    description:
      "Activity in dark mode with a selected pull request, its live workspace, and the workspace's selected Codex session.",
  },
];

const firstRunCases: CaptureCase[] = [
  {
    name: "first-run",
    theme: "light",
    path: "/",
    readySelector: ".onboarding",
    readyText: "Connect a code forge",
    requiredSelector: ".provider-readiness",
    requiredButtonName: "Continue with GitHub",
    description:
      "First-run code forge readiness with an authenticated synthetic GitHub account and other provider paths.",
  },
  {
    name: "first-run",
    theme: "dark",
    path: "/",
    readySelector: ".onboarding",
    readyText: "Connect a code forge",
    requiredSelector: ".provider-readiness",
    requiredButtonName: "Continue with GitHub",
    description:
      "First-run code forge readiness in dark mode with an authenticated synthetic GitHub account and other provider paths.",
  },
];

async function preparePage(page: Page, theme: ThemeName): Promise<void> {
  await page.addInitScript((themeName) => {
    localStorage.setItem("kenn-forge-theme", themeName);
    localStorage.setItem("kenn-forge-sidebar", "expanded");
  }, theme);
}

async function stabilizePage(page: Page): Promise<void> {
  await page.addStyleTag({
    content: `
      *, *::before, *::after {
        animation: none !important;
        transition: none !important;
        caret-color: transparent !important;
      }
    `,
  });
}

async function waitForIdleSync(page: Page): Promise<void> {
  await expect(page.getByRole("button", { name: "Sync", exact: true })).toBeEnabled();
  await expect(page.getByText(/syncing/i)).toHaveCount(0, { timeout: 15_000 });
}

async function configureSyntheticCodexAgent(page: Page, baseURL: string): Promise<void> {
  const desiredAgent = {
    key: "codex",
    label: "Codex",
    command: ["/bin/sh", "-lc", "while :; do sleep 3600; done"],
    enabled: true,
  };
  const currentResponse = await page.request.get(`${baseURL}/api/v1/settings`);
  if (currentResponse.status() !== 200) {
    throw new Error(`GET settings returned ${currentResponse.status()}: ${await currentResponse.text()}`);
  }
  const currentSettings = (await currentResponse.json()) as {
    agents: Array<typeof desiredAgent>;
    launch_targets: Array<{ key: string; available: boolean }>;
  };
  const currentAgent = currentSettings.agents.find((agent) => agent.key === desiredAgent.key);
  if (
    currentAgent?.label === desiredAgent.label &&
    currentAgent.enabled === desiredAgent.enabled &&
    JSON.stringify(currentAgent.command) === JSON.stringify(desiredAgent.command)
  ) {
    expect(currentSettings.launch_targets).toEqual(
      expect.arrayContaining([expect.objectContaining({ key: "codex", available: true })]),
    );
    return;
  }

  const response = await page.request.put(`${baseURL}/api/v1/settings`, {
    data: { agents: [desiredAgent] },
  });
  if (response.status() !== 200) {
    throw new Error(`PUT settings returned ${response.status()}: ${await response.text()}`);
  }
  const settings = (await response.json()) as {
    launch_targets: Array<{ key: string; available: boolean }>;
  };
  expect(settings.launch_targets).toEqual(
    expect.arrayContaining([expect.objectContaining({ key: "codex", available: true })]),
  );
}

async function waitForBackendIdleSync(page: Page, baseURL: string): Promise<void> {
  await expect
    .poll(
      async () => {
        const response = await page.request.get(`${baseURL}/api/v1/sync/status`);
        if (response.status() !== 200) {
          throw new Error(`GET sync status returned ${response.status()}: ${await response.text()}`);
        }
        return ((await response.json()) as { running: boolean }).running;
      },
      { timeout: 60_000 },
    )
    .toBe(false);
}

type WorkspaceResponse = {
  id: string;
  status: string;
  error_message?: string | null;
  item_type?: string;
  item_number?: number;
  repo?: {
    provider: string;
    platform_host: string;
    owner: string;
    name: string;
  };
};

async function ensureSyntheticCodexWorkspace(page: Page, baseURL: string): Promise<void> {
  await configureSyntheticCodexAgent(page, baseURL);
  const listResponse = await page.request.get(`${baseURL}/api/v1/workspaces`);
  if (listResponse.status() !== 200) {
    throw new Error(`GET workspaces returned ${listResponse.status()}: ${await listResponse.text()}`);
  }
  const listed = (await listResponse.json()) as { workspaces: WorkspaceResponse[] };
  let workspace = listed.workspaces.find(
    (candidate) =>
      candidate.repo?.provider === "github" &&
      candidate.repo.platform_host === "github.com" &&
      candidate.repo.owner === "acme" &&
      candidate.repo.name === "widgets" &&
      candidate.item_type === "pull_request" &&
      candidate.item_number === 1,
  );

  if (!workspace) {
    const createResponse = await page.request.post(`${baseURL}/api/v1/workspaces`, {
      data: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        mr_number: 1,
      },
    });
    if (createResponse.status() !== 202) {
      throw new Error(`POST workspace returned ${createResponse.status()}: ${await createResponse.text()}`);
    }
    workspace = (await createResponse.json()) as WorkspaceResponse;
  }

  for (let attempt = 0; attempt < 100 && workspace.status !== "ready"; attempt += 1) {
    const statusResponse = await page.request.get(`${baseURL}/api/v1/workspaces/${workspace.id}`);
    if (statusResponse.status() !== 200) {
      throw new Error(`GET workspace returned ${statusResponse.status()}: ${await statusResponse.text()}`);
    }
    workspace = (await statusResponse.json()) as WorkspaceResponse;
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspace.id} failed to become ready`);
    }
    if (workspace.status !== "ready") {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  expect(workspace.status).toBe("ready");

  const runtimeResponse = await page.request.get(`${baseURL}/api/v1/workspaces/${workspace.id}/runtime`);
  if (runtimeResponse.status() !== 200) {
    throw new Error(`GET workspace runtime returned ${runtimeResponse.status()}: ${await runtimeResponse.text()}`);
  }
  const runtime = (await runtimeResponse.json()) as { sessions?: Array<{ target_key: string }> };
  if (!(runtime.sessions ?? []).some((session) => session.target_key === "codex")) {
    const launchResponse = await page.request.post(`${baseURL}/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
      data: { target_key: "codex" },
    });
    if (launchResponse.status() !== 200) {
      throw new Error(`POST runtime session returned ${launchResponse.status()}: ${await launchResponse.text()}`);
    }
  }
}

async function removeConfiguredRepositories(page: Page, baseURL: string): Promise<void> {
  const response = await page.request.get(`${baseURL}/api/v1/settings`);
  expect(response.ok()).toBe(true);
  const settings = (await response.json()) as {
    repos: Array<{
      provider: string;
      platform_host: string;
      owner: string;
      name: string;
    }>;
  };
  for (const repo of settings.repos) {
    const removed = await page.request.delete(
      `${baseURL}/api/v1/host/${encodeURIComponent(repo.platform_host)}` +
        `/repo/${encodeURIComponent(repo.provider)}/${encodeURIComponent(repo.owner)}/${encodeURIComponent(repo.name)}`,
      { headers: { "content-type": "application/json" } },
    );
    expect(removed.ok()).toBe(true);
  }
}

async function prepareFirstRunPage(page: Page, baseURL: string): Promise<void> {
  await removeConfiguredRepositories(page, baseURL);
  await page.addInitScript(() => {
    localStorage.removeItem("kenn-forge:first-run-onboarding");
    sessionStorage.removeItem("kenn-forge:first-run-onboarding");
  });
  await page.route("**/api/v1/tooling-status", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        git: { available: true, version: "2.50" },
        gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
        glab: { available: false, authenticated: false, host: "gitlab.com" },
      }),
    });
  });
}

function escapeXMLText(value: string): string {
  return value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}

function normalizeNativeSVG(
  svg: string,
  input: {
    title: string;
    description: string;
    width: number;
    height: number;
  },
): string {
  const svgStart = svg.indexOf("<svg");
  const openingTagEnd = svg.indexOf(">", svgStart);
  if (svgStart < 0 || openingTagEnd < 0) {
    throw new Error("pdftocairo output did not contain an SVG root element");
  }

  const openingTag = svg
    .slice(svgStart, openingTagEnd + 1)
    .replace(/\swidth="[^"]*"/, ` width="${input.width}"`)
    .replace(/\sheight="[^"]*"/, ` height="${input.height}"`)
    .replace(/>$/, ' role="img" aria-labelledby="title desc">');
  const metadata =
    `<title id="title">${escapeXMLText(input.title)}</title>` +
    `<desc id="desc">${escapeXMLText(input.description)}</desc>`;

  return `${svg.slice(0, svgStart)}${openingTag}${metadata}${svg.slice(openingTagEnd + 1).trimEnd()}\n`;
}

async function validateCaptureDOM(page: Page): Promise<void> {
  const captureText = await page.evaluate(() => {
    const attributeNames = ["aria-label", "title", "alt", "placeholder"];
    const attributes = Array.from(document.querySelectorAll("*"), (element) =>
      attributeNames.flatMap((name) => {
        const value = element.getAttribute(name);
        return value ? [value] : [];
      }),
    ).flat();
    const inputValues = Array.from(document.querySelectorAll("input, textarea"), (element) =>
      element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement ? element.value : "",
    ).filter(Boolean);
    return [document.body.innerText, ...attributes, ...inputValues].join("\n");
  });

  if (/\/var\/folders|kenn-forge-e2e-\d+/i.test(captureText)) {
    throw new Error("Documentation screenshot contains a private path");
  }
  if (/\bsyncing\b/i.test(captureText)) {
    throw new Error("Documentation screenshot contains a transient syncing state");
  }
}

async function nativeSVGSnapshot(
  page: Page,
  input: {
    title: string;
    description: string;
    width: number;
    height: number;
  },
): Promise<string> {
  const temporaryDir = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-svg-"));
  const pdfPath = path.join(temporaryDir, "capture.pdf");
  const svgPath = path.join(temporaryDir, "capture.svg");

  try {
    await validateCaptureDOM(page);
    await page.emulateMedia({ media: "screen" });
    await page.pdf({
      path: pdfPath,
      width: `${input.width}px`,
      height: `${input.height}px`,
      printBackground: true,
      margin: { top: "0", right: "0", bottom: "0", left: "0" },
      pageRanges: "1",
    });
    try {
      await execFileAsync("pdftocairo", ["-svg", "-f", "1", "-l", "1", pdfPath, svgPath]);
    } catch (error) {
      throw new Error("pdftocairo failed; install Poppler to generate documentation screenshots", { cause: error });
    }
    return normalizeNativeSVG(await readFile(svgPath, "utf8"), input);
  } finally {
    await rm(temporaryDir, { recursive: true, force: true });
  }
}

test.describe("docs screenshot export safety", () => {
  test("rejects private paths before text becomes SVG glyphs", async ({ page }) => {
    await page.setContent("<main>Workspace: /var/folders/private/kenn-forge-e2e-123</main>");

    await expect(
      nativeSVGSnapshot(page, {
        title: "unsafe path",
        description: "unsafe path fixture",
        width: 1280,
        height: 820,
      }),
    ).rejects.toThrow(/private path/i);
  });

  test("rejects transient syncing attributes before export", async ({ page }) => {
    await page.setContent('<main><span aria-label="Syncing">Repository activity</span></main>');

    await expect(
      nativeSVGSnapshot(page, {
        title: "unsafe sync state",
        description: "unsafe sync state fixture",
        width: 1280,
        height: 820,
      }),
    ).rejects.toThrow(/syncing/i);
  });
});

async function captureCase(page: Page, baseURL: string, capture: CaptureCase): Promise<void> {
  await preparePage(page, capture.theme);
  await capture.prepare?.(page, baseURL);
  if (capture.waitForSync) {
    await waitForBackendIdleSync(page, baseURL);
  }
  await page.goto(`${baseURL}${capture.path}`);
  await stabilizePage(page);
  await expect(page.locator(capture.readySelector)).toContainText(capture.readyText);
  if (capture.requiredSelector) {
    await expect(page.locator(`${capture.requiredSelector}:visible`).first()).toBeVisible();
  }
  if (capture.requiredButtonName) {
    await expect(page.getByRole("button", { name: capture.requiredButtonName, exact: true }).first()).toBeVisible();
  }
  if (capture.loadingText) {
    await expect(page.getByText(capture.loadingText)).toHaveCount(0);
  }
  if (capture.waitForSync) {
    await waitForIdleSync(page);
  }
  await capture.afterReady?.(page, capture.theme);
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(capture.theme === "dark");

  if (capture.name === "workspace-codex-session" || capture.name === "maintainer-overview") {
    const terminal = page.locator(".docs-codex-transcript:visible").first();
    const composer = terminal.getByLabel("Codex prompt composer");
    const status = terminal.getByLabel("Codex model and workspace status");
    await expect(composer).toContainText("Summarize recent commits");
    await expect(status).toContainText(/gpt-5\.6-sol high\s*·\s*~\/src\/kenn-io\/forge/);
    const [terminalBox, composerBox, statusBox] = await Promise.all([
      terminal.boundingBox(),
      composer.boundingBox(),
      status.boundingBox(),
    ]);
    expect(terminalBox).not.toBeNull();
    expect(composerBox).not.toBeNull();
    expect(statusBox).not.toBeNull();
    expect(composerBox!.y + composerBox!.height).toBeLessThanOrEqual(terminalBox!.y + terminalBox!.height);
    expect(statusBox!.y + statusBox!.height).toBeLessThanOrEqual(terminalBox!.y + terminalBox!.height);

    const [terminalLightness, composerLightness] = await terminal.evaluate((element) => {
      const sample = (target: Element): number => {
        const canvas = document.createElement("canvas");
        canvas.width = 1;
        canvas.height = 1;
        const context = canvas.getContext("2d");
        if (!context) throw new Error("2D canvas context is unavailable");
        context.fillStyle = getComputedStyle(target).backgroundColor;
        context.fillRect(0, 0, 1, 1);
        return Array.from(context.getImageData(0, 0, 1, 1).data.slice(0, 3)).reduce(
          (sum, channel) => sum + channel,
          0,
        );
      };
      const prompt = element.querySelector('[aria-label="Codex prompt composer"] > div');
      if (!prompt) throw new Error("Codex prompt surface was not found");
      return [sample(element), sample(prompt)];
    });
    if (capture.theme === "light") {
      expect(terminalLightness).toBeGreaterThan(650);
      expect(composerLightness).toBeGreaterThan(600);
    } else {
      expect(terminalLightness).toBeLessThan(100);
      expect(composerLightness).toBeLessThan(250);
    }
  }

  const svg = await nativeSVGSnapshot(page, {
    title: `${capture.name} ${capture.theme}`,
    description: capture.description,
    width: page.viewportSize()?.width ?? 1280,
    height: page.viewportSize()?.height ?? 820,
  });
  expect(svg).not.toMatch(/<foreignObject\b/);
  expect(svg).not.toMatch(/<script\b/i);
  expect(svg).not.toMatch(/\b(?:href|xlink:href)="https?:/i);
  expect(svg).not.toMatch(/\/var\/folders|kenn-forge-e2e-\d+/i);
  expect(svg).toContain(`width="${page.viewportSize()?.width ?? 1280}"`);
  expect(svg).toContain(`height="${page.viewportSize()?.height ?? 820}"`);
  expect(svg).toContain(`<title id="title">${capture.name} ${capture.theme}</title>`);
  expect(svg).toMatch(/<path\b/);
  await writeFile(path.join(outputDir!, `${capture.name}-${capture.theme}.svg`), svg);
}

test.describe("docs workflow screenshots", () => {
  let server: Awaited<ReturnType<typeof startIsolatedWorkspaceE2EServer>> | null = null;

  test.beforeAll(async () => {
    if (!outputDir) {
      throw new Error("KENN_FORGE_DOCS_SCREENSHOT_DIR must point to the staged docs asset directory");
    }
    server = await startIsolatedWorkspaceE2EServer();
    await mkdir(outputDir, { recursive: true });
  });

  test.afterAll(async () => {
    await server?.stop();
  });

  for (const capture of cases) {
    test(`${capture.name} ${capture.theme}`, async ({ page }) => {
      if (!server) throw new Error("e2e server was not started");
      await captureCase(page, server.info.base_url, capture);
    });
  }
});

test.describe("docs first-run screenshots", () => {
  let server: Awaited<ReturnType<typeof startIsolatedE2EServerWithOptions>> | null = null;

  test.beforeAll(async () => {
    if (!outputDir) {
      throw new Error("KENN_FORGE_DOCS_SCREENSHOT_DIR must point to the staged docs asset directory");
    }
    server = await startIsolatedE2EServerWithOptions();
    await mkdir(outputDir, { recursive: true });
  });

  test.afterAll(async () => {
    await server?.stop();
  });

  for (const capture of firstRunCases) {
    test(`${capture.name} ${capture.theme}`, async ({ page }) => {
      if (!server) throw new Error("e2e server was not started");
      await prepareFirstRunPage(page, server.info.base_url);
      await captureCase(page, server.info.base_url, capture);
    });
  }
});
