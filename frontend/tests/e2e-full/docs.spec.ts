import { expect, test, type Locator, type Page } from "@playwright/test";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
  DocsPane,
  createDocsFixture,
  docsPublishRemoteHead,
  startDocsPublishServer,
  startDocsServer,
} from "./support/docsFixture";
import { startIsolatedE2EServer } from "./support/e2eServer";

async function createEngineeringDocsFixture(): Promise<string> {
  const dir = await mkdtemp(path.join(os.tmpdir(), "middleman-engineering-docs-e2e-"));
  await mkdir(dir, { recursive: true });
  await writeFile(
    path.join(dir, "index.md"),
    ["# Engineering Wiki", "", "## Systems", "", "This folder lands on index.md.", ""].join("\n"),
  );
  return dir;
}

// A README that lands tall enough that scrolling to its "## Architecture"
// heading (id "architecture") moves the scroll container measurably and
// pushes the h1 out of view. Both anchor fixtures share the heading id so
// a stale anchor would scroll the second folder's landing doc.
async function createTallAnchorDocsFixture(h1: string): Promise<string> {
  const dir = await mkdtemp(path.join(os.tmpdir(), "middleman-anchor-docs-e2e-"));
  await mkdir(dir, { recursive: true });
  // Each paragraph needs a blank line after it or markdown folds them
  // into one short block that never overflows the scroll container.
  const filler = Array.from({ length: 80 }, (_, i) => `Filler paragraph ${i + 1}.`).flatMap((p) => [p, ""]);
  await writeFile(
    path.join(dir, "README.md"),
    [`# ${h1}`, "", ...filler, "## Architecture", "", "Architecture section body.", ""].join("\n"),
  );
  return dir;
}

async function docScrollTop(page: Page): Promise<number> {
  return page.locator(".doc-scroll").evaluate((el) => el.scrollTop);
}

test.describe("docs workspace", () => {
  test("opens the configured folder, navigates tree rows, outline entries, wikilinks, and blobs", async ({ page }) => {
    const server = await startDocsServer(page);
    try {
      await page.goto(`${server.info.base_url}/docs`);

      const folderButton = page.getByRole("button", { name: "Switch folder" });
      await expect(folderButton).toContainText("Notes");
      await expect(page).toHaveURL(/folder=notes/);
      await expect(page).toHaveURL(/doc=README\.md/);
      await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();

      await page.getByRole("treeitem", { name: "Projects" }).click();
      await page.getByRole("treeitem", { name: "roadmap.md" }).click();
      await expect(page).toHaveURL(/doc=Projects(%2F|\/)roadmap\.md/);
      await expect(page.getByRole("heading", { name: "Roadmap", level: 1 })).toBeVisible();

      const outline = page.getByRole("complementary", { name: "Document outline" });
      await expect(outline.getByRole("button", { name: "Roadmap" })).toBeVisible();
      await expect(outline.getByRole("button", { name: "Architecture" })).toBeVisible();

      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);
      const wikilink = page.locator('a[data-wikilink="resolved"][data-doc-link="Projects/roadmap.md"]').first();
      await wikilink.click();
      await expect(page.getByRole("heading", { name: "Roadmap", level: 1 })).toBeVisible();

      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);
      const logo = page.locator('img[alt="logo"]');
      await expect(logo).toBeVisible();
      await expect(logo).toHaveAttribute("src", /\/api\/v1\/docs\/folders\/notes\/blob\?path=assets%2Flogo\.png/);
    } finally {
      await server.stop();
    }
  });

  test("filters the file tree by filename search", async ({ page }) => {
    const server = await startDocsServer(page);
    try {
      await page.goto(`${server.info.base_url}/docs`);
      await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();

      await page.getByRole("textbox", { name: /Search/ }).fill("road");

      await expect(page.getByRole("treeitem", { name: "roadmap.md" })).toBeVisible();
      await expect(page.getByRole("treeitem", { name: "README.md" })).toHaveCount(0);
      await expect(page.getByRole("treeitem", { name: "inbox.md" })).toHaveCount(0);
    } finally {
      await server.stop();
    }
  });

  test("switching folders through the chip loads the selected folder landing doc", async ({ page }) => {
    const notesRoot = await createDocsFixture();
    const engineeringRoot = await createEngineeringDocsFixture();
    const server = await startIsolatedE2EServer();
    try {
      const notes = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
        data: {
          id: "notes",
          name: "Notes",
          path: notesRoot,
        },
      });
      expect(notes.status()).toBe(201);
      const engineering = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
        data: {
          id: "engineering",
          name: "Engineering",
          path: engineeringRoot,
        },
      });
      expect(engineering.status()).toBe(201);

      await page.goto(`${server.info.base_url}/docs`);
      await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();

      await page.getByRole("button", { name: "Switch folder" }).click();
      await page.getByRole("option", { name: /Engineering/ }).click();

      await expect(page).toHaveURL(/folder=engineering/);
      await expect(page).toHaveURL(/doc=index\.md/);
      await expect(page.getByRole("heading", { name: "Engineering Wiki", level: 1 })).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("a consumed URL-hash anchor does not scroll the next folder's landing doc", async ({ page }) => {
    const alphaRoot = await createTallAnchorDocsFixture("Alpha Home");
    const betaRoot = await createTallAnchorDocsFixture("Beta Home");
    const server = await startIsolatedE2EServer();
    try {
      for (const [id, name, root] of [
        ["alpha", "Alpha", alphaRoot],
        ["beta", "Beta", betaRoot],
      ] as const) {
        const res = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
          data: { id, name, path: root },
        });
        expect(res.status()).toBe(201);
      }

      // Deep link straight to the Architecture anchor in alpha; the doc
      // must scroll down to it (scrollTop > 0, h1 pushed out of view).
      await page.goto(`${server.info.base_url}/docs?folder=alpha&doc=README.md#architecture`);
      await expect(page.getByRole("heading", { name: "Architecture", level: 2 })).toBeVisible();
      await expect.poll(() => docScrollTop(page)).toBeGreaterThan(0);
      await expect(page.getByRole("heading", { name: "Alpha Home", level: 1 })).not.toBeInViewport();

      // Switch folders via the chip — landing auto-open loads beta/README
      // without an explicit anchor. The consumed anchor must not be reused:
      // beta opens at the top with its h1 in view.
      await page.getByRole("button", { name: "Switch folder" }).click();
      await page.getByRole("option", { name: /Beta/ }).click();
      await expect(page).toHaveURL(/folder=beta/);
      await expect(page.getByRole("heading", { name: "Beta Home", level: 1 })).toBeVisible();
      // Let any stale-anchor scroll microtask fire before asserting it didn't.
      await page.waitForTimeout(300);
      expect(await docScrollTop(page)).toBe(0);
      await expect(page.getByRole("heading", { name: "Beta Home", level: 1 })).toBeInViewport();
    } finally {
      await server.stop();
    }
  });

  test("creates, rejects duplicate, renames, and deletes markdown files", async ({ page }) => {
    const server = await startDocsServer(page);
    try {
      await page.goto(`${server.info.base_url}/docs`);
      await expect(page).toHaveURL(/doc=README\.md/);
      const docs = new DocsPane(page);

      await docs.createFile("test-new.md");
      await expect(page).toHaveURL(/doc=test-new\.md/);
      await expect(docs.treeRow("test-new.md")).toBeVisible();

      const message = await docs.createFileExpectingError("README.md");
      expect(message.toLowerCase()).toContain("already exists");

      await docs.renameCurrentFile("Welcome.md");
      await expect(page).toHaveURL(/doc=Welcome\.md/);
      await expect(docs.treeRow("Welcome.md")).toBeVisible();
      await expect(page.getByRole("treeitem", { name: "test-new.md" })).toHaveCount(0);

      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=inbox.md`);
      await expect(page).toHaveURL(/doc=inbox\.md/);
      await docs.deleteCurrentFile();
      await expect(page).not.toHaveURL(/doc=inbox\.md/);
      await expect(page.getByRole("treeitem", { name: "inbox.md" })).toHaveCount(0);
    } finally {
      await server.stop();
    }
  });

  test("saves edited markdown and renders the updated document", async ({ page }) => {
    const server = await startDocsServer(page);
    try {
      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=README.md`);
      await expect(page.getByRole("heading", { name: "Welcome to Notes" })).toBeVisible();

      const editor = await openDocsEditor(page);
      await replaceEditorText(page, editor, "# Updated Notes\n\nSaved through the docs editor.\n");

      const saveResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "PUT" &&
          response.url().includes("/api/v1/docs/folders/notes/file") &&
          response.ok(),
      );
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await saveResponse;

      await expect(page.getByRole("heading", { name: "Updated Notes", level: 1 })).toBeVisible();
      await expect(page.getByText("Saved through the docs editor.")).toBeVisible();

      await page.reload();
      await expect(page.getByRole("heading", { name: "Updated Notes", level: 1 })).toBeVisible();
      await expect(page.getByText("Saved through the docs editor.")).toBeVisible();
    } finally {
      await server.stop();
    }
  });

  test("publishes markdown changes from the docs UI", async ({ page }) => {
    const fixture = await startDocsPublishServer(page);
    try {
      await page.goto(`${fixture.server.info.base_url}/docs?folder=publish&doc=README.md`);
      await expect(page.getByRole("heading", { name: "Publish Fixture", level: 1 })).toBeVisible();

      const publishButton = page.getByRole("button", { name: "Publish to git" });
      await expect(publishButton).toBeEnabled();
      await publishButton.click();

      const dialog = page.getByRole("dialog", { name: "Commit & Push Docs" });
      await expect(dialog).toBeVisible();
      const changeRow = dialog.locator(".file-row").filter({ hasText: "new.md" });
      await expect(changeRow).toBeVisible();
      await expect(changeRow).toContainText("untracked");
      await expect(dialog.getByRole("textbox", { name: "Commit message" })).toHaveValue(
        /docs: update new\.md[\s\S]*- new\.md/,
      );

      const publishResponse = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          response.url().includes("/api/v1/docs/folders/publish/git/publish") &&
          response.ok(),
      );
      await dialog.getByRole("button", { name: "Commit & Push" }).click();
      const body = (await (await publishResponse).json()) as {
        commit: string;
        files: Array<{ path: string; status: string }>;
        short_commit: string;
      };

      expect(body.files).toEqual([expect.objectContaining({ path: "new.md", status: "untracked" })]);
      await expect(dialog).toBeHidden();
      await expect(
        page.getByRole("status").filter({
          hasText: new RegExp(`Committed and pushed 1 file as ${body.short_commit}\\.`),
        }),
      ).toBeVisible();
      expect(docsPublishRemoteHead(fixture)).toBe(body.commit);

      await publishButton.click();
      const cleanDialog = page.getByRole("dialog", { name: "Commit & Push Docs" });
      await expect(cleanDialog.getByText("No changed Markdown files to publish.")).toBeVisible();
      await expect(cleanDialog.getByRole("button", { name: "Commit & Push" })).toBeDisabled();
    } finally {
      await fixture.stop();
    }
  });

  test("keeps the publish action and explains the block for unsafe git attributes", async ({ page }) => {
    const fixture = await startDocsPublishServer(page);
    try {
      await writeFile(path.join(fixture.workDir, ".gitattributes"), "*.md filter=evil\n");

      // Anchor the test on the real wire behavior: the git status request
      // must come back as the 400 unsafeGitConfig envelope, not is_repo=false.
      const gitStatus = page.waitForResponse(
        (response) =>
          response.url().includes("/api/v1/docs/folders/publish/git") &&
          !response.url().includes("/git/changes") &&
          response.status() === 400,
      );
      await page.goto(`${fixture.server.info.base_url}/docs?folder=publish&doc=README.md`);
      await expect(page.getByRole("heading", { name: "Publish Fixture", level: 1 })).toBeVisible();
      await gitStatus;

      const publishButton = page.getByRole("button", { name: "Publish to git" });
      await expect(publishButton).toBeEnabled();
      await publishButton.click();

      const dialog = page.getByRole("dialog", { name: "Commit & Push Docs" });
      await expect(dialog).toBeVisible();
      await expect(dialog.getByText(/command-bearing config or attributes/i)).toBeVisible();
      await expect(dialog.getByRole("button", { name: "Commit & Push" })).toHaveCount(0);
    } finally {
      await fixture.stop();
    }
  });

  test("strips unsafe raw HTML from real markdown files", async ({ page }) => {
    const docsRoot = await createDocsFixture();
    await writeFile(
      path.join(docsRoot, "unsafe.md"),
      [
        "# Unsafe Fixture",
        "",
        'Before <img src=x onerror="alert(1)"><script>alert(1)</script> after.',
        "",
        "![safe](assets/logo.png)",
        "",
      ].join("\n"),
    );
    const server = await startIsolatedE2EServer();
    try {
      const folder = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
        data: {
          id: "notes",
          name: "Notes",
          path: docsRoot,
        },
      });
      expect(folder.status()).toBe(201);

      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=unsafe.md`);
      await expect(page.getByRole("heading", { name: "Unsafe Fixture", level: 1 })).toBeVisible();

      const rendered = page.locator(".doc-markdown");
      await expect(rendered).toContainText("Before");
      await expect(rendered).toContainText("after.");
      await expect(rendered.locator("script")).toHaveCount(0);
      await expect(rendered.locator('img[onerror], img[src="x"]')).toHaveCount(0);
      await expect(rendered.getByRole("img", { name: "safe" })).toHaveAttribute(
        "src",
        /\/api\/v1\/docs\/folders\/notes\/blob\?path=assets%2Flogo\.png/,
      );
    } finally {
      await server.stop();
    }
  });

  test("strips protocol-relative links and images from real markdown files", async ({ page }) => {
    const docsRoot = await createDocsFixture();
    await writeFile(
      path.join(docsRoot, "protorel.md"),
      [
        "# Protorel Fixture",
        "",
        "[md link](//evil.example/x) and ![md img](//evil.example/x.png) inline.",
        "",
        '<a href="//evil.example/raw">raw slashes</a>',
        '<a href="\\\\evil.example/bs">raw backslashes</a>',
        '<a href="/\\evil.example/mix">raw mixed</a>',
        '<img src="//evil.example/raw.png" alt="raw img">',
        "",
        "[ok root](/docs/readme)",
        "",
      ].join("\n"),
    );
    const server = await startIsolatedE2EServer();
    try {
      const folder = await page.request.post(`${server.info.base_url}/api/v1/docs/folders`, {
        data: {
          id: "notes",
          name: "Notes",
          path: docsRoot,
        },
      });
      expect(folder.status()).toBe(201);

      await page.goto(`${server.info.base_url}/docs?folder=notes&doc=protorel.md`);
      await expect(page.getByRole("heading", { name: "Protorel Fixture", level: 1 })).toBeVisible();

      const rendered = page.locator(".doc-markdown");
      // No rendered href/src may point at the protocol-relative host.
      await expect(rendered.locator('a[href*="evil.example"], img[src*="evil.example"]')).toHaveCount(0);
      // The single-slash root link must survive unchanged.
      await expect(rendered.locator('a[href="/docs/readme"]')).toHaveCount(1);
    } finally {
      await server.stop();
    }
  });

  test("preserves doc-only deep links while auto-selecting the first folder", async ({ page }) => {
    const server = await startDocsServer(page);
    try {
      await page.goto(`${server.info.base_url}/docs?doc=Projects/roadmap.md`);

      await expect(page).toHaveURL(/folder=notes/);
      await expect(page).toHaveURL(/doc=Projects(%2F|\/)roadmap\.md/);
      await expect(page.getByRole("heading", { name: "Roadmap", level: 1 })).toBeVisible();
    } finally {
      await server.stop();
    }
  });
});

async function openDocsEditor(page: Page): Promise<Locator> {
  const editButton = page.getByRole("button", { name: "Edit", exact: true });
  await expect(editButton).toBeEnabled();
  await editButton.click();
  const editor = page.locator(".cm-editor .cm-content");
  await expect(editor).toBeVisible();
  await editor.click();
  return editor;
}

async function replaceEditorText(page: Page, editor: Locator, value: string): Promise<void> {
  await editor.focus();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.press("Delete");
  await page.keyboard.insertText(value);
}
