import type { components } from "../generated/schema.js";

// Generated OpenAPI schemas remain the wire authority. These aliases are
// exact when the UI consumes the wire shape directly and narrow only the
// nullable collections or string enums that the Docs API adapter validates.

export type Folder = components["schemas"]["DocsFolderResponse"];

export type AddFolderInput = Omit<components["schemas"]["CreateDocsFolderInputBody"], "path"> & {
  path: string;
};

export type BrowseEntry = components["schemas"]["DocsBrowseEntry"];

export type BrowseResponse = Omit<components["schemas"]["DocsBrowseOutputBody"], "entries" | "parent"> & {
  parent: string;
  entries: BrowseEntry[];
};

type TreeNodeWire = components["schemas"]["Node"];

export type TreeNode = Omit<TreeNodeWire, "children"> & {
  children?: TreeNode[];
};

export type SearchHit = components["schemas"]["Hit"];

export type SearchResponse = Omit<components["schemas"]["DocsSearchOutputBody"], "hits"> & {
  hits: SearchHit[];
};

export interface DocsAPIError extends Error {
  status: number;
  code?: string;
}

// These refinements mirror @pierre/trees and the server's documented Docs
// status set. The API adapter rejects an unknown generated string before it
// reaches the component tree.
export type GitFileStatus = "added" | "deleted" | "ignored" | "modified" | "renamed" | "untracked";

export type GitStatusEntry = Omit<components["schemas"]["GitStatusEntry"], "status"> & {
  status: GitFileStatus;
};

export type GitStatusResponse = Omit<components["schemas"]["GitStatusResponse"], "entries"> & {
  entries: GitStatusEntry[];
};

export type SnippetRange = components["schemas"]["SnippetRange"];

export type BodySnippet = Omit<components["schemas"]["BodySnippet"], "matches"> & {
  matches: SnippetRange[];
};

export type CrossFolderSearchHit = Omit<components["schemas"]["CrossFolderHit"], "hit_type" | "snippet"> & {
  hit_type: "filename" | "body";
  snippet?: BodySnippet;
};

export type CrossFolderSearchResponse = Omit<components["schemas"]["DocsSearchAllOutputBody"], "hits" | "warnings"> & {
  hits: CrossFolderSearchHit[];
  warnings?: string[];
};

export type GitPublishChangeStatus = "added" | "deleted" | "modified" | "renamed" | "untracked";

export type GitPublishChange = Omit<components["schemas"]["PublishChange"], "status"> & {
  status: GitPublishChangeStatus;
};

export type GitChangesResponse = Omit<components["schemas"]["GitChangesResponse"], "changes"> & {
  changes: GitPublishChange[];
};

export type GitPublishResponse = Omit<components["schemas"]["PublishResponse"], "files"> & {
  files: GitPublishChange[];
};

export type GitPullResponse = components["schemas"]["PullResponse"];
