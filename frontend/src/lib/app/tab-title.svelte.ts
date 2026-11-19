import type { IssueDetail, PullDetail } from "../api/types.js";
import { issueDetailMatchesRef, pullDetailMatchesRef } from "../components/detail/detail-match.js";
import type { RoutedItemRef } from "../routes.js";
import type { Page, Route } from "../stores/router.svelte.js";

const pageLabel: Record<Exclude<Page, "focus">, string> = {
  activity: "Activity",
  "mobile-activity": "Activity",
  actions: "Actions",
  repos: "Repos",
  "repo-browser": "Repos",
  pulls: "Pulls",
  "mobile-pulls": "Pulls",
  issues: "Issues",
  "mobile-issues": "Issues",
  docs: "Docs",
  workspaces: "Workspaces",
  "mobile-workspaces": "Workspaces",
  terminal: "Workspaces",
  "mobile-workspace-terminal": "Workspaces",
  "mobile-workspace-item": "Workspaces",
  settings: "Settings",
  "project-intake": "Add project",
  "design-system": "Design system",
  worklists: "Worklists",
  "mobile-worklists": "Worklists",
  queues: "Queues",
};

let workspaceTitles = $state<Record<string, string>>({});
const workspaceKey = (hostKey: string | undefined, id: string) => JSON.stringify([hostKey ?? null, id]);

export function publishWorkspaceTitle(hostKey: string | undefined, id: string, name: string | null): void {
  const key = workspaceKey(hostKey, id);
  if (name === null && workspaceTitles[key] !== undefined) {
    const { [key]: _, ...rest } = workspaceTitles;
    workspaceTitles = rest;
  } else if (name !== null && workspaceTitles[key] !== name) {
    workspaceTitles = { ...workspaceTitles, [key]: name };
  }
}

export function workspaceTitle(hostKey: string | undefined, id: string): string | undefined {
  return workspaceTitles[workspaceKey(hostKey, id)];
}

export function routeTabTitle(
  route: Route,
  options: {
    pull?: PullDetail | null;
    issue?: IssueDetail | null;
    activitySelection?: RoutedItemRef | null;
    workspaceName?: string | undefined;
  } = {},
): string {
  let selected: RoutedItemRef | undefined;
  if (route.page === "focus" && (route.itemType === "pr" || route.itemType === "issue")) selected = route;
  else if ((route.page === "pulls" || route.page === "queues") && route.selected)
    selected = { ...route.selected, itemType: "pr" };
  else if (route.page === "issues" && route.selected) selected = { ...route.selected, itemType: "issue" };
  else if (route.page === "worklists") selected = route.selected;
  else if (route.page === "activity") selected = options.activitySelection ?? undefined;

  if (selected) {
    const title =
      selected.itemType === "pr"
        ? pullDetailMatchesRef(options.pull, selected)
          ? options.pull!.merge_request.Title
          : "Pull request"
        : issueDetailMatchesRef(options.issue, selected)
          ? options.issue!.issue.Title
          : "Issue";
    return `${title} (${selected.repoPath}#${selected.number}) · Forge`;
  }
  if (route.page === "focus") return `${route.itemType === "mrs" ? "Pulls" : "Issues"} · Forge`;
  if (
    route.page === "terminal" ||
    route.page === "mobile-workspace-terminal" ||
    route.page === "mobile-workspace-item"
  ) {
    return `${options.workspaceName || "Workspace"} · Workspaces · Forge`;
  }
  if (route.page === "repo-browser") return `${route.repoPath} · Repos · Forge`;
  return `${pageLabel[route.page]} · Forge`;
}
