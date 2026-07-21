import type { KataTaskEventStreamFrame } from "../../api/kata/eventStream.js";
import type { KataSnapshotIntent } from "../../api/kata/snapshot.js";
import type { KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
import type {
  KataProjectSummary,
  KataTaskSearchFilters,
  KataTaskSummary,
  KataTaskViewName,
  KataTaskViewResponse,
} from "../../api/kata/taskTypes.js";
import { buildKataTaskView } from "../../api/kata/taskViewBuilder.js";

type SnapshotProject = KataWorkspaceSnapshotProjection["projects"][number];
type SnapshotIssue = KataWorkspaceSnapshotProjection["issues"][number];

export interface KataWorkspaceAuthorityRequestOptions {
  daemonID?: string | undefined;
  view: KataTaskViewName;
  filters: KataTaskSearchFilters;
  selectedIssueUID?: string | null | undefined;
  graphSourceUID?: string | null | undefined;
}

export interface KataWorkspaceAuthorityPresentation {
  text: string;
  owner: string;
  label: string;
}

export interface KataWorkspaceAuthorityRequest {
  intent: KataSnapshotIntent;
  presentation: KataWorkspaceAuthorityPresentation;
}

export interface ProjectKataWorkspaceViewOptions {
  view: KataTaskViewName;
  filters: KataTaskSearchFilters;
  snapshot: {
    projects: readonly (KataProjectSummary | SnapshotProject)[];
    fetched_at: string;
  };
  issues: readonly (KataTaskSummary | SnapshotIssue)[];
  today?: string | undefined;
}

type KataWorkspaceFrameSnapshot = Pick<
  KataWorkspaceSnapshotProjection,
  "server_instance_id" | "daemon_id" | "invalidation_epoch" | "event_cursor"
>;

function optionalValue(value: string | null | undefined): string | undefined {
  return value?.trim() || undefined;
}

function hasActiveFilters(filters: KataTaskSearchFilters): boolean {
  return (
    filters.scope.kind === "project" ||
    filters.status !== "open" ||
    filters.owner.trim() !== "" ||
    filters.label.trim() !== "" ||
    filters.query.trim() !== ""
  );
}

export function kataWorkspaceAuthorityRequest(
  options: KataWorkspaceAuthorityRequestOptions,
): KataWorkspaceAuthorityRequest {
  const daemonID = optionalValue(options.daemonID);
  const selectedIssueUID = optionalValue(options.selectedIssueUID);
  const graphSourceUID = optionalValue(options.graphSourceUID);
  const intent: KataSnapshotIntent = {
    ...(daemonID ? { daemon_id: daemonID } : {}),
    scope: options.filters.scope.kind === "project" ? "project" : "global",
    ...(options.filters.scope.kind === "project" ? { project_uid: options.filters.scope.project_uid } : {}),
    authority:
      options.filters.status !== "open" ? options.filters.status : options.view === "logbook" ? "closed" : "open",
    ...(selectedIssueUID ? { selected_issue_uid: selectedIssueUID } : {}),
    ...(graphSourceUID ? { graph_source_uid: graphSourceUID } : {}),
  };
  return {
    intent,
    presentation: {
      text: options.filters.query,
      owner: options.filters.owner,
      label: options.filters.label,
    },
  };
}

export function projectKataWorkspaceView(options: ProjectKataWorkspaceViewOptions): KataTaskViewResponse {
  const issues = options.issues.map((issue) => ({ ...issue }) as KataTaskSummary);
  if (hasActiveFilters(options.filters)) {
    return {
      view: options.filters.scope.kind === "project" ? "all" : options.view,
      groups: issues.length > 0 ? [{ id: "search-results", title: "Results", issues }] : [],
      fetched_at: options.snapshot.fetched_at,
    };
  }

  return buildKataTaskView({
    view: options.view,
    issues,
    projects: options.snapshot.projects.map((project) => ({ ...project }) as KataProjectSummary),
    ...(options.today ? { today: options.today } : {}),
    fetched_at: options.snapshot.fetched_at,
  });
}

export function shouldReloadKataWorkspaceForFrame(
  frame: KataTaskEventStreamFrame,
  snapshot: KataWorkspaceFrameSnapshot | null,
  desiredDaemonID: string | undefined,
): boolean {
  if (!desiredDaemonID || frame.daemon_id !== desiredDaemonID) return false;
  if (!snapshot) return frame.kind === "reset";

  if (frame.kind === "reset") {
    return (
      frame.server_instance_id !== snapshot.server_instance_id ||
      frame.epoch > snapshot.invalidation_epoch ||
      frame.cursor > snapshot.event_cursor
    );
  }

  return (
    frame.server_instance_id === snapshot.server_instance_id &&
    frame.epoch > snapshot.invalidation_epoch &&
    frame.cursor > snapshot.event_cursor
  );
}
