import { SvelteMap } from "svelte/reactivity";
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

type WorkspacePage = "terminal" | "mobile-workspace-terminal" | "mobile-workspace-item";
type WorkspaceTab = {
  page: WorkspacePage;
  hostKey: string | undefined;
  id: string;
  name: string;
  item?: RoutedItemRef | undefined;
};
const workspaces = new SvelteMap<symbol, WorkspaceTab>();

export function useWorkspaceTab(read: () => WorkspaceTab | null): void {
  const key = Symbol();
  $effect(() => {
    const workspace = read();
    if (workspace) workspaces.set(key, workspace);
    return () => {
      workspaces.delete(key);
    };
  });
}

export function workspaceTab(route: Route): WorkspaceTab | undefined {
  if (route.page !== "terminal" && route.page !== "mobile-workspace-terminal" && route.page !== "mobile-workspace-item")
    return undefined;
  return [...workspaces.values()].find(
    (workspace) =>
      workspace.page === route.page && workspace.hostKey === route.hostKey && workspace.id === route.workspaceId,
  );
}

export function routeTabTitle(
  route: Route,
  options: {
    pull?: PullDetail | null;
    issue?: IssueDetail | null;
    activitySelection?: RoutedItemRef | null;
    workspace?: WorkspaceTab | undefined;
    worklists?: readonly { id: number; title: string }[] | undefined;
    activityCommit?: { title: string; repoPath: string } | null;
    worklistSelection?: RoutedItemRef | undefined;
  } = {},
): string {
  let selected: RoutedItemRef | undefined;
  if (route.page === "focus" && (route.itemType === "pr" || route.itemType === "issue")) selected = route;
  else if ((route.page === "pulls" || route.page === "queues") && route.selected)
    selected = { ...route.selected, itemType: "pr" };
  else if (route.page === "issues" && route.selected) selected = { ...route.selected, itemType: "issue" };
  else if (route.page === "worklists") selected = options.worklistSelection ?? route.selected;
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
    return `${title} (${selected.repoPath}#${selected.number}) · Forge`;
  }
  if (route.page === "activity" && options.activityCommit) {
    return `${options.activityCommit.title} (${options.activityCommit.repoPath}) · Forge`;
  }
  if (route.page === "focus") return `${route.itemType === "mrs" ? "Pulls" : "Issues"} · Forge`;
  if (
    route.page === "terminal" ||
    route.page === "mobile-workspace-terminal" ||
    route.page === "mobile-workspace-item"
  ) {
    const label = route.page === "mobile-workspace-item" && !options.workspace ? "Linked item" : "Workspace";
    const name =
      options.workspace?.name || `${label} (${route.hostKey ? `${route.hostKey}/` : ""}${route.workspaceId})`;
    return `${name} · Workspaces · Forge`;
  }
  if (route.page === "repo-browser") {
    const parts = [route.path, route.refName, route.refSHA, route.repoPath, "Repos", "Forge"].filter(Boolean);
    return parts.join(" · ");
  }
  if (route.page === "docs") return [route.doc, route.folder, "Docs", "Forge"].filter(Boolean).join(" · ");
  if (route.page === "worklists" && route.importFragment !== undefined) return "Import worklist · Worklists · Forge";
  if ((route.page === "worklists" || route.page === "mobile-worklists") && route.worklistId !== null) {
    const name =
      options.worklists?.find((worklist) => worklist.id === route.worklistId)?.title ||
      `Worklist (${route.worklistId})`;
    return `${name} · Worklists · Forge`;
  }
  return `${pageLabel[route.page]} · Forge`;
}
