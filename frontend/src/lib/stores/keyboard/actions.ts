import { getDetailTab, getSelectedPRFromRoute, navigate, replaceUrl } from "../router.svelte.js";
import { getUIConfig } from "../embed-config.svelte.js";
import { isSidebarToggleEnabled, toggleSidebar } from "../sidebar.svelte.js";
import { toggleTheme } from "../theme.svelte.js";
import { toggleCheatsheet } from "./cheatsheet-state.svelte.js";
import { openNewWorkspaceDialog } from "../new-workspace.svelte.js";
import { togglePalette } from "./palette-state.svelte.js";
import {
  openLabelPickerFor,
  type OpenLabelPickerDetail,
} from "../../../../../packages/ui/src/components/detail/labelPickerCommand.js";
import {
  buildPullRequestFilesRoute,
  buildPullRequestRoute,
  buildRepoBrowserRoute,
  type RepositoryRouteRef,
} from "@middleman/ui/routes";
import {
  getPaneLayoutStore,
  promoteSessionBesideWorkspace,
  type PaneLayoutStore,
  type PaneRenderReport,
  type PaneSurfaceKey,
} from "@middleman/ui/stores/paneLayout";
import { isSessionPaneKey } from "@middleman/ui";
import { activeHostedSession, hostedWorkspaceLauncher } from "../workspace-host.svelte.js";
import type { ConfigRepo } from "@middleman/ui/api/types";
import type { StoreInstances } from "@middleman/ui";
import type { Action, Context, PreviewBlock } from "./types.js";
import { parseActivitySelection } from "../../utils/activitySelection.js";

let storesGetter: (() => StoreInstances) | null = null;

export function setStoreInstances(getter: () => StoreInstances): void {
  storesGetter = getter;
}

function stores(): StoreInstances {
  if (!storesGetter) {
    throw new Error("setStoreInstances has not been called");
  }
  return storesGetter();
}

const always = (): boolean => true;

const onPullsList = (ctx: Context): boolean => ctx.page === "pulls" && !ctx.isDiffView;

const onIssuesList = (ctx: Context): boolean => ctx.page === "issues";

function hasSidebarShortcutTarget(ctx: Context): boolean {
  if (!ctx.sidebarTargetAvailable) return false;
  switch (ctx.route.page) {
    case "pulls":
      return ctx.route.view === "list";
    case "issues":
    case "workspaces":
    case "terminal":
      return true;
    default:
      return false;
  }
}

type LabelEditableSelection = Omit<OpenLabelPickerDetail, "itemType">;

type LabelEditableDetail = {
  repo_owner: string;
  repo_name: string;
  number: number | undefined;
  repo?: {
    provider?: string;
    platform_host?: string;
    repo_path?: string;
    capabilities?: { read_labels?: boolean; label_mutation?: boolean };
  };
};

function hasLabelEditingCapability(detail: LabelEditableDetail): boolean {
  const caps = detail.repo?.capabilities;
  return Boolean(caps?.read_labels && caps.label_mutation);
}

function labelEditableDetailMatches(detail: LabelEditableDetail, selection: LabelEditableSelection): boolean {
  return (
    detail.repo_owner === selection.owner &&
    detail.repo_name === selection.name &&
    detail.number === selection.number &&
    detail.repo?.provider === selection.provider &&
    detail.repo?.platform_host === selection.platformHost &&
    detail.repo?.repo_path === selection.repoPath
  );
}

function labelPickerDetailFor(
  itemType: OpenLabelPickerDetail["itemType"],
  selection: LabelEditableSelection | null,
  detail: LabelEditableDetail | null,
): OpenLabelPickerDetail | null {
  if (selection === null || detail === null) return null;
  if (!hasLabelEditingCapability(detail)) return null;
  if (!labelEditableDetailMatches(detail, selection)) return null;
  return { itemType, ...selection };
}

function prLabelPickerDetail(ctx: Context): OpenLabelPickerDetail | null {
  const detail = stores().detail.getDetail();
  return labelPickerDetailFor(
    "pull",
    ctx.selectedPR,
    detail && {
      repo_owner: detail.repo_owner,
      repo_name: detail.repo_name,
      number: detail.merge_request?.Number,
      repo: detail.repo,
    },
  );
}

function issueLabelPickerDetail(ctx: Context): OpenLabelPickerDetail | null {
  const detail = stores().issues.getIssueDetail();
  return labelPickerDetailFor(
    "issue",
    ctx.selectedIssue,
    detail && {
      repo_owner: detail.repo_owner,
      repo_name: detail.repo_name,
      number: detail.issue?.Number,
      repo: detail.repo,
    },
  );
}

function labelPickerDetail(ctx: Context): OpenLabelPickerDetail | null {
  if (ctx.page === "pulls") return prLabelPickerDetail(ctx);
  if (ctx.page === "issues") return issueLabelPickerDetail(ctx);
  return null;
}

function cleanRepoPath(repoPath: string | undefined): string {
  return (repoPath ?? "").replace(/^\/+|\/+$/g, "");
}

function repoIdentityFromPath(repoPath: string): { owner: string; name: string } | null {
  const separator = repoPath.lastIndexOf("/");
  if (separator <= 0 || separator === repoPath.length - 1) return null;
  return {
    owner: repoPath.slice(0, separator),
    name: repoPath.slice(separator + 1),
  };
}

type RepoSelectionRef = {
  provider?: string | undefined;
  platformHost?: string | undefined;
  owner?: string | undefined;
  name?: string | undefined;
  repoPath?: string | undefined;
};

type WorkspaceConfigRepo = NonNullable<ReturnType<typeof getUIConfig>["repo"]>;

function itemRepoRef(ref: RepoSelectionRef | null): RepositoryRouteRef | null {
  if (!ref) return null;
  const repoPath = cleanRepoPath(ref.repoPath);
  if (!ref.provider || !repoPath || !ref.owner || !ref.name) return null;
  return {
    provider: ref.provider,
    platformHost: ref.platformHost,
    owner: ref.owner,
    name: ref.name,
    repoPath,
  };
}

function workspaceConfigRepoRef(repo: WorkspaceConfigRepo): RepositoryRouteRef | null {
  const provider = repo.provider?.trim();
  const platformHost = (repo.platform_host ?? repo.host)?.trim();
  const repoPath = cleanRepoPath(repo.repo_path);
  if (!provider || !platformHost || !repoPath) return null;
  const identity = repo.owner && repo.name ? { owner: repo.owner, name: repo.name } : repoIdentityFromPath(repoPath);
  if (!identity) return null;
  return {
    provider,
    platformHost,
    owner: identity.owner,
    name: identity.name,
    repoPath,
  };
}

function configuredRepoRef(repo: ConfigRepo): RepositoryRouteRef | null {
  if (repo.is_glob) return null;
  const repoPath = cleanRepoPath(repo.repo_path || `${repo.owner}/${repo.name}`);
  if (!repo.provider || !repo.platform_host || !repo.owner || !repo.name || !repoPath) {
    return null;
  }
  return {
    provider: repo.provider,
    platformHost: repo.platform_host,
    owner: repo.owner,
    name: repo.name,
    repoPath,
  };
}

function configuredRepoMatchesWorkspace(repo: ConfigRepo, selectedRepo: WorkspaceConfigRepo): boolean {
  if (repo.owner !== selectedRepo.owner || repo.name !== selectedRepo.name) return false;
  if (selectedRepo.provider && repo.provider !== selectedRepo.provider) return false;
  const selectedHost = selectedRepo.platform_host ?? selectedRepo.host;
  if (selectedHost && repo.platform_host !== selectedHost) return false;
  const selectedRepoPath = cleanRepoPath(selectedRepo.repo_path);
  if (selectedRepoPath && cleanRepoPath(repo.repo_path || `${repo.owner}/${repo.name}`) !== selectedRepoPath) {
    return false;
  }
  return true;
}

function workspaceRepoRef(): RepositoryRouteRef | null {
  const selectedRepo = getUIConfig().repo;
  if (!selectedRepo) return null;
  const directRef = workspaceConfigRepoRef(selectedRepo);
  if (directRef) return directRef;
  if (!storesGetter) return null;

  const matches = stores()
    .settings.getConfiguredRepos()
    .filter((repo) => !repo.is_glob)
    .filter((repo) => configuredRepoMatchesWorkspace(repo, selectedRepo))
    .map(configuredRepoRef)
    .filter((repo): repo is RepositoryRouteRef => repo !== null);
  return matches.length === 1 ? (matches[0] ?? null) : null;
}

function routeRepoRef(ctx: Context): RepositoryRouteRef | null {
  switch (ctx.route.page) {
    case "repo-browser":
      return {
        provider: ctx.route.provider,
        platformHost: ctx.route.platformHost,
        owner: ctx.route.owner,
        name: ctx.route.name,
        repoPath: cleanRepoPath(ctx.route.repoPath),
      };
    case "embed-workspace-detail":
      return {
        provider: ctx.route.provider,
        platformHost: ctx.route.platformHost,
        owner: ctx.route.owner,
        name: ctx.route.name,
        repoPath: cleanRepoPath(ctx.route.repoPath),
      };
    case "focus":
      if (ctx.route.itemType !== "pr" && ctx.route.itemType !== "issue") return null;
      return {
        provider: ctx.route.provider,
        platformHost: ctx.route.platformHost,
        owner: ctx.route.owner,
        name: ctx.route.name,
        repoPath: cleanRepoPath(ctx.route.repoPath),
      };
    default:
      return null;
  }
}

function workspacePageRepoRef(ctx: Context): RepositoryRouteRef | null {
  switch (ctx.page) {
    case "workspaces":
    case "terminal":
    case "embed-workspace-terminal":
    case "embed-workspace-project":
      return workspaceRepoRef();
    default:
      return null;
  }
}

function pageSelectedPRRef(ctx: Context): RepositoryRouteRef | null {
  if (
    ctx.page === "pulls" ||
    ctx.page === "mobile-pulls" ||
    (ctx.route.page === "focus" && ctx.route.itemType === "pr")
  ) {
    return itemRepoRef(ctx.selectedPR);
  }
  return null;
}

function pageSelectedIssueRef(ctx: Context): RepositoryRouteRef | null {
  if (ctx.route.page === "issues" && ctx.route.selected) {
    return itemRepoRef(ctx.route.selected);
  }
  if (
    ctx.page === "issues" ||
    ctx.page === "mobile-issues" ||
    (ctx.route.page === "focus" && ctx.route.itemType === "issue")
  ) {
    return itemRepoRef(ctx.selectedIssue);
  }
  return null;
}

function repoBrowserCommandRef(ctx: Context): RepositoryRouteRef | null {
  const routeRef = routeRepoRef(ctx);
  if (routeRef) return routeRef;
  if (ctx.page === "activity") {
    return itemRepoRef(parseActivitySelection(window.location.search));
  }
  const workspaceRef = workspacePageRepoRef(ctx);
  if (workspaceRef) return workspaceRef;
  const selectedPRRef = pageSelectedPRRef(ctx);
  if (selectedPRRef) return selectedPRRef;
  return pageSelectedIssueRef(ctx);
}

function repoBrowserSubtitle(ref: RepositoryRouteRef | null): string | undefined {
  if (!ref) return undefined;
  return ref.platformHost ? `${ref.platformHost}/${ref.repoPath}` : ref.repoPath;
}

function repoBrowserPreview(ctx: Context): PreviewBlock {
  const subtitle = repoBrowserSubtitle(repoBrowserCommandRef(ctx));
  if (subtitle) {
    return {
      title: "View repository source",
      subtitle,
    };
  }
  return {
    title: "View repository source",
  };
}

// Mirrors App.svelte's pre-migration page exclusions for `1`/`2`/`f`/etc.:
// settings, design-system, repos, reviews, workspaces, activity all returned
// early before the global shortcut switch ran.
const onNumberNavPages = (ctx: Context): boolean => {
  switch (ctx.page) {
    case "settings":
    case "design-system":
    case "repos":
    case "kata":
    case "reviews":
    case "workspaces":
    case "activity":
      return false;
    default:
      return true;
  }
};

// Mirror App.svelte's navigateToSelectedPR helper (replaceUrl when a PR is
// already selected in the URL, navigate otherwise).
function navigateToSelectedPR(): void {
  const sel = stores().pulls.getSelectedPR();
  if (!sel) return;
  const tab = getDetailTab();
  const path = tab === "files" ? buildPullRequestFilesRoute(sel) : buildPullRequestRoute(sel);
  if (getSelectedPRFromRoute()) {
    replaceUrl(path);
  } else {
    navigate(path);
  }
}

/**
 * The detail pane surface the current page arranges, if any.
 *
 * Pane commands are surface-scoped, never global: the Workspaces tab has its
 * own tree with its own drag scope and must not be reachable from here.
 */
function paneSurfaceFor(ctx: Context): PaneSurfaceKey | null {
  switch (ctx.page) {
    case "pulls":
      return "prs";
    case "issues":
      return "issues";
    case "activity":
      return "activity";
    default:
      return null;
  }
}

interface PaneCommandTarget {
  layout: PaneLayoutStore;
  tabKey: string;
  leafID: string;
}

/**
 * The layout a command may act on: one that is actually mounted and not flattened.
 *
 * A page can be a pane surface with nothing on screen (a list with no selection),
 * and below the flatten width every structural edit is disabled — a command that
 * ignored either would rearrange a persisted tree the user cannot see. The same
 * goes for the target: the last-focused pane may since have been hidden, and
 * maximizing a hidden pane is a dead command while splitting one moves it behind
 * the user's back.
 */
function mountedPaneLayout(ctx: Context): { layout: PaneLayoutStore; render: PaneRenderReport } | null {
  const surface = paneSurfaceFor(ctx);
  if (surface === null) return null;
  const layout = getPaneLayoutStore(surface);
  const render = layout.paneRender();
  if (render === null || render.flattened) return null;
  return { layout, render };
}

/**
 * The pane a command acts on: the last one focused, falling back to the pane the
 * route names so the commands work before the user has touched a tab header. The
 * target must be a pane this surface currently offers — Activity's diff pane, for
 * instance, is gone the moment the selection stops being a pull request.
 */
function paneCommandTarget(ctx: Context): PaneCommandTarget | null {
  const mounted = mountedPaneLayout(ctx);
  if (mounted === null) return null;
  const { layout, render } = mounted;
  const preferred = layout.lastFocusedTabKey() ?? (paneSurfaceFor(ctx) === "issues" ? "conversation" : ctx.detailTab);
  const tabKey = render.editableTabs.includes(preferred) ? preferred : render.editableTabs[0];
  if (tabKey === undefined) return null;
  const leafID = layout.leafIDForTab(tabKey);
  if (leafID === null) return null;
  return { layout, tabKey, leafID };
}

function paneIsZoomed(ctx: Context): boolean {
  const target = paneCommandTarget(ctx);
  return target !== null && target.layout.zoomedLeafID() === target.leafID;
}

/**
 * The promotion a keyboard command can perform here: the workspace pane's
 * current session, the leaf it would split off, and the layout to record it in.
 *
 * Null once the session already has a pane — promoting twice would move the tab
 * the user placed by hand — and null unless the workspace pane is actually ON
 * SCREEN. Holding a leaf in the tree is not enough: a pane closed, tabbed behind a
 * sibling, or covered by another leaf's zoom still has one, while the view keeps
 * publishing its sessions from the parked host, so the command would move a
 * terminal the user cannot see. The split also needs a rendered leaf to grow from.
 *
 * `promoteSessionBesideWorkspace` refuses on the same grounds, which is what covers
 * the dock's own control. The check is repeated here because this decides whether
 * the command is OFFERED: a palette row that does nothing when chosen is worse than
 * one that is absent.
 */
function sessionPromotionTarget(ctx: Context): { layout: PaneLayoutStore; paneKey: string } | null {
  const mounted = mountedPaneLayout(ctx);
  const surface = paneSurfaceFor(ctx);
  if (mounted === null || surface === null) return null;
  if (!mounted.render.onScreenTabs.includes("workspace")) return null;
  const session = activeHostedSession(surface);
  if (session === null || mounted.layout.hasTab(session.paneKey)) return null;
  if (mounted.layout.leafIDForTab("workspace") === null) return null;
  return { layout: mounted.layout, paneKey: session.paneKey };
}

/** The pane command target when it names a promoted session, for demotion. */
function sessionDemotionTarget(ctx: Context): PaneCommandTarget | null {
  const target = paneCommandTarget(ctx);
  return target !== null && isSessionPaneKey(target.tabKey) ? target : null;
}

function splitActivePane(ctx: Context, direction: "horizontal" | "vertical"): void {
  const target = paneCommandTarget(ctx);
  if (target === null) return;
  target.layout.splitTab(target.tabKey, target.leafID, direction, "after");
}

export const defaultActions: Action[] = [
  {
    id: "go.next",
    label: "Next pull request",
    scope: "view-pulls",
    binding: { key: "j" },
    priority: 0,
    when: onPullsList,
    handler: () => {
      stores().pulls.selectNextPR();
      navigateToSelectedPR();
    },
  },
  {
    id: "go.prev",
    label: "Previous pull request",
    scope: "view-pulls",
    binding: { key: "k" },
    priority: 0,
    when: onPullsList,
    handler: () => {
      stores().pulls.selectPrevPR();
      navigateToSelectedPR();
    },
  },
  {
    id: "go.next.issues",
    label: "Next issue",
    scope: "view-issues",
    binding: { key: "j" },
    priority: 0,
    when: onIssuesList,
    handler: () => {
      stores().issues.selectNextIssue();
    },
  },
  {
    id: "go.prev.issues",
    label: "Previous issue",
    scope: "view-issues",
    binding: { key: "k" },
    priority: 0,
    when: onIssuesList,
    handler: () => {
      stores().issues.selectPrevIssue();
    },
  },
  {
    id: "tab.toggle",
    label: "Toggle conversation/files tab",
    scope: "view-pulls",
    binding: { key: "f" },
    priority: 0,
    when: (ctx) => ctx.page === "pulls" && getSelectedPRFromRoute() !== null,
    handler: () => {
      const sel = getSelectedPRFromRoute();
      if (!sel) return;
      const tab = getDetailTab();
      if (tab === "conversation") {
        navigate(buildPullRequestFilesRoute(sel));
      } else {
        navigate(buildPullRequestRoute(sel));
      }
    },
  },
  {
    id: "escape.list",
    label: "Back to list",
    scope: "view-pulls",
    binding: { key: "Escape" },
    priority: 0,
    when: (ctx) => ctx.page === "pulls" || ctx.page === "issues",
    handler: (ctx) => {
      if (ctx.page === "issues") {
        navigate("/issues");
      } else {
        navigate("/pulls");
      }
    },
  },
  {
    id: "nav.pulls.list",
    label: "Pull requests (list)",
    scope: "global",
    binding: { key: "1" },
    priority: 0,
    when: onNumberNavPages,
    handler: () => navigate("/pulls"),
  },
  {
    id: "sidebar.toggle",
    label: "Toggle sidebar",
    scope: "global",
    binding: { key: "[", ctrlOrMeta: true },
    priority: 0,
    when: () => isSidebarToggleEnabled(),
    visible: (ctx) => isSidebarToggleEnabled() && hasSidebarShortcutTarget(ctx),
    handler: (ctx) => {
      if (hasSidebarShortcutTarget(ctx)) toggleSidebar();
    },
  },
  {
    id: "palette.open",
    label: "Open command palette",
    scope: "global",
    binding: [
      { key: "k", ctrlOrMeta: true, shift: true },
      { key: "k", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true, shift: true },
    ],
    priority: 0,
    when: always,
    handler: () => togglePalette(),
  },
  {
    id: "repo.browser.open",
    label: "View repository source",
    scope: "global",
    binding: null,
    priority: 0,
    when: (ctx) => repoBrowserCommandRef(ctx) !== null,
    handler: (ctx) => {
      const ref = repoBrowserCommandRef(ctx);
      if (!ref) return;
      navigate(buildRepoBrowserRoute(ref));
    },
    preview: repoBrowserPreview,
  },
  {
    id: "cheatsheet.open",
    label: "Show keyboard shortcuts",
    scope: "global",
    // `?` is Shift+/ on a US keyboard; the matcher treats omitted `shift`
    // as `false`, so the binding must declare it explicitly to fire from a
    // real keystroke (Playwright's keyboard.press synthesizes the char and
    // hides this in tests).
    binding: [
      { key: "?", shift: true },
      { key: "/", shift: true },
    ],
    priority: 0,
    // The reviews page renders roborev's UI, which owns its own `?`-bound
    // help modal. Letting the middleman cheatsheet also fire on `?` opens
    // both modals at once and the cheatsheet's filter input then steals
    // focus, causing roborev's window-level handler to ignore the
    // subsequent Escape (its tag === "INPUT" guard returns early).
    when: (ctx) => ctx.page !== "reviews",
    handler: () => toggleCheatsheet(),
  },
  {
    id: "labels.edit",
    label: "Edit labels",
    scope: "detail",
    binding: null,
    priority: 0,
    when: (ctx) => labelPickerDetail(ctx) !== null,
    handler: (ctx) => {
      const detail = labelPickerDetail(ctx);
      if (detail !== null) openLabelPickerFor(detail);
    },
  },
  {
    id: "pane.splitRight",
    label: "Split pane right",
    scope: "detail",
    binding: null,
    priority: 0,
    // Splitting a lone tab out of its own leaf is a no-op in the tree model, so
    // offering it would put a dead row in the palette.
    when: (ctx) => {
      const target = paneCommandTarget(ctx);
      return target !== null && target.layout.canSplitTab(target.tabKey);
    },
    handler: (ctx) => splitActivePane(ctx, "horizontal"),
  },
  {
    id: "pane.splitDown",
    label: "Split pane down",
    scope: "detail",
    binding: null,
    priority: 0,
    when: (ctx) => {
      const target = paneCommandTarget(ctx);
      return target !== null && target.layout.canSplitTab(target.tabKey);
    },
    handler: (ctx) => splitActivePane(ctx, "vertical"),
  },
  {
    id: "pane.toggleZoom",
    label: "Maximize pane",
    scope: "detail",
    binding: null,
    priority: 0,
    when: (ctx) => paneCommandTarget(ctx) !== null && !paneIsZoomed(ctx),
    handler: (ctx) => {
      const target = paneCommandTarget(ctx);
      target?.layout.toggleZoom(target.leafID);
    },
  },
  {
    id: "pane.restore",
    label: "Restore pane size",
    scope: "detail",
    binding: null,
    priority: 0,
    when: paneIsZoomed,
    handler: (ctx) => {
      const target = paneCommandTarget(ctx);
      target?.layout.toggleZoom(target.leafID);
    },
  },
  {
    id: "pane.reset",
    label: "Reset pane layout",
    scope: "detail",
    binding: null,
    priority: 0,
    // Surface-scoped and only here: a leaf cluster is the wrong place for an
    // action that discards arrangements the user cannot currently see. Gated on a
    // mounted, unflattened layout for the same reason the splits are.
    when: (ctx) => mountedPaneLayout(ctx) !== null,
    handler: (ctx) => mountedPaneLayout(ctx)?.layout.reset(),
  },
  {
    id: "session.promote",
    label: "Move terminal session to a pane",
    scope: "detail",
    binding: null,
    priority: 0,
    when: (ctx) => sessionPromotionTarget(ctx) !== null,
    handler: (ctx) => {
      const target = sessionPromotionTarget(ctx);
      if (target !== null) promoteSessionBesideWorkspace(target.layout, target.paneKey);
    },
  },
  {
    id: "workspace.launcher",
    label: "Launch a workspace session",
    scope: "detail",
    binding: null,
    priority: 0,
    // The launcher is an overlay, not a pane, so it needs no room in the layout -
    // only a hosted workspace to launch into.
    when: (ctx) => {
      const surface = paneSurfaceFor(ctx);
      return surface !== null && hostedWorkspaceLauncher(surface) !== null;
    },
    handler: (ctx) => {
      const surface = paneSurfaceFor(ctx);
      if (surface !== null) hostedWorkspaceLauncher(surface)?.();
    },
  },
  {
    id: "session.demote",
    label: "Return terminal session to the workspace pane",
    scope: "detail",
    binding: null,
    priority: 0,
    when: (ctx) => sessionDemotionTarget(ctx) !== null,
    handler: (ctx) => {
      const target = sessionDemotionTarget(ctx);
      target?.layout.demoteTab(target.tabKey);
    },
  },
  {
    id: "sync.repos",
    label: "Sync repositories",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: () => stores().sync.triggerSync(),
  },
  {
    id: "theme.toggle",
    label: "Toggle theme",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: () => toggleTheme(),
  },
  {
    id: "nav.settings",
    label: "Settings",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: () => navigate("/settings"),
  },
  {
    id: "nav.repos",
    label: "Repositories",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: () => navigate("/repos"),
  },
  {
    id: "nav.reviews",
    label: "Reviews",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: () => navigate("/reviews"),
  },
  {
    id: "workspace.new",
    label: "New workspace",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: (ctx) => {
      const ref = workspacePageRepoRef(ctx);
      openNewWorkspaceDialog(
        ref
          ? {
              provider: ref.provider,
              platformHost: ref.platformHost ?? "",
              owner: ref.owner,
              name: ref.name,
            }
          : undefined,
      );
    },
    preview: () => ({
      title: "New workspace",
      subtitle: "Start work in a tracked repository on a new worktree branch",
    }),
  },
  {
    id: "nav.workspaces",
    label: "Workspaces",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: () => navigate("/workspaces"),
  },
  {
    id: "nav.design-system",
    label: "Design system",
    scope: "global",
    binding: null,
    priority: 0,
    when: always,
    handler: () => navigate("/design-system"),
  },
];
