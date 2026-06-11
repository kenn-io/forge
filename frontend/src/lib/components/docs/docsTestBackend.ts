import type { DocsAPI } from "../../api/docs/api";
import type {
  AddFolderInput,
  BodySnippet,
  BrowseEntry,
  BrowseResponse,
  CrossFolderSearchHit,
  CrossFolderSearchResponse,
  DocsAPIError,
  GitChangesResponse,
  GitPublishChange,
  GitPublishChangeStatus,
  GitPublishResponse,
  GitStatusEntry,
  GitStatusResponse,
  SearchHit,
  SearchResponse,
  TreeNode,
  Folder,
} from "../../api/docs/types";

/**
 * In-memory mock of the docs API. Mirrors the Go server's
 * /api/docs/* surface so the Svelte UI can run without a live backend
 * during tests or offline dev. Fixtures intentionally include the
 * Obsidian-shaped features Reader v0 needs to exercise (frontmatter,
 * wikilinks, embedded images, nested folders).
 */

interface FolderFixture {
  meta: Folder;
  // Path → file body. Strings are utf-8 text (markdown). Uint8Array values
  // are binary blobs surfaced via blobURL.
  files: Record<string, string | Uint8Array>;
  // Optional git status mocked per path. When omitted (or empty) the
  // gitStatus endpoint reports is_repo=false.
  git?: Record<string, GitStatusEntry["status"]> | undefined;
  // Publish-side state. When omitted, defaults are:
  //   isRepo: any `git` entries → true, else false
  //   branch: "main", upstream: "origin/main"
  gitRepo?:
    | {
        branch?: string;
        upstream?: string; // "" disables upstream → no_upstream error
      }
    | undefined;
}

// 1x1 transparent PNG, used so blobURL has something real to point at when
// fixtures want to embed an image. Encoded once at module load.
const PIXEL_PNG = new Uint8Array([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00,
  0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00, 0x0d, 0x49,
  0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
  0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
]);

const fixtures: FolderFixture[] = [
  {
    meta: { id: "notes", name: "Notes", path: "/mock/notes" },
    files: {
      "README.md":
        "---\ntitle: Notes\n---\n\n# Welcome to Notes\n\nThis is your personal folder. " +
        "Try [[Daily/2026-05-15]] or browse [[Projects/reader]].\n",
      "Daily/2026-05-15.md": "# 2026-05-15\n\n- [[Projects/reader]] kickoff\n- Met with team\n",
      "Daily/2026-05-14.md": "# 2026-05-14\n\nInitial planning for [[Projects/obsidian-replacement]].\n",
      "Projects/reader.md":
        "# Reader\n\nUnified task + docs interface.\n\n## Architecture\n\n" +
        "- Go server: `/api/docs/*` endpoints\n- Svelte frontend with CodeMirror 6\n\n" +
        "![logo](../assets/logo.png)\n",
      "Projects/obsidian-replacement.md":
        "# Obsidian Replacement\n\nKey requirements:\n- Treeview navigation\n" +
        "- Filename search\n- Markdown viewer/editor\n",
      "inbox.md": "# Inbox\n\nQuick capture lands here before filing into the tree.\n",
      "assets/logo.png": PIXEL_PNG,
    },
    git: {
      "Daily/2026-05-15.md": "modified",
      "Projects/reader.md": "modified",
      "inbox.md": "untracked",
    },
  },
  {
    meta: { id: "engineering", name: "Engineering", path: "/mock/engineering" },
    files: {
      "index.md": "# Engineering Wiki\n\nStart here. See [[architecture/overview]] for the system map.\n",
      "architecture/overview.md": "# Architecture Overview\n\nSystem map and the design decisions behind it.\n",
      "architecture/kata-daemon.md": "# Kata Daemon\n\nGo-based local daemon for headless task tracking.\n",
      "decisions/2025-Q4-typescript.md":
        "# 2025-Q4: Adopt TypeScript\n\nStandardize on TypeScript for new frontends.\n",
    },
  },
];

export interface MockDocsBackendOptions {
  folders?: FolderFixture[];
}

export function createMockDocsBackend(options: MockDocsBackendOptions = {}): DocsAPI {
  const sourceFolders = options.folders ?? fixtures;
  const state = sourceFolders.map((folder) => ({
    meta: { ...folder.meta },
    files: { ...folder.files },
    git: folder.git ? { ...folder.git } : undefined,
    gitRepo: folder.gitRepo ? { ...folder.gitRepo } : undefined,
  }));
  const repoState = sourceFolders.map((folder) => ({
    branch: folder.gitRepo?.branch ?? "main",
    upstream: folder.gitRepo?.upstream ?? "origin/main",
    isRepo: folder.git !== undefined || folder.gitRepo !== undefined,
  }));

  function findFolder(id: string): FolderFixture {
    const folder = state.find((v) => v.meta.id === id);
    if (!folder) throw makeError(404, "folder_not_found", `folder not found: ${id}`);
    return folder;
  }

  return {
    async listFolders() {
      return state.map((v) => ({ ...v.meta }));
    },
    async addFolder(input: AddFolderInput) {
      if (!input.path) {
        throw makeError(400, "invalid_path", "path is required");
      }
      const path = input.path;
      const name = input.name && input.name !== "" ? input.name : basename(path);
      // Mirror Go's docs.DeriveFolderID when the client omits id:
      // sanitize the basename and pick a unique suffix on collision so
      // mock dev/tests produce ids the real server would also accept.
      const id =
        input.id && input.id !== ""
          ? input.id
          : deriveMockFolderID(
              path,
              state.map((v) => v.meta.id),
            );
      if (state.some((v) => v.meta.id === id)) {
        throw makeError(409, "duplicate_folder_id", `folder id already exists: ${id}`);
      }
      const meta: Folder = { id, name, path, ...(input.daemon ? { daemon: input.daemon } : {}) };
      state.push({ meta, files: {}, git: undefined, gitRepo: undefined });
      repoState.push({ branch: "main", upstream: "origin/main", isRepo: false });
      return { ...meta };
    },
    async removeFolder(id) {
      const idx = state.findIndex((v) => v.meta.id === id);
      if (idx < 0) {
        throw makeError(404, "folder_not_found", `folder not found: ${id}`);
      }
      state.splice(idx, 1);
      repoState.splice(idx, 1);
    },
    async renameFolder(id, name) {
      const folder = state.find((v) => v.meta.id === id);
      if (!folder) {
        throw makeError(404, "folder_not_found", `folder not found: ${id}`);
      }
      if (!name) {
        throw makeError(400, "missing_name", "name is required");
      }
      folder.meta = { ...folder.meta, name };
      return { ...folder.meta };
    },
    async browseDirectories(path) {
      return mockBrowse(path);
    },
    async tree(folderID) {
      const folder = findFolder(folderID);
      return buildTree(folder);
    },
    async readFile(folderID, relPath) {
      const folder = findFolder(folderID);
      const content = folder.files[relPath];
      if (content === undefined) {
        throw makeError(404, "not_found", `file not found: ${relPath}`);
      }
      if (typeof content !== "string") {
        throw makeError(415, "unsupported_extension", `not text: ${relPath}`);
      }
      return content;
    },
    async writeFile(folderID, relPath, content) {
      const folder = findFolder(folderID);
      assertWritablePath(folder, relPath);
      folder.files[relPath] = content;
    },
    async createFile(folderID, relPath, content = "") {
      const folder = findFolder(folderID);
      assertWritablePath(folder, relPath);
      if (folder.files[relPath] !== undefined) {
        throw makeError(409, "already_exists", `file already exists: ${relPath}`);
      }
      folder.files[relPath] = content;
    },
    async deleteFile(folderID, relPath) {
      const folder = findFolder(folderID);
      if (!/\.(md|markdown)$/i.test(relPath)) {
        throw makeError(415, "unsupported_extension", `only .md/.markdown can be deleted: ${relPath}`);
      }
      if (folder.files[relPath] === undefined) {
        throw makeError(404, "not_found", `file not found: ${relPath}`);
      }
      delete folder.files[relPath];
      if (folder.git) delete folder.git[relPath];
    },
    async renameFile(folderID, fromPath, toPath) {
      const folder = findFolder(folderID);
      assertWritablePath(folder, toPath);
      if (!/\.(md|markdown)$/i.test(fromPath)) {
        throw makeError(415, "unsupported_extension", `only .md/.markdown can be renamed: ${fromPath}`);
      }
      const content = folder.files[fromPath];
      if (content === undefined) {
        throw makeError(404, "not_found", `file not found: ${fromPath}`);
      }
      if (fromPath === toPath) return;
      if (folder.files[toPath] !== undefined) {
        throw makeError(409, "already_exists", `file already exists: ${toPath}`);
      }
      folder.files[toPath] = content;
      delete folder.files[fromPath];
    },
    async search(folderID, query, limit = 25) {
      const folder = findFolder(folderID);
      return runSearch(folder, query, normalizeSearchLimit(limit));
    },
    async searchAll(query, limit = 25) {
      return runSearchAll(state, query, normalizeSearchLimit(limit));
    },
    async gitStatus(folderID): Promise<GitStatusResponse> {
      const idx = state.findIndex((v) => v.meta.id === folderID);
      if (idx < 0) throw makeError(404, "folder_not_found", `folder not found: ${folderID}`);
      const repo = repoState[idx]!;
      const folder = state[idx]!;
      if (!repo.isRepo) {
        return { is_repo: false, entries: [] };
      }
      // Real server returns is_repo=true with empty entries for a
      // clean repo. Honor that even when the git decoration map is
      // empty (e.g. after a mock publish drains it).
      const entries: GitStatusEntry[] = Object.entries(folder.git ?? {}).map(([path, status]) => ({ path, status }));
      return { is_repo: true, entries };
    },
    async gitChanges(folderID): Promise<GitChangesResponse> {
      const idx = state.findIndex((v) => v.meta.id === folderID);
      if (idx < 0) throw makeError(404, "folder_not_found", `folder not found: ${folderID}`);
      const repo = repoState[idx]!;
      if (!repo.isRepo) {
        return { is_repo: false, changes: [], ignored_non_markdown_count: 0 };
      }
      const folder = state[idx]!;
      const changes: GitPublishChange[] = [];
      let nonMd = 0;
      for (const [path, status] of Object.entries(folder.git ?? {})) {
        if (status === "ignored") continue;
        if (!isMarkdownPathMock(path)) {
          nonMd++;
          continue;
        }
        changes.push({ path, status: status as GitPublishChangeStatus });
      }
      const suggested = changes.length === 0 ? "" : suggestedMessageMock(changes);
      const out: GitChangesResponse = {
        is_repo: true,
        branch: repo.branch,
        changes,
        ignored_non_markdown_count: nonMd,
      };
      if (repo.upstream) out.upstream = repo.upstream;
      if (suggested) out.suggested_message = suggested;
      return out;
    },
    async gitPublish(folderID, message): Promise<GitPublishResponse> {
      const idx = state.findIndex((v) => v.meta.id === folderID);
      if (idx < 0) throw makeError(404, "folder_not_found", `folder not found: ${folderID}`);
      const repo = repoState[idx]!;
      if (!repo.isRepo) {
        throw makeError(400, "not_a_git_repo", "folder is not a git repository");
      }
      if (!message || !message.trim()) {
        throw makeError(400, "empty_message", "commit message must not be empty");
      }
      const folder = state[idx]!;
      const publishable: GitPublishChange[] = [];
      for (const [path, status] of Object.entries(folder.git ?? {})) {
        if (status === "ignored") continue;
        if (!isMarkdownPathMock(path)) continue;
        publishable.push({ path, status: status as GitPublishChangeStatus });
      }
      if (publishable.length === 0) {
        throw makeError(400, "no_markdown_changes", "no markdown changes to publish");
      }
      if (!repo.upstream) {
        throw makeError(
          400,
          "no_upstream",
          `No upstream is configured for ${repo.branch}. Run: git push -u origin ${repo.branch}`,
        );
      }
      // Drain the publish set from the fixture's git decoration so the
      // next gitChanges call returns a clean preview, matching the
      // real backend's post-commit state.
      const remaining: Record<string, GitStatusEntry["status"]> = {};
      for (const [path, status] of Object.entries(folder.git ?? {})) {
        if (status === "ignored") continue;
        if (!isMarkdownPathMock(path)) remaining[path] = status;
      }
      folder.git = remaining;
      const commit = generateMockSHA();
      return {
        commit,
        short_commit: commit.slice(0, 7),
        branch: repo.branch,
        upstream: repo.upstream,
        pushed: true,
        files: publishable,
      };
    },
    blobURL(folderID, relPath) {
      const folder = state.find((v) => v.meta.id === folderID);
      if (!folder) return "";
      const content = folder.files[relPath];
      if (!(content instanceof Uint8Array)) return "";
      return toDataURL(content, mimeFor(relPath));
    },
  };
}

// Synthetic directory tree the mock backend exposes through
// browseDirectories. Modelled on a typical user home so the add-folder
// picker has plausible folders to navigate in dev/offline mode.
const MOCK_BROWSE: Record<string, string[]> = {
  "/Users/mock": ["Documents", "Notes", "Projects", ".config"],
  "/Users/mock/Notes": ["personal", "work", ".hidden"],
  "/Users/mock/Notes/personal": ["daily", "ideas"],
  "/Users/mock/Notes/work": [],
  "/Users/mock/Documents": [],
  "/Users/mock/Projects": ["reader", "obsidian-replacement"],
  "/Users/mock/Projects/reader": [],
  "/Users/mock/Projects/obsidian-replacement": [],
  "/Users/mock/.config": ["middleman"],
  "/Users/mock/.config/middleman": [],
  "/Users": ["mock"],
};

function mockBrowse(requested?: string): BrowseResponse {
  let path = requested ?? "/Users/mock";
  if (path === "~" || path === "~/") path = "/Users/mock";
  else if (path.startsWith("~/")) path = `/Users/mock/${path.slice(2)}`;
  // Normalize before lookup so /Users/mock/Notes/ matches /Users/mock/Notes —
  // the real Go endpoint cleans paths before serving, so the mock has to
  // match that contract.
  if (path.length > 1 && path.endsWith("/")) {
    path = path.replace(/\/+$/, "");
  }
  if (!(path in MOCK_BROWSE)) {
    throw makeError(404, "not_found", `no such directory: ${path}`);
  }
  const children = MOCK_BROWSE[path]!;
  const entries: BrowseEntry[] = children.map((name) => ({
    name,
    path: `${path}/${name}`,
    ...(name.startsWith(".") ? { hidden: true } : {}),
  }));
  let parent = "";
  if (path !== "/") {
    const idx = path.lastIndexOf("/");
    parent = idx <= 0 ? "/" : path.slice(0, idx);
  }
  return { path, parent, entries };
}

// Mirror of the Go server's limit handling at /api/docs/search and
// /api/docs/folders/{id}/search: accept 1..200 and fall back to 25 for
// anything outside that range (including 0, negative, NaN).
function normalizeSearchLimit(raw: number): number {
  if (!Number.isFinite(raw) || raw < 1 || raw > 200) return 25;
  return Math.floor(raw);
}

// Mirror of internal/docs.DeriveFolderID: lowercase basename, collapse
// non-alphanumeric runs to single dashes, trim trailing dashes, fall
// back to "folder" when sanitization empties the string, and append a
// numeric suffix on collision so mock dev/tests can't synthesize ids
// the real server would reject.
function deriveMockFolderID(absPath: string, existing: string[]): string {
  const baseLower = basename(absPath).toLowerCase();
  let collapsed = "";
  let prevDash = false;
  for (const ch of baseLower) {
    const isAlnum = (ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9");
    if (isAlnum) {
      collapsed += ch;
      prevDash = false;
    } else if (!prevDash && collapsed.length > 0) {
      collapsed += "-";
      prevDash = true;
    }
  }
  let id = collapsed.replace(/-+$/, "");
  if (id === "") id = "folder";
  const taken = new Set(existing);
  if (!taken.has(id)) return id;
  for (let i = 2; ; i += 1) {
    const candidate = `${id}-${i}`;
    if (!taken.has(candidate)) return candidate;
  }
}

function basename(path: string): string {
  const trimmed = path.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  return idx >= 0 ? trimmed.slice(idx + 1) : trimmed;
}

function buildTree(folder: FolderFixture): TreeNode {
  const root: TreeNode = {
    name: folder.meta.name,
    rel_path: "",
    is_dir: true,
    children: [],
  };
  const paths = Object.keys(folder.files)
    .filter((path) => /\.(md|markdown)$/i.test(path))
    .sort();
  for (const path of paths) {
    insertPath(root, path, (folder.files[path] as string).length);
  }
  return root;
}

function insertPath(root: TreeNode, path: string, size: number) {
  const parts = path.split("/");
  let cursor = root;
  for (let i = 0; i < parts.length; i += 1) {
    const name = parts[i]!;
    const relPath = parts.slice(0, i + 1).join("/");
    const isFile = i === parts.length - 1;
    cursor.children ??= [];
    let next = cursor.children.find((child) => child.name === name);
    if (!next) {
      next = isFile
        ? { name, rel_path: relPath, is_dir: false, size }
        : { name, rel_path: relPath, is_dir: true, children: [] };
      cursor.children.push(next);
    }
    cursor = next;
  }
}

function runSearch(folder: FolderFixture, query: string, limit: number): SearchResponse {
  const trimmed = query.trim();
  if (trimmed === "") return { query, hits: [] };
  const q = trimmed.toLowerCase();
  const hits: SearchHit[] = [];
  for (const path of Object.keys(folder.files)) {
    if (!/\.(md|markdown)$/i.test(path)) continue;
    const name = path.split("/").pop() ?? path;
    const score = scoreMatch(name.toLowerCase(), path.toLowerCase(), q);
    if (score > 0) hits.push({ name, rel_path: path, score });
  }
  hits.sort((a, b) => b.score - a.score || a.rel_path.localeCompare(b.rel_path));
  return { query, hits: hits.slice(0, limit) };
}

// Mirror of the Go server's scoreMatch (internal/docs/search.go). Keep
// the two in sync so mock-mode tests reflect production ranking.
function scoreMatch(name: string, rel: string, q: string): number {
  const stem = name.replace(/\.[^.]+$/, "");
  if (stem === q) return 100;
  if (name === q) return 90;
  if (name.startsWith(q)) return 70;
  if (name.includes(q)) return 50;
  if (rel.includes(q)) return 30;
  return 0;
}

function mimeFor(path: string): string {
  const ext = path.slice(path.lastIndexOf(".")).toLowerCase();
  switch (ext) {
    case ".png":
      return "image/png";
    case ".jpg":
    case ".jpeg":
      return "image/jpeg";
    case ".gif":
      return "image/gif";
    case ".webp":
      return "image/webp";
    default:
      return "application/octet-stream";
  }
}

function toDataURL(bytes: Uint8Array, mime: string): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 1) {
    binary += String.fromCharCode(bytes[i]!);
  }
  const encoded = typeof btoa === "function" ? btoa(binary) : Buffer.from(bytes).toString("base64");
  return `data:${mime};base64,${encoded}`;
}

function makeError(status: number, code: string, message: string): DocsAPIError {
  const err = new Error(message) as DocsAPIError;
  err.status = status;
  err.code = code;
  return err;
}

function runSearchAll(folders: FolderFixture[], query: string, limit: number): CrossFolderSearchResponse {
  const trimmed = query.trim();
  if (!trimmed) {
    return { query, hits: [], truncated: false };
  }
  const lower = trimmed.toLowerCase();
  const all: CrossFolderSearchHit[] = [];

  for (const folder of folders) {
    for (const [path, body] of Object.entries(folder.files)) {
      const lowerPath = path.toLowerCase();
      if (!lowerPath.endsWith(".md") && !lowerPath.endsWith(".markdown")) continue;
      const name = path.includes("/") ? path.slice(path.lastIndexOf("/") + 1) : path;
      const filenameScore = scoreMatch(name.toLowerCase(), path.toLowerCase(), lower);

      let bodyLine = 0;
      let bodyScore = 0;
      let snippet: BodySnippet | undefined;
      if (typeof body === "string") {
        const lines = body.split(/\r?\n/);
        let matches = 0;
        for (let i = 0; i < lines.length; i++) {
          const lineLower = lines[i]!.toLowerCase();
          if (!lineLower.includes(lower)) continue;
          matches++;
          if (bodyLine === 0) {
            bodyLine = i + 1;
            snippet = buildMockSnippet(lines[i]!, lower);
          }
        }
        bodyScore = Math.min(100, matches * 10);
      }

      if (filenameScore === 0 && bodyLine === 0) continue;

      const hit: CrossFolderSearchHit = {
        folder: folder.meta.id,
        folder_name: folder.meta.name,
        name,
        rel_path: path,
        score: filenameScore > 0 ? filenameScore : bodyScore,
        hit_type: filenameScore > 0 ? "filename" : "body",
      };
      if (bodyLine > 0 && snippet) {
        hit.line = bodyLine;
        hit.snippet = snippet;
      }
      all.push(hit);
    }
  }

  all.sort((a, b) => {
    const ba = a.hit_type === "filename" ? 0 : 1;
    const bb = b.hit_type === "filename" ? 0 : 1;
    if (ba !== bb) return ba - bb;
    if (a.score !== b.score) return b.score - a.score;
    if (a.folder_name !== b.folder_name) return a.folder_name.localeCompare(b.folder_name);
    if (a.rel_path !== b.rel_path) return a.rel_path.localeCompare(b.rel_path);
    return (a.line ?? 0) - (b.line ?? 0);
  });

  const truncated = all.length > limit;
  return { query, hits: all.slice(0, limit), truncated };
}

function buildMockSnippet(text: string, lower: string): BodySnippet {
  const textRunes = Array.from(text);
  const queryRunes = Array.from(lower);
  const matches: { start: number; end: number }[] = [];
  let i = 0;
  while (i + queryRunes.length <= textRunes.length) {
    let ok = true;
    for (let j = 0; j < queryRunes.length; j++) {
      if (textRunes[i + j]!.toLowerCase() !== queryRunes[j]) {
        ok = false;
        break;
      }
    }
    if (ok) {
      matches.push({ start: i, end: i + queryRunes.length });
      i += queryRunes.length;
    } else {
      i++;
    }
  }
  return { text, matches };
}

function isMarkdownPathMock(p: string): boolean {
  const lower = p.toLowerCase();
  return lower.endsWith(".md") || lower.endsWith(".markdown");
}

function suggestedMessageMock(changes: GitPublishChange[]): string {
  const subject = changes.length === 1 ? `docs: update ${changes[0]!.path}` : `docs: update ${changes.length} files`;
  const body = changes.map((c) => `- ${c.path}`).join("\n");
  return `${subject}\n\n${body}\n`;
}

function generateMockSHA(): string {
  // 40 hex chars, derived from Math.random so each publish gets a unique SHA.
  let s = "";
  while (s.length < 40) {
    s += Math.floor(Math.random() * 0xfffffff)
      .toString(16)
      .padStart(7, "0");
  }
  return s.slice(0, 40);
}

// Shared validation for any write — markdown extension, no traversal,
// parent directory must exist in the fixture. Throws DocsAPIError so
// callers can let it propagate to the UI surface.
function assertWritablePath(folder: FolderFixture, relPath: string): void {
  if (!/\.(md|markdown)$/i.test(relPath)) {
    throw makeError(415, "unsupported_extension", `only .md/.markdown can be written: ${relPath}`);
  }
  if (relPath.startsWith("/") || relPath.includes("..")) {
    throw makeError(403, "outside_folder", `path escapes folder: ${relPath}`);
  }
  const parent = relPath.includes("/") ? relPath.slice(0, relPath.lastIndexOf("/")) : "";
  if (parent) {
    const hasParent = Object.keys(folder.files).some((p) => p === parent || p.startsWith(`${parent}/`));
    if (!hasParent) {
      throw makeError(404, "not_found", `parent dir missing: ${parent}`);
    }
  }
}
