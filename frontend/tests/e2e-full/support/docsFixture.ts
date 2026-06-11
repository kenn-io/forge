import { execFileSync } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { expect, type Locator, type Page } from "@playwright/test";
import { startIsolatedE2EServer, startIsolatedE2EServerWithOptions, type IsolatedE2EServer } from "./e2eServer";

export class DocsPane {
  constructor(private readonly page: Page) {}

  newFileTrigger(): Locator {
    return this.page.getByRole("button", { name: "New file" });
  }

  fileActionsTrigger(): Locator {
    return this.page.getByRole("button", { name: "File actions" });
  }

  fileActionsMenu(): Locator {
    return this.page.getByRole("menu", { name: "File actions" });
  }

  treeRow(name: string): Locator {
    return this.page.getByRole("treeitem", { name });
  }

  async createFile(filename: string): Promise<void> {
    await this.newFileTrigger().click();
    const dialog = this.page.getByRole("dialog", { name: "New file" });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("Filename").fill(filename);
    await dialog.getByRole("button", { name: "Create" }).click();
    await expect(dialog).toBeHidden();
  }

  async createFileExpectingError(filename: string): Promise<string> {
    await this.newFileTrigger().click();
    const dialog = this.page.getByRole("dialog", { name: "New file" });
    await dialog.getByLabel("Filename").fill(filename);
    await dialog.getByRole("button", { name: "Create" }).click();
    const error = dialog.getByRole("alert");
    await expect(error).toBeVisible();
    const text = (await error.textContent()) ?? "";
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
    return text;
  }

  async renameCurrentFile(newName: string): Promise<void> {
    await this.fileActionsTrigger().click();
    await this.fileActionsMenu()
      .getByRole("menuitem", { name: /Rename/ })
      .click();
    const dialog = this.page.getByRole("dialog", { name: "Rename file" });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("New name").fill(newName);
    await dialog.getByRole("button", { name: "Rename" }).click();
    await expect(dialog).toBeHidden();
  }

  async deleteCurrentFile(): Promise<void> {
    await this.fileActionsTrigger().click();
    await this.fileActionsMenu()
      .getByRole("menuitem", { name: /Delete/ })
      .click();
    const dialog = this.page.getByRole("dialog", { name: "Delete file" });
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Delete" }).click();
    await expect(dialog).toBeHidden();
  }
}

export async function createDocsFixture(): Promise<string> {
  const dir = await mkdtemp(path.join(os.tmpdir(), "middleman-docs-e2e-"));
  await mkdir(path.join(dir, "Projects"), { recursive: true });
  await mkdir(path.join(dir, "Daily"), { recursive: true });
  await mkdir(path.join(dir, "assets"), { recursive: true });
  await writeFile(
    path.join(dir, "README.md"),
    [
      "# Welcome to Notes",
      "",
      "Read the [[Projects/roadmap]] before publishing.",
      "",
      "![logo](assets/logo.png)",
      "",
    ].join("\n"),
  );
  await writeFile(
    path.join(dir, "Projects", "roadmap.md"),
    ["# Roadmap", "", "## Architecture", "", "The docs workspace uses real files.", ""].join("\n"),
  );
  await writeFile(path.join(dir, "Daily", "2026-05-15.md"), "# Daily\n");
  await writeFile(path.join(dir, "Daily", "2026-05-14.md"), "# Previous Daily\n");
  await writeFile(path.join(dir, "inbox.md"), "# Inbox\n");
  await writeFile(
    path.join(dir, "assets", "logo.png"),
    Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=",
      "base64",
    ),
  );
  return dir;
}

export async function startDocsServer(
  page: Page,
  options: {
    folder?: { id?: string; name?: string; daemon?: string | undefined };
    // Spawn a dedicated process for tests that steer the server via
    // process env (e.g. KATA_HOME); pooled servers cannot inherit
    // env set after they were spawned.
    freshProcess?: boolean;
  } = {},
): Promise<IsolatedE2EServer> {
  const docsRoot = await createDocsFixture();
  const server = options.freshProcess
    ? await startIsolatedE2EServerWithOptions({ freshProcess: true })
    : await startIsolatedE2EServer();
  const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
    data: {
      id: options.folder?.id ?? "notes",
      name: options.folder?.name ?? "Notes",
      path: docsRoot,
      ...(options.folder?.daemon ? { daemon: options.folder.daemon } : {}),
    },
  });
  expect(res.status()).toBe(201);
  return server;
}

export type DocsPublishFixture = {
  server: IsolatedE2EServer;
  workDir: string;
  remoteDir: string;
  gitEnv: NodeJS.ProcessEnv;
  stop: () => Promise<void>;
};

export async function startDocsPublishServer(page: Page): Promise<DocsPublishFixture> {
  const root = await mkdtemp(path.join(os.tmpdir(), "middleman-docs-publish-e2e-"));
  const workDir = path.join(root, "work");
  const remoteDir = path.join(root, "remote.git");
  const homeDir = path.join(root, "home");
  await mkdir(workDir, { recursive: true });
  await mkdir(homeDir, { recursive: true });
  await mkdir(path.join(homeDir, ".config"), { recursive: true });

  const gitEnv = isolatedFixtureGitEnv(homeDir);
  runFixtureGit(workDir, gitEnv, "init", "-b", "main");
  runFixtureGit(workDir, gitEnv, "config", "user.name", "Middleman E2E");
  runFixtureGit(workDir, gitEnv, "config", "user.email", "middleman-e2e@example.invalid");
  runFixtureGit(workDir, gitEnv, "config", "commit.gpgsign", "false");
  // The middleman server performs the publish commit with its own environment,
  // not gitEnv, so pin hooks to the repo's own dir to ignore any global
  // core.hooksPath a developer or CI runner may have configured.
  runFixtureGit(workDir, gitEnv, "config", "core.hooksPath", ".git/hooks");
  await writeFile(path.join(workDir, "README.md"), "# Publish Fixture\n\nTracked page.\n");
  runFixtureGit(workDir, gitEnv, "add", "README.md");
  runFixtureGit(workDir, gitEnv, "commit", "-m", "docs: seed publish fixture");
  runFixtureGit(root, gitEnv, "init", "--bare", remoteDir);
  runFixtureGit(workDir, gitEnv, "remote", "add", "origin", remoteDir);
  runFixtureGit(workDir, gitEnv, "push", "-u", "origin", "main");
  await writeFile(path.join(workDir, "new.md"), "# New Publish Page\n\nReady to publish.\n");

  const server = await startIsolatedE2EServer();
  let stopped = false;
  const stop = async () => {
    if (stopped) return;
    stopped = true;
    try {
      await server.stop();
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  };

  try {
    const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
      data: {
        id: "publish",
        name: "Publish Fixture",
        path: workDir,
      },
    });
    expect(res.status()).toBe(201);
  } catch (error) {
    await stop();
    throw error;
  }

  return { server, workDir, remoteDir, gitEnv, stop };
}

export function docsPublishRemoteHead(fixture: DocsPublishFixture, ref = "main"): string {
  return execFileSync("git", ["--git-dir", fixture.remoteDir, "rev-parse", ref], {
    env: fixture.gitEnv,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

function isolatedFixtureGitEnv(homeDir: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    HOME: homeDir,
    XDG_CONFIG_HOME: path.join(homeDir, ".config"),
    GIT_CONFIG_NOSYSTEM: "1",
  };
  for (const key of [
    "EMAIL",
    "GIT_AUTHOR_EMAIL",
    "GIT_AUTHOR_NAME",
    "GIT_COMMITTER_EMAIL",
    "GIT_COMMITTER_NAME",
    "GIT_CONFIG",
    "GIT_CONFIG_GLOBAL",
    "GIT_DIR",
    "GIT_WORK_TREE",
  ]) {
    delete env[key];
  }
  return env;
}

function runFixtureGit(cwd: string, env: NodeJS.ProcessEnv, ...args: string[]): string {
  return execFileSync("git", args, {
    cwd,
    env,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}
