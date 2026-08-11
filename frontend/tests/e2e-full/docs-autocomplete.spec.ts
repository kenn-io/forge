import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { once } from "node:events";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { expect, type Locator, type Page, test } from "@playwright/test";
import { startDocsServer } from "./support/docsFixture";
import { openSettingsPanel } from "./support/settingsPanel";

type IssueRow = {
  id: number;
  uid: string;
  project_id: number;
  project_uid: string;
  project_name: string;
  short_id: string;
  qualified_id: string;
  title: string;
  body: string;
  status: "open" | "closed";
  labels: string[];
  metadata: Record<string, unknown>;
  revision: number;
  author: string;
  created_at: string;
  updated_at: string;
};

type TaskBackend = {
  url: string;
  seenPaths: string[];
  launchReferers: Array<string | undefined>;
  close: () => Promise<void>;
};

type TaskBackendOptions = {
  waitForInstance?: Promise<void> | undefined;
  waitForLaunchTarget?: Promise<void> | undefined;
  launchUnavailable?: boolean;
  projectsUnavailable?: boolean;
};

type KataHome = {
  restore: () => void;
};

const now = "2026-05-15T10:00:00Z";
const autocompleteIssues: IssueRow[] = [
  issueRow({
    id: 1,
    uid: "issue-rent",
    project_id: 1,
    project_uid: "project-finances",
    project_name: "Finances",
    short_id: "rent",
    title: "Pay rent",
    body: "Send rent.",
  }),
  issueRow({
    id: 2,
    uid: "issue-read",
    project_id: 2,
    project_uid: "project-work",
    project_name: "Work",
    short_id: "read",
    title: "Read project brief",
    body: "Review the brief.",
  }),
  issueRow({
    id: 3,
    uid: "issue-dent",
    project_id: 3,
    project_uid: "project-health",
    project_name: "Health",
    short_id: "dent",
    title: "Call dentist",
    body: "Book cleaning.",
  }),
  issueRow({
    id: 4,
    uid: "issue-yoga",
    project_id: 3,
    project_uid: "project-health",
    project_name: "Health",
    short_id: "yoga",
    title: "Yoga class",
    body: "Reserve spot.",
  }),
];

function issueRow(
  input: Omit<
    IssueRow,
    "qualified_id" | "status" | "labels" | "metadata" | "revision" | "author" | "created_at" | "updated_at"
  > &
    Partial<Pick<IssueRow, "qualified_id" | "status" | "labels" | "metadata">>,
): IssueRow {
  return {
    ...input,
    qualified_id: input.qualified_id ?? `${input.project_name}#${input.short_id}`,
    status: input.status ?? "open",
    labels: input.labels ?? [],
    metadata: input.metadata ?? {},
    revision: 1,
    author: "e2e",
    created_at: now,
    updated_at: now,
  };
}

async function startTaskBackend(
  issues: IssueRow[] = autocompleteIssues,
  options: TaskBackendOptions = {},
): Promise<TaskBackend> {
  const seenPaths: string[] = [];
  const launchReferers: Array<string | undefined> = [];
  const server = createServer((req, res) => {
    void handleTaskRequest(req, res, issues, seenPaths, launchReferers, options).catch(() => {
      if (!res.headersSent) writeJSON(res, 500, { error: "e2e_backend_error" });
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const addr = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${addr.port}`,
    seenPaths,
    launchReferers,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((err) => {
          if (err) reject(err);
          else resolve();
        });
      }),
  };
}

async function handleTaskRequest(
  req: IncomingMessage,
  res: ServerResponse,
  issues: IssueRow[],
  seenPaths: string[],
  launchReferers: Array<string | undefined>,
  options: TaskBackendOptions,
): Promise<void> {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  seenPaths.push(`${req.method ?? "GET"} ${url.pathname}${url.search}`);
  if (url.pathname === "/api/v1/health") {
    await options.waitForInstance;
    writeJSON(res, 200, { ok: true, api_schema_version: "0.10.0" });
    return;
  }
  if (url.pathname === "/api/v1/instance") {
    await options.waitForInstance;
    writeJSON(res, 200, { instance_uid: "docs-autocomplete-e2e", version: "0.0.0-e2e", schema_version: 1 });
    return;
  }
  if (url.pathname === "/api/v1/projects") {
    if (options.projectsUnavailable) {
      writeJSON(res, 503, { error: "projects_unavailable" });
      return;
    }
    writeJSON(res, 200, {
      projects: projectsFromIssues(issues),
      fetched_at: now,
    });
    return;
  }
  if (url.pathname === "/api/v1/ui/references") {
    const query = (url.searchParams.get("q") ?? "").toLocaleLowerCase();
    const requestedUIDs = new Set(url.searchParams.getAll("issue_uid"));
    const projectUID = url.searchParams.get("project_uid") ?? "";
    const limit = Number.parseInt(url.searchParams.get("limit") ?? "200", 10);
    const matches = issues.filter((issue) => {
      if (requestedUIDs.size > 0 && !requestedUIDs.has(issue.uid)) return false;
      if (projectUID && issue.project_uid !== projectUID) return false;
      if (!query) return true;
      return [issue.short_id, issue.qualified_id, issue.title, issue.project_name].some((value) =>
        value.toLocaleLowerCase().includes(query),
      );
    });
    writeJSON(res, 200, { issues: matches.slice(0, Number.isFinite(limit) ? limit : 200) });
    return;
  }
  if (url.pathname === "/api/v1/ui/issue-reference") {
    const projectID = Number.parseInt(url.searchParams.get("project_id") ?? "", 10);
    const reference = url.searchParams.get("ref") ?? "";
    const issue = issues.find((candidate) => candidate.project_id === projectID && candidate.short_id === reference);
    if (!issue) {
      writeJSON(res, 404, { error: "not_found" });
      return;
    }
    writeJSON(res, 200, { issue: { uid: issue.uid, project_uid: issue.project_uid } });
    return;
  }
  if (url.pathname.startsWith("/api/v1/issues/")) {
    const issueUID = decodeURIComponent(url.pathname.slice("/api/v1/issues/".length));
    const issue = issues.find((candidate) => candidate.uid === issueUID);
    if (!issue) {
      writeJSON(res, 404, { error: "not_found" });
      return;
    }
    writeJSON(res, 200, {
      issue,
      comments: [],
      links: [],
      children: [],
      pending_claims: [],
    });
    return;
  }
  if (url.pathname === "/api/v1/ui/launch-target") {
    await options.waitForLaunchTarget;
    const issueUID = url.searchParams.get("issue_uid") ?? "";
    const available = !options.launchUnavailable && issues.some((candidate) => candidate.uid === issueUID);
    writeJSON(res, 200, {
      available,
      ...(available ? { url: `http://${req.headers.host}/launched/${encodeURIComponent(issueUID)}` } : {}),
      ...(!available ? { reason: "browser_unavailable" } : {}),
    });
    return;
  }
  if (url.pathname.startsWith("/launched/")) {
    launchReferers.push(req.headers.referer);
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end("<!doctype html><title>Kata issue</title><p>Opened in Kata</p>");
    return;
  }
  writeJSON(res, 404, { error: "not_found" });
}

function projectsFromIssues(issues: IssueRow[]) {
  const projects = new Map<
    number,
    {
      id: number;
      uid: string;
      name: string;
      metadata: Record<string, unknown>;
      revision: number;
      created_at: string;
      stats: { open: number; closed: number };
    }
  >();
  for (const issue of issues) {
    const existing = projects.get(issue.project_id);
    if (existing) {
      existing.stats[issue.status] += 1;
      continue;
    }
    projects.set(issue.project_id, {
      id: issue.project_id,
      uid: issue.project_uid,
      name: issue.project_name,
      metadata: {},
      revision: 1,
      created_at: now,
      stats: {
        open: issue.status === "open" ? 1 : 0,
        closed: issue.status === "closed" ? 1 : 0,
      },
    });
  }
  return [...projects.values()];
}

function writeJSON(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

async function configureKataHome(backendURL: string): Promise<KataHome> {
  return configureKataHomeDaemons([{ name: "docs", url: backendURL }], "docs");
}

async function configureKataHomeDaemons(
  daemons: { name: string; url: string }[],
  activeDaemon: string,
): Promise<KataHome> {
  const home = await mkdtemp(path.join(os.tmpdir(), "kenn-forge-docs-kata-e2e-"));
  await mkdir(home, { recursive: true });
  await writeFile(
    path.join(home, "config.toml"),
    [
      `active_daemon = ${JSON.stringify(activeDaemon)}`,
      "",
      ...daemons.flatMap((daemon) => [
        "[[daemon]]",
        `name = ${JSON.stringify(daemon.name)}`,
        `url = ${JSON.stringify(daemon.url)}`,
        "",
      ]),
    ].join("\n"),
  );
  const previous = process.env.KATA_HOME;
  process.env.KATA_HOME = home;
  return {
    restore: () => {
      if (previous === undefined) {
        delete process.env.KATA_HOME;
      } else {
        process.env.KATA_HOME = previous;
      }
    },
  };
}

async function openDocsEditor(page: Page, baseURL: string, route = "/docs"): Promise<Locator> {
  await page.goto(`${baseURL}${route}`);
  await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();
  const editButton = page.getByRole("button", { name: "Edit", exact: true });
  await expect(editButton).toBeEnabled();
  await editButton.click();
  const editor = page.locator(".cm-editor .cm-content");
  await expect(editor).toBeVisible();
  await editor.click();
  return editor;
}

async function clearEditor(page: Page, editor: Locator): Promise<void> {
  await editor.focus();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.press("Delete");
}

function autocompleteTooltip(page: Page): Locator {
  return page.locator(".cm-tooltip-autocomplete").locator("visible=true");
}

function autocompleteOption(page: Page, label: string): Locator {
  return autocompleteTooltip(page).getByRole("option", { name: label, exact: false });
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T | PromiseLike<T>) => void } {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((innerResolve) => {
    resolve = innerResolve;
  });
  return { promise, resolve };
}

test.describe("docs markdown editor autocomplete", () => {
  test("keeps healthy Kata daemons selectable when the default mapping diagnostics fail", async ({ page }) => {
    const home = await startTaskBackend(autocompleteIssues, { projectsUnavailable: true });
    const work = await startTaskBackend([
      issueRow({
        id: 501,
        uid: "issue-secondary",
        project_id: 501,
        project_uid: "project-secondary",
        project_name: "Secondary project",
        short_id: "secondary",
        title: "Healthy secondary task",
        body: "The healthy daemon remains selectable.",
      }),
    ]);
    const kataHome = await configureKataHomeDaemons(
      [
        { name: "home", url: home.url },
        { name: "work", url: work.url },
      ],
      "home",
    );
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      await page.goto(`${server.info.base_url}/settings`);
      await openSettingsPanel(page, "Kata mappings");

      const picker = page.getByRole("combobox", { name: /Kata mapping daemon: home/ });
      await expect(picker).toBeVisible();
      await expect(page.getByRole("alert")).toBeVisible();

      await picker.click();
      await expect(page.getByRole("option", { name: /home.*Health: connected.*API schema 0\.10\.0/ })).toBeVisible();
      const workOption = page.getByRole("option", { name: /work.*Health: connected.*API schema 0\.10\.0/ });
      await expect(workOption).toBeVisible();
      await workOption.click();

      await expect(page.getByRole("combobox", { name: /Kata mapping daemon: work/ })).toBeVisible();
      await expect(page.getByText("Secondary project", { exact: true })).toBeVisible();
      expect(home.seenPaths).toContain("GET /api/v1/projects");
      expect(work.seenPaths).toContain("GET /api/v1/projects");
    } finally {
      await server.stop();
      kataHome.restore();
      await home.close();
      await work.close();
    }
  });

  test("opens a completed canonical Kata UID through the folder's pinned daemon", async ({ page }) => {
    const activeBackend = await startTaskBackend([
      issueRow({
        id: 101,
        uid: "issue-active-only",
        project_id: 101,
        project_uid: "project-active",
        project_name: "Active",
        short_id: "active-only",
        title: "Active daemon task",
        body: "This task must not resolve for the folder.",
      }),
    ]);
    const folderBackend = await startTaskBackend(
      autocompleteIssues.map((issue) => (issue.uid === "issue-rent" ? { ...issue, status: "closed" } : issue)),
    );
    const kataHome = await configureKataHomeDaemons(
      [
        { name: "active", url: activeBackend.url },
        { name: "docs", url: folderBackend.url },
      ],
      "active",
    );
    const server = await startDocsServer(page, { folder: { daemon: "docs" }, freshProcess: true });
    try {
      const writeResponse = await page.request.put(
        `${server.info.base_url}/api/v1/docs/folders/notes/file?path=README.md`,
        { data: { content: "[Open task](kata://issue/issue-rent)" } },
      );
      expect(writeResponse.ok()).toBe(true);
      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);

      const popupPromise = page.waitForEvent("popup");
      const launchURL = `${folderBackend.url}/launched/issue-rent`;
      const targetRequest = page.context().waitForEvent("request", {
        predicate: (request) => request.url() === launchURL,
      });
      await page.getByRole("link", { name: "Open task" }).click();
      const popup = await popupPromise;
      const navigationRequest = await targetRequest;
      expect(navigationRequest.url()).toBe(launchURL);
      expect(await navigationRequest.allHeaders()).not.toHaveProperty("referer");
      expect(folderBackend.seenPaths.some((entry) => entry.includes("issue_uid=issue-rent"))).toBe(true);
      expect(folderBackend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/launch-target?"))).toBe(true);
      expect(activeBackend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/references?"))).toBe(false);
      expect(activeBackend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/launch-target?"))).toBe(false);
      await popup.close();
    } finally {
      await server.stop();
      kataHome.restore();
      await activeBackend.close();
      await folderBackend.close();
    }
  });

  test("opens a completed canonical qualified Kata reference through the folder's pinned daemon", async ({ page }) => {
    const activeBackend = await startTaskBackend([
      issueRow({
        id: 101,
        uid: "issue-active-rent",
        project_id: 101,
        project_uid: "project-active-finances",
        project_name: "Finances",
        short_id: "rent",
        title: "Active daemon rent",
        body: "This duplicate reference must not win.",
      }),
    ]);
    const folderBackend = await startTaskBackend(
      autocompleteIssues.map((issue) =>
        issue.uid === "issue-rent"
          ? {
              ...issue,
              project_name: "Finances display name",
              qualified_id: "finances-identity#rent",
              status: "closed",
            }
          : issue,
      ),
    );
    const kataHome = await configureKataHomeDaemons(
      [
        { name: "active", url: activeBackend.url },
        { name: "docs", url: folderBackend.url },
      ],
      "active",
    );
    const server = await startDocsServer(page, { folder: { daemon: "docs" }, freshProcess: true });
    try {
      const writeResponse = await page.request.put(
        `${server.info.base_url}/api/v1/docs/folders/notes/file?path=README.md`,
        { data: { content: "Open finances-identity/#rent" } },
      );
      expect(writeResponse.ok()).toBe(true);
      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);

      const popupPromise = page.waitForEvent("popup");
      const launchURL = `${folderBackend.url}/launched/issue-rent`;
      const targetRequest = page.context().waitForEvent("request", {
        predicate: (request) => request.url() === launchURL,
      });
      await page.getByRole("link", { name: "finances-identity/#rent" }).click();
      const popup = await popupPromise;
      expect((await targetRequest).url()).toBe(launchURL);
      expect(folderBackend.seenPaths).toContain("GET /api/v1/ui/references?limit=200&q=finances-identity%23rent");
      expect(folderBackend.seenPaths).not.toContain("GET /api/v1/projects");
      expect(folderBackend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/issue-reference?"))).toBe(false);
      expect(folderBackend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/launch-target?"))).toBe(true);
      expect(activeBackend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/references?"))).toBe(false);
      expect(activeBackend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/launch-target?"))).toBe(false);
      await popup.close();
    } finally {
      await server.stop();
      kataHome.restore();
      await activeBackend.close();
      await folderBackend.close();
    }
  });

  test("opens a rendered completed bare Kata reference when its short ID is unique", async ({ page }) => {
    const backend = await startTaskBackend(
      autocompleteIssues.map((issue) => (issue.uid === "issue-rent" ? { ...issue, status: "closed" } : issue)),
    );
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { folder: { daemon: "docs" }, freshProcess: true });
    try {
      const writeResponse = await page.request.put(
        `${server.info.base_url}/api/v1/docs/folders/notes/file?path=README.md`,
        { data: { content: "Open #rent" } },
      );
      expect(writeResponse.ok()).toBe(true);
      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);

      const popupPromise = page.waitForEvent("popup");
      const launchURL = `${backend.url}/launched/issue-rent`;
      const targetRequest = page.context().waitForEvent("request", {
        predicate: (request) => request.url() === launchURL,
      });
      await page.getByRole("link", { name: "#rent" }).click();
      const popup = await popupPromise;
      expect((await targetRequest).url()).toBe(launchURL);
      expect(backend.seenPaths).toContain("GET /api/v1/projects");
      expect(backend.seenPaths).toContain("GET /api/v1/ui/issue-reference?project_id=1&ref=rent");
      expect(backend.seenPaths).toContain("GET /api/v1/ui/issue-reference?project_id=2&ref=rent");
      expect(backend.seenPaths).toContain("GET /api/v1/ui/issue-reference?project_id=3&ref=rent");
      expect(backend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/references?"))).toBe(false);
      expect(backend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/launch-target?"))).toBe(true);
      await popup.close();
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("closes the reserved popup when Kata has no launch target", async ({ page }) => {
    const backend = await startTaskBackend(autocompleteIssues, { launchUnavailable: true });
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { folder: { daemon: "docs" }, freshProcess: true });
    try {
      const writeResponse = await page.request.put(
        `${server.info.base_url}/api/v1/docs/folders/notes/file?path=README.md`,
        { data: { content: "[Open task](kata://issue/issue-rent)" } },
      );
      expect(writeResponse.ok()).toBe(true);
      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);

      const popupPromise = page.waitForEvent("popup");
      await page.getByRole("link", { name: "Open task" }).click();
      const popup = await popupPromise;
      await expect(page.getByText("Kata cannot open this issue in a browser.")).toBeVisible();
      await expect.poll(() => popup.isClosed()).toBe(true);
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("binds a new folder to the only available Kata daemon", async ({ page }) => {
    const backend = await startTaskBackend();
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      const foldersResponse = await page.request.get(`${server.info.base_url}/api/v1/docs/folders`);
      expect(foldersResponse.ok()).toBe(true);
      const folders = (await foldersResponse.json()) as { folders: { path: string }[] };
      const projectsPath = path.join(folders.folders[0]!.path, "Projects");

      await page.goto(`${server.info.base_url}/docs`);
      await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();
      await page.getByRole("button", { name: "Add folder" }).click();

      const dialog = page.getByRole("dialog", { name: "Add folder" });
      await expect(dialog.getByRole("combobox", { name: "Daemon" })).toBeVisible();
      await dialog.getByLabel("Folder path").fill(projectsPath);
      await dialog.getByRole("combobox", { name: "Daemon" }).click();
      await page.getByRole("option", { name: "docs", exact: true }).click();
      await dialog.getByRole("button", { name: "Add folder" }).click();
      await expect(dialog).toBeHidden();

      await expect
        .poll(async () => {
          const response = await page.request.get(`${server.info.base_url}/api/v1/docs/folders`);
          const body = (await response.json()) as { folders: { path: string; daemon?: string }[] };
          return body.folders.some((folder) => folder.path === projectsPath && folder.daemon === "docs");
        })
        .toBe(true);
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("typing wikilink prefix opens the menu and inserts the chosen doc", async ({ page }) => {
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url);
      await clearEditor(page, editor);

      await page.keyboard.type("see [[road");
      await expect(autocompleteTooltip(page)).toBeVisible();
      const roadmap = autocompleteOption(page, "roadmap");
      await expect(roadmap).toBeVisible();

      await roadmap.click();

      await expect(editor).toContainText("see [[roadmap]]");
      await expect(editor).not.toContainText("]]]]");
    } finally {
      await server.stop();
    }
  });

  test("wikilink menu matches nested docs by basename prefix", async ({ page }) => {
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url);
      await clearEditor(page, editor);

      await page.keyboard.type("[[2026");
      const tooltip = autocompleteTooltip(page);

      await expect(tooltip).toBeVisible();
      await expect(tooltip).toContainText("2026-05-15");
      await expect(tooltip).toContainText("2026-05-14");
    } finally {
      await server.stop();
    }
  });

  test("wikilink menu matches nested docs by path prefix and inserts the chosen doc", async ({ page }) => {
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url);
      await clearEditor(page, editor);

      await page.keyboard.type("[[Daily/2026");
      const daily = autocompleteOption(page, "2026-05-15");

      await expect(autocompleteTooltip(page)).toBeVisible();
      await expect(daily).toBeVisible();
      await daily.click();

      await expect(editor).toContainText("[[2026-05-15]]");
      await expect(editor).not.toContainText("]]]]");
    } finally {
      await server.stop();
    }
  });

  test("typing bare issue references opens the task menu and inserts the chosen task", async ({ page }) => {
    const backend = await startTaskBackend();
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { folder: { daemon: "docs" }, freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url);
      await clearEditor(page, editor);

      await page.keyboard.type("ref #re");
      const tooltip = autocompleteTooltip(page);
      await expect(tooltip).toBeVisible();
      const rent = autocompleteOption(page, "Finances/#rent");
      await expect(rent).toBeVisible();

      await rent.click();

      await expect(editor).toContainText("ref Finances/#rent");
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("folder-bound task references search the bound daemon", async ({ page }) => {
    const home = await startTaskBackend([
      issueRow({
        id: 101,
        uid: "issue-home-shared",
        project_id: 101,
        project_uid: "project-home",
        project_name: "Home",
        short_id: "shared-1",
        title: "Default daemon completion",
        body: "This task belongs to the default daemon.",
      }),
    ]);
    const work = await startTaskBackend([
      issueRow({
        id: 202,
        uid: "issue-work-shared",
        project_id: 202,
        project_uid: "project-work",
        project_name: "Work",
        short_id: "shared-1",
        title: "Bound daemon completion",
        body: "This task belongs to the bound daemon.",
      }),
    ]);
    const kataHome = await configureKataHomeDaemons(
      [
        { name: "home", url: home.url },
        { name: "work", url: work.url },
      ],
      "home",
    );
    const server = await startDocsServer(page, { folder: { daemon: "work" }, freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url, "/docs?folder=notes&doc=README.md");
      await clearEditor(page, editor);

      await page.keyboard.type("see #shared");

      const tooltip = autocompleteTooltip(page);
      await expect(tooltip).toBeVisible();
      await expect(tooltip).toContainText("Bound daemon completion");
      await expect(tooltip).not.toContainText("Default daemon completion");
      await expect
        .poll(() => work.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/references?")))
        .toBe(true);
      expect(home.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/references?"))).toBe(false);
    } finally {
      await server.stop();
      kataHome.restore();
      await home.close();
      await work.close();
    }
  });

  test("stale folder daemon binding warns without falling back to another daemon", async ({ page }) => {
    const backend = await startTaskBackend([
      issueRow({
        id: 101,
        uid: "issue-fallback",
        project_id: 101,
        project_uid: "project-home",
        project_name: "Home",
        short_id: "fallback",
        title: "Active daemon fallback",
        body: "This task belongs to the active daemon.",
      }),
    ]);
    const kataHome = await configureKataHomeDaemons([{ name: "home", url: backend.url }], "home");
    const server = await startDocsServer(page, { folder: { daemon: "gone" }, freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url, "/docs?folder=notes&doc=README.md");
      const warning = page.locator(".folder-daemon-warning");
      await expect(warning).toContainText("gone");
      await expect(warning).toContainText("cannot be opened");

      await clearEditor(page, editor);
      await page.keyboard.type("see #fallback");
      await expect(autocompleteTooltip(page)).toHaveCount(0);
      expect(backend.seenPaths.some((entry) => entry.startsWith("GET /api/v1/ui/references?"))).toBe(false);
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("folder daemon binding does not warn while the daemon roster is pending", async ({ page }) => {
    const instanceGate = deferred<void>();
    const backend = await startTaskBackend(
      [
        issueRow({
          id: 101,
          uid: "issue-delayed",
          project_id: 101,
          project_uid: "project-delayed",
          project_name: "Delayed",
          short_id: "delayed",
          title: "Delayed roster task",
          body: "This task belongs to the configured daemon.",
        }),
      ],
      { waitForInstance: instanceGate.promise },
    );
    const kataHome = await configureKataHomeDaemons([{ name: "docs", url: backend.url }], "docs");
    const server = await startDocsServer(page, { folder: { daemon: "docs" }, freshProcess: true });
    try {
      const rosterResponse = page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/v1/kata/daemons" && response.status() === 200,
      );
      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);
      await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();
      await expect.poll(() => backend.seenPaths).toContain("GET /api/v1/health");
      const warning = page.locator(".folder-daemon-warning");
      await expect(warning).toHaveCount(0);

      await page.getByRole("button", { name: "Add folder" }).click();
      const dialog = page.getByRole("dialog", { name: "Add folder" });
      await dialog.getByLabel("Folder path").fill("/tmp/pending-docs-folder");
      await expect(dialog.getByRole("button", { name: "Add folder" })).toBeDisabled();
      await expect(dialog.getByRole("status")).toHaveText("Loading Kata daemons…");

      instanceGate.resolve();

      await rosterResponse;
      await expect(warning).toHaveCount(0);
      await expect(dialog.getByRole("combobox", { name: "Daemon" })).toBeVisible();
      await expect(dialog.getByRole("button", { name: "Add folder" })).toBeEnabled();
    } finally {
      instanceGate.resolve();
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("qualified task references scope suggestions to the named project", async ({ page }) => {
    const backend = await startTaskBackend();
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { folder: { daemon: "docs" }, freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url);
      await clearEditor(page, editor);

      await page.keyboard.type("see Finances/#");
      const tooltip = autocompleteTooltip(page);
      await expect(tooltip).toBeVisible();
      await expect(tooltip).toContainText("Finances/#rent");
      await expect(tooltip).not.toContainText("Finances/#dent");
      await expect(tooltip).not.toContainText("Finances/#yoga");
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("no-match task references leave the editor text unchanged", async ({ page }) => {
    const backend = await startTaskBackend();
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url);
      await clearEditor(page, editor);

      await page.keyboard.type("nothing #zzzzzz");

      await expect(editor).toContainText("nothing #zzzzzz");
      await expect(autocompleteTooltip(page).getByText("zzzzzz")).toHaveCount(0);
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("links a Kata task to a pull request and renders shared inline detail", async ({ page }) => {
    const launchTarget = deferred<void>();
    const backend = await startTaskBackend(
      [
        issueRow({
          id: 301,
          uid: "issue-linked",
          project_id: 301,
          project_uid: "project-widgets",
          project_name: "External",
          short_id: "linked-1",
          title: "Keep the shared Kata detail",
          body: "Render this through the Kata-owned component.",
        }),
      ],
      { waitForLaunchTarget: launchTarget.promise },
    );
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      let linkReads = 0;
      page.on("request", (request) => {
        const url = new URL(request.url());
        if (
          request.method() === "GET" &&
          url.pathname === "/api/v1/host/github.com/pulls/github/acme/widgets/1/kata-links"
        ) {
          linkReads += 1;
        }
      });
      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);
      const kataTab = page.getByRole("tab", { name: "Kata" });
      await expect(kataTab).toBeVisible();
      expect(linkReads).toBe(0);
      await kataTab.click();
      await expect(page.getByText("No Kata issues linked yet.")).toBeVisible();

      await page.getByRole("button", { name: "Link Kata issue" }).click();
      const dialog = page.getByRole("dialog", { name: "Link Kata issue" });
      await expect(dialog).toBeVisible();
      await dialog.getByRole("searchbox", { name: "Search Kata issues" }).fill("shared");
      const result = dialog.getByRole("button", { name: /External#linked-1 Keep the shared Kata detail/ });
      await expect(result).toBeVisible();
      await result.click();
      const linked = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "POST" &&
          url.pathname === "/api/v1/host/github.com/pulls/github/acme/widgets/1/kata-links"
        );
      });
      await dialog.getByRole("button", { name: "Link issue" }).click();
      expect((await linked).status()).toBe(200);

      await expect(page.getByLabel("Kata issue detail")).toBeVisible();
      await expect(page.getByText("Render this through the Kata-owned component.")).toBeVisible();
      await expect(
        page.getByText("No repository mapping matches this Kata project. Configure a mapping in Settings."),
      ).toBeVisible();

      const popupPromise = page.waitForEvent("popup");
      await page.getByRole("button", { name: "Open in Kata" }).click();
      const popup = await popupPromise;
      expect(popup.url()).toBe("about:blank");
      launchTarget.resolve();
      await popup.waitForURL(/\/launched\/issue-linked$/);
      await expect(popup.getByText("Opened in Kata")).toBeVisible();
      expect(backend.launchReferers).toEqual([undefined]);
      await popup.close();

      const unlinked = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "DELETE" &&
          url.pathname.startsWith("/api/v1/host/github.com/pulls/github/acme/widgets/1/kata-links/")
        );
      });
      await page.getByRole("button", { name: "Unlink External#linked-1" }).click();
      expect((await unlinked).status()).toBe(204);
      await expect(page.getByText("No Kata issues linked yet.")).toBeVisible();
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("keeps delayed linked-task workspace creation identity-scoped across selection changes", async ({ page }) => {
    test.slow();
    const issues = [
      issueRow({
        id: 303,
        uid: "issue-workspace-a",
        project_id: 303,
        project_uid: "project-widgets",
        project_name: "widgets",
        short_id: "workspace-a",
        title: "Create workspace A",
        body: "First linked task.",
      }),
      issueRow({
        id: 304,
        uid: "issue-workspace-b",
        project_id: 303,
        project_uid: "project-widgets",
        project_name: "widgets",
        short_id: "workspace-b",
        title: "Create workspace B",
        body: "Second linked task.",
      }),
    ];
    const backend = await startTaskBackend(issues);
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);
      await page.getByRole("tab", { name: "Kata" }).click();
      for (const issue of issues) {
        await page.getByRole("button", { name: "Link Kata issue" }).click();
        const dialog = page.getByRole("dialog", { name: "Link Kata issue" });
        await dialog.getByRole("searchbox", { name: "Search Kata issues" }).fill(issue.title);
        await dialog.getByRole("button", { name: new RegExp(`${issue.qualified_id} ${issue.title}`) }).click();
        const linked = page.waitForResponse((response) => {
          const url = new URL(response.url());
          return (
            response.request().method() === "POST" &&
            url.pathname === "/api/v1/host/github.com/pulls/github/acme/widgets/1/kata-links"
          );
        });
        await dialog.getByRole("button", { name: "Link issue" }).click();
        const response = await linked;
        expect(response.status(), await response.text()).toBe(200);
        await expect(dialog).toBeHidden();
      }

      await expect(page.getByLabel("Kata issue detail")).toBeVisible();

      const releaseCreate = deferred<void>();
      let createRequests = 0;
      await page.route("**/api/v1/kata/workspaces", async (route) => {
        if (route.request().method() !== "POST") {
          await route.continue();
          return;
        }
        createRequests += 1;
        await releaseCreate.promise;
        await route.continue();
      });

      await page.getByRole("button", { name: "Create workspace", exact: true }).click();
      await expect.poll(() => createRequests).toBe(1);
      await page.getByRole("button", { name: /widgets#workspace-b Create workspace B/ }).click();
      await expect(page.getByText("Second linked task.")).toBeVisible();
      await page.getByRole("button", { name: /widgets#workspace-a Create workspace A/ }).click();
      const creating = page.getByRole("button", { name: "Creating…" });
      await expect(creating).toBeDisabled();
      await creating.click({ force: true });
      expect(createRequests).toBe(1);

      releaseCreate.resolve();
      const openWorkspace = page.getByRole("button", { name: "Open workspace" });
      await expect(openWorkspace).toBeVisible();
      await expect(page).toHaveURL(/\/pulls\/github\/acme\/widgets\/1$/);
      await openWorkspace.click();
      await expect(page).toHaveURL(/\/terminal\//);
      const workspaceID = new URL(page.url()).pathname.split("/").at(-1) ?? "";
      await expect
        .poll(
          async () => {
            const detail = await page.request.get(`${server.info.base_url}/api/v1/workspaces/${workspaceID}`);
            if (!detail.ok()) return `http-${detail.status()}`;
            return ((await detail.json()) as { status: string }).status;
          },
          { timeout: 60_000 },
        )
        .toBe("ready");
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("shares Kata workspace creation between linked-task and New Workspace entry points", async ({ page }) => {
    test.slow();
    const issue = issueRow({
      id: 305,
      uid: "issue-shared-workspace",
      project_id: 305,
      project_uid: "project-widgets",
      project_name: "widgets",
      short_id: "shared-workspace",
      title: "Share workspace creation",
      body: "One task should create one workspace.",
    });
    const backend = await startTaskBackend([issue]);
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      await page.goto(`${server.info.base_url}/pulls/github/acme/widgets/1`);
      await page.getByRole("tab", { name: "Kata" }).click();
      await page.getByRole("button", { name: "Link Kata issue" }).click();
      const linkDialog = page.getByRole("dialog", { name: "Link Kata issue" });
      await linkDialog.getByRole("searchbox", { name: "Search Kata issues" }).fill(issue.title);
      await linkDialog.getByRole("button", { name: new RegExp(`${issue.qualified_id} ${issue.title}`) }).click();
      const linked = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return (
          response.request().method() === "POST" &&
          url.pathname === "/api/v1/host/github.com/pulls/github/acme/widgets/1/kata-links"
        );
      });
      await linkDialog.getByRole("button", { name: "Link issue" }).click();
      const linkResponse = await linked;
      expect(linkResponse.status(), await linkResponse.text()).toBe(200);
      await expect(linkDialog).toBeHidden();

      const releaseCreate = deferred<void>();
      let createRequests = 0;
      await page.route("**/api/v1/kata/workspaces", async (route) => {
        if (route.request().method() !== "POST") {
          await route.continue();
          return;
        }
        createRequests += 1;
        await releaseCreate.promise;
        await route.continue();
      });

      await page.getByRole("button", { name: "Create workspace", exact: true }).click();
      await expect.poll(() => createRequests).toBe(1);
      await page.getByRole("button", { name: "Workspaces", exact: true }).click();
      await page.getByRole("button", { name: "New workspace" }).click();
      const workspaceDialog = page.getByRole("dialog", { name: "New workspace" });
      await workspaceDialog.getByRole("button", { name: "Kata issue" }).click();
      await workspaceDialog.getByRole("searchbox", { name: "Search Kata issues" }).fill(issue.title);
      await workspaceDialog.getByRole("button", { name: new RegExp(`${issue.qualified_id} ${issue.title}`) }).click();

      const creating = workspaceDialog.getByRole("button", { name: "Opening workspace…" });
      await expect(creating).toBeDisabled();
      await creating.click({ force: true });
      expect(createRequests).toBe(1);

      releaseCreate.resolve();
      const openWorkspace = workspaceDialog.getByRole("button", {
        name: "Create or open workspace",
        exact: true,
      });
      await expect(openWorkspace).toBeEnabled();
      await openWorkspace.click();
      expect(createRequests).toBe(1);
      await expect(page).toHaveURL(/\/terminal\//);
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });

  test("creates a workspace from a selected Kata issue", async ({ page }) => {
    test.slow();
    const backend = await startTaskBackend([
      issueRow({
        id: 302,
        uid: "issue-workspace",
        project_id: 302,
        project_uid: "project-widgets",
        project_name: "widgets",
        short_id: "workspace-1",
        title: "Create the Kata workspace",
        body: "Use the mapped widgets repository.",
      }),
    ]);
    const kataHome = await configureKataHome(backend.url);
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      await page.goto(`${server.info.base_url}/workspaces`);
      await page.getByRole("button", { name: "New workspace" }).click();
      const dialog = page.getByRole("dialog", { name: "New workspace" });
      await dialog.getByRole("button", { name: "Kata issue" }).click();
      await expect(dialog.getByRole("combobox", { name: /Kata daemon: docs/ })).toBeVisible();
      await dialog.getByRole("searchbox", { name: "Search Kata issues" }).fill("Create the Kata workspace");
      const result = dialog.getByRole("button", { name: /widgets#workspace-1 Create the Kata workspace/ });
      await expect(result).toBeVisible();
      await result.click();
      const created = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "POST" && url.pathname === "/api/v1/kata/workspaces";
      });
      await dialog.getByRole("button", { name: "Create or open workspace", exact: true }).click();
      const response = await created;
      expect(response.status(), await response.text()).toBe(202);
      const workspace = (await response.json()) as { id: string };
      await expect(page).toHaveURL(new RegExp(`/terminal/${workspace.id}$`));
      await expect
        .poll(
          async () => {
            const detail = await page.request.get(`${server.info.base_url}/api/v1/workspaces/${workspace.id}`);
            if (!detail.ok()) return `http-${detail.status()}`;
            return ((await detail.json()) as { status: string }).status;
          },
          { timeout: 60_000 },
        )
        .toBe("ready");
      await page.reload();
      await page.getByRole("button", { name: "Kata", exact: true }).click();
      await expect(page.getByLabel("Kata issue detail")).toBeVisible();
      await expect(page.getByText("Use the mapped widgets repository.")).toBeVisible();
    } finally {
      await server.stop();
      kataHome.restore();
      await backend.close();
    }
  });
});
