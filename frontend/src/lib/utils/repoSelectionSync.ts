import type { Route } from "../stores/router.svelte.ts";

type SelectedRouteRepo = {
  provider: string;
  platformHost?: string | undefined;
  repoPath: string;
};

function requireSelectedRouteRepoValue(value: string | undefined, field: string): string {
  if (!value) {
    throw new Error(`selected route is missing ${field}`);
  }
  return value;
}

function repoFilterValue(repo: SelectedRouteRepo, routeLabel = "selected route"): string {
  const provider = requireSelectedRouteRepoValue(repo.provider, "provider");
  const platformHost = repo.platformHost;
  if (!platformHost) {
    throw new Error(`${routeLabel} is missing platformHost`);
  }
  const repoPath = requireSelectedRouteRepoValue(repo.repoPath, "repoPath");
  return `${provider}|${platformHost}/${repoPath}`;
}

// When the URL points at a specific PR or issue, returns the repo key
// (`provider|platformHost/repoPath`) that the global repo filter and dropdown
// should follow. Returns undefined for routes that don't nail down a single
// item. Selected item routes must carry the static provider and host identity
// from the route; missing values are route construction bugs.
export function globalRepoForSelectedRoute(route: Route): string | undefined {
  let selected: SelectedRouteRepo | undefined;
  if (route.page === "pulls" && "selected" in route && route.selected) {
    selected = route.selected;
  } else if (route.page === "issues" && route.selected) {
    selected = route.selected;
  } else if (route.page === "focus" && (route.itemType === "pr" || route.itemType === "issue")) {
    selected = route;
  }
  if (!selected) return undefined;

  return repoFilterValue(selected);
}

// A scoped sync may also use the repository browser route, which identifies a
// repository without selecting a pull request or issue.
export function syncRepoForRoute(route: Route): string | undefined {
  const selectedRepo = globalRepoForSelectedRoute(route);
  if (selectedRepo || route.page !== "repo-browser") return selectedRepo;

  return repoFilterValue(route, "repository browser route");
}
