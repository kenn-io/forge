import {
  buildRoutedItemRoute,
  type IssueRouteRef,
  type NumberedRouteItemRef,
  type RepositoryRouteRef,
  type RoutedItemRef,
} from "@middleman/ui/routes";
import { canonicalProvider } from "@middleman/ui/api/provider-routes";

export type RepoRef = RepositoryRouteRef;
export type NumberedItemRef = NumberedRouteItemRef;
export type HostedItemRef = IssueRouteRef;
export type RoutableItemRef = RoutedItemRef;

export type EmbedEmptyReason = "noSelection" | "noRepo" | "noWorkspace";

export type EmbedDetailTab = "pr" | "issue" | "reviews";

export type Route =
  | { page: "activity" }
  | { page: "mobile-activity" }
  | { page: "mobile-pulls" }
  | { page: "mobile-issues" }
  | { page: "design-system" }
  | { page: "repos" }
  | { page: "workspaces" }
  | {
      page: "pulls";
      view: "list" | "board";
      selected?: NumberedItemRef;
      tab?: "files";
    }
  | { page: "issues"; selected?: HostedItemRef }
  | { page: "settings" }
  | {
      page: "focus";
      itemType: "pr";
      tab?: "files";
    } & NumberedItemRef
  | ({ page: "focus" } & IssueRouteRef & { itemType: "issue" })
  | { page: "focus"; itemType: "mrs"; repo?: string }
  | { page: "focus"; itemType: "issues"; repo?: string }
  | { page: "reviews"; jobId?: number }
  | { page: "terminal"; workspaceId: string }
  // Embed-targetable workspace surfaces. Hosts mount these
  // routes to render a single component of the workspaces UX
  // (list, terminal, per-item detail, empty placeholder, the
  // empty-registry First Run Panel, or a per-project card)
  // without the surrounding app chrome.
  | { page: "embed-workspace-list" }
  | { page: "embed-workspace-terminal"; workspaceId: string }
  | {
      page: "embed-workspace-detail";
      provider: string;
      itemType: "pr" | "issue";
      platformHost: string;
      repoPath: string;
      owner: string;
      name: string;
      number: number;
      branch?: string;
      tab?: EmbedDetailTab;
    }
  | { page: "embed-workspace-empty"; reason: EmbedEmptyReason }
  | { page: "embed-workspace-first-run" }
  | { page: "embed-workspace-project"; projectId: string };

export type Page = Route["page"];

import {
  isEmbedded,
  getOnNavigate,
  getOnRouteChange,
  getUIConfig as getEmbedUIConfig,
  getHost,
  getInitialRoute,
} from "./embed-config.svelte.js";

// Runtime base path injected by the Go server (e.g., "/" or "/middleman/").
const rawBase = window.__BASE_PATH__ ?? "/";
const basePrefix = rawBase === "/" ? "" : rawBase.replace(/\/$/, "");

export function getBasePath(): string {
  return rawBase;
}

function stripBase(path: string): string {
  if (basePrefix && path.startsWith(basePrefix)) {
    return path.slice(basePrefix.length) || "/";
  }
  return path;
}

function currentLocationPath(): string {
  return window.location.pathname + window.location.search;
}

const defaultPlatformHosts: Record<string, string> = {
  github: "github.com",
  gitlab: "gitlab.com",
  forgejo: "codeberg.org",
  gitea: "gitea.com",
};

function defaultPlatformHost(provider: string): string | undefined {
  return defaultPlatformHosts[canonicalProvider(provider)];
}

function decodeRouteSegment(segment: string): string | undefined {
  try {
    return decodeURIComponent(segment);
  } catch {
    return undefined;
  }
}

function parseProviderNumberedPath(
  parts: string[],
  start: number,
  platformHost?: string | undefined,
): NumberedItemRef | undefined {
  if (parts.length < start + 4) return undefined;
  const provider = decodeRouteSegment(parts[start] ?? "")?.trim();
  const owner = decodeRouteSegment(parts[start + 1] ?? "")?.replace(/^\/+|\/+$/g, "");
  const name = decodeRouteSegment(parts[start + 2] ?? "")?.replace(/^\/+|\/+$/g, "");
  const numberText = decodeRouteSegment(parts[start + 3] ?? "");
  if (!provider || !owner || !name || !numberText) return undefined;

  const number = parseInt(numberText, 10);
  if (!Number.isFinite(number) || number <= 0) return undefined;

  const resolvedPlatformHost = platformHost ?? defaultPlatformHost(provider);
  const ref: NumberedItemRef = {
    provider,
    owner,
    name,
    number,
    repoPath: `${owner}/${name}`,
    ...(resolvedPlatformHost && { platformHost: resolvedPlatformHost }),
  };
  return ref;
}

function parseHostProviderNumberedPath(
  parts: string[],
  kind: "pulls" | "issues",
  start = 0,
): NumberedItemRef | undefined {
  if (parts[start] === kind) {
    return parseProviderNumberedPath(parts, start + 1);
  }
  if (parts[start] === "host" && parts[start + 2] === kind) {
    const platformHost = decodeRouteSegment(parts[start + 1] ?? "")?.trim();
    if (!platformHost) return undefined;
    return parseProviderNumberedPath(parts, start + 3, platformHost);
  }
  return undefined;
}

function inferLegacyEmbedProvider(platformHost: string): string {
  return platformHost.toLowerCase().includes("gitlab") ? "gitlab" : "github";
}

function splitRepoPath(repoPath: string): { owner: string; name: string } | undefined {
  const pathParts = repoPath.replace(/^\/+|\/+$/g, "").split("/").filter(Boolean);
  if (pathParts.length < 2) return undefined;
  return {
    owner: pathParts.slice(0, -1).join("/"),
    name: pathParts[pathParts.length - 1]!,
  };
}

function parseRoute(fullPath: string): Route {
  const qIdx = fullPath.indexOf("?");
  const pathname = qIdx >= 0 ? fullPath.slice(0, qIdx) : fullPath;
  const search = qIdx >= 0 ? fullPath.slice(qIdx + 1) : "";
  const path = stripBase(pathname).replace(/\/+$/, "") || "/";
  const parts = path.split("/").filter(Boolean);
  if (path === "/m" || path === "/m/activity") {
    return { page: "mobile-activity" };
  }
  if (path === "/m/pulls") {
    return { page: "mobile-pulls" };
  }
  if (path === "/m/issues") {
    return { page: "mobile-issues" };
  }
  if (path.startsWith("/focus/")) {
    if (path === "/focus/mrs") {
      const sp = new URLSearchParams(search);
      const repo = sp.get("repo");
      const r: Route = { page: "focus", itemType: "mrs" };
      if (repo) r.repo = repo;
      return r;
    }
    if (path === "/focus/issues") {
      const sp = new URLSearchParams(search);
      const repo = sp.get("repo");
      const r: Route = { page: "focus", itemType: "issues" };
      if (repo) r.repo = repo;
      return r;
    }
    const pull = parseHostProviderNumberedPath(parts, "pulls", 1);
    const isPullFiles = parts[parts.length - 1] === "files";
    const focusPullLength = parts[1] === "host" ? 8 : 6;
    if (pull && (parts.length === focusPullLength || (isPullFiles && parts.length === focusPullLength + 1))) {
      return {
        page: "focus",
        itemType: "pr",
        ...pull,
        ...(isPullFiles && { tab: "files" as const }),
      };
    }
    const issue = parseHostProviderNumberedPath(parts, "issues", 1);
    if (issue && parts.length === (parts[1] === "host" ? 8 : 6)) {
      return {
        page: "focus",
        itemType: "issue",
        ...issue,
      };
    }
  }
  if (path === "/design-system") {
    return { page: "design-system" };
  }
  if (path.startsWith("/pulls")) {
    const rest = path.slice("/pulls".length);
    if (rest !== "" && rest !== "/board") {
      const selected = parseHostProviderNumberedPath(parts, "pulls");
      const isFiles = parts[parts.length - 1] === "files";
      if (selected && (parts.length === 5 || (parts.length === 6 && isFiles))) {
        return {
          page: "pulls",
          view: "list",
          selected,
          ...(isFiles && { tab: "files" as const }),
        };
      }
    }
    if (rest === "/board") return { page: "pulls", view: "board" };
    return { page: "pulls", view: "list" };
  }
  if (path === "/repos") {
    return { page: "repos" };
  }
  if (path === "/settings" && !isEmbedded()) return { page: "settings" };
  if (path.startsWith("/issues")) {
    if (path !== "/issues") {
      const selected = parseHostProviderNumberedPath(parts, "issues");
      if (selected && parts.length === 5) {
        return {
          page: "issues",
          selected,
        };
      }
    }
    return { page: "issues" };
  }
  if (path.startsWith("/host/")) {
    const pull = parseHostProviderNumberedPath(parts, "pulls");
    const isPullFiles = parts[parts.length - 1] === "files";
    if (pull && (parts.length === 7 || (parts.length === 8 && isPullFiles))) {
      return {
        page: "pulls",
        view: "list",
        selected: pull,
        ...(isPullFiles && { tab: "files" as const }),
      };
    }
    const issue = parseHostProviderNumberedPath(parts, "issues");
    if (issue && parts.length === 7) {
      return {
        page: "issues",
        selected: issue,
      };
    }
  }
  const reviewsMatch = path.match(/^\/reviews(?:\/(\d+))?$/);
  if (reviewsMatch) {
    if (reviewsMatch[1]) {
      return {
        page: "reviews",
        jobId: parseInt(reviewsMatch[1], 10),
      };
    }
    return { page: "reviews" };
  }
  const terminalMatch = path.match(/^\/terminal\/([^/]+)$/);
  if (terminalMatch) {
    return {
      page: "terminal",
      workspaceId: terminalMatch[1]!,
    };
  }
  // Embed routes must be matched before the generic /workspaces
  // catch-all so they don't fall back to the standalone page.
  if (path === "/workspaces/embed/list") {
    return { page: "embed-workspace-list" };
  }
  const embedTerminalMatch = path.match(
    /^\/workspaces\/embed\/terminal(?:\/([^/]+))?$/,
  );
  if (embedTerminalMatch) {
    return {
      page: "embed-workspace-terminal",
      workspaceId: embedTerminalMatch[1] ?? "",
    };
  }
  const embedDetailMatch = path.match(
    /^\/workspaces\/embed\/detail\/([^/]+)\/(pr|issue)\/([^/]+)\/(\d+)$/,
  );
  if (embedDetailMatch) {
    const sp = new URLSearchParams(search);
    const repoPath = sp.get("repo_path")?.trim();
    const repo = repoPath ? splitRepoPath(repoPath) : undefined;
    if (!repoPath || !repo) {
      return { page: "workspaces" };
    }
    const branch = sp.get("branch") ?? undefined;
    const tabParam = sp.get("tab");
    const tab: EmbedDetailTab | undefined =
      tabParam === "pr" || tabParam === "issue" || tabParam === "reviews"
        ? tabParam
        : undefined;
    const r: Route = {
      page: "embed-workspace-detail",
      provider: embedDetailMatch[1]!,
      itemType: embedDetailMatch[2] as "pr" | "issue",
      platformHost: embedDetailMatch[3]!,
      repoPath,
      owner: repo.owner,
      name: repo.name,
      number: parseInt(embedDetailMatch[4]!, 10),
    };
    if (branch) r.branch = branch;
    if (tab) r.tab = tab;
    return r;
  }
  const legacyProviderEmbedDetailMatch = path.match(
    /^\/workspaces\/embed\/detail\/([^/]+)\/(pr|issue)\/([^/]+)\/([^/]+)\/([^/]+)\/(\d+)$/,
  );
  if (legacyProviderEmbedDetailMatch) {
    const sp = new URLSearchParams(search);
    const branch = sp.get("branch") ?? undefined;
    const tabParam = sp.get("tab");
    const tab: EmbedDetailTab | undefined =
      tabParam === "pr" || tabParam === "issue" || tabParam === "reviews"
        ? tabParam
        : undefined;
    const owner = legacyProviderEmbedDetailMatch[4]!;
    const name = legacyProviderEmbedDetailMatch[5]!;
    const r: Route = {
      page: "embed-workspace-detail",
      provider: legacyProviderEmbedDetailMatch[1]!,
      itemType: legacyProviderEmbedDetailMatch[2] as "pr" | "issue",
      platformHost: legacyProviderEmbedDetailMatch[3]!,
      repoPath: `${owner}/${name}`,
      owner,
      name,
      number: parseInt(legacyProviderEmbedDetailMatch[6]!, 10),
    };
    if (branch) r.branch = branch;
    if (tab) r.tab = tab;
    return r;
  }
  const legacyEmbedDetailMatch = path.match(
    /^\/workspaces\/embed\/detail\/(pr|issue)\/([^/]+)\/([^/]+)\/([^/]+)\/(\d+)$/,
  );
  if (legacyEmbedDetailMatch) {
    const sp = new URLSearchParams(search);
    const branch = sp.get("branch") ?? undefined;
    const tabParam = sp.get("tab");
    const tab: EmbedDetailTab | undefined =
      tabParam === "pr" || tabParam === "issue" || tabParam === "reviews"
        ? tabParam
        : undefined;
    const platformHost = legacyEmbedDetailMatch[2]!;
    const owner = legacyEmbedDetailMatch[3]!;
    const name = legacyEmbedDetailMatch[4]!;
    const r: Route = {
      page: "embed-workspace-detail",
      provider: inferLegacyEmbedProvider(platformHost),
      itemType: legacyEmbedDetailMatch[1] as "pr" | "issue",
      platformHost,
      repoPath: `${owner}/${name}`,
      owner,
      name,
      number: parseInt(legacyEmbedDetailMatch[5]!, 10),
    };
    if (branch) r.branch = branch;
    if (tab) r.tab = tab;
    return r;
  }
  const embedEmptyMatch = path.match(
    /^\/workspaces\/embed\/empty\/(noSelection|noRepo|noWorkspace)$/,
  );
  if (embedEmptyMatch) {
    return {
      page: "embed-workspace-empty",
      reason: embedEmptyMatch[1] as EmbedEmptyReason,
    };
  }
  if (path === "/workspaces/embed/first-run") {
    return { page: "embed-workspace-first-run" };
  }
  const embedProjectMatch = path.match(
    /^\/workspaces\/embed\/project\/([A-Za-z0-9_-]+)$/,
  );
  if (embedProjectMatch) {
    return {
      page: "embed-workspace-project",
      projectId: embedProjectMatch[1]!,
    };
  }
  if (path === "/workspaces" || path.startsWith("/workspaces/")) {
    return { page: "workspaces" };
  }
  return { page: "activity" };
}

const configuredInitialRoute = getInitialRoute();
if (configuredInitialRoute) {
  history.replaceState(
    null,
    "",
    basePrefix + configuredInitialRoute,
  );
}

let route = $state<Route>(
  parseRoute(configuredInitialRoute ?? currentLocationPath()),
);

// Fire onRouteChange for the initial route after the module loads.
// Deferred so the embedder has time to set up the callback.
if (typeof window !== "undefined") {
  queueMicrotask(() => fireRouteChange(route));
}

export function getRoute(): Route {
  return route;
}

export function getPage(): Page {
  return route.page;
}

export function isFocusMode(): boolean {
  return route.page === "focus";
}

export function buildItemRoute(ref: RoutableItemRef): string {
  return buildRoutedItemRoute(ref, { focus: isFocusMode() });
}

export function navigate(path: string, state?: Record<string, unknown>): void {
  const fullPath = basePrefix + path;
  history.pushState(state ?? null, "", fullPath);
  route = parseRoute(fullPath);
  fireMiddlemanNavigateEvent(route);
  fireRouteChange(route);
}

function buildRouteEvent(r: Route): MiddlemanNavigateEvent {
  const focus = r.page === "focus";
  let navType: MiddlemanNavigateEvent["type"];
  if (r.page === "focus") {
    if (r.itemType === "mrs") {
      navType = "pull";
    } else if (r.itemType === "issues") {
      navType = "issue";
    } else {
      navType = r.itemType === "pr" ? "pull" : "issue";
    }
  } else if (r.page === "mobile-pulls") {
    navType = "pull";
  } else if (r.page === "mobile-issues") {
    navType = "issue";
  } else if (r.page === "mobile-activity") {
    navType = "activity";
  } else if (r.page === "pulls") {
    navType = r.view === "board" ? "board" : "pull";
  } else if (r.page === "issues") {
    navType = "issue";
  } else if (r.page === "repos") {
    navType = "repos";
  } else if (r.page === "reviews") {
    navType = "reviews";
  } else if (isWorkspacePage(r.page)) {
    navType = "workspaces";
  } else if (r.page === "design-system") {
    navType = "activity";
  } else {
    navType = "activity";
  }

  const event: MiddlemanNavigateEvent = {
    type: navType,
    focus,
    view: stripBase(window.location.pathname) + window.location.search,
  };

  if (r.page === "focus" && "owner" in r) {
    event.owner = r.owner;
    event.name = r.name;
    event.number = r.number;
  } else if (r.page === "pulls" && "selected" in r && r.selected) {
    event.owner = r.selected.owner;
    event.name = r.selected.name;
    event.number = r.selected.number;
  } else if (r.page === "issues" && "selected" in r && r.selected) {
    event.owner = r.selected.owner;
    event.name = r.selected.name;
    event.number = r.selected.number;
  }

  // Populate repo from focus list route or global config.
  if (r.page === "focus" && "repo" in r && r.repo) {
    event.repo = r.repo;
  } else {
    const cfgRepo = getEmbedUIConfig().repo;
    if (cfgRepo) {
      event.repo = `${cfgRepo.owner}/${cfgRepo.name}`;
    }
  }

  const host = getHost();
  if (host) {
    event.host = host;
  }

  return event;
}

export function isWorkspacePage(page: Page): boolean {
  return (
    page === "workspaces" || page === "terminal" || isWorkspaceEmbedPage(page)
  );
}

export function isWorkspaceEmbedPage(page: Page): boolean {
  switch (page) {
    case "embed-workspace-list":
    case "embed-workspace-terminal":
    case "embed-workspace-detail":
    case "embed-workspace-empty":
    case "embed-workspace-first-run":
    case "embed-workspace-project":
      return true;
    default:
      return false;
  }
}

export function isMobilePage(page: Page): boolean {
  return page === "mobile-activity"
    || page === "mobile-pulls"
    || page === "mobile-issues";
}

function fireMiddlemanNavigateEvent(r: Route): void {
  const cb = getOnNavigate();
  if (cb) cb(buildRouteEvent(r));
}

function fireRouteChange(r: Route): void {
  const cb = getOnRouteChange();
  if (cb) cb(buildRouteEvent(r));
}

export function replaceUrl(path: string): void {
  const fullPath = basePrefix + path;
  history.replaceState(null, "", fullPath);
  route = parseRoute(fullPath);
  fireRouteChange(route);
}

// Listen for browser back/forward.
if (typeof window !== "undefined") {
  window.addEventListener("popstate", () => {
    route = parseRoute(currentLocationPath());
    fireRouteChange(route);
  });
}

// Expose imperative navigation for the host embedder.
if (typeof window !== "undefined") {
  window.__middleman_navigate_to_route = (route: string) => {
    navigate(route);
  };
}

// --- detail tab derived from route ---

export type DetailTab = "conversation" | "files";

export function getDetailTab(): DetailTab {
  if (route.page === "pulls" && "tab" in route && route.tab === "files") {
    return "files";
  }
  if (
    route.page === "focus" &&
    route.itemType === "pr" &&
    "tab" in route &&
    route.tab === "files"
  ) {
    return "files";
  }
  return "conversation";
}

export function getSelectedPRFromRoute(): NumberedItemRef | null {
  if (route.page !== "pulls") return null;
  if ("selected" in route && route.selected) {
    return route.selected;
  }
  return null;
}

// --- backward-compat helpers for existing components ---

export type View = "list" | "board";
export type Tab = "pulls" | "issues";

export function getView(): View {
  return route.page === "pulls" && "view" in route && route.view === "board"
    ? "board"
    : "list";
}

export function setView(v: View): void {
  if (route.page === "pulls") {
    navigate(v === "board" ? "/pulls/board" : "/pulls");
  }
}

export function getTab(): Tab {
  if (route.page === "pulls" || route.page === "mobile-pulls") return "pulls";
  if (route.page === "issues" || route.page === "mobile-issues") return "issues";
  return "pulls";
}

export function setTab(t: Tab): void {
  navigate(t === "pulls" ? "/pulls" : "/issues");
}

export function isDiffView(): boolean {
  return getDetailTab() === "files";
}
