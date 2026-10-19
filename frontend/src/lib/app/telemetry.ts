import {
  startBrowserTelemetry,
  type BrowserTelemetryOptions,
  type VisitEvent,
} from "@kenn-io/kit-ui/utils/browser-telemetry";
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

const today = () => new Date().toISOString().slice(0, 10);
const coreKey = (action: VisitEvent) =>
  action.event === "pr_viewed"
    ? `pr_viewed:${String(action.properties.provider)}`
    : action.event === "workspace_opened"
      ? `workspace_opened:${String(action.properties.item_type)}`
      : undefined;

const reporters = new WeakMap<
  AppRuntime,
  {
    reportScreen: (screen: ScreenName) => void;
    reportAction: (action: CoreAction) => void;
  }
>();

export function startRuntimeTelemetry(runtime: AppRuntime, options: BrowserTelemetryOptions) {
  const reportedDay = new Map<string, string>();
  const pending = new Map<string, { action: CoreAction; day: string }>();
  let quiet = true;
  let disabled = false;
  const reporter = startBrowserTelemetry({
    ...options,
    post: async (route, event, visitOptions) => {
      const key = coreKey(event);
      const day = today();
      if (key) reportedDay.set(key, day);
      const release = () => {
        if (key && reportedDay.get(key) === day) reportedDay.delete(key);
      };
      try {
        const response = await options.post(route, event, visitOptions);
        if (response.status < 200 || response.status >= 300) release();
        if (response.status !== 202) return response;
        const data: unknown = "data" in response ? response.data : await response.json().catch(() => undefined);
        if (data && typeof data === "object" && "status" in data && data.status === "disabled") {
          disabled = true;
          pending.clear();
        }
        return { status: response.status, data };
      } catch (error) {
        release();
        throw error;
      }
    },
  });
  const send = (action: CoreAction) => {
    if (reportedDay.get(coreKey(action)!) === today()) return;
    reporter.reportVisit({ event: action.event, properties: { ...action.properties, surface: "web" } });
  };
  const unsubscribe = reporter.onQuietChange((value) => {
    quiet = value;
    if (quiet) pending.clear();
    else {
      disabled = false;
      const actions = [...pending.values()];
      pending.clear();
      for (const { action, day } of actions) if (day === today()) send(action);
    }
  });
  reporters.set(runtime, {
    reportScreen: (screen) => reporter.reportScreen(screen),
    reportAction: (action) => {
      if (disabled) return;
      if (quiet) pending.set(coreKey(action)!, { action, day: today() });
      else send(action);
    },
  });
  return {
    ...reporter,
    stop: () => {
      reporters.delete(runtime);
      pending.clear();
      unsubscribe();
      reporter.stop();
    },
  };
}

export function reportScreen(runtime: AppRuntime, screen: ScreenName): void {
  reporters.get(runtime)?.reportScreen(screen);
}

function reportCoreAction(runtime: AppRuntime, action: CoreAction): void {
  reporters.get(runtime)?.reportAction(action);
}

export function reportPRViewed(runtime: AppRuntime, rawProvider: string): void {
  const provider = PR_PROVIDERS.find((value) => value === canonicalProvider(rawProvider));
  if (provider) reportCoreAction(runtime, { event: "pr_viewed", properties: { provider } });
}

export function reportWorkspaceOpened(runtime: AppRuntime, itemType: WorkspaceDetail["item_type"]): void {
  reportCoreAction(runtime, { event: "workspace_opened", properties: { item_type: itemType } });
}
