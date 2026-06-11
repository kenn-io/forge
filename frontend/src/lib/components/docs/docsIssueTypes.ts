export interface IssueSummary {
  id?: number | undefined;
  uid: string;
  project_id?: number | undefined;
  short_id: string;
  qualified_id: string;
  title: string;
  status: "open" | "closed" | string;
  project_uid?: string | undefined;
  project_name: string;
  metadata?: Record<string, unknown> | undefined;
  revision?: number | undefined;
  owner?: string | undefined;
  author?: string | undefined;
  created_at?: string | undefined;
  updated_at?: string | undefined;
}

export type IssueStatusFilter = "open" | "closed" | "all";

export type SearchScope = { kind: "all" } | { kind: "project"; project_uid: string };

export interface SearchFilters {
  scope: SearchScope;
  status: IssueStatusFilter;
  owner: string;
  label: string;
  query: string;
}

export interface SearchResponse {
  filters?: SearchFilters | undefined;
  issues: IssueSummary[];
  fetched_at?: string | undefined;
}

export interface KataAPI {
  search(filters: SearchFilters, opts?: { daemonId?: string }): Promise<SearchResponse>;
}
