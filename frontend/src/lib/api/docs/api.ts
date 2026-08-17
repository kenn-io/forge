import type { components } from "../generated/schema.js";
import { Effect, Schema } from "effect";
import { configuredAPIBaseURL } from "../runtime-base.js";
import { transientRetrySchedule } from "../retry-policy.js";
import type {
  AddFolderInput,
  BrowseResponse,
  CrossFolderSearchHit,
  CrossFolderSearchResponse,
  GitChangesResponse,
  GitFileStatus,
  GitPublishChange,
  GitPublishChangeStatus,
  GitPublishResponse,
  GitPullResponse,
  GitStatusResponse,
  SearchResponse,
  TreeNode,
  Folder,
} from "./types";

import { apiErrorMessage, createRuntimeClient } from "../runtime.js";

/**
 * Typed wrapper around the kenn-forge Go server's /api/docs/* endpoints.
 *
 * Image blob URLs aren't fetched through this API — markdown <img src=...>
 * tags request them directly. `blobURL` builds the right URL.
 */
export interface DocsAPI {
  listFolders(signal?: AbortSignal): Promise<Folder[]>;
  // Register a new folder. Server canonicalizes the path (tilde expansion,
  // symlink resolution) and defaults name/id when omitted. Throws
  // DocsAPIError with status 409 / code "duplicate_folder_id" on collision
  // and 503 / "save_unavailable" when the server was started without a
  // writable config path.
  addFolder(input: AddFolderInput, signal?: AbortSignal): Promise<Folder>;
  removeFolder(id: string, signal?: AbortSignal): Promise<void>;
  renameFolder(id: string, name: string, signal?: AbortSignal): Promise<Folder>;
  // List subdirectories at path (defaults to the user's home dir on the
  // server). Used by the add-folder folder picker.
  browseDirectories(path?: string, signal?: AbortSignal): Promise<BrowseResponse>;
  tree(folderID: string, signal?: AbortSignal): Promise<TreeNode>;
  readFile(folderID: string, relPath: string, signal?: AbortSignal): Promise<string>;
  writeFile(folderID: string, relPath: string, content: string, signal?: AbortSignal): Promise<void>;
  // Create a new file. Throws DocsAPIError with status 409 / code
  // "already_exists" if the destination is in use.
  createFile(folderID: string, relPath: string, content?: string, signal?: AbortSignal): Promise<void>;
  deleteFile(folderID: string, relPath: string, signal?: AbortSignal): Promise<void>;
  renameFile(folderID: string, fromPath: string, toPath: string, signal?: AbortSignal): Promise<void>;
  search(folderID: string, query: string, limit?: number, signal?: AbortSignal): Promise<SearchResponse>;
  searchAll(query: string, limit?: number, signal?: AbortSignal): Promise<CrossFolderSearchResponse>;
  gitStatus(folderID: string, signal?: AbortSignal): Promise<GitStatusResponse>;
  gitChanges(folderID: string, signal?: AbortSignal): Promise<GitChangesResponse>;
  gitPublish(folderID: string, message: string, signal?: AbortSignal): Promise<GitPublishResponse>;
  // Fast-forward the folder's branch to its upstream. Throws DocsAPIError
  // with code "diverged" when local and remote history have both moved.
  gitPull(folderID: string, signal?: AbortSignal): Promise<GitPullResponse>;
  blobURL(folderID: string, relPath: string): string;
}

export interface DocsAPIClientOptions {
  baseURL?: string;
  fetch?: typeof fetch;
}

export class DocsRequestError extends Schema.TaggedError<DocsRequestError>()("DocsRequestError", {
  operation: Schema.String,
  message: Schema.String,
  status: Schema.Number,
  code: Schema.optional(Schema.String),
  commit: Schema.optional(Schema.String),
  cause: Schema.Defect(),
}) {}

function docsRequestError(operation: string, cause: unknown): DocsRequestError {
  const message = cause instanceof Error ? cause.message : "Docs request failed";
  const status = cause instanceof Error && "status" in cause && typeof cause.status === "number" ? cause.status : 0;
  const code = cause instanceof Error && "code" in cause && typeof cause.code === "string" ? cause.code : undefined;
  const commit =
    cause instanceof Error && "commit" in cause && typeof cause.commit === "string" ? cause.commit : undefined;
  return DocsRequestError.make({
    operation,
    message,
    status,
    ...(code === undefined ? {} : { code }),
    ...(commit === undefined ? {} : { commit }),
    cause,
  });
}

export const executeDocsRequest = Effect.fn("DocsApi.execute")(function* <A>(
  operation: string,
  request: (signal: AbortSignal) => Promise<A>,
) {
  return yield* Effect.tryPromise({
    try: request,
    catch: (cause) => docsRequestError(operation, cause),
  });
});

export const retryIdempotentDocsRequest = <A, R>(effect: Effect.Effect<A, DocsRequestError, R>) =>
  effect.pipe(
    Effect.retry({
      schedule: transientRetrySchedule,
      while: (failure) => failure.status === 0,
    }),
  );

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
    async listFolders(signal) {
      const { data, error, response } = await api.GET("/docs/folders", signal === undefined ? {} : { signal });
      throwOnDocsError(error, response);
      return requiredData(data, "list Docs folders").folders ?? [];
    },
    async addFolder(input, signal) {
      const { data, error, response } = await api.POST("/docs/folders", {
        body: input,
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return requiredData(data, "add Docs folder").folder;
    },
    async removeFolder(id, signal) {
      const { error, response } = await api.DELETE("/docs/folders/{id}", {
        params: { path: { id } },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
    },
    async renameFolder(id, name, signal) {
      const { data, error, response } = await api.PATCH("/docs/folders/{id}", {
        params: { path: { id } },
        body: { name },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return requiredData(data, "rename Docs folder").folder;
    },
    async browseDirectories(path, signal) {
      const query: { path?: string } = {};
      if (path !== undefined) query.path = path;
      const { data, error, response } = await api.GET("/docs/browse", {
        params: { query },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      const payload = requiredData(data, "browse Docs directories");
      return { ...payload, parent: payload.parent ?? "", entries: payload.entries ?? [] };
    },
    async tree(folderID, signal) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/tree", {
        params: { path: { id: folderID } },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return normalizeTree(requiredData(data, "load Docs tree"));
    },
    async readFile(folderID, relPath, signal) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return requiredData(data, "read Docs document").content;
    },
    async writeFile(folderID, relPath, content, signal) {
      const { error, response } = await api.PUT("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
        body: { content },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
    },
    async createFile(folderID, relPath, content = "", signal) {
      const { error, response } = await api.POST("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
        body: { content },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
    },
    async deleteFile(folderID, relPath, signal) {
      const { error, response } = await api.DELETE("/docs/folders/{id}/file", {
        params: { path: { id: folderID }, query: { path: relPath } },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
    },
    async renameFile(folderID, fromPath, toPath, signal) {
      const { error, response } = await api.POST("/docs/folders/{id}/file/actions/rename", {
        params: { path: { id: folderID } },
        body: { from: fromPath, to: toPath },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
    },
    async search(folderID, query, limit, signal) {
      const searchQuery: { q?: string; limit?: number } = { q: query };
      if (limit !== undefined) searchQuery.limit = limit;
      const { data, error, response } = await api.GET("/docs/folders/{id}/search", {
        params: { path: { id: folderID }, query: searchQuery },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      const payload = requiredData(data, "search Docs folder");
      return { ...payload, hits: payload.hits ?? [] };
    },
    async searchAll(query, limit, signal) {
      const searchQuery: { q?: string; limit?: number } = { q: query };
      if (limit !== undefined) searchQuery.limit = limit;
      const { data, error, response } = await api.GET("/docs/search", {
        params: { query: searchQuery },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return normalizeCrossFolderSearch(requiredData(data, "search Docs folders"));
    },
    async gitStatus(folderID, signal) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/git", {
        params: { path: { id: folderID } },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return normalizeGitStatus(requiredData(data, "load Docs git status"));
    },
    async gitChanges(folderID, signal) {
      const { data, error, response } = await api.GET("/docs/folders/{id}/git/changes", {
        params: { path: { id: folderID } },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return normalizeGitChanges(requiredData(data, "load Docs publish preview"));
    },
    async gitPublish(folderID, message, signal) {
      const { data, error, response } = await api.POST("/docs/folders/{id}/git/publish", {
        params: { path: { id: folderID } },
        body: { message },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return normalizeGitPublish(requiredData(data, "publish Docs"));
    },
    async gitPull(folderID, signal) {
      const { data, error, response } = await api.POST("/docs/folders/{id}/git/pull", {
        params: { path: { id: folderID } },
        ...(signal === undefined ? {} : { signal }),
      });
      throwOnDocsError(error, response);
      return requiredData(data, "pull Docs folder");
    },
    blobURL(folderID, relPath) {
      return blobURLFor(folderID, relPath);
    },
  };
}

function requiredData<A>(data: A | undefined, operation: string): A {
  if (data === undefined) throw new Error(`${operation} returned no response body`);
  return data;
}

function normalizeTree(node: components["schemas"]["Node"]): TreeNode {
  const { children, ...rest } = node;
  const normalizedChildren = children?.map(normalizeTree);
  return {
    ...rest,
    ...(normalizedChildren === undefined ? {} : { children: normalizedChildren }),
  };
}

function normalizeGitStatus(payload: components["schemas"]["GitStatusResponse"]): GitStatusResponse {
  const entries = (payload.entries ?? []).map((entry) => ({
    ...entry,
    status: normalizeGitFileStatus(entry.status),
  }));
  return { ...payload, entries };
}

function normalizeGitFileStatus(status: string): GitFileStatus {
  switch (status) {
    case "added":
    case "deleted":
    case "ignored":
    case "modified":
    case "renamed":
    case "untracked":
      return status;
    default:
      throw new Error(`Unsupported Docs git status: ${status}`);
  }
}

function normalizeGitChanges(payload: components["schemas"]["GitChangesResponse"]): GitChangesResponse {
  return { ...payload, changes: (payload.changes ?? []).map(normalizeGitPublishChange) };
}

function normalizeGitPublish(payload: components["schemas"]["PublishResponse"]): GitPublishResponse {
  return { ...payload, files: (payload.files ?? []).map(normalizeGitPublishChange) };
}

function normalizeGitPublishChange(change: components["schemas"]["PublishChange"]): GitPublishChange {
  return { ...change, status: normalizeGitPublishStatus(change.status) };
}

function normalizeGitPublishStatus(status: string): GitPublishChangeStatus {
  switch (status) {
    case "added":
    case "deleted":
    case "modified":
    case "renamed":
    case "untracked":
      return status;
    default:
      throw new Error(`Unsupported Docs publish status: ${status}`);
  }
}

function normalizeCrossFolderSearch(
  payload: components["schemas"]["DocsSearchAllOutputBody"],
): CrossFolderSearchResponse {
  const { hits, warnings, ...rest } = payload;
  return {
    ...rest,
    hits: (hits ?? []).map(normalizeCrossFolderSearchHit),
    ...(warnings === undefined || warnings === null ? {} : { warnings }),
  };
}

function normalizeCrossFolderSearchHit(hit: components["schemas"]["CrossFolderHit"]): CrossFolderSearchHit {
  const { hit_type, snippet, ...rest } = hit;
  return {
    ...rest,
    hit_type: normalizeCrossFolderHitType(hit_type),
    ...(snippet === undefined ? {} : { snippet: { ...snippet, matches: snippet.matches ?? [] } }),
  };
}

function normalizeCrossFolderHitType(hitType: string): CrossFolderSearchHit["hit_type"] {
  switch (hitType) {
    case "filename":
    case "body":
      return hitType;
    default:
      throw new Error(`Unsupported Docs search hit type: ${hitType}`);
  }
}

function resourceURLFor(baseURL: string | undefined, path: string): URL {
  const base = new URL(baseURL ?? defaultAPIBaseURL(), runtimeOrigin()).toString().replace(/\/+$/, "");
  return new URL(`${base}/${path.replace(/^\/+/, "")}`);
}

function defaultAPIBaseURL(): string {
  return configuredAPIBaseURL();
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
  const code = docsErrorCodeFromEnvelope(error);
  const commit = error?.details?.["commit"];
  throw new DocsTransportError(
    apiErrorMessage(error, `${response.status}`),
    response.status,
    code,
    typeof commit === "string" ? commit : undefined,
  );
}

class DocsTransportError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly commit?: string;

  constructor(message: string, status: number, code: string | undefined, commit: string | undefined) {
    super(message);
    this.name = "DocsAPIError";
    this.status = status;
    if (code !== undefined) this.code = code;
    if (commit !== undefined) this.commit = commit;
  }
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
      case "diverged":
        return "diverged";
      case "pullFailed":
        return "pull_failed";
      case "gitOperationInProgress":
        return "git_operation_in_progress";
      case "notGitRepo":
        return "not_a_git_repo";
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
