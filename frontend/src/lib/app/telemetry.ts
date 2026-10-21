import {
  startBrowserTelemetry,
  type BrowserTelemetryOptions,
  type TelemetryReceipt,
  type VisitEvent,
} from "@kenn-io/kit-ui/utils/browser-telemetry";
import { getCaptureTelemetryEventUrl } from "../api/generated/system/system.js";
import { orvalRequest } from "../api/runtime.js";
import type { WorkspaceDetail } from "../components/terminal/workspace-detail.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
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

export const prErrorScope = (provider: string, host: string | undefined, repoPath: string, number: number) =>
  `${canonicalProvider(provider)}:${resolvedPlatformHost(provider, host)}:${repoPath}#${number}`;

const today = () => new Date().toISOString().slice(0, 10);
const coreKey = (action: VisitEvent) =>
  action.event === "pr_viewed"
    ? `pr_viewed:${String(action.properties.provider)}`
    : action.event === "workspace_opened"
      ? `workspace_opened:${String(action.properties.item_type)}`
      : undefined;

/**
 * Keeps the last UTC day a 202 receipt confirmed each core action. Tabs racing at
 * midnight can overwrite each other's day, costing one send the daemon deduplicates.
 * Blocked storage reads as unclaimed and drops writes, leaving the page-local guard.
 */
function actionClaims(storageKey: string) {
  const prefix = `${storageKey}.actions.v1.`;
  const item = (key: string) => prefix + encodeURIComponent(key);
  const has = (key: string, day: string) => {
    try {
      return localStorage.getItem(item(key)) === day;
    } catch {
      return false;
    }
  };
  const add = (key: string, day: string) => {
    try {
      localStorage.setItem(item(key), day);
    } catch {
      /* The page-local guard still holds this claim. */
    }
  };
  // Opting out drops these claims, like kit-ui drops stored screen names.
  const forget = () => {
    try {
      for (const stored of Object.keys(localStorage)) if (stored.startsWith(prefix)) localStorage.removeItem(stored);
    } catch {
      /* Blocked storage holds no claims to drop. */
    }
  };
  return { has, add, forget };
}

const replyStatus = (data: unknown) =>
  data !== null && typeof data === "object" && "status" in data ? data.status : undefined;

/** Hands a 202 receipt's body to `read` once the gate reads it, even when the body is unreadable. */
function observeReceipt(response: TelemetryReceipt, read: (data: unknown) => void): TelemetryReceipt {
  if ("data" in response) {
    read(response.data);
    return response;
  }
  return {
    status: response.status,
    json: async () => {
      let data: unknown;
      try {
        return (data = await response.json());
      } finally {
        read(data);
      }
    },
  };
}

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
  const reportedDay = new Map<string, string>();
  const pending = new Map<string, { action: VisitEvent; day: string }>();
  const pendingOutcomes = new Map<VisitEvent, string>();
  const storageKey = options.storageKey ?? "kit-ui.app-opened.web";
  const stored = actionClaims(storageKey);
  const post = options.post ?? postReceipt;
  const reporter = startBrowserTelemetry({
    route: getCaptureTelemetryEventUrl(),
    surface: "web",
    ...options,
    storageKey,
    post: async (route, event, visitOptions) => {
      const key = coreKey(event);
      const day = today();
      if (key) pending.delete(key);
      else pendingOutcomes.delete(event);
      if (key) {
        reportedDay.set(key, day);
      }
      const release = () => {
        if (key && reportedDay.get(key) === day) reportedDay.delete(key);
      };
      try {
        const response = await post(route, event, visitOptions);
        if (response.status < 200 || response.status >= 300) release();
        if (response.status !== 202) return response;
        // Actions dropped during backoff resend once the gate has applied this receipt.
        return observeReceipt(response, (data) => {
          const status = replyStatus(data);
          if (status === "disabled") {
            stored.forget();
            pending.clear();
            pendingOutcomes.clear();
          } else if (key && (status === "queued" || status === "skipped")) stored.add(key, day);
          setTimeout(flush, 0);
        });
      } catch (error) {
        release();
        throw error;
      }
    },
  });
  // Pending actions stay queued until posted; the post wrapper removes them.
  const claimed = (key: string) => reportedDay.get(key) === today() || stored.has(key, today());
  const send = (action: VisitEvent) => {
    const key = coreKey(action);
    if (key && claimed(key)) return void pending.delete(key);
    reporter.reportVisit(action);
  };
  const flush = () => {
    for (const [key, { action, day }] of pending) {
      if (day === today()) send(action);
      else pending.delete(key);
    }
    for (const [outcome, day] of pendingOutcomes) {
      if (day === today()) send(outcome);
      else pendingOutcomes.delete(outcome);
    }
  };
  const unsubscribe = reporter.onQuietChange((value) => {
    if (!value) flush();
  });
  reporters.set(runtime, {
    reportScreen: (screen) => reporter.reportScreen(screen),
    reportOutcome: (outcome) => {
      const visit: VisitEvent = {
        event: outcome.event,
        properties: { surface: "web", ...(outcome.event === "error_shown" ? { kind: outcome.kind } : {}) },
      };
      pendingOutcomes.set(visit, today());
      send(visit);
    },
    reportAction: (action) => {
      const visit = { event: action.event, properties: { ...action.properties, surface: "web" } };
      pending.set(coreKey(action)!, { action: visit, day: today() });
      send(visit);
    },
  });
  return {
    ...reporter,
    stop: () => {
      reporters.delete(runtime);
      pending.clear();
      pendingOutcomes.clear();
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

export function reportOutcome(runtime: AppRuntime, outcome: Outcome): void {
  reporters.get(runtime)?.reportOutcome(outcome);
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
