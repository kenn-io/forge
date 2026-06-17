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

  const provider = requireSelectedRouteRepoValue(selected.provider, "provider");
  const platformHost = requireSelectedRouteRepoValue(selected.platformHost, "platformHost");
  const repoPath = requireSelectedRouteRepoValue(selected.repoPath, "repoPath");
  return `${provider}|${platformHost}/${repoPath}`;
}
