import { expect, test, type Page } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

import {
  startIsolatedE2EServerWithOptions,
  startIsolatedWorkspaceE2EServer,
} from "../../frontend/tests/e2e-full/support/e2eServer";

const outputDir = process.env.KENN_FORGE_DOCS_SCREENSHOT_DIR;

type ThemeName = "light" | "dark";

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
  afterReady?: (page: Page) => Promise<void>;
  description: string;
};

async function openCodexLaunchMenu(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Create Workspace options" }).click();
  await expect(page.getByRole("menuitem", { name: "Codex" })).toBeVisible();
}

async function showCodexWorkspace(page: Page): Promise<void> {
  const row = page.locator(".workspace-list-sidebar .ws-row", { hasText: "Add widget caching layer" });
  await row.click();
  await expect(row).toHaveClass(/\bselected\b/);
  await expect(page.getByRole("region", { name: "Workflow panes" }).getByRole("tab", { name: "Codex" })).toBeVisible();

  const syntheticPath = "/worktrees/github/github.com/acme/widgets/pr-1";
  await page.locator(".meta-chip.mono.path").evaluate((element, replacement) => {
    const pathText = Array.from(element.childNodes).find((node) => node.nodeType === Node.TEXT_NODE);
    if (!pathText) throw new Error("workspace path text node was not found");
    pathText.textContent = replacement;
    element.setAttribute("title", replacement);
  }, syntheticPath);
}

const cases: CaptureCase[] = [
  {
    name: "maintainer-overview",
    theme: "light",
    path: "/",
    readySelector: ".activity-feed",
    readyText: "Add widget caching layer",
    loadingText: /Loading activity/i,
    waitForSync: true,
    description: "Activity overview with recent pull request and issue context across seeded repositories.",
  },
  {
    name: "maintainer-overview",
    theme: "dark",
    path: "/",
    readySelector: ".activity-feed",
    readyText: "Add widget caching layer",
    loadingText: /Loading activity/i,
    waitForSync: true,
    description:
      "Activity overview in dark mode with recent pull request and issue context across seeded repositories.",
  },
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
        animation-duration: 0.001s !important;
        animation-delay: 0s !important;
        transition-duration: 0s !important;
        caret-color: transparent !important;
      }
    `,
  });
}

async function waitForIdleSync(page: Page): Promise<void> {
  await expect(page.getByRole("button", { name: "Sync", exact: true })).toBeEnabled();
  await expect(page.getByText(/syncing/i)).toHaveCount(0);
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
    const launchResponse = await page.request.post(
      `${baseURL}/api/v1/workspaces/${workspace.id}/runtime/sessions`,
      { data: { target_key: "codex" } },
    );
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

async function svgDOMSnapshot(
  page: Page,
  input: {
    title: string;
    description: string;
    width: number;
    height: number;
  },
): Promise<string> {
  return page.evaluate(async ({ title, description, width, height }) => {
    const svgNS = "http://www.w3.org/2000/svg";
    const xhtmlNS = "http://www.w3.org/1999/xhtml";

    const styles = Array.from(document.styleSheets)
      .map((sheet) => {
        try {
          return Array.from(sheet.cssRules)
            .map((rule) => rule.cssText)
            .join("\n");
        } catch {
          return "";
        }
      })
      .filter(Boolean)
      .join("\n\n");
    const normalizedStyles = styles.replace(/[ \t]+$/gm, "");
    const rootStyle = getComputedStyle(document.documentElement);
    const rootCustomProperties = Array.from(rootStyle)
      .filter((name) => name.startsWith("--"))
      .map((name) => `${name}: ${rootStyle.getPropertyValue(name).trim()};`)
      .join(" ");

    const svgDoc = document.implementation.createDocument(svgNS, "svg", null);
    const svg = svgDoc.documentElement;
    svg.setAttribute("xmlns", svgNS);
    svg.setAttribute("width", String(width));
    svg.setAttribute("height", String(height));
    svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-labelledby", "title desc");

    const titleNode = svgDoc.createElementNS(svgNS, "title");
    titleNode.setAttribute("id", "title");
    titleNode.textContent = title;
    svg.appendChild(titleNode);

    const descNode = svgDoc.createElementNS(svgNS, "desc");
    descNode.setAttribute("id", "desc");
    descNode.textContent = description;
    svg.appendChild(descNode);

    const foreignObject = svgDoc.createElementNS(svgNS, "foreignObject");
    foreignObject.setAttribute("x", "0");
    foreignObject.setAttribute("y", "0");
    foreignObject.setAttribute("width", String(width));
    foreignObject.setAttribute("height", String(height));

    const htmlDoc = document.implementation.createDocument(xhtmlNS, "html", null);
    const html = htmlDoc.documentElement;
    for (const attr of Array.from(document.documentElement.attributes)) {
      if (attr.name === "xmlns") continue;
      html.setAttribute(attr.name, attr.value);
    }
    html.setAttribute("xmlns", xhtmlNS);
    html.setAttribute(
      "style",
      [
        document.documentElement.getAttribute("style") ?? "",
        rootCustomProperties,
        `width: ${width}px`,
        `height: ${height}px`,
        "margin: 0",
        "padding: 0",
        "overflow: hidden",
      ]
        .filter(Boolean)
        .join("; "),
    );

    const head = htmlDoc.createElementNS(xhtmlNS, "head");
    const style = htmlDoc.createElementNS(xhtmlNS, "style");
    style.textContent = `
${normalizedStyles}

html,
body {
  width: ${width}px !important;
  height: ${height}px !important;
  margin: 0 !important;
  overflow: hidden !important;
}

*,
*::before,
*::after {
  animation-duration: 0.001s !important;
  animation-delay: 0s !important;
  transition-duration: 0s !important;
  caret-color: transparent !important;
}
`;
    head.appendChild(style);
    html.appendChild(head);

    const body = htmlDoc.createElementNS(xhtmlNS, "body");
    for (const attr of Array.from(document.body.attributes)) {
      body.setAttribute(attr.name, attr.value);
    }
    body.setAttribute(
      "style",
      `${document.body.getAttribute("style") ?? ""}; width: ${width}px; height: ${height}px; margin: 0; overflow: hidden;`,
    );
    for (const child of Array.from(document.body.childNodes)) {
      body.appendChild(htmlDoc.importNode(child.cloneNode(true), true));
    }
    for (const script of Array.from(body.querySelectorAll("script"))) {
      script.remove();
    }

    const liveImages = Array.from(document.body.querySelectorAll("img"));
    const clonedImages = Array.from(body.querySelectorAll("img"));
    await Promise.all(
      liveImages.map(async (liveImage, index) => {
        const clonedImage = clonedImages[index];
        const source = liveImage.currentSrc || liveImage.src;
        if (!clonedImage || !source || source.startsWith("data:")) return;

        const sourceURL = new URL(source, document.baseURI);
        if (!sourceURL.pathname.toLowerCase().endsWith(".svg")) return;

        const response = await fetch(sourceURL);
        const svgImage = await response.text();
        clonedImage.setAttribute("src", `data:image/svg+xml,${encodeURIComponent(svgImage)}`);
        clonedImage.removeAttribute("srcset");
      }),
    );
    html.appendChild(body);

    foreignObject.appendChild(svgDoc.importNode(html, true));
    svg.appendChild(foreignObject);

    return `${new XMLSerializer().serializeToString(svg).replace(/[ \t]+$/gm, "")}\n`;
  }, input);
}

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
  await capture.afterReady?.(page);
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(capture.theme === "dark");

  const svg = await svgDOMSnapshot(page, {
    title: `${capture.name} ${capture.theme}`,
    description: capture.description,
    width: page.viewportSize()?.width ?? 1280,
    height: page.viewportSize()?.height ?? 820,
  });
  if (capture.theme === "dark") {
    expect(svg).toMatch(/<html[^>]*class="[^"]*\bdark\b[^"]*"/);
  } else {
    expect(svg).not.toMatch(/<html[^>]*class="[^"]*\bdark\b[^"]*"/);
  }
  expect(svg).not.toMatch(/>\s*Syncing(?:\.\.\.)?\s*</i);
  expect(svg).not.toMatch(/>\s*syncing(?:\u2026|\s*\([^<]*\))?\s*</i);
  expect(svg).not.toMatch(/\b(?:aria-label|title)="Syncing"/);
  expect(svg).not.toMatch(/<img[^>]+src="(?:https?:|\/)/i);
  expect(svg).not.toMatch(/data:image\/(?:avif|gif|jpe?g|png|webp)/i);
  expect(svg).not.toMatch(/\/var\/folders|kenn-forge-e2e-\d+/i);
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
