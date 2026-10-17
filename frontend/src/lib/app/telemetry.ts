import { Effect } from "effect";
import { ProblemCodes } from "../api/problems.js";
import { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import type { ProviderRouteRef } from "../api/provider-routes.js";
import { providerItemKey } from "../stores/provider-key.js";
import type { WorkspaceDetail } from "../components/terminal/workspace-detail.js";
import type { OwnedAppRuntime } from "./runtime.js";
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

export type CoreAction =
  | { event: "search_empty"; properties: Record<string, never> }
  | { event: "error_shown"; properties: { kind: ErrorKind } }
  | { event: "pr_viewed"; properties: { provider: "github" | "gitlab" | "forgejo" | "gitea" | "bitbucket" } }
  | { event: "workspace_opened"; properties: { item_type: WorkspaceDetail["item_type"] } };

export function prVisitKey(ref: (ProviderRouteRef & { number: number }) | null): string | null {
  return ref ? providerItemKey({ ...ref, platformHost: ref.platformHost ?? "" }) : null;
}

/** Each mounted presentation reports once until its selection changes or it is hidden. */
export function createCoreActionVisitReporter(runtime: Pick<OwnedAppRuntime, "runCommand">) {
  let selection: string | null = null;
  let reported = false;
  return (key: string | null, action?: CoreAction) => {
    if (key !== selection) {
      selection = key;
      reported = false;
    }
    if (key === null || reported || !action) return;
    reported = true;
    reportOutcome(runtime, action);
  };
}

const reportCoreAction = Effect.fn("Telemetry.reportCoreAction")(function* (action: CoreAction) {
  yield* executeGeneratedApiRequest("report core action", (client, signal) =>
    client.SystemService.captureTelemetryEvent(
      { ...action, properties: { ...action.properties, surface: "web" } },
      { signal },
    ),
  ).pipe(Effect.timeout("8 seconds"));
});

export type ErrorKind = "auth" | "not_found" | "rate_limited" | "provider" | "network" | "other";

export function outcomeErrorKind(failure: unknown): ErrorKind {
  if (failure instanceof TransientTransportError) return "network";
  if (failure instanceof ApiProblemError) {
    if (failure.problem.code === ProblemCodes.badRequest && failure.problem.details?.reason === "missing_token")
      return "auth";
    if (failure.problem.code === ProblemCodes.upstreamError) {
      const kind = failure.problem.details?.kind;
      if (kind !== undefined)
        return (
          (["auth", "not_found", "rate_limited", "provider", "network", "other"] as const).find(
            (value) => value === kind,
          ) ?? "other"
        );
      return "provider";
    }
    switch (failure.problem.status) {
      case 401:
      case 403:
        return "auth";
      case 404:
        return "not_found";
      case 429:
        return "rate_limited";
      case 502:
      case 503:
      case 504:
        return "provider";
    }
  }
  return "other";
}

export function reportOutcome(runtime: Pick<OwnedAppRuntime, "runCommand">, action: CoreAction): void {
  runtime.runCommand(reportCoreAction(action), {
    operation:
      action.event === "pr_viewed" || action.event === "workspace_opened"
        ? "telemetry.coreAction"
        : "telemetry.outcome",
    safeContext: { surface: "web" },
    onFailure: () => undefined,
  });
}

export const SEARCH_DEBOUNCE_MS = 150;

/** Only deliberate queries and retries start attempts. Accepted results consume them. */
export function createSearchOutcome(runtime: Pick<OwnedAppRuntime, "runCommand">, visible = () => true) {
  let sequence = 0;
  let pending: { id: number } | undefined;
  return {
    start(query: string | undefined) {
      pending = query?.trim() ? { id: ++sequence } : undefined;
    },
    cancel() {
      pending = undefined;
    },
    reject(id: number | undefined) {
      if (pending?.id === id) pending = undefined;
    },
    current() {
      return pending?.id;
    },
    settle(id: number | undefined, count: number) {
      if (!pending || id !== pending.id) return;
      pending = undefined;
      if (count === 0 && visible() && document.visibilityState === "visible") {
        reportOutcome(runtime, { event: "search_empty", properties: {} });
      }
    },
  };
}
