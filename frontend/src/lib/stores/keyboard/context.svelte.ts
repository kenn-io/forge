import { getRoute, getPage, getDetailTab, isDiffView, getSelectedPRFromRoute, type Route } from "../router.svelte.js";
import type { Context } from "./types.js";
import type { PullSelection } from "@middleman/ui/stores/pulls";
import type { IssueSelection } from "@middleman/ui/stores/issues";

interface SelectionSources {
  pulls: { getSelectedPR: () => PullSelection | null };
  issues: { getSelectedIssue: () => IssueSelection | null };
}

function sidebarTargetAvailable(route: Route): boolean {
  if (route.page !== "pulls" && route.page !== "issues") return true;
  return document.querySelector(".sidebar") !== null;
}

export function buildContext(stores: SelectionSources): Context {
  const route = getRoute();
  return {
    page: getPage(),
    route,
    // Mirror App.svelte's render path: route-derived selection wins so PR
    // detail navigation via deep-link or back/forward keeps actions enabled
    // before the pulls store has hydrated.
    selectedPR: getSelectedPRFromRoute() ?? stores.pulls.getSelectedPR(),
    selectedIssue: stores.issues.getSelectedIssue(),
    isDiffView: isDiffView(),
    detailTab: getDetailTab(),
    sidebarTargetAvailable: sidebarTargetAvailable(route),
  };
}
