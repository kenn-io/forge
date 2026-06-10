import type {
  KataComment,
  KataInstanceResponse,
  KataLinkPeer,
  KataProjectMetadata,
  KataProjectSummary,
  KataProjectsResponse,
  KataRecurrence,
  KataRecurrenceResponse,
  KataRecurrencesResponse,
  KataTaskChecklistItem,
  KataTaskDetail,
  KataTaskEvent,
  KataTaskEventsResponse,
  KataTaskLabel,
  KataTaskLink,
  KataTaskRef,
  KataTaskSummary,
  KataTaskViewName,
  KataTaskViewResponse,
} from "./taskTypes.js";

type JsonObject = Record<string, unknown>;

const viewTitles: Record<KataTaskViewName, string> = {
  inbox: "Inbox",
  today: "Today",
  upcoming: "Upcoming",
  deadlines: "Deadlines",
  all: "All Open",
  logbook: "Logbook",
};

function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function bodyOf(raw: unknown): unknown {
  return isObject(raw) && "body" in raw ? raw.body : raw;
}

function stringValue(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function numberValue(value: unknown, fallback = 0): number {
  return typeof value === "number" ? value : fallback;
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === "number" ? value : undefined;
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function stringArrayValue(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((item): item is string => typeof item === "string");
  }
  if (typeof value !== "string" || value.trim() === "") return [];
  try {
    const parsed: unknown = JSON.parse(value);
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === "string") : [];
  } catch {
    return [];
  }
}

function parseObject(value: unknown): JsonObject {
  if (isObject(value)) return { ...value };
  if (typeof value !== "string" || value.trim() === "") return {};

  try {
    const parsed: unknown = JSON.parse(value);
    return isObject(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function isChecklistItem(value: unknown): value is KataTaskChecklistItem {
  return (
    isObject(value) && typeof value.id === "string" && typeof value.text === "string" && typeof value.done === "boolean"
  );
}

function sanitizeIssueMetadata(value: unknown): JsonObject {
  const metadata = parseObject(value);
  const out: JsonObject = {};
  for (const [key, raw] of Object.entries(metadata)) {
    switch (key) {
      case "scheduled_on":
      case "deadline_on":
      case "area":
      case "timezone":
        if (typeof raw === "string") out[key] = raw;
        break;
      case "today_bucket":
        if (raw === "day" || raw === "evening") out[key] = raw;
        break;
      case "checklist": {
        if (!Array.isArray(raw)) break;
        out[key] = raw.filter(isChecklistItem).map((item) => ({ ...item }));
        break;
      }
      default:
        out[key] = raw;
    }
  }
  return out;
}

function sanitizeProjectMetadata(value: unknown): KataProjectMetadata {
  const metadata = parseObject(value);
  const out: JsonObject = {};
  for (const [key, raw] of Object.entries(metadata)) {
    switch (key) {
      case "area":
      case "icon":
      case "timezone":
      case "role":
        if (typeof raw === "string") out[key] = raw;
        break;
      case "sidebar_order":
        if (typeof raw === "number") out[key] = raw;
        break;
      default:
        out[key] = raw;
    }
  }
  return out as KataProjectMetadata;
}

function normalizeStatus(value: unknown): "open" | "closed" {
  return value === "closed" ? "closed" : "open";
}

function normalizeClosedReason(
  value: unknown,
): "done" | "wontfix" | "duplicate" | "superseded" | "audit-no-change" | undefined {
  if (
    value === "done" ||
    value === "wontfix" ||
    value === "duplicate" ||
    value === "superseded" ||
    value === "audit-no-change"
  ) {
    return value;
  }
  return undefined;
}

function normalizeViewName(value: unknown, fallback: KataTaskViewName = "today"): KataTaskViewName {
  if (
    value === "inbox" ||
    value === "today" ||
    value === "upcoming" ||
    value === "deadlines" ||
    value === "all" ||
    value === "logbook"
  ) {
    return value;
  }
  return fallback;
}

function normalizeLinkPeer(raw: unknown): KataLinkPeer {
  const peer = isObject(raw) ? raw : {};
  return {
    uid: stringValue(peer.uid),
    short_id: stringValue(peer.short_id),
  };
}

function normalizeTaskLabel(raw: unknown): KataTaskLabel {
  const label = isObject(raw) ? raw : {};
  return {
    issue_id: numberValue(label.issue_id),
    label: stringValue(label.label),
    author: stringValue(label.author),
    created_at: stringValue(label.created_at),
  };
}

function normalizeComment(raw: unknown): KataComment {
  const comment = isObject(raw) ? raw : {};
  return {
    id: numberValue(comment.id),
    issue_id: numberValue(comment.issue_id),
    author: stringValue(comment.author),
    body: stringValue(comment.body),
    created_at: stringValue(comment.created_at),
  };
}

function normalizeTaskLink(raw: unknown): KataTaskLink {
  const link = isObject(raw) ? raw : {};
  return {
    id: numberValue(link.id),
    project_id: numberValue(link.project_id),
    from: normalizeLinkPeer(link.from),
    to: normalizeLinkPeer(link.to),
    type: link.type === "parent" || link.type === "related" ? link.type : "blocks",
    author: stringValue(link.author),
    created_at: stringValue(link.created_at),
  };
}

function normalizeTaskRef(raw: unknown): KataTaskRef | undefined {
  if (!isObject(raw)) return undefined;
  return {
    uid: stringValue(raw.uid),
    short_id: stringValue(raw.short_id),
    qualified_id: stringValue(raw.qualified_id),
    title: stringValue(raw.title),
    status: normalizeStatus(raw.status),
  };
}

function normalizeLabels(rawLabels: unknown, labelRows?: KataTaskLabel[]): string[] {
  if (Array.isArray(rawLabels)) {
    return rawLabels.filter((label): label is string => typeof label === "string");
  }
  return labelRows?.map((label) => label.label).filter(Boolean) ?? [];
}

export function normalizeKataTaskSummary(raw: unknown, labelRows?: KataTaskLabel[]): KataTaskSummary {
  const issue = isObject(raw) ? raw : {};
  return {
    id: numberValue(issue.id),
    uid: stringValue(issue.uid),
    project_id: numberValue(issue.project_id),
    short_id: stringValue(issue.short_id),
    qualified_id: stringValue(issue.qualified_id),
    title: stringValue(issue.title),
    body: optionalString(issue.body),
    status: normalizeStatus(issue.status),
    project_uid: stringValue(issue.project_uid),
    project_name: stringValue(issue.project_name),
    metadata: sanitizeIssueMetadata(issue.metadata),
    revision: numberValue(issue.revision),
    owner: optionalString(issue.owner),
    author: stringValue(issue.author),
    priority: optionalNumber(issue.priority),
    labels: normalizeLabels(issue.labels, labelRows),
    parent_short_id: optionalString(issue.parent_short_id),
    blocks: arrayValue(issue.blocks).map(normalizeLinkPeer),
    blocked_by: arrayValue(issue.blocked_by).map(normalizeLinkPeer),
    related: arrayValue(issue.related).map(normalizeLinkPeer),
    child_counts: isObject(issue.child_counts)
      ? { open: numberValue(issue.child_counts.open), total: numberValue(issue.child_counts.total) }
      : undefined,
    recurrence_id: optionalNumber(issue.recurrence_id),
    occurrence_key: optionalString(issue.occurrence_key),
    created_at: stringValue(issue.created_at),
    updated_at: stringValue(issue.updated_at),
    closed_reason: normalizeClosedReason(issue.closed_reason),
    closed_at: optionalString(issue.closed_at),
    deleted_at: optionalString(issue.deleted_at),
  };
}

export function normalizeKataProject(raw: unknown): KataProjectSummary {
  const project = isObject(raw) ? raw : {};
  const stats = isObject(project.stats) ? project.stats : {};
  return {
    id: numberValue(project.id),
    uid: stringValue(project.uid),
    name: stringValue(project.name),
    metadata: sanitizeProjectMetadata(project.metadata),
    revision: optionalNumber(project.revision),
    created_at: optionalString(project.created_at),
    deleted_at: optionalString(project.deleted_at),
    open_count: numberValue(project.open_count, numberValue(stats.open)),
  };
}

export function normalizeKataProjectList(raw: unknown): KataProjectsResponse {
  const body = bodyOf(raw);
  const source = isObject(body) ? body : {};
  return {
    projects: arrayValue(source.projects).map(normalizeKataProject),
    fetched_at: stringValue(source.fetched_at),
  };
}

export function normalizeKataInstance(raw: unknown): KataInstanceResponse {
  const body = bodyOf(raw);
  const source = isObject(body) ? body : {};
  return {
    instance_uid: stringValue(source.instance_uid),
    version: stringValue(source.version),
    schema_version: numberValue(source.schema_version),
  };
}

export function normalizeKataTaskList(raw: unknown, options: { view?: KataTaskViewName } = {}): KataTaskViewResponse {
  const body = bodyOf(raw);
  const source = isObject(body) ? body : {};
  const view = normalizeViewName(source.view, options.view ?? "today");

  if (Array.isArray(source.groups)) {
    return {
      view,
      fetched_at: stringValue(source.fetched_at),
      groups: source.groups.map((rawGroup) => {
        const group = isObject(rawGroup) ? rawGroup : {};
        return {
          id: stringValue(group.id),
          title: stringValue(group.title),
          issues: arrayValue(group.issues).map((issue) => normalizeKataTaskSummary(issue)),
        };
      }),
    };
  }

  return {
    view,
    fetched_at: stringValue(source.fetched_at),
    groups: [
      {
        id: view,
        title: viewTitles[view],
        issues: arrayValue(source.issues).map((issue) => normalizeKataTaskSummary(issue)),
      },
    ],
  };
}

export function normalizeKataTaskDetail(raw: unknown): KataTaskDetail {
  const body = bodyOf(raw);
  const source = isObject(body) ? body : {};
  const labels = arrayValue(source.labels).map(normalizeTaskLabel);
  const issue = normalizeKataTaskSummary(source.issue, labels);

  return {
    issue: {
      ...issue,
      body: stringValue(isObject(source.issue) ? source.issue.body : undefined),
    },
    comments: arrayValue(source.comments).map(normalizeComment),
    labels,
    links: arrayValue(source.links).map(normalizeTaskLink),
    parent: normalizeTaskRef(source.parent),
    children: arrayValue(source.children).map((child) => normalizeKataTaskSummary(child)),
  };
}

export function normalizeKataRecurrence(raw: unknown): KataRecurrence {
  const recurrence = isObject(raw) ? raw : {};
  return {
    id: numberValue(recurrence.id),
    uid: stringValue(recurrence.uid),
    project_id: numberValue(recurrence.project_id),
    rrule: stringValue(recurrence.rrule),
    dtstart: stringValue(recurrence.dtstart),
    timezone: stringValue(recurrence.timezone),
    template_title: stringValue(recurrence.template_title),
    template_body: stringValue(recurrence.template_body),
    template_owner: optionalString(recurrence.template_owner),
    template_priority: optionalNumber(recurrence.template_priority),
    template_labels: stringArrayValue(recurrence.template_labels),
    template_metadata: sanitizeIssueMetadata(recurrence.template_metadata),
    next_occurrence_key: optionalString(recurrence.next_occurrence_key),
    last_materialized_uid: optionalString(recurrence.last_materialized_uid),
    author: stringValue(recurrence.author),
    revision: numberValue(recurrence.revision),
    created_at: stringValue(recurrence.created_at),
    updated_at: stringValue(recurrence.updated_at),
    deleted_at: optionalString(recurrence.deleted_at),
  };
}

export function normalizeKataRecurrences(raw: unknown): KataRecurrencesResponse {
  const body = bodyOf(raw);
  const source = isObject(body) ? body : {};
  return {
    recurrences: arrayValue(source.recurrences).map(normalizeKataRecurrence),
    fetched_at: stringValue(source.fetched_at),
  };
}

export function normalizeKataRecurrenceResponse(raw: unknown, etag?: string): KataRecurrenceResponse {
  const body = bodyOf(raw);
  const source = isObject(body) ? body : {};
  const out: KataRecurrenceResponse = {
    recurrence: normalizeKataRecurrence(source.recurrence),
  };
  if (etag !== undefined && etag !== "") out.etag = etag;
  if (typeof source.changed === "boolean") out.changed = source.changed;
  return out;
}

function normalizePayload(raw: unknown): Record<string, unknown> | undefined {
  if (raw === undefined || raw === null) return undefined;
  return parseObject(raw);
}

function normalizeEvent(raw: unknown): KataTaskEvent {
  const event = isObject(raw) ? raw : {};
  return {
    event_id: numberValue(event.event_id),
    event_uid: stringValue(event.event_uid),
    origin_instance_uid: stringValue(event.origin_instance_uid),
    type: stringValue(event.type),
    project_id: numberValue(event.project_id),
    project_uid: stringValue(event.project_uid),
    project_name: stringValue(event.project_name),
    issue_id: optionalNumber(event.issue_id),
    issue_uid: optionalString(event.issue_uid),
    issue_short_id: optionalString(event.issue_short_id),
    related_issue_id: optionalNumber(event.related_issue_id),
    related_issue_uid: optionalString(event.related_issue_uid),
    related_issue_short_id: optionalString(event.related_issue_short_id),
    actor: stringValue(event.actor),
    payload: normalizePayload(event.payload),
    created_at: stringValue(event.created_at),
  };
}

export function normalizeKataEvents(raw: unknown): KataTaskEventsResponse {
  const body = bodyOf(raw);
  const source = isObject(body) ? body : {};
  return {
    reset_required: source.reset_required === true,
    reset_after_id: optionalNumber(source.reset_after_id),
    events: arrayValue(source.events).map(normalizeEvent),
    next_after_id: numberValue(source.next_after_id),
  };
}
