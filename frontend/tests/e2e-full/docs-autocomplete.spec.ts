import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { once } from "node:events";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { expect, type Locator, type Page, test } from "@playwright/test";
import { startDocsServer } from "./support/docsFixture";

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
};

type TaskBackend = {
  url: string;
  seenPaths: string[];
  close: () => Promise<void>;
};

type TaskBackendOptions = {
  waitForInstance?: Promise<void> | undefined;
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
  input: Omit<IssueRow, "qualified_id" | "status" | "labels"> & Partial<Pick<IssueRow, "status" | "labels">>,
): IssueRow {
  return {
    ...input,
    qualified_id: `${input.project_name}#${input.short_id}`,
    status: input.status ?? "open",
    labels: input.labels ?? [],
  };
}

async function startTaskBackend(
  issues: IssueRow[] = autocompleteIssues,
  options: TaskBackendOptions = {},
): Promise<TaskBackend> {
  const seenPaths: string[] = [];
  const server = createServer((req, res) => {
    void handleTaskRequest(req, res, issues, seenPaths, options).catch(() => {
      if (!res.headersSent) writeJSON(res, 500, { error: "e2e_backend_error" });
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const addr = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${addr.port}`,
    seenPaths,
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
  options: TaskBackendOptions,
): Promise<void> {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  seenPaths.push(`${req.method ?? "GET"} ${url.pathname}${url.search}`);
  if (url.pathname === "/api/v1/instance") {
    await options.waitForInstance;
    writeJSON(res, 200, { instance_uid: "docs-autocomplete-e2e", version: "0.0.0-e2e", schema_version: 1 });
    return;
  }
  if (url.pathname === "/api/v1/projects") {
    writeJSON(res, 200, {
      projects: projectsFromIssues(issues),
      fetched_at: now,
    });
    return;
  }
  if (url.pathname === "/api/v1/issues") {
    const status = url.searchParams.get("status") ?? "open";
    writeJSON(res, 200, {
      issues: issues.filter((issue) => status === "all" || issue.status === status),
      fetched_at: now,
    });
    return;
  }
  writeJSON(res, 404, { error: "not_found" });
}

function projectsFromIssues(issues: IssueRow[]) {
  const projects = new Map<number, { id: number; uid: string; name: string; metadata: {}; open_count: number }>();
  for (const issue of issues) {
    const existing = projects.get(issue.project_id);
    if (existing) {
      if (issue.status === "open") existing.open_count += 1;
      continue;
    }
    projects.set(issue.project_id, {
      id: issue.project_id,
      uid: issue.project_uid,
      name: issue.project_name,
      metadata: {},
      open_count: issue.status === "open" ? 1 : 0,
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
  const home = await mkdtemp(path.join(os.tmpdir(), "middleman-docs-kata-e2e-"));
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
    const server = await startDocsServer(page, { freshProcess: true });
    try {
      const editor = await openDocsEditor(page, server.info.base_url);
      await clearEditor(page, editor);

      await page.keyboard.type("ref #re");
      const tooltip = autocompleteTooltip(page);
      await expect(tooltip).toBeVisible();
      const rent = autocompleteOption(page, "#rent");
      await expect(rent).toBeVisible();

      await rent.click();

      await expect(editor).toContainText("ref #rent");
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
      await expect.poll(() => work.seenPaths).toContain("GET /api/v1/issues?status=open");
      expect(home.seenPaths).not.toContain("GET /api/v1/issues?status=open");
    } finally {
      await server.stop();
      kataHome.restore();
      await home.close();
      await work.close();
    }
  });

  test("stale folder daemon binding warns and falls back to the active daemon", async ({ page }) => {
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
      await expect(warning).toContainText("active daemon");

      await clearEditor(page, editor);
      await page.keyboard.type("see #fallback");

      const tooltip = autocompleteTooltip(page);
      await expect(tooltip).toBeVisible();
      await expect(tooltip).toContainText("Active daemon fallback");
      await expect.poll(() => backend.seenPaths).toContain("GET /api/v1/issues?status=open");
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
      await expect.poll(() => backend.seenPaths).toContain("GET /api/v1/instance");
      const warning = page.locator(".folder-daemon-warning");
      await expect(warning).toHaveCount(0);

      instanceGate.resolve();

      await rosterResponse;
      await expect(warning).toHaveCount(0);
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
    const server = await startDocsServer(page, { freshProcess: true });
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
});
