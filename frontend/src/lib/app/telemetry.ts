import { Effect } from "effect";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import type { WorkspaceDetail } from "../components/terminal/workspace-detail.js";
import { canonicalProvider } from "../api/provider-routes.js";
import type { AppRuntime } from "./runtime.js";
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

// Must match the daemon's provider allowlist in internal/telemetry/telemetry.go.
export const PR_PROVIDERS = ["github", "gitlab", "forgejo", "gitea", "bitbucket"] as const;
// WorkspaceDetail["item_type"] must match the daemon's item_type allowlist in internal/telemetry/telemetry.go.
type CoreAction =
  | { event: "pr_viewed"; properties: { provider: (typeof PR_PROVIDERS)[number] } }
  | { event: "workspace_opened"; properties: { item_type: WorkspaceDetail["item_type"] } };

function createDailyReporter(now: () => Date = () => new Date()) {
  const reportedDay = new Map<string, string>();
  return Effect.fn("Telemetry.reportDaily")(function* (key: string, event: string, properties: Record<string, string>) {
    const day = now().toISOString().slice(0, 10);
    if (reportedDay.get(key) === day) return;
    reportedDay.set(key, day);
    yield* executeGeneratedApiRequest("report daily telemetry", (client, signal) =>
      client.SystemService.captureTelemetryEvent({ event, properties: { ...properties, surface: "web" } }, { signal }),
    ).pipe(
      Effect.timeout("8 seconds"),
      Effect.onExit((exit) =>
        Effect.sync(() => {
          if (exit._tag === "Failure" && reportedDay.get(key) === day) reportedDay.delete(key);
        }),
      ),
    );
  });
}

/** Reports each screen at most once per UTC day per page; the daemon still dedupes across tabs. */
export function createScreenViewReporter(now: () => Date = () => new Date()) {
  const report = createDailyReporter(now);
  return (screen: ScreenName) => report(`screen_viewed:${screen}`, "screen_viewed", { screen });
}

const coreReporters = new WeakMap<AppRuntime, ReturnType<typeof createDailyReporter>>();
function reportCoreAction(runtime: AppRuntime, action: CoreAction): void {
  let report = coreReporters.get(runtime);
  if (!report) {
    report = createDailyReporter();
    coreReporters.set(runtime, report);
  }
  runtime.runCommand(
    report(`${action.event}:${Object.values(action.properties)[0]}`, action.event, action.properties).pipe(
      Effect.ignore,
    ),
    {
      operation: "report core action",
      safeContext: {},
      onFailure: () => {},
    },
  );
}

export function reportPRViewed(runtime: AppRuntime, rawProvider: string): void {
  const provider = PR_PROVIDERS.find((value) => value === canonicalProvider(rawProvider));
  if (provider) reportCoreAction(runtime, { event: "pr_viewed", properties: { provider } });
}

export function reportWorkspaceOpened(runtime: AppRuntime, itemType: WorkspaceDetail["item_type"]): void {
  reportCoreAction(runtime, { event: "workspace_opened", properties: { item_type: itemType } });
}
