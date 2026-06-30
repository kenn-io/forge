import { expect, test, type Page } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { startIsolatedWorkspaceE2EServer } from "../../frontend/tests/e2e-full/support/e2eServer";

const here = path.dirname(fileURLToPath(import.meta.url));
const docsDir = path.resolve(here, "..");
const outputDir = path.join(docsDir, "assets", "generated");

type ThemeName = "light" | "dark";

type CaptureCase = {
  name: "issue-triager" | "code-reviewer";
  theme: ThemeName;
  path: string;
  readySelector: string;
  readyText: string;
  workspaceSelector: string;
  loadingText: RegExp;
  description: string;
};

const cases: CaptureCase[] = [
  {
    name: "issue-triager",
    theme: "light",
    path: "/issues/github/acme/widgets/10",
    readySelector: ".issue-detail .detail-title",
    readyText: "Widget rendering broken on Safari",
    workspaceSelector: ".issue-detail .btn--workspace",
    loadingText: /Loading comments/i,
    description: "Issue triage view with the newest issue context first and a workspace action in the detail pane.",
  },
  {
    name: "issue-triager",
    theme: "dark",
    path: "/issues/github/acme/widgets/10",
    readySelector: ".issue-detail .detail-title",
    readyText: "Widget rendering broken on Safari",
    workspaceSelector: ".issue-detail .btn--workspace",
    loadingText: /Loading comments/i,
    description: "Issue triage view in dark mode with the newest issue context first and a workspace action in the detail pane.",
  },
  {
    name: "code-reviewer",
    theme: "light",
    path: "/pulls/github/acme/widgets/1",
    readySelector: ".pull-detail .detail-title",
    readyText: "Add widget caching layer",
    workspaceSelector: ".pull-detail .btn--workspace",
    loadingText: /Loading discussion/i,
    description: "Code review view with recent PR activity, review state, CI context, and workspace creation in one pane.",
  },
  {
    name: "code-reviewer",
    theme: "dark",
    path: "/pulls/github/acme/widgets/1",
    readySelector: ".pull-detail .detail-title",
    readyText: "Add widget caching layer",
    workspaceSelector: ".pull-detail .btn--workspace",
    loadingText: /Loading discussion/i,
    description: "Code review view in dark mode with recent PR activity, review state, CI context, and workspace creation in one pane.",
  },
];

async function preparePage(page: Page, theme: ThemeName): Promise<void> {
  await page.addInitScript((themeName) => {
    localStorage.setItem("middleman-theme", themeName);
    localStorage.setItem("middleman-sidebar", "expanded");
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

async function svgDOMSnapshot(page: Page, input: {
  title: string;
  description: string;
  width: number;
  height: number;
}): Promise<string> {
  return page.evaluate(({ title, description, width, height }) => {
    const svgNS = "http://www.w3.org/2000/svg";
    const xhtmlNS = "http://www.w3.org/1999/xhtml";

    const styles = Array.from(document.styleSheets)
      .map((sheet) => {
        try {
          return Array.from(sheet.cssRules).map((rule) => rule.cssText).join("\n");
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
    html.appendChild(body);

    foreignObject.appendChild(svgDoc.importNode(html, true));
    svg.appendChild(foreignObject);

    return `${new XMLSerializer().serializeToString(svg).replace(/[ \t]+$/gm, "")}\n`;
  }, input);
}

test.describe("docs workflow screenshots", () => {
  let server: Awaited<ReturnType<typeof startIsolatedWorkspaceE2EServer>> | null = null;

  test.beforeAll(async () => {
    server = await startIsolatedWorkspaceE2EServer();
    await mkdir(outputDir, { recursive: true });
  });

  test.afterAll(async () => {
    await server?.stop();
  });

  for (const capture of cases) {
    test(`${capture.name} ${capture.theme}`, async ({ page }) => {
      if (!server) throw new Error("e2e server was not started");

      await preparePage(page, capture.theme);
      await page.goto(`${server.info.base_url}${capture.path}`);
      await stabilizePage(page);
      await expect(page.locator(capture.readySelector)).toContainText(capture.readyText);
      await expect(page.locator(`${capture.workspaceSelector}:visible`).first()).toBeVisible();
      await expect(page.getByText(capture.loadingText)).toHaveCount(0);
      await waitForIdleSync(page);
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
      await writeFile(
        path.join(outputDir, `${capture.name}-${capture.theme}.svg`),
        svg,
      );
    });
  }
});
