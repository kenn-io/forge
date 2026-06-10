import { describe, expect, test } from "vite-plus/test";
import type { TreeNode } from "../../api/docs/types";
import { createMockDocsBackend } from "./docsTestBackend";

describe("docs test backend", () => {
  test("listFolders returns Notes and Engineering fixtures", async () => {
    const api = createMockDocsBackend();
    const folders = await api.listFolders();
    expect(folders.map((v) => v.id)).toEqual(["notes", "engineering"]);
    expect(folders[0]).toEqual({ id: "notes", name: "Notes", path: "/mock/notes" });
  });

  test("tree returns a hierarchical structure of markdown files only", async () => {
    const api = createMockDocsBackend();
    const tree = await api.tree("notes");
    expect(tree.name).toBe("Notes");
    expect(tree.is_dir).toBe(true);

    const names = (tree.children ?? []).map((child) => child.name);
    expect(names).toEqual(expect.arrayContaining(["Daily", "Projects", "README.md", "inbox.md"]));
    expect(names).not.toContain("assets");

    const daily = (tree.children ?? []).find((c) => c.name === "Daily");
    expect(daily?.is_dir).toBe(true);
    expect((daily?.children ?? []).map((c) => c.name)).toEqual(["2026-05-14.md", "2026-05-15.md"]);

    const readme = (tree.children ?? []).find((c) => c.name === "README.md") as TreeNode;
    expect(readme.is_dir).toBe(false);
    expect(readme.rel_path).toBe("README.md");
    expect(readme.size).toBeGreaterThan(0);
  });

  test("readFile returns the markdown body", async () => {
    const api = createMockDocsBackend();
    const body = await api.readFile("notes", "README.md");
    expect(body).toContain("# Welcome to Notes");
    expect(body).toContain("[[Daily/2026-05-15]]");
  });

  test("writeFile then readFile round-trips", async () => {
    const api = createMockDocsBackend();
    await api.writeFile("notes", "README.md", "# Edited\n");
    expect(await api.readFile("notes", "README.md")).toBe("# Edited\n");
  });

  test("writeFile refuses non-markdown paths", async () => {
    const api = createMockDocsBackend();
    await expect(api.writeFile("notes", "assets/logo.png", "x")).rejects.toMatchObject({
      status: 415,
      code: "unsupported_extension",
    });
  });

  test("readFile on a missing path throws 404", async () => {
    const api = createMockDocsBackend();
    await expect(api.readFile("notes", "missing.md")).rejects.toMatchObject({
      status: 404,
      code: "not_found",
    });
  });

  test("unknown folder throws 404", async () => {
    const api = createMockDocsBackend();
    await expect(api.tree("does-not-exist")).rejects.toMatchObject({
      status: 404,
      code: "folder_not_found",
    });
  });

  test("search ranks exact, prefix, and substring filename matches", async () => {
    const api = createMockDocsBackend();
    const result = await api.search("notes", "reader");
    expect(result.query).toBe("reader");
    expect(result.hits.length).toBeGreaterThan(0);
    expect(result.hits[0]?.rel_path).toBe("Projects/reader.md");
    const last = result.hits[result.hits.length - 1]!;
    expect(result.hits[0]?.score).toBeGreaterThanOrEqual(last.score);
  });

  test("blobURL returns a data URL for binary assets", () => {
    const api = createMockDocsBackend();
    const url = api.blobURL("notes", "assets/logo.png");
    expect(url.startsWith("data:image/png;base64,")).toBe(true);
  });

  test("blobURL returns empty string for unknown or non-binary files", () => {
    const api = createMockDocsBackend();
    expect(api.blobURL("notes", "README.md")).toBe("");
    expect(api.blobURL("notes", "no-such-image.png")).toBe("");
  });

  test("gitStatus returns fixture entries for folders with git config", async () => {
    const api = createMockDocsBackend();
    const status = await api.gitStatus("notes");
    expect(status.is_repo).toBe(true);
    expect(status.entries).toEqual(
      expect.arrayContaining([
        { path: "Daily/2026-05-15.md", status: "modified" },
        { path: "Projects/reader.md", status: "modified" },
        { path: "inbox.md", status: "untracked" },
      ]),
    );
  });

  test("gitStatus reports is_repo=false for folders without git fixture", async () => {
    const api = createMockDocsBackend();
    const status = await api.gitStatus("engineering");
    expect(status.is_repo).toBe(false);
    expect(status.entries).toEqual([]);
  });

  test("addFolder appends a new folder and makes it visible to listFolders", async () => {
    const api = createMockDocsBackend();
    const added = await api.addFolder({ path: "/mock/research", id: "research", name: "Research" });
    expect(added).toEqual({ id: "research", name: "Research", path: "/mock/research" });
    const ids = (await api.listFolders()).map((v) => v.id);
    expect(ids).toContain("research");
  });

  test("addFolder derives id and name from path when omitted", async () => {
    const api = createMockDocsBackend();
    const added = await api.addFolder({ path: "/mock/Briefs" });
    expect(added.id).toBe("briefs");
    expect(added.name).toBe("Briefs");
  });

  test("addFolder rejects duplicate ids with status 409", async () => {
    const api = createMockDocsBackend();
    await expect(api.addFolder({ path: "/mock/notes", id: "notes" })).rejects.toMatchObject({
      status: 409,
      code: "duplicate_folder_id",
    });
  });

  test("removeFolder drops the entry from listFolders", async () => {
    const api = createMockDocsBackend();
    await api.removeFolder("engineering");
    const ids = (await api.listFolders()).map((v) => v.id);
    expect(ids).not.toContain("engineering");
  });

  test("removeFolder throws 404 for unknown ids", async () => {
    const api = createMockDocsBackend();
    await expect(api.removeFolder("ghost")).rejects.toMatchObject({ status: 404 });
  });

  test("renameFolder updates the display name", async () => {
    const api = createMockDocsBackend();
    const updated = await api.renameFolder("notes", "Personal Notes");
    expect(updated.name).toBe("Personal Notes");
    const found = (await api.listFolders()).find((v) => v.id === "notes");
    expect(found?.name).toBe("Personal Notes");
  });

  test("browseDirectories returns subfolders for the synthetic tree", async () => {
    const api = createMockDocsBackend();
    const result = await api.browseDirectories("/Users/mock");
    expect(result.path).toBe("/Users/mock");
    expect(result.parent).toBe("/Users");
    expect(result.entries.map((e) => e.name)).toEqual(["Documents", "Notes", "Projects", ".config"]);
    const hidden = result.entries.find((e) => e.name === ".config");
    expect(hidden?.hidden).toBe(true);
  });

  test("browseDirectories defaults to the synthetic home when no path given", async () => {
    const api = createMockDocsBackend();
    const result = await api.browseDirectories();
    expect(result.path).toBe("/Users/mock");
  });

  test("browseDirectories expands ~/ shorthand", async () => {
    const api = createMockDocsBackend();
    const result = await api.browseDirectories("~/Notes");
    expect(result.path).toBe("/Users/mock/Notes");
  });

  test("browseDirectories throws 404 for unknown paths", async () => {
    const api = createMockDocsBackend();
    await expect(api.browseDirectories("/does/not/exist")).rejects.toMatchObject({
      status: 404,
      code: "not_found",
    });
  });
});

describe("docs test backend global search", () => {
  test("returns hits across fixture folders", async () => {
    const api = createMockDocsBackend();
    const res = await api.searchAll("reader", 25);
    expect(res.hits.length).toBeGreaterThan(0);
    const folders = new Set(res.hits.map((h) => h.folder));
    expect(folders.has("notes")).toBe(true);
  });

  test("empty query returns no hits without throwing", async () => {
    const api = createMockDocsBackend();
    const res = await api.searchAll("", 25);
    expect(res.query).toBe("");
    expect(Array.isArray(res.hits)).toBe(true);
    expect(res.hits.length).toBe(0);
  });

  test("query with no fixture match returns empty hits", async () => {
    const api = createMockDocsBackend();
    const res = await api.searchAll("zzzz-nothing-matches-this", 25);
    expect(res.hits.length).toBe(0);
    expect(res.truncated).toBe(false);
  });

  test("filename hit outranks body hit globally", async () => {
    const api = createMockDocsBackend();
    const res = await api.searchAll("reader", 25);
    const firstFilenameIdx = res.hits.findIndex((h) => h.hit_type === "filename");
    const firstBodyIdx = res.hits.findIndex((h) => h.hit_type === "body");
    if (firstFilenameIdx >= 0 && firstBodyIdx >= 0) {
      expect(firstFilenameIdx).toBeLessThan(firstBodyIdx);
    }
  });

  test("respects limit and truncated flag", async () => {
    const api = createMockDocsBackend();
    const res = await api.searchAll("the", 2);
    expect(res.hits.length).toBeLessThanOrEqual(2);
    if (res.hits.length === 2) {
      expect(res.truncated).toBe(true);
    }
  });
});

describe("docs test backend git changes and publish", () => {
  test("gitChanges returns is_repo=false when fixture has no git state", async () => {
    const api = createMockDocsBackend({
      folders: [{ meta: { id: "x", name: "X", path: "/x" }, files: { "a.md": "a" } }],
    });
    const res = await api.gitChanges("x");
    expect(res.is_repo).toBe(false);
    expect(res.changes).toEqual([]);
  });

  test("gitChanges returns the fixture's publish set when git state is configured", async () => {
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "new.md": "n", "code.go": "g" },
          git: { "new.md": "untracked" },
        },
      ],
    });
    const res = await api.gitChanges("x");
    expect(res.is_repo).toBe(true);
    expect(res.branch).toBe("main");
    expect(res.upstream).toBe("origin/main");
    expect(res.changes).toEqual([{ path: "new.md", status: "untracked" }]);
    expect(res.suggested_message).toContain("- new.md");
  });

  test("gitPublish moves the publish set out of the change list and returns a commit", async () => {
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "new.md": "n" },
          git: { "new.md": "untracked" },
        },
      ],
    });
    const pub = await api.gitPublish("x", "docs: new\n\n- new.md\n");
    expect(pub.pushed).toBe(true);
    expect(pub.commit.length).toBe(40);
    expect(pub.files).toEqual([{ path: "new.md", status: "untracked" }]);
    const next = await api.gitChanges("x");
    expect(next.changes).toEqual([]);
  });

  test("gitPublish rejects an empty message", async () => {
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "new.md": "n" },
          git: { "new.md": "untracked" },
        },
      ],
    });
    await expect(api.gitPublish("x", "   ")).rejects.toMatchObject({
      status: 400,
      code: "empty_message",
    });
  });

  test("gitPublish rejects when there are no markdown changes", async () => {
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "a.md": "a" },
          git: {},
        },
      ],
    });
    await expect(api.gitPublish("x", "docs: x")).rejects.toMatchObject({
      status: 400,
      code: "no_markdown_changes",
    });
  });
});
