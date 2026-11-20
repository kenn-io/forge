import { canonicalProvider } from "../api/provider-routes.js";
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

export type WorkspaceTab = {
  hostKey: string | undefined;
  id: string;
  name: string;
  item?: RoutedItemRef | undefined;
};
export function routeTabTitle(
  route: Route,
  options: {
    pull?: PullDetail | null;
    issue?: IssueDetail | null;
    activitySelection?: RoutedItemRef | null;
    workspace?: WorkspaceTab | undefined;
  } = {},
): string {
  let selected: RoutedItemRef | undefined;
  if (route.page === "focus" && (route.itemType === "pr" || route.itemType === "issue")) selected = route;
  else if ((route.page === "pulls" || route.page === "queues") && route.selected)
    selected = { ...route.selected, itemType: "pr" };
  else if (route.page === "issues" && route.selected) selected = { ...route.selected, itemType: "issue" };
  else if (route.page === "worklists") selected = route.selected;
  else if (route.page === "activity") selected = options.activitySelection ?? undefined;
  else if (route.page === "mobile-workspace-item") selected = options.workspace?.item;

  if (selected) {
    const title =
      selected.itemType === "pr"
        ? pullDetailMatchesRef(options.pull, selected)
          ? options.pull!.merge_request.Title
          : "Pull request"
        : issueDetailMatchesRef(options.issue, selected)
          ? options.issue!.issue.Title
          : "Issue";
    return `${title} (${selected.repoPath}${selected.itemType === "pr" && canonicalProvider(selected.provider) === "gitlab" ? "!" : "#"}${selected.number}) · Forge`;
  }
  if (route.page === "focus") return `${route.itemType === "mrs" ? "Pulls" : "Issues"} · Forge`;
  if (
    route.page === "terminal" ||
    route.page === "mobile-workspace-terminal" ||
    route.page === "mobile-workspace-item"
  ) {
    const label = route.page === "mobile-workspace-item" ? "Linked item" : "Workspace";
    const name = options.workspace?.name;
    return `${name && route.page === "mobile-workspace-item" ? `${label} (${name})` : name || label} · Workspaces · Forge`;
  }
  return `${pageLabel[route.page]} · Forge`;
}
