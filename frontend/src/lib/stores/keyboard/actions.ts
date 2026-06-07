import { getDetailTab, getSelectedPRFromRoute, navigate, replaceUrl } from "../router.svelte.js";
import { isSidebarToggleEnabled, toggleSidebar } from "../sidebar.svelte.js";
import { toggleTheme } from "../theme.svelte.js";
import { toggleCheatsheet } from "./cheatsheet-state.svelte.js";
import { togglePalette } from "./palette-state.svelte.js";
import {
  openLabelPickerFor,
  type OpenLabelPickerDetail,
} from "../../../../../packages/ui/src/components/detail/labelPickerCommand.js";
import { buildPullRequestFilesRoute, buildPullRequestRoute } from "@middleman/ui/routes";
import type { StoreInstances } from "@middleman/ui";
import type { Action, Context } from "./types.js";

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

const isBoardRoute = (ctx: Context): boolean =>
  ctx.route.page === "pulls" && "view" in ctx.route && ctx.route.view === "board";

const onPullsListNotBoard = (ctx: Context): boolean => ctx.page === "pulls" && !ctx.isDiffView && !isBoardRoute(ctx);

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

// Mirrors App.svelte's pre-migration page exclusions for `1`/`2`/`f`/etc.:
// settings, design-system, repos, reviews, workspaces, activity all returned
// early before the global shortcut switch ran.
const onNumberNavPages = (ctx: Context): boolean => {
  switch (ctx.page) {
    case "settings":
    case "design-system":
    case "repos":
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

export const defaultActions: Action[] = [
  {
    id: "go.next",
    label: "Next pull request",
    scope: "view-pulls",
    binding: { key: "j" },
    priority: 0,
    when: onPullsListNotBoard,
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
    when: onPullsListNotBoard,
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
    when: (ctx) => (ctx.page === "pulls" || ctx.page === "issues") && !isBoardRoute(ctx),
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
    id: "nav.pulls.board",
    label: "Pull requests (board)",
    scope: "global",
    binding: { key: "2" },
    priority: 0,
    when: onNumberNavPages,
    handler: () => navigate("/pulls/board"),
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
      { key: "k", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true },
    ],
    priority: 0,
    when: always,
    handler: () => togglePalette(),
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
