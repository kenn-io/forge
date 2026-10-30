import { startBrowserTelemetry, type BrowserTelemetryOptions } from "@kenn-io/kit-ui/utils/browser-telemetry";
import { getCaptureTelemetryEventUrl } from "../api/generated/system/system.js";
import { orvalRequest } from "../api/runtime.js";
import type { WorkspaceDetail } from "../components/terminal/workspace-detail.js";
import { canonicalProvider } from "../api/provider-routes.js";
import { documentVisible } from "../browser/document-visible.svelte.js";
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
  "worklists",
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
  worklists: "worklists",
  "mobile-worklists": "worklists",
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

export type Outcome = { event: "search_empty" } | { event: "error_shown"; kind: CoreAction["event"] };

const coreKey = (action: CoreAction) =>
  action.event === "pr_viewed"
    ? `pr_viewed:${action.properties.provider}`
    : `workspace_opened:${action.properties.item_type}`;

/** Posts through the daemon client, aborting a stalled request or receipt body after 8 seconds. */
const postReceipt: BrowserTelemetryOptions["post"] = async (route, event, visitOptions) => {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 8_000);
  try {
    const response = await orvalRequest(route, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(event),
      keepalive: visitOptions?.keepalive ?? false,
      signal: controller.signal,
    });
    if (response.status !== 202) {
      clearTimeout(timeout);
      return response;
    }
    return {
      status: response.status,
      json: async () => {
        try {
          return await response.json();
        } finally {
          clearTimeout(timeout);
        }
      },
    };
  } catch (error) {
    clearTimeout(timeout);
    throw error;
  }
};

const reporters = new WeakMap<
  AppRuntime,
  {
    reportScreen: (screen: ScreenName) => void;
    reportAction: (action: CoreAction) => void;
    reportOutcome: (outcome: Outcome) => void;
  }
>();

export function startRuntimeTelemetry(
  runtime: AppRuntime,
  options: Partial<Pick<BrowserTelemetryOptions, "storageKey" | "post">> = {},
) {
  // Kit UI holds visits until the permission check and sends each daily key once per UTC day.
  const reporter = startBrowserTelemetry({
    route: getCaptureTelemetryEventUrl(),
    surface: "web",
    storageKey: options.storageKey ?? "kit-ui.app-opened.web",
    post: options.post ?? postReceipt,
  });
  reporters.set(runtime, {
    reportScreen: (screen) => reporter.reportScreen(screen),
    reportOutcome: (outcome) =>
      reporter.reportVisit({
        event: outcome.event,
        properties: { surface: "web", ...(outcome.event === "error_shown" ? { kind: outcome.kind } : {}) },
      }),
    reportAction: (action) =>
      reporter.reportDaily(coreKey(action), {
        event: action.event,
        properties: { ...action.properties, surface: "web" },
      }),
  });
  return {
    ...reporter,
    stop: () => {
      reporters.delete(runtime);
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

export function reportOutcome(runtime: AppRuntime, outcome: Outcome): void {
  reporters.get(runtime)?.reportOutcome(outcome);
}

export function reportWorkspaceOpenError(runtime: AppRuntime): void {
  if (documentVisible()) reportOutcome(runtime, { event: "error_shown", kind: "workspace_opened" });
}

export function searchEmptyReporter(runtime: AppRuntime) {
  let attempt: { lastQuery?: string } = {};
  return {
    textChanged: () => {
      attempt = {};
    },
    submission: (query: string | undefined) => {
      const submitted = attempt;
      return () => {
        if (query === undefined || query === submitted.lastQuery) return;
        submitted.lastQuery = query;
        reportOutcome(runtime, { event: "search_empty" });
      };
    },
  };
}
