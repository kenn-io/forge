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

export interface KataAreaSummary {
  name: string;
  projects: KataProjectSummary[];
}

export interface KataCurrentView {
  name: KataTaskViewName;
  groups: KataTaskViewResponse["groups"];
  fetched_at?: string | undefined;
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
    filters.status !== "open" ||
    filters.owner.trim() !== "" ||
    filters.label.trim() !== "" ||
    filters.query.trim() !== ""
  );
}

export function defaultKataTaskSearchFilters(): KataTaskSearchFilters {
  return {
    scope: { kind: "all" },
    status: "open",
    owner: "",
    label: "",
    query: "",
  };
}

function projectArea(project: KataProjectSummary): string {
  const area = project.metadata.area?.trim();
  return area && area !== "Unfiled" ? area : "Unfiled";
}

function compareProjectOrder(a: KataProjectSummary, b: KataProjectSummary): number {
  const leftOrder = a.metadata.sidebar_order ?? Number.MAX_SAFE_INTEGER;
  const rightOrder = b.metadata.sidebar_order ?? Number.MAX_SAFE_INTEGER;
  if (leftOrder !== rightOrder) return leftOrder - rightOrder;
  return a.name.localeCompare(b.name);
}

export function deriveKataAreas(projects: readonly KataProjectSummary[]): KataAreaSummary[] {
  const groups = new Map<string, KataProjectSummary[]>();
  for (const project of projects) {
    if (project.metadata.role === "inbox") continue;
    const area = projectArea(project);
    groups.set(area, [...(groups.get(area) ?? []), project]);
  }

  const preferred = ["Personal", "Work", "Unfiled"];
  return [...groups.entries()]
    .sort(([left], [right]) => {
      const leftIndex = preferred.indexOf(left);
      const rightIndex = preferred.indexOf(right);
      if (leftIndex !== -1 || rightIndex !== -1) {
        return (
          (leftIndex === -1 ? Number.MAX_SAFE_INTEGER : leftIndex) -
          (rightIndex === -1 ? Number.MAX_SAFE_INTEGER : rightIndex)
        );
      }
      return left.localeCompare(right);
    })
    .map(([name, areaProjects]) => ({
      name,
      projects: [...areaProjects].sort(compareProjectOrder),
    }));
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
      view: options.view,
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
