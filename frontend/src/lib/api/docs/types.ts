/**
 * Wire types for the middleman Go server's /api/docs/* endpoints.
 *
 * Keep in sync with `internal/server/docs_routes.go` and
 * `internal/docs/{folder,search}.go`.
 */

export interface Folder {
  id: string;
  name: string;
  path: string;
  daemon?: string;
}

export interface FolderListResponse {
  folders: Folder[];
}

export interface FolderMutationResponse {
  folder: Folder;
}

export interface AddFolderInput {
  path: string;
  id?: string;
  name?: string;
  daemon?: string;
}

export interface BrowseEntry {
  name: string;
  path: string;
  hidden?: boolean;
}

export interface BrowseResponse {
  // Canonicalized absolute path that was listed. Reflects tilde
  // expansion and symlink resolution the server applied.
  path: string;
  // Parent directory or "" when at the filesystem root, so the
  // picker UI can render an "up one level" affordance.
  parent: string;
  entries: BrowseEntry[];
}

export interface TreeNode {
  name: string;
  rel_path: string;
  is_dir: boolean;
  size?: number;
  children?: TreeNode[];
}

export interface FileContentResponse {
  rel_path: string;
  content: string;
}

export interface SearchHit {
  name: string;
  rel_path: string;
  score: number;
}

export interface SearchResponse {
  query: string;
  hits: SearchHit[];
}

export interface DocsAPIError extends Error {
  status: number;
  code?: string;
}

// Mirrors @pierre/trees' GitStatus union so the wire shape can be
// passed through to the tree component without translation.
export type GitFileStatus = "added" | "deleted" | "ignored" | "modified" | "renamed" | "untracked";

export interface GitStatusEntry {
  path: string;
  status: GitFileStatus;
}

export interface GitStatusResponse {
  is_repo: boolean;
  entries: GitStatusEntry[];
}

// Wire shape returned by GET /api/docs/search (cross-folder). Mirrors
// internal/docs.CrossFolderHit + SearchAllResult on the Go side.

export interface SnippetRange {
  start: number; // inclusive, Unicode code-point offset
  end: number; // exclusive, Unicode code-point offset
}

export interface BodySnippet {
  text: string;
  matches: SnippetRange[];
}

export interface CrossFolderSearchHit {
  folder: string; // folder id
  folder_name: string;
  name: string; // basename
  rel_path: string;
  score: number;
  hit_type: "filename" | "body";
  line?: number; // 1-based; present when a body snippet is attached
  snippet?: BodySnippet;
}

export interface CrossFolderSearchResponse {
  query: string;
  hits: CrossFolderSearchHit[];
  warnings?: string[];
  truncated: boolean;
}

// Publish-set entry returned by GET /git/changes and POST /git/publish.
// Mirrors internal/docs.PublishChange.
export type GitPublishChangeStatus = "added" | "deleted" | "modified" | "renamed" | "untracked";

export interface GitPublishChange {
  path: string;
  old_path?: string; // present for renames
  status: GitPublishChangeStatus;
}

export interface GitChangesResponse {
  is_repo: boolean;
  branch?: string;
  upstream?: string;
  changes: GitPublishChange[];
  ignored_non_markdown_count: number;
  suggested_message?: string;
}

export interface GitPublishResponse {
  commit: string;
  short_commit: string;
  branch: string;
  upstream?: string;
  pushed: boolean;
  files: GitPublishChange[];
}

// push_failed_after_commit carries an extra field beyond the standard
// error envelope: the SHA of the local commit that succeeded. DocsAPIError
// callers can inspect `commit` to render the "local commit exists, push
// failed" recovery copy.
export interface DocsPublishError extends DocsAPIError {
  commit?: string;
}
