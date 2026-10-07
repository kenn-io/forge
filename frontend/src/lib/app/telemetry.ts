import { Effect } from "effect";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import type { Page, Route } from "../stores/router.svelte.js";

// Must match the daemon's screen allowlist in internal/telemetry/telemetry.go.
export const SCREEN_NAMES = [
  "activity",
  "actions",
  "repos",
  "repo-browser",
  "pulls",
  "issues",
  "docs",
  "workspaces",
  "terminal",
  "workspace-item",
  "settings",
  "project-intake",
  "design-system",
  "onboarding",
] as const;

export type ScreenName = (typeof SCREEN_NAMES)[number];

// A Record keyed by every non-focus page makes a new route a compile error until it is mapped.
const screenByPage: Record<Exclude<Page, "focus">, ScreenName> = {
  activity: "activity",
  "mobile-activity": "activity",
  actions: "actions",
  repos: "repos",
  "repo-browser": "repo-browser",
  pulls: "pulls",
  "mobile-pulls": "pulls",
  issues: "issues",
  "mobile-issues": "issues",
  docs: "docs",
  workspaces: "workspaces",
  "mobile-workspaces": "workspaces",
  terminal: "terminal",
  "mobile-workspace-terminal": "terminal",
  "mobile-workspace-item": "workspace-item",
  settings: "settings",
  "project-intake": "project-intake",
  "design-system": "design-system",
};

export function visibleScreen(route: Route, ready: boolean, onboarding: boolean): ScreenName | null {
  if (!ready) return null;
  if (onboarding) return "onboarding";
  if (route.page === "focus") return route.itemType === "pr" || route.itemType === "mrs" ? "pulls" : "issues";
  return screenByPage[route.page];
}

/** Reports each screen at most once per UTC day per page; the daemon still dedupes across tabs. */
export function createScreenViewReporter(now: () => Date = () => new Date()) {
  const reportedDay = new Map<ScreenName, string>();
  return Effect.fn("Telemetry.reportScreenViewed")(function* (screen: ScreenName) {
    const day = now().toISOString().slice(0, 10);
    if (reportedDay.get(screen) === day) return;
    yield* executeGeneratedApiRequest("report screen view", (client, signal) =>
      client.SystemService.captureTelemetryEvent(
        { event: "screen_viewed", properties: { screen, surface: "web" } },
        { signal },
      ),
    ).pipe(Effect.timeout("8 seconds"));
    reportedDay.set(screen, day);
  });
}
