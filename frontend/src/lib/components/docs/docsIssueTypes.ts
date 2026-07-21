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
