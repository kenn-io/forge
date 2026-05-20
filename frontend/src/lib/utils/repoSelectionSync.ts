import type { Route } from "../stores/router.svelte.ts";

// When the URL points at a specific PR or issue, returns the repo
// key (`platformHost/owner/name`) that the global repo filter and
// dropdown should follow. Returns undefined for routes that don't
// nail down a single item, or when the selected item has no
// platformHost (which leaves the dropdown without a stable match).
export function globalRepoForSelectedRoute(route: Route): string | undefined {
  let selected:
    | { platformHost?: string | undefined; owner: string; name: string }
    | undefined;
  if (route.page === "pulls" && "selected" in route && route.selected) {
    selected = route.selected;
  } else if (route.page === "issues" && route.selected) {
    selected = route.selected;
  } else if (
    route.page === "focus"
    && (route.itemType === "pr" || route.itemType === "issue")
  ) {
    selected = {
      platformHost: route.platformHost,
      owner: route.owner,
      name: route.name,
    };
  }
  if (!selected || !selected.platformHost) return undefined;
  return `${selected.platformHost}/${selected.owner}/${selected.name}`;
}
