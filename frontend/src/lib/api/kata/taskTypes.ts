export type KataTaskViewName = "inbox" | "today" | "upcoming" | "deadlines" | "all" | "logbook";

export interface KataTaskChecklistItem {
  id: string;
  text: string;
  done: boolean;
}

export interface KataTaskMetadata {
  scheduled_on?: string | undefined;
  deadline_on?: string | undefined;
  today_bucket?: "day" | "evening" | undefined;
  checklist?: KataTaskChecklistItem[] | undefined;
  area?: string | undefined;
  mail_links?: KataTaskMessageLinkRef[] | undefined;
  [key: string]: unknown;
}

export interface KataProjectMetadata {
  area?: string | undefined;
  sidebar_order?: number | undefined;
  icon?: string | undefined;
  timezone?: string | undefined;
  role?: string | undefined;
  [key: string]: unknown;
}

export interface KataInstanceResponse {
  instance_uid: string;
  version: string;
  schema_version: number;
}

export interface KataProjectSummary {
  id: number;
  uid: string;
  name: string;
  metadata: KataProjectMetadata;
  revision?: number | undefined;
  created_at?: string | undefined;
  deleted_at?: string | undefined;
  open_count: number;
}

export interface KataProjectsResponse {
  projects: KataProjectSummary[];
  fetched_at: string;
}

export interface KataLinkPeer {
  uid: string;
  short_id: string;
}

export interface KataTaskSummary {
  id: number;
  uid: string;
  project_id: number;
  short_id: string;
  qualified_id: string;
  title: string;
  body?: string | undefined;
  status: "open" | "closed";
  project_uid: string;
  project_name: string;
  metadata: KataTaskMetadata;
  revision: number;
  owner?: string | undefined;
  author: string;
  priority?: number | undefined;
  labels?: string[] | undefined;
  parent_short_id?: string | undefined;
  blocks?: KataLinkPeer[] | undefined;
  blocked_by?: KataLinkPeer[] | undefined;
  related?: KataLinkPeer[] | undefined;
  child_counts?: { open: number; total: number } | undefined;
  recurrence_id?: number | undefined;
  occurrence_key?: string | undefined;
  created_at: string;
  updated_at: string;
  closed_reason?: "done" | "wontfix" | "duplicate" | "superseded" | "audit-no-change" | undefined;
  closed_at?: string | undefined;
  deleted_at?: string | undefined;
}

export interface KataTaskGroup {
  id: string;
  title: string;
  issues: KataTaskSummary[];
}

export interface KataTaskViewResponse {
  view: KataTaskViewName;
  groups: KataTaskGroup[];
  fetched_at: string;
}

export type KataTaskStatusFilter = "open" | "closed" | "all";
export type KataTaskSearchScope = { kind: "all" } | { kind: "project"; project_uid: string };

export interface KataTaskSearchFilters {
  scope: KataTaskSearchScope;
  status: KataTaskStatusFilter;
  owner: string;
  label: string;
  query: string;
}

export interface KataTaskSearchResponse {
  filters: KataTaskSearchFilters;
  issues: KataTaskSummary[];
  fetched_at: string;
}

export interface KataDuplicateCandidateDisplay {
  title: string;
  qualified_id: string;
  reason?: string | undefined;
}

export interface KataComment {
  id: number;
  issue_id: number;
  author: string;
  body: string;
  created_at: string;
}

export interface KataTaskLabel {
  issue_id: number;
  label: string;
  author: string;
  created_at: string;
}

export interface KataTaskMessageLinkRef {
  message_id: number;
  conversation_id?: number | undefined;
  subject: string;
  from: string;
  sent_at: string;
  added_at: string;
}

export interface KataTaskLink {
  id: number;
  project_id: number;
  from: KataLinkPeer;
  to: KataLinkPeer;
  type: "parent" | "blocks" | "related";
  author: string;
  created_at: string;
}

export interface KataTaskRef {
  uid: string;
  short_id: string;
  qualified_id: string;
  title: string;
  status: "open" | "closed";
}

export interface KataTaskDetail {
  issue: KataTaskSummary & {
    body: string;
  };
  etag?: string | undefined;
  comments: KataComment[];
  labels: KataTaskLabel[];
  links: KataTaskLink[];
  parent?: KataTaskRef | undefined;
  children?: KataTaskSummary[] | undefined;
}

export interface KataTaskEvent {
  event_id: number;
  event_uid: string;
  origin_instance_uid: string;
  type: string;
  project_id: number;
  project_uid: string;
  project_name: string;
  issue_id?: number | undefined;
  issue_uid?: string | undefined;
  issue_short_id?: string | undefined;
  related_issue_id?: number | undefined;
  related_issue_uid?: string | undefined;
  related_issue_short_id?: string | undefined;
  actor: string;
  payload?: Record<string, unknown> | undefined;
  created_at: string;
}

export interface KataTaskEventsQuery {
  project_id?: number | undefined;
  issue_uid?: string | undefined;
  after_id?: number | undefined;
  limit?: number | undefined;
}

export interface KataTaskEventsResponse {
  reset_required: boolean;
  reset_after_id?: number | undefined;
  events: KataTaskEvent[];
  next_after_id: number;
}

export type KataTaskEventStreamMessage =
  | { kind: "event"; event: KataTaskEvent; lastEventID: number }
  | { kind: "reset"; event_id: number; reset_after_id: number; lastEventID: number };

export interface KataRecurrence {
  id: number;
  uid: string;
  project_id: number;
  rrule: string;
  dtstart: string;
  timezone: string;
  template_title: string;
  template_body: string;
  template_owner?: string | undefined;
  template_priority?: number | undefined;
  template_labels: string[];
  template_metadata: KataTaskMetadata;
  next_occurrence_key?: string | undefined;
  last_materialized_uid?: string | undefined;
  author: string;
  revision: number;
  created_at: string;
  updated_at: string;
  deleted_at?: string | undefined;
}

export interface KataRecurrencesResponse {
  recurrences: KataRecurrence[];
  fetched_at: string;
}

export interface KataRecurrenceTemplateInput {
  title: string;
  body?: string | undefined;
  owner?: string | undefined;
  priority?: number | undefined;
  labels?: string[] | undefined;
  metadata?: KataTaskMetadata | undefined;
}

export interface KataCreateRecurrenceInput {
  actor: string;
  rrule: string;
  dtstart: string;
  timezone: string;
  template: KataRecurrenceTemplateInput;
}

export interface KataRecurrenceTemplateUpdateInput {
  title?: string | undefined;
  body?: string | undefined;
  owner?: string | undefined;
  priority?: number | undefined;
  labels?: string[] | undefined;
  metadata?: KataTaskMetadata | undefined;
}

export interface KataPatchRecurrenceInput {
  actor: string;
  rrule?: string | undefined;
  dtstart?: string | undefined;
  timezone?: string | undefined;
  template?: KataRecurrenceTemplateUpdateInput | undefined;
}

export interface KataRecurrenceResponse {
  recurrence: KataRecurrence;
  etag?: string | undefined;
  changed?: boolean | undefined;
}

export interface KataTaskMutationTarget {
  project_id: number;
  ref: string;
}

export interface KataTaskCreateDraft {
  title: string;
  body?: string | undefined;
  owner?: string | undefined;
  priority?: number | undefined;
  labels?: string[] | undefined;
  metadata?: KataTaskMetadata | undefined;
  force_new?: boolean | undefined;
}

export interface KataTaskEditPatch {
  title?: string | undefined;
  body?: string | undefined;
  links_delta?: KataTaskLinkDelta | undefined;
}

export interface KataTaskLinkDelta {
  add_related?: string[] | undefined;
  add_blocks?: string[] | undefined;
  add_blocked_by?: string[] | undefined;
  set_parent?: string | null | undefined;
  remove?: string[] | undefined;
}

export type KataTaskMetadataPatch = Record<string, unknown>;
export type KataProjectMetadataPatch = Record<string, unknown | null>;

export interface KataTaskCloseOptions {
  reason?: "done" | "wontfix" | "duplicate" | "superseded" | "audit-no-change" | undefined;
  message?: string | undefined;
  source?: string | undefined;
}

export interface KataTaskMutationResponse {
  changed: boolean;
  issue?: KataTaskSummary | undefined;
  comment?: KataComment | undefined;
  label?: KataTaskLabel | undefined;
  event?: KataTaskEvent | undefined;
  etag?: string | undefined;
}

export interface KataProjectMutationResponse {
  changed: boolean;
  project?: KataProjectSummary | undefined;
  event?: KataTaskEvent | undefined;
  etag?: string | undefined;
}

export interface KataTaskMoveResponse extends KataTaskMutationResponse {
  new_short_id: string;
}

export interface KataTaskIssuesQuery {
  view: KataTaskViewName;
  project_uid?: string | undefined;
  area?: string | undefined;
}

export interface KataTaskAPI {
  instance(): Promise<KataInstanceResponse>;
  projects(): Promise<KataProjectsResponse>;
  createProject(name: string): Promise<KataProjectSummary>;
  renameProject(projectID: number, name: string): Promise<KataProjectSummary>;
  patchProjectMetadata(
    projectID: number,
    actor: string,
    patch: KataProjectMetadataPatch,
    ifMatch: string,
  ): Promise<KataProjectMutationResponse>;
  createIssue(
    projectID: number,
    actor: string,
    draft: KataTaskCreateDraft,
    idempotencyKey?: string | undefined,
  ): Promise<KataTaskMutationResponse>;
  issues(query: KataTaskIssuesQuery): Promise<KataTaskViewResponse>;
  search(filters: KataTaskSearchFilters, opts?: { daemonId?: string }): Promise<KataTaskSearchResponse>;
  issue(uid: string, opts?: { daemonId?: string; pinned?: boolean }): Promise<KataTaskDetail>;
  events(query?: KataTaskEventsQuery): Promise<KataTaskEventsResponse>;
  addComment(target: KataTaskMutationTarget, actor: string, body: string): Promise<KataTaskMutationResponse>;
  addLabel(target: KataTaskMutationTarget, actor: string, label: string): Promise<KataTaskMutationResponse>;
  removeLabel(target: KataTaskMutationTarget, actor: string, label: string): Promise<KataTaskMutationResponse>;
  assignOwner(target: KataTaskMutationTarget, actor: string, owner: string): Promise<KataTaskMutationResponse>;
  unassignOwner(target: KataTaskMutationTarget, actor: string): Promise<KataTaskMutationResponse>;
  setPriority(
    target: KataTaskMutationTarget,
    actor: string,
    priority: number | null,
  ): Promise<KataTaskMutationResponse>;
  closeIssue(
    target: KataTaskMutationTarget,
    actor: string,
    options?: KataTaskCloseOptions,
  ): Promise<KataTaskMutationResponse>;
  reopenIssue(target: KataTaskMutationTarget, actor: string): Promise<KataTaskMutationResponse>;
  editIssue(target: KataTaskMutationTarget, actor: string, patch: KataTaskEditPatch): Promise<KataTaskMutationResponse>;
  patchIssueMetadata(
    target: KataTaskMutationTarget,
    actor: string,
    patch: KataTaskMetadataPatch,
    ifMatch: string,
  ): Promise<KataTaskMutationResponse>;
  moveIssue(
    target: KataTaskMutationTarget,
    actor: string,
    toProjectUID: string,
    ifMatch: string,
  ): Promise<KataTaskMoveResponse>;
  recurrences(projectID: number): Promise<KataRecurrencesResponse>;
  createRecurrence(projectID: number, input: KataCreateRecurrenceInput): Promise<KataRecurrenceResponse>;
  showRecurrence(projectID: number, recurrenceUID: string): Promise<KataRecurrenceResponse>;
  patchRecurrence(
    projectID: number,
    recurrenceUID: string,
    patch: KataPatchRecurrenceInput,
    ifMatch: string,
  ): Promise<KataRecurrenceResponse>;
  deleteRecurrence(projectID: number, recurrenceUID: string, actor: string, ifMatch?: string): Promise<void>;
}
