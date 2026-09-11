import {
  buildRoutedItemRoute,
  type IssueRouteRef,
  type NumberedRouteItemRef,
  type RepositoryRouteRef,
  type RoutedItemRef,
} from "../routes.js";
import { canonicalProvider } from "../api/provider-routes.js";

export type RepoRef = RepositoryRouteRef;
export type NumberedItemRef = NumberedRouteItemRef;
export type HostedItemRef = IssueRouteRef;
export type RoutableItemRef = RoutedItemRef;

export type Route =
  | { page: "activity" }
  | { page: "actions" }
  | { page: "mobile-activity" }
  | { page: "mobile-pulls" }
  | { page: "mobile-issues" }
  | { page: "mobile-workspaces" }
  | {
      page: "mobile-workspace-terminal";
      workspaceId: string;
      hostKey?: string | undefined;
    }
  | {
      page: "mobile-workspace-item";
      workspaceId: string;
      hostKey?: string | undefined;
      tab?: "files" | undefined;
    }
  | { page: "design-system" }
  | { page: "repos" }
  | { page: "docs"; folder: string | null; doc: string | null }
  | {
      page: "repo-browser";
      provider: string;
      platformHost?: string | undefined;
      repoPath: string;
      owner: string;
      name: string;
      refType?: string | undefined;
      refName?: string | undefined;
      refSHA?: string | undefined;
      path?: string | undefined;
      mode?: "source" | "preview" | undefined;
      anchor?: string | undefined;
    }
  | { page: "workspaces" }
  | {
      page: "pulls";
      view: "list";
      selected?: NumberedItemRef;
      tab?: "files";
    }
  | { page: "issues"; selected?: HostedItemRef }
  | { page: "settings" }
  | ({
      page: "focus";
      itemType: "pr";
      tab?: "files";
    } & NumberedItemRef)
  | ({ page: "focus" } & IssueRouteRef & { itemType: "issue" })
  | { page: "focus"; itemType: "mrs"; repo?: string }
  | { page: "focus"; itemType: "issues"; repo?: string }
  | { page: "reviews"; jobId?: number }
  | { page: "project-intake"; hostKey?: string }
  | { page: "terminal"; workspaceId: string; hostKey?: string };

export type Page = Route["page"];

// Runtime base path injected by the Go server (e.g., "/" or "/kenn-forge/").
const rawBase = window.__BASE_PATH__ ?? "/";
const basePrefix = rawBase === "/" ? "" : rawBase.replace(/\/$/, "");

export function getBasePath(): string {
  return rawBase;
}

// Prefix an in-app absolute path (e.g. "/docs?...") with the configured
// base path so it works as a real href attribute. navigate() applies the
// prefix itself, so use this only when building hrefs that the browser
// follows directly (new-tab, copy-link, non-intercepted navigation).
export function withBasePath(path: string): string {
  return basePrefix + path;
}

function stripBase(path: string): string {
  if (basePrefix && path.startsWith(basePrefix)) {
    return path.slice(basePrefix.length) || "/";
  }
  return path;
}

function currentLocationPath(): string {
  return window.location.pathname + window.location.search + window.location.hash;
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

function decodeRouteHash(hash: string): string | undefined {
  if (!hash) return undefined;
  try {
    return decodeURIComponent(hash);
  } catch {
    return hash;
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

function splitRepoPath(repoPath: string): { owner: string; name: string } | undefined {
  const pathParts = repoPath
    .replace(/^\/+|\/+$/g, "")
    .split("/")
    .filter(Boolean);
  if (pathParts.length < 2) return undefined;
  return {
    owner: pathParts.slice(0, -1).join("/"),
    name: pathParts[pathParts.length - 1]!,
  };
}

function parseRoute(fullPath: string): Route {
  const hashIdx = fullPath.indexOf("#");
  const routePath = hashIdx >= 0 ? fullPath.slice(0, hashIdx) : fullPath;
  const anchor = hashIdx >= 0 ? decodeRouteHash(fullPath.slice(hashIdx + 1)) : undefined;
  const qIdx = routePath.indexOf("?");
  const pathname = qIdx >= 0 ? routePath.slice(0, qIdx) : routePath;
  const search = qIdx >= 0 ? routePath.slice(qIdx + 1) : "";
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
  if (path === "/m/workspaces") {
    return { page: "mobile-workspaces" };
  }
  const mobileFleetWorkspaceMatch = path.match(/^\/m\/workspaces\/fleet\/([^/]+)\/([^/]+)$/);
  if (mobileFleetWorkspaceMatch) {
    const hostKey = decodeRouteSegment(mobileFleetWorkspaceMatch[1] ?? "");
    const workspaceId = decodeRouteSegment(mobileFleetWorkspaceMatch[2] ?? "");
    if (hostKey && workspaceId) {
      return { page: "mobile-workspace-terminal", workspaceId, hostKey };
    }
  }
  const mobileLocalWorkspaceMatch = path.match(/^\/m\/workspaces\/local\/([^/]+)(?:\/(item)(?:\/(files))?)?$/);
  if (mobileLocalWorkspaceMatch) {
    const workspaceId = decodeRouteSegment(mobileLocalWorkspaceMatch[1] ?? "");
    if (workspaceId) {
      return mobileLocalWorkspaceMatch[2] === "item"
        ? {
            page: "mobile-workspace-item",
            workspaceId,
            ...(mobileLocalWorkspaceMatch[3] === "files" && { tab: "files" as const }),
          }
        : { page: "mobile-workspace-terminal", workspaceId };
    }
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
  if (path === "/actions") {
    return { page: "actions" };
  }
  if (path === "/design-system") {
    return { page: "design-system" };
  }
  if (path.startsWith("/pulls")) {
    const rest = path.slice("/pulls".length);
    if (rest !== "") {
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
    return { page: "pulls", view: "list" };
  }
  if (path === "/repos") {
    return { page: "repos" };
  }
  if (path === "/repo/browser") {
    const sp = new URLSearchParams(search);
    const provider = emptyToNull(sp.get("provider"));
    const repoPath = emptyToNull(sp.get("repo_path"))?.replace(/^\/+|\/+$/g, "");
    const repo = repoPath ? splitRepoPath(repoPath) : undefined;
    if (!provider || !repoPath || !repo) return { page: "repos" };
    const platformHost = emptyToNull(sp.get("platform_host")) ?? defaultPlatformHost(provider);
    const refType = parseRepoBrowserRefType(sp.get("ref_type"));
    const refName = emptyToNull(sp.get("ref_name"));
    const refSHA = emptyToNull(sp.get("ref_sha"));
    const selectedPath = emptyToNull(sp.get("path"));
    const mode = parseRepoBrowserViewMode(sp.get("mode"));
    return {
      page: "repo-browser",
      provider,
      ...(platformHost ? { platformHost } : {}),
      repoPath,
      owner: repo.owner,
      name: repo.name,
      ...(refType ? { refType } : {}),
      ...(refName ? { refName } : {}),
      ...(refSHA ? { refSHA } : {}),
      ...(selectedPath ? { path: selectedPath } : {}),
      ...(mode ? { mode } : {}),
      ...(anchor ? { anchor } : {}),
    };
  }
  if (path === "/docs") {
    const sp = new URLSearchParams(search);
    return {
      page: "docs",
      folder: emptyToNull(sp.get("folder")),
      doc: emptyToNull(sp.get("doc")),
    };
  }
  if (path === "/settings") return { page: "settings" };
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
  if (path === "/project-intake") {
    const sp = new URLSearchParams(search);
    const hostKey = emptyToNull(sp.get("host"));
    return {
      page: "project-intake",
      ...(hostKey ? { hostKey } : {}),
    };
  }
  const fleetTerminalMatch = path.match(/^\/terminal\/fleet\/([^/]+)\/([^/]+)$/);
  if (fleetTerminalMatch) {
    return {
      page: "terminal",
      hostKey: decodeRouteSegment(fleetTerminalMatch[1]!) ?? fleetTerminalMatch[1]!,
      workspaceId: decodeRouteSegment(fleetTerminalMatch[2]!) ?? fleetTerminalMatch[2]!,
    };
  }
  const terminalMatch = path.match(/^\/terminal\/([^/]+)$/);
  if (terminalMatch) {
    return {
      page: "terminal",
      workspaceId: decodeRouteSegment(terminalMatch[1]!) ?? terminalMatch[1]!,
    };
  }
  if (path === "/workspaces" || path.startsWith("/workspaces/")) {
    return { page: "workspaces" };
  }
  return { page: "activity" };
}

let route = $state<Route>(parseRoute(currentLocationPath()));

// The Activity selection, detail tab, and feed filters all live in the URL
// query string. Remember the full Activity path so the top-bar Activity tab
// can restore the previous view instead of resetting to a bare "/". The cache
// is refreshed at every point the router observes the URL — navigate() (which
// reads the live location before leaving, so a tab/settings/palette exit keeps
// even filter-only writes), replaceUrl() (drawer selection changes), direct
// history.replaceState() writes from the Activity store, popstate (browser
// Back/Forward), and initial load — so it stays current regardless of how
// Activity is entered or left.
const LAST_ACTIVITY_ROUTE_STORAGE_KEY = "kenn-forge:last-activity-route";
const RESTORABLE_ACTIVITY_FILTER_PARAMS = [
  "range",
  "view",
  "hide_closed",
  "rollup_commits",
  "collapsed",
  "search",
  "item_types",
  "event_types",
  "types",
  "notif",
  "hide_branch",
  "author",
] as const;

function isRestorableActivityRoute(routePath: string): boolean {
  if (!routePath.startsWith("/") || routePath.startsWith("//")) return false;
  const pathEnd = [routePath.indexOf("?"), routePath.indexOf("#")]
    .filter((index) => index >= 0)
    .reduce((first, index) => Math.min(first, index), routePath.length);
  const pathname = routePath.slice(0, pathEnd).replace(/\/+$/, "") || "/";
  return pathname === "/" && parseRoute(routePath).page === "activity";
}

function readLastActivityRoute(): string {
  try {
    const storedRoute = sessionStorage.getItem(LAST_ACTIVITY_ROUTE_STORAGE_KEY);
    return storedRoute && isRestorableActivityRoute(storedRoute) ? storedRoute : "/";
  } catch {
    return "/";
  }
}

function persistLastActivityRoute(activityRoute: string): void {
  try {
    sessionStorage.setItem(LAST_ACTIVITY_ROUTE_STORAGE_KEY, activityRoute);
  } catch {
    // Storage can be blocked in private or embedded contexts. The in-memory
    // route still preserves Activity state for ordinary navigation.
  }
}

let lastActivityRoute = readLastActivityRoute();

function restoreMissingActivityFilters(): void {
  if (route.page !== "activity") return;

  const currentRoute = stripBase(currentLocationPath());
  if (!isRestorableActivityRoute(currentRoute) || !isRestorableActivityRoute(lastActivityRoute)) return;

  const currentURL = new URL(currentRoute, "https://example.invalid");
  const storedURL = new URL(lastActivityRoute, "https://example.invalid");
  const currentHasLegacyTypes = currentURL.searchParams.has("types");
  let restored = false;
  for (const param of RESTORABLE_ACTIVITY_FILTER_PARAMS) {
    const representedByLegacyTypes = currentHasLegacyTypes && (param === "item_types" || param === "event_types");
    if (!currentURL.searchParams.has(param) && !representedByLegacyTypes && storedURL.searchParams.has(param)) {
      currentURL.searchParams.set(param, storedURL.searchParams.get(param)!);
      restored = true;
    }
  }
  if (!restored) return;

  const restoredRoute = `${currentURL.pathname}${currentURL.search}${currentURL.hash}`;
  history.replaceState(history.state, "", basePrefix + restoredRoute);
  route = parseRoute(restoredRoute);
}

export function getLastActivityRoute(): string {
  return lastActivityRoute;
}

function rememberActivityRoute(): void {
  const currentPath = currentLocationPath();
  const activityRoute = stripBase(currentPath);
  if (route.page === "activity" && isRestorableActivityRoute(activityRoute)) {
    lastActivityRoute = activityRoute;
    persistLastActivityRoute(lastActivityRoute);
  }
}

// Restore omitted session filters before the Activity store hydrates, then
// seed the route cache from the resulting URL.
restoreMissingActivityFilters();
rememberActivityRoute();

// Same contract as lastActivityRoute, for the Workspaces tab: remember the
// full last workspace-mode path (/workspaces, /terminal/{id}, or
// /terminal/fleet/{hostKey}/{id}) so the top-bar tab restores the previous
// selection instead of resetting to the bare list.
let lastWorkspaceRoute = "/workspaces";

// Terminal routes whose workspace has been deleted this session. Deletion
// alone can't just reset lastWorkspaceRoute: deleting from the terminal
// page navigates to /workspaces afterwards, and that navigate() re-captures
// the dying /terminal/{id} URL as the "last" route. Remembering consults
// this set so a deleted workspace's route can never come back.
const forgottenTerminalKeys = new Set<string>();

function terminalRouteKey(workspaceId: string, hostKey: string | undefined): string {
  return `${hostKey ?? ""}\n${workspaceId}`;
}

function isForgottenTerminalPath(path: string): boolean {
  const parsed = parseRoute(path);
  return parsed.page === "terminal" && forgottenTerminalKeys.has(terminalRouteKey(parsed.workspaceId, parsed.hostKey));
}

// Called when a workspace is deleted: the Workspaces tab must never restore
// a /terminal route for a workspace that no longer exists.
export function forgetWorkspaceRoute(workspaceId: string, hostKey?: string): void {
  forgottenTerminalKeys.add(terminalRouteKey(workspaceId, hostKey));
  if (isForgottenTerminalPath(lastWorkspaceRoute)) {
    lastWorkspaceRoute = "/workspaces";
  }
}

export function getLastWorkspaceRoute(): string {
  return lastWorkspaceRoute;
}

function rememberWorkspaceRoute(): void {
  const currentPath = currentLocationPath();
  const parsed = parseRoute(currentPath);
  const onWorkspaceRoute =
    (route.page === "workspaces" || route.page === "terminal") &&
    (parsed.page === "workspaces" || parsed.page === "terminal");
  if (onWorkspaceRoute && !isForgottenTerminalPath(currentPath)) {
    lastWorkspaceRoute = stripBase(currentPath);
  }
}

// Seed the cache when the app loads directly on a workspace URL.
rememberWorkspaceRoute();

if (typeof window !== "undefined") {
  const originalReplaceState = history.replaceState.bind(history);
  history.replaceState = ((data: unknown, unused: string, url?: string | URL | null) => {
    originalReplaceState(data, unused, url);
    rememberActivityRoute();
    rememberWorkspaceRoute();
  }) as History["replaceState"];
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
  // route still reflects the page being left; capture its live URL first.
  rememberActivityRoute();
  rememberWorkspaceRoute();
  const fullPath = basePrefix + path;
  history.pushState(state ?? null, "", fullPath);
  route = parseRoute(fullPath);
  restoreMissingActivityFilters();
  // Record the workspace destination too: leaving it via browser
  // Back/Forward skips navigate(), and the popstate handler only
  // remembers the route it lands on — a terminal visit exited that way
  // would otherwise never enter route memory.
  rememberWorkspaceRoute();
}

function emptyToNull(value: string | null): string | null {
  return value && value.length > 0 ? value : null;
}

function parseRepoBrowserRefType(value: string | null): "branch" | "tag" | "commit" | undefined {
  return value === "branch" || value === "tag" || value === "commit" ? value : undefined;
}

function parseRepoBrowserViewMode(value: string | null): "source" | "preview" | undefined {
  return value === "source" || value === "preview" ? value : undefined;
}

export function isWorkspacePage(page: Page): boolean {
  return (
    page === "workspaces" ||
    page === "terminal" ||
    page === "mobile-workspaces" ||
    page === "mobile-workspace-terminal" ||
    page === "mobile-workspace-item"
  );
}

export function isMobilePage(page: Page): boolean {
  return (
    page === "mobile-activity" ||
    page === "mobile-pulls" ||
    page === "mobile-issues" ||
    page === "mobile-workspaces" ||
    page === "mobile-workspace-terminal" ||
    page === "mobile-workspace-item"
  );
}

export function buildMobileWorkspaceRoute(workspaceId: string, hostKey?: string): string {
  const encodedWorkspaceId = encodeURIComponent(workspaceId);
  return hostKey
    ? `/m/workspaces/fleet/${encodeURIComponent(hostKey)}/${encodedWorkspaceId}`
    : `/m/workspaces/local/${encodedWorkspaceId}`;
}

export function buildMobileWorkspaceItemRoute(workspaceId: string, hostKey?: string, tab?: "files"): string {
  const base = buildMobileWorkspaceRoute(workspaceId, hostKey);
  return `${base}/item${tab === "files" ? "/files" : ""}`;
}

export function replaceUrl(path: string, state?: Record<string, unknown>): void {
  const fullPath = basePrefix + path;
  history.replaceState(state ?? null, "", fullPath);
  route = parseRoute(fullPath);
  rememberActivityRoute();
  rememberWorkspaceRoute();
}

// Listen for browser back/forward.
if (typeof window !== "undefined") {
  window.addEventListener("popstate", () => {
    route = parseRoute(currentLocationPath());
    restoreMissingActivityFilters();
    rememberActivityRoute();
    rememberWorkspaceRoute();
  });
}

// --- detail tab derived from route ---

export type DetailTab = "conversation" | "files";

export function getDetailTab(): DetailTab {
  if (route.page === "pulls" && "tab" in route && route.tab === "files") {
    return "files";
  }
  if (route.page === "focus" && route.itemType === "pr" && "tab" in route && route.tab === "files") {
    return "files";
  }
  if (route.page === "mobile-workspace-item" && route.tab === "files") {
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

export type Tab = "pulls" | "issues";

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
