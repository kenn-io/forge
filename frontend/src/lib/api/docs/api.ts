import type { components } from "@middleman/ui/api/schema";
import type {
  AddFolderInput,
  BrowseResponse,
  CrossFolderSearchResponse,
  DocsPublishError,
  GitChangesResponse,
  GitPublishResponse,
  GitStatusResponse,
  SearchResponse,
  TreeNode,
  Folder,
} from "./types";

import { apiErrorMessage, createRuntimeClient } from "../runtime.js";

/**
 * Typed wrapper around the middleman Go server's /api/docs/* endpoints.
 *
 * Image blob URLs aren't fetched through this API — markdown <img src=...>
 * tags request them directly. `blobURL` builds the right URL.
 */
export interface DocsAPI {
  listFolders(): Promise<Folder[]>;
  // Register a new folder. Server canonicalizes the path (tilde expansion,
  // symlink resolution) and defaults name/id when omitted. Throws
  // DocsAPIError with status 409 / code "duplicate_folder_id" on collision
  // and 503 / "save_unavailable" when the server was started without a
  // writable config path.
  addFolder(input: AddFolderInput): Promise<Folder>;
  removeFolder(id: string): Promise<void>;
  renameFolder(id: string, name: string): Promise<Folder>;
  // List subdirectories at path (defaults to the user's home dir on the
  // server). Used by the add-folder folder picker.
  browseDirectories(path?: string): Promise<BrowseResponse>;
  tree(folderID: string): Promise<TreeNode>;
  readFile(folderID: string, relPath: string): Promise<string>;
  writeFile(folderID: string, relPath: string, content: string): Promise<void>;
  // Create a new file. Throws DocsAPIError with status 409 / code
  // "already_exists" if the destination is in use.
  createFile(folderID: string, relPath: string, content?: string): Promise<void>;
  deleteFile(folderID: string, relPath: string): Promise<void>;
  renameFile(folderID: string, fromPath: string, toPath: string): Promise<void>;
  search(folderID: string, query: string, limit?: number): Promise<SearchResponse>;
  searchAll(query: string, limit?: number): Promise<CrossFolderSearchResponse>;
  gitStatus(folderID: string): Promise<GitStatusResponse>;
  gitChanges(folderID: string): Promise<GitChangesResponse>;
  gitPublish(folderID: string, message: string): Promise<GitPublishResponse>;
  blobURL(folderID: string, relPath: string): string;
}

export interface DocsAPIClientOptions {
  baseURL?: string;
  fetch?: typeof fetch;
}

export function createDocsAPI(options: DocsAPIClientOptions = {}): DocsAPI {
  const api = createRuntimeClient(options.fetch, options.baseURL);

  // Build a blob URL by hand: it isn't fetched through the typed client —
  // markdown <img src=...> tags request it directly. Same shape as the old
  // url() helper: an absolute URL when baseURL is http(s), else path-only.
  function blobURLFor(folderID: string, relPath: string): string {
    const u = resourceURLFor(options.baseURL, `docs/folders/${encodeURIComponent(folderID)}/blob`);
    u.searchParams.set("path", relPath);
    return isSameRuntimeOrigin(u) ? u.pathname + u.search : u.toString();
  }

  return {
    async listFolders() {
      const { data, error, response } = await api.GET("/docs/folders");
      throwOnDocsError(error, response);
      return (data!.folders ?? []) as Folder[];
    },
    async addFolder(input) {
      const { data, error, response } = await api.POST("/docs/folders", { body: input });
      throwOnDocsError(error, response);
      return data!.folder as Folder;
    },
    async removeFolder(id) {
      const { error, response } = await api.DELETE("/docs/folders/{id}", {
        params: { path: { id } },
      });
      throwOnDocsError(error, response);
    },
    async renameFolder(id, name) {
      const { data, error, response } = await api.PATCH("/docs/folders/{id}", {
        params: { path: { id } },
        body: { name },
      });
      throwOnDocsError(error, response);
      return data!.folder as Folder;
    },
    async browseDirectories(path) {
      const query: { path?: string } = {};
      if (path !== undefined) query.path = path;
      const { data, error, response } = await api.GET("/docs/browse", {
        params: { query },
      });
      throwOnDocsError(error, response);
      return { ...data!, entries: data!.entries ?? [] } as BrowseResponse;
    },
    async tree(folderID) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/tree", {
        params: { path: { id: folderID } },
      });
      throwOnDocsError(error, response);
      return data as TreeNode;
    },
    async readFile(folderID, relPath) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
      });
      throwOnDocsError(error, response);
      return data!.content;
    },
    async writeFile(folderID, relPath, content) {
      const { error, response } = await api.PUT("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
        body: { content },
      });
      throwOnDocsError(error, response);
    },
    async createFile(folderID, relPath, content = "") {
      const { error, response } = await api.POST("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
        body: { content },
      });
      throwOnDocsError(error, response);
    },
    async deleteFile(folderID, relPath) {
      const { error, response } = await api.DELETE("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
      });
      throwOnDocsError(error, response);
    },
    async renameFile(folderID, fromPath, toPath) {
      const { error, response } = await api.POST("/docs/folders/{id}/file/actions/rename", {
        params: { path: { id: folderID } },
        body: { from: fromPath, to: toPath },
      });
      throwOnDocsError(error, response);
    },
    async search(folderID, query, limit) {
      const searchQuery: { q?: string; limit?: number } = { q: query };
      if (limit !== undefined) searchQuery.limit = limit;
      const { data, error, response } = await api.GET("/docs/folders/{id}/search", {
        params: { path: { id: folderID }, query: searchQuery },
      });
      throwOnDocsError(error, response);
      return { ...data!, hits: data!.hits ?? [] } as SearchResponse;
    },
    async searchAll(query, limit) {
      const searchQuery: { q?: string; limit?: number } = { q: query };
      if (limit !== undefined) searchQuery.limit = limit;
      const { data, error, response } = await api.GET("/docs/search", {
        params: { query: searchQuery },
      });
      throwOnDocsError(error, response);
      return { ...data!, hits: data!.hits ?? [] } as CrossFolderSearchResponse;
    },
    async gitStatus(folderID) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/git", {
        params: { path: { id: folderID } },
      });
      throwOnDocsError(error, response);
      return { ...data!, entries: data!.entries ?? [] } as GitStatusResponse;
    },
    async gitChanges(folderID) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/git/changes", {
        params: { path: { id: folderID } },
      });
      throwOnDocsError(error, response);
      return { ...data!, changes: data!.changes ?? [] } as GitChangesResponse;
    },
    async gitPublish(folderID, message) {
      const { data, error, response } = await api.POST("/docs/folders/{id}/git/publish", {
        params: { path: { id: folderID } },
        body: { message },
      });
      throwOnDocsError(error, response);
      return { ...data!, files: data!.files ?? [] } as GitPublishResponse;
    },
    blobURL(folderID, relPath) {
      return blobURLFor(folderID, relPath);
    },
  };
}

function resourceURLFor(baseURL: string | undefined, path: string): URL {
  const base = new URL(baseURL ?? defaultAPIBaseURL(), runtimeOrigin()).toString().replace(/\/+$/, "");
  return new URL(`${base}/${path.replace(/^\/+/, "")}`);
}

function defaultAPIBaseURL(): string {
  const basePath = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";
  return new URL(`${basePath.replace(/\/$/, "")}/api/v1`, runtimeOrigin()).toString();
}

function runtimeOrigin(): string {
  return typeof window !== "undefined" ? window.location.origin : "http://localhost";
}

function isSameRuntimeOrigin(url: URL): boolean {
  return url.origin === runtimeOrigin();
}

function throwOnDocsError(
  error: Pick<Partial<components["schemas"]["ProblemError"]>, "code" | "detail" | "details" | "title"> | undefined,
  response: Response,
): void {
  if (response.ok) return;
  const err = new Error(apiErrorMessage(error, `${response.status}`)) as DocsPublishError;
  err.name = "DocsAPIError";
  err.status = response.status;
  const code = docsErrorCodeFromEnvelope(error);
  if (code !== undefined) {
    err.code = code;
  }
  const commit = error?.details?.["commit"];
  if (typeof commit === "string") {
    err.commit = commit;
  }
  throw err;
}

function docsErrorCodeFromEnvelope(
  error: Pick<Partial<components["schemas"]["ProblemError"]>, "code" | "details"> | undefined,
): string | undefined {
  const reason = error?.details?.["reason"];
  if (typeof reason === "string") {
    switch (reason) {
      case "indexNotClean":
        return "index_not_clean";
      case "noUpstream":
        return "no_upstream";
      case "pushFailedAfterCommit":
        return "push_failed_after_commit";
      case "unsafeGitConfig":
        return "unsafe_git_config";
      case "conflict":
        return "conflict";
      case "alreadyExists":
        return "already_exists";
      case "unsupportedExtension":
        return "unsupported_extension";
      case "outsideFolder":
        return "outside_folder";
      case "duplicateFolderID":
        return "duplicate_folder_id";
    }
  }
  return typeof error?.code === "string" ? error.code : undefined;
}
