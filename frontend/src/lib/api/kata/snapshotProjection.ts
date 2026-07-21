import type { KataAuthority, KataAuthorityScope, KataWorkspaceSnapshotResponse } from "./snapshot.js";
import {
  normalizeKataEventEnvelope,
  normalizeKataProject,
  normalizeKataReachableGraph,
  normalizeKataTaskDetail,
  normalizeKataTaskSummary,
} from "./taskNormalizers.js";
import type {
  KataProjectSummary,
  KataReachableGraphResponse,
  KataTaskDetail,
  KataTaskEvent,
  KataTaskSummary,
} from "./taskTypes.js";

type JsonObject = Record<string, unknown>;

export type Immutable<T> = T extends (...args: never[]) => unknown
  ? T
  : T extends ReadonlyArray<infer Item>
    ? readonly Immutable<Item>[]
    : T extends object
      ? { readonly [Key in keyof T]: Immutable<T[Key]> }
      : T;

export interface KataSnapshotEnrichmentErrorProjection {
  readonly code: string;
  readonly message: string;
}

export type KataAuthorityIntentProjection =
  | { readonly scope: "global"; readonly authority: KataAuthority }
  | { readonly scope: "project"; readonly project_uid: string; readonly authority: KataAuthority };

export interface KataWorkspaceSnapshotProjection {
  readonly server_instance_id: string;
  readonly daemon_id: string;
  readonly intent: KataAuthorityIntentProjection;
  readonly generation: number;
  readonly invalidation_epoch: number;
  readonly event_cursor: number;
  readonly fetched_at: string;
  readonly projects: readonly Immutable<KataProjectSummary>[];
  readonly member_issue_uids: readonly string[];
  readonly member_issue_uid_set: ReadonlySet<string>;
  readonly issues: readonly Immutable<KataTaskSummary>[];
  readonly selected_issue_uid?: string | undefined;
  readonly selected_detail?: Immutable<KataTaskDetail> | undefined;
  readonly selected_history: readonly Immutable<KataTaskEvent>[];
  readonly graph_source_uid?: string | undefined;
  readonly graph?: Immutable<KataReachableGraphResponse> | undefined;
  readonly graph_fetched_at?: string | undefined;
  readonly enrichment_errors: Readonly<Record<string, KataSnapshotEnrichmentErrorProjection>>;
}

function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function bodyOf(raw: unknown): unknown {
  return isObject(raw) && "body" in raw ? raw.body : raw;
}

function assertAllowedString(value: unknown, allowed: readonly string[], field: string): void {
  if (typeof value !== "string" || !allowed.includes(value)) {
    throw new Error(`Invalid Kata snapshot ${field}: ${String(value)}`);
  }
}

function validateTaskSummary(raw: unknown, field: string): void {
  const issue = isObject(raw) ? raw : {};
  assertAllowedString(issue.status, ["open", "closed"], `${field} status`);
}

function normalizeAuthorityIntent(raw: KataWorkspaceSnapshotResponse["intent"]): KataAuthorityIntentProjection {
  assertAllowedString(raw.scope, ["global", "project"] satisfies readonly KataAuthorityScope[], "intent scope");
  assertAllowedString(
    raw.authority,
    ["open", "ready", "closed", "all"] satisfies readonly KataAuthority[],
    "intent authority",
  );
  const authority = raw.authority as KataAuthority;
  if (raw.scope === "project") {
    const projectUID = raw.project_uid;
    if (!projectUID || projectUID !== projectUID.trim()) {
      throw new Error("Invalid Kata snapshot intent: project scope requires an unpadded project_uid");
    }
    return Object.freeze({ scope: "project", project_uid: projectUID, authority });
  }
  if (raw.project_uid !== undefined) throw new Error("Invalid Kata snapshot intent: global scope forbids project_uid");
  return Object.freeze({ scope: "global", authority });
}

function validateSelectedDetail(raw: unknown): void {
  const detail = bodyOf(raw);
  if (!isObject(detail)) return;

  if (isObject(detail.issue)) validateTaskSummary(detail.issue, "selected detail issue");
  for (const child of arrayValue(detail.children)) validateTaskSummary(child, "selected detail child");
  if (isObject(detail.parent)) validateTaskSummary(detail.parent, "selected detail parent");
  for (const rawLink of arrayValue(detail.links)) {
    const link = isObject(rawLink) ? rawLink : {};
    assertAllowedString(link.type, ["parent", "blocks", "related"], "selected detail relation");
  }
}

function validateGraph(raw: unknown): void {
  const graph = isObject(raw) ? raw : {};
  assertAllowedString(graph.depth, ["full", "1", "2", "3"], "graph depth");
  for (const node of arrayValue(graph.nodes)) validateTaskSummary(node, "graph node");
  for (const rawEdge of arrayValue(graph.edges)) {
    const edge = isObject(rawEdge) ? rawEdge : {};
    assertAllowedString(edge.kind, ["parent", "blocks", "related"], "graph edge kind");
  }
  for (const rawRef of arrayValue(graph.unresolved_refs)) {
    const ref = isObject(rawRef) ? rawRef : {};
    assertAllowedString(ref.side, ["from", "to"], "graph unresolved side");
    assertAllowedString(ref.kind, ["parent", "blocks", "related"], "graph unresolved kind");
  }
}

function immutableCopy<T>(value: T): Immutable<T> {
  if (Array.isArray(value)) {
    return Object.freeze(value.map((item) => immutableCopy(item))) as Immutable<T>;
  }
  if (isObject(value)) {
    const entries = Object.entries(value).map(([key, item]) => [key, immutableCopy(item)]);
    return Object.freeze(Object.fromEntries(entries)) as Immutable<T>;
  }
  return value as Immutable<T>;
}

function immutableSet<T>(values: readonly T[]): ReadonlySet<T> {
  const source = new Set(values);
  let projection: ReadonlySet<T>;
  projection = Object.freeze({
    get size(): number {
      return source.size;
    },
    has(value: T): boolean {
      return source.has(value);
    },
    entries(): SetIterator<[T, T]> {
      return source.entries();
    },
    keys(): SetIterator<T> {
      return source.keys();
    },
    values(): SetIterator<T> {
      return source.values();
    },
    union<U>(other: ReadonlySetLike<U>): Set<T | U> {
      return source.union(other);
    },
    intersection<U>(other: ReadonlySetLike<U>): Set<T & U> {
      return source.intersection(other);
    },
    difference<U>(other: ReadonlySetLike<U>): Set<T> {
      return source.difference(other);
    },
    symmetricDifference<U>(other: ReadonlySetLike<U>): Set<T | U> {
      return source.symmetricDifference(other);
    },
    isSubsetOf(other: ReadonlySetLike<unknown>): boolean {
      return source.isSubsetOf(other);
    },
    isSupersetOf(other: ReadonlySetLike<unknown>): boolean {
      return source.isSupersetOf(other);
    },
    isDisjointFrom(other: ReadonlySetLike<unknown>): boolean {
      return source.isDisjointFrom(other);
    },
    [Symbol.iterator](): SetIterator<T> {
      return source[Symbol.iterator]();
    },
    forEach(callback: (value: T, key: T, set: ReadonlySet<T>) => void, thisArg?: unknown): void {
      source.forEach((value, key) => callback.call(thisArg, value, key, projection));
    },
  });
  return projection;
}

function normalizeSelectedDetail(
  selected: NonNullable<KataWorkspaceSnapshotResponse["enrichment"]["selected_detail"]>,
): Immutable<KataTaskDetail> {
  validateSelectedDetail(selected.detail);
  const detail: KataTaskDetail = {
    ...normalizeKataTaskDetail(selected.detail),
    ...(selected.etag === undefined ? {} : { etag: selected.etag }),
    workspace_target: selected.workspace_target,
  };
  return immutableCopy(detail);
}

function normalizeGraph(
  raw: NonNullable<KataWorkspaceSnapshotResponse["enrichment"]["graph"]>,
  graphFetchedAt: string | undefined,
  projects: readonly Immutable<KataProjectSummary>[],
): Immutable<KataReachableGraphResponse> {
  validateGraph(raw);
  const projectsByID = new Map(projects.map((project) => [project.id, project]));
  const graph = normalizeKataReachableGraph({
    ...raw,
    nodes: (raw.nodes ?? []).map((node) => {
      const project = projectsByID.get(node.project_id);
      return {
        ...node,
        project_uid: node.project_uid || project?.uid,
        project_name: project?.name,
      };
    }),
    edges: raw.edges ?? [],
    unresolved_refs: raw.unresolved_refs ?? [],
    fetched_at: graphFetchedAt ?? "",
  });
  return immutableCopy(graph);
}

export function normalizeKataWorkspaceSnapshot(
  response: KataWorkspaceSnapshotResponse,
): KataWorkspaceSnapshotProjection {
  const intent = normalizeAuthorityIntent(response.intent);
  for (const issue of response.issues ?? []) validateTaskSummary(issue, "authority issue");

  const projects = immutableCopy((response.projects ?? []).map((project) => normalizeKataProject(project)));
  const issues = immutableCopy((response.issues ?? []).map((issue) => normalizeKataTaskSummary(issue)));
  const memberIssueUIDs = Object.freeze([...(response.member_issue_uids ?? [])]);
  const enrichmentErrors: Record<string, KataSnapshotEnrichmentErrorProjection> = {
    ...(response.enrichment.errors ?? {}),
  };
  let selectedDetail: Immutable<KataTaskDetail> | undefined;
  if (response.enrichment.selected_detail) {
    try {
      selectedDetail = normalizeSelectedDetail(response.enrichment.selected_detail);
    } catch {
      enrichmentErrors.detail = {
        code: "invalid_snapshot_enrichment",
        message: "Could not normalize selected task detail.",
      };
    }
  }
  const selectedHistory = immutableCopy(
    (response.enrichment.selected_history ?? []).map((event) => normalizeKataEventEnvelope(event)),
  );
  let graph: Immutable<KataReachableGraphResponse> | undefined;
  if (response.enrichment.graph) {
    try {
      graph = normalizeGraph(response.enrichment.graph, response.enrichment.graph_fetched_at, projects);
    } catch {
      enrichmentErrors.graph = {
        code: "invalid_snapshot_enrichment",
        message: "Could not normalize reachable graph.",
      };
    }
  }

  return Object.freeze({
    server_instance_id: response.server_instance_id,
    daemon_id: response.daemon_id,
    intent,
    generation: response.generation,
    invalidation_epoch: response.invalidation_epoch,
    event_cursor: response.event_cursor,
    fetched_at: response.fetched_at,
    projects,
    member_issue_uids: memberIssueUIDs,
    member_issue_uid_set: immutableSet(memberIssueUIDs),
    issues,
    ...(response.enrichment.selected_issue_uid ? { selected_issue_uid: response.enrichment.selected_issue_uid } : {}),
    ...(selectedDetail ? { selected_detail: selectedDetail } : {}),
    selected_history: selectedHistory,
    ...(response.graph_source_uid ? { graph_source_uid: response.graph_source_uid } : {}),
    ...(graph ? { graph } : {}),
    ...(graph && response.enrichment.graph_fetched_at
      ? { graph_fetched_at: response.enrichment.graph_fetched_at }
      : {}),
    enrichment_errors: immutableCopy(enrichmentErrors),
  });
}
