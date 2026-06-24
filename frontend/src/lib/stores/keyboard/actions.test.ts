import { afterEach, describe, expect, it } from "vite-plus/test";

import { defaultActions, setStoreInstances } from "./actions.js";
import { isSidebarCollapsed, setSidebarCollapsed } from "../sidebar.svelte.js";
import {
  OPEN_LABEL_PICKER_EVENT,
  type OpenLabelPickerDetail,
} from "../../../../../packages/ui/src/components/detail/labelPickerCommand.js";
import type { Context } from "./types.js";

function ctx(page: Context["page"], overrides: Partial<Context> = {}): Context {
  return {
    page,
    route: { page } as never,
    selectedPR: null,
    selectedIssue: null,
    isDiffView: false,
    detailTab: "conversation",
    sidebarTargetAvailable: true,
    ...overrides,
  };
}

const repo = {
  provider: "github",
  platform_host: "github.com",
  owner: "octo",
  name: "repo",
  repo_path: "octo/repo",
  capabilities: { read_labels: true, label_mutation: true },
};

const selected = {
  provider: "github",
  platformHost: "github.com",
  owner: "octo",
  name: "repo",
  repoPath: "octo/repo",
  number: 1,
};

const staleSelected = {
  ...selected,
  owner: "stale",
  name: "selection",
  repoPath: "stale/selection",
};

function command(id: string) {
  const action = defaultActions.find((a) => a.id === id);
  expect(action).toBeDefined();
  return action!;
}

function locationPath(): string {
  return window.location.pathname + window.location.search;
}

function configuredRepo(overrides: {
  provider?: string;
  platformHost?: string;
  owner: string;
  name: string;
  repoPath?: string;
  isGlob?: boolean;
}) {
  const repoPath = overrides.repoPath ?? `${overrides.owner}/${overrides.name}`;
  return {
    provider: overrides.provider ?? "github",
    platform_host: overrides.platformHost ?? "github.com",
    owner: overrides.owner,
    name: overrides.name,
    repo_path: repoPath,
    is_glob: overrides.isGlob ?? false,
    matched_repo_count: 1,
  };
}

function setConfiguredRepos(repos: ReturnType<typeof configuredRepo>[]): void {
  setStoreInstances(
    () =>
      ({
        settings: {
          getConfiguredRepos: () => repos,
        },
      }) as never,
  );
}

describe("defaultActions", () => {
  afterEach(() => {
    setSidebarCollapsed(false);
    delete window.__middleman_config;
    window.history.replaceState(null, "", "/");
  });

  it("includes the migrated globals", () => {
    const ids = defaultActions.map((a) => a.id);
    expect(ids).toEqual(
      expect.arrayContaining([
        "labels.edit",
        "go.next",
        "go.prev",
        "tab.toggle",
        "escape.list",
        "nav.pulls.list",
        "nav.pulls.board",
        "sidebar.toggle",
        "palette.open",
        "repo.browser.open",
        "cheatsheet.open",
        "sync.repos",
        "theme.toggle",
        "nav.settings",
        "nav.repos",
        "nav.reviews",
        "nav.workspaces",
        "nav.design-system",
      ]),
    );
  });

  it("palette.open binds Cmd/Ctrl+K, Cmd/Ctrl+P, and Cmd/Ctrl+Shift+P", () => {
    const palette = defaultActions.find((a) => a.id === "palette.open");
    expect(palette).toBeDefined();
    expect(palette!.binding).toEqual([
      { key: "k", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true, shift: true },
    ]);
  });

  it("cheatsheet.open binds shifted slash variants so the dispatcher matches the real keystroke", () => {
    // Browsers disagree on whether Shift+/ arrives as `?` or `/`.
    // The dispatcher's matcher treats omitted `shift` as `false`, so both
    // variants need an explicit `shift: true`.
    const cheatsheet = defaultActions.find((a) => a.id === "cheatsheet.open");
    expect(cheatsheet).toBeDefined();
    expect(cheatsheet!.binding).toEqual([
      { key: "?", shift: true },
      { key: "/", shift: true },
    ]);
  });

  it("dispatches Edit labels from PR detail context", () => {
    const action = defaultActions.find((a) => a.id === "labels.edit");
    expect(action).toBeDefined();
    setStoreInstances(
      () =>
        ({
          detail: {
            getDetail: () => ({
              repo_owner: "octo",
              repo_name: "repo",
              repo,
              merge_request: { Number: 1 },
            }),
          },
        }) as never,
    );
    const events: OpenLabelPickerDetail[] = [];
    const listener = (event: Event) => events.push((event as CustomEvent<OpenLabelPickerDetail>).detail);
    window.addEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    try {
      const context = ctx("pulls", { selectedPR: selected });
      expect(action!.when(context)).toBe(true);
      action!.handler(context);
    } finally {
      window.removeEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    }

    expect(events).toEqual([{ itemType: "pull", ...selected }]);
  });

  it("dispatches Edit labels from issue detail context", () => {
    const action = defaultActions.find((a) => a.id === "labels.edit");
    expect(action).toBeDefined();
    setStoreInstances(
      () =>
        ({
          issues: {
            getIssueDetail: () => ({
              repo_owner: "octo",
              repo_name: "repo",
              repo,
              issue: { Number: 1 },
            }),
          },
        }) as never,
    );
    const events: OpenLabelPickerDetail[] = [];
    const listener = (event: Event) => events.push((event as CustomEvent<OpenLabelPickerDetail>).detail);
    window.addEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    try {
      const context = ctx("issues", { selectedIssue: selected });
      expect(action!.when(context)).toBe(true);
      action!.handler(context);
    } finally {
      window.removeEventListener(OPEN_LABEL_PICKER_EVENT, listener);
    }

    expect(events).toEqual([{ itemType: "issue", ...selected }]);
  });

  it("cheatsheet.open does not fire on the reviews page (roborev owns ?)", () => {
    // Roborev's ReviewsView has its own window-level `?` handler that
    // opens a help modal. If middleman's cheatsheet also fires on `?`,
    // both modals open and the cheatsheet's filter input steals focus,
    // causing roborev's Escape handler to short-circuit on its
    // tag === "INPUT" guard. Gate the action by page to avoid that.
    const cheatsheet = defaultActions.find((a) => a.id === "cheatsheet.open");
    expect(cheatsheet).toBeDefined();
    expect(cheatsheet!.when(ctx("reviews"))).toBe(false);
    expect(cheatsheet!.when(ctx("pulls"))).toBe(true);
    expect(cheatsheet!.when(ctx("issues"))).toBe(true);
  });

  it("sidebar.toggle reserves the chord everywhere but only toggles pages with a sidebar target", () => {
    const action = defaultActions.find((a) => a.id === "sidebar.toggle");
    expect(action).toBeDefined();

    const visible = action!.visible ?? action!.when;

    expect(action!.when(ctx("activity"))).toBe(true);
    expect(visible(ctx("activity"))).toBe(false);
    setSidebarCollapsed(false);
    action!.handler(ctx("activity"));
    expect(isSidebarCollapsed()).toBe(false);

    expect(action!.when(ctx("repos"))).toBe(true);
    expect(visible(ctx("repos"))).toBe(false);
    expect(
      action!.when(
        ctx("pulls", {
          route: { page: "pulls", view: "list" } as never,
        }),
      ),
    ).toBe(true);
    expect(
      visible(
        ctx("pulls", {
          route: { page: "pulls", view: "list" } as never,
        }),
      ),
    ).toBe(true);
    action!.handler(
      ctx("pulls", {
        route: { page: "pulls", view: "list" } as never,
      }),
    );
    expect(isSidebarCollapsed()).toBe(true);
    setSidebarCollapsed(false);
    const compactPulls = ctx("pulls", {
      route: { page: "pulls", view: "list" } as never,
      sidebarTargetAvailable: false,
    });
    expect(action!.when(compactPulls)).toBe(true);
    expect(visible(compactPulls)).toBe(false);
    action!.handler(compactPulls);
    expect(isSidebarCollapsed()).toBe(false);
    expect(
      action!.when(
        ctx("pulls", {
          route: { page: "pulls", view: "board" } as never,
        }),
      ),
    ).toBe(true);
    expect(
      visible(
        ctx("pulls", {
          route: { page: "pulls", view: "board" } as never,
        }),
      ),
    ).toBe(false);
    expect(action!.when(ctx("issues"))).toBe(true);
    expect(visible(ctx("issues"))).toBe(true);
    expect(action!.when(ctx("workspaces"))).toBe(true);
    expect(visible(ctx("workspaces"))).toBe(true);
    expect(
      action!.when(
        ctx("terminal", {
          route: { page: "terminal", workspaceId: "1" } as never,
        }),
      ),
    ).toBe(true);
    expect(
      visible(
        ctx("terminal", {
          route: { page: "terminal", workspaceId: "1" } as never,
        }),
      ),
    ).toBe(true);
  });

  it("does not enable pull request number navigation on Kata", () => {
    const list = defaultActions.find((a) => a.id === "nav.pulls.list");
    const board = defaultActions.find((a) => a.id === "nav.pulls.board");

    expect(list).toBeDefined();
    expect(board).toBeDefined();
    expect(list!.when(ctx("kata"))).toBe(false);
    expect(board!.when(ctx("kata"))).toBe(false);
    expect(list!.when(ctx("pulls"))).toBe(true);
    expect(board!.when(ctx("pulls"))).toBe(true);
  });

  it("opens the repo browser from a selected pull request", () => {
    const action = command("repo.browser.open");
    const context = ctx("pulls", { selectedPR: selected });

    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe("/repo/browser?provider=github&platform_host=github.com&repo_path=octo%2Frepo");
  });

  it("opens the repo browser from selected issue and activity contexts", () => {
    const action = command("repo.browser.open");

    const issueContext = ctx("issues", {
      selectedPR: staleSelected,
      selectedIssue: selected,
    });
    expect(action.when(issueContext)).toBe(true);
    action.handler(issueContext);
    expect(locationPath()).toBe("/repo/browser?provider=github&platform_host=github.com&repo_path=octo%2Frepo");

    window.history.replaceState(
      null,
      "",
      "/?selected=issue:8&provider=gitlab&platform_host=gitlab.example.com&repo_path=group%2Fproject",
    );
    const context = ctx("activity", { selectedPR: staleSelected });
    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe(
      "/repo/browser?provider=gitlab&platform_host=gitlab.example.com&repo_path=group%2Fproject",
    );
  });

  it("opens the repo browser from the route-selected issue before stale issue store state", () => {
    const action = command("repo.browser.open");
    const context = ctx("issues", {
      selectedIssue: staleSelected,
      route: {
        page: "issues",
        selected,
      },
    });

    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe("/repo/browser?provider=github&platform_host=github.com&repo_path=octo%2Frepo");
  });

  it("opens the repo browser for the current repo-browser route", () => {
    const action = command("repo.browser.open");
    const context = ctx("repo-browser", {
      route: {
        page: "repo-browser",
        provider: "forgejo",
        platformHost: "code.example.com",
        owner: "team",
        name: "tools",
        repoPath: "team/tools",
      },
    });

    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe("/repo/browser?provider=forgejo&platform_host=code.example.com&repo_path=team%2Ftools");
  });

  it("opens the repo browser from focus routes without stale selected item state", () => {
    const action = command("repo.browser.open");
    const pullContext = ctx("focus", {
      selectedPR: staleSelected,
      route: {
        page: "focus",
        itemType: "pr",
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "group",
        name: "project",
        repoPath: "group/project",
        number: 42,
      } as never,
    });

    expect(action.when(pullContext)).toBe(true);
    action.handler(pullContext);
    expect(locationPath()).toBe(
      "/repo/browser?provider=gitlab&platform_host=gitlab.example.com&repo_path=group%2Fproject",
    );

    const issueContext = ctx("focus", {
      selectedIssue: staleSelected,
      route: {
        page: "focus",
        itemType: "issue",
        provider: "forgejo",
        platformHost: "code.example.com",
        owner: "team",
        name: "docs",
        repoPath: "team/docs",
        number: 7,
      } as never,
    });

    action.handler(issueContext);
    expect(locationPath()).toBe("/repo/browser?provider=forgejo&platform_host=code.example.com&repo_path=team%2Fdocs");
  });

  it("opens the repo browser for a uniquely configured workspace repo", () => {
    const action = command("repo.browser.open");
    window.__middleman_config = {
      ui: {
        repo: { owner: "acme", name: "widgets" },
      },
    };
    setConfiguredRepos([
      configuredRepo({
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
      }),
    ]);
    const context = ctx("workspaces", { selectedPR: staleSelected });

    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe("/repo/browser?provider=github&platform_host=github.com&repo_path=acme%2Fwidgets");
  });

  it("opens the repo browser from fully qualified workspace repo config", () => {
    const action = command("repo.browser.open");
    window.__middleman_config = {
      ui: {
        repo: {
          provider: "gitea",
          platform_host: "code.example.com",
          repo_path: "team/widgets",
          owner: "acme",
          name: "widgets",
        },
      },
    };
    setConfiguredRepos([
      configuredRepo({
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
      }),
      configuredRepo({
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
      }),
    ]);
    const context = ctx("workspaces", { selectedPR: staleSelected });

    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe("/repo/browser?provider=gitea&platform_host=code.example.com&repo_path=team%2Fwidgets");
  });

  it("opens the repo browser from canonical workspace repo identity", () => {
    const action = command("repo.browser.open");
    window.__middleman_config = {
      ui: {
        repo: {
          provider: "gitlab",
          platform_host: "gitlab.example.com",
          repo_path: "group/subgroup/widgets",
        },
      },
    };
    setConfiguredRepos([]);
    const context = ctx("workspaces", { selectedPR: staleSelected });

    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe(
      "/repo/browser?provider=gitlab&platform_host=gitlab.example.com&repo_path=group%2Fsubgroup%2Fwidgets",
    );
  });

  it("uses workspace provider and host hints when matching configured repos", () => {
    const action = command("repo.browser.open");
    window.__middleman_config = {
      ui: {
        repo: {
          provider: "gitlab",
          platform_host: "gitlab.example.com",
          owner: "acme",
          name: "widgets",
        },
      },
    };
    setConfiguredRepos([
      configuredRepo({
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
      }),
      configuredRepo({
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "acme",
        name: "widgets",
        repoPath: "group/widgets",
      }),
    ]);
    const context = ctx("workspaces", { selectedPR: staleSelected });

    expect(action.when(context)).toBe(true);
    action.handler(context);

    expect(locationPath()).toBe(
      "/repo/browser?provider=gitlab&platform_host=gitlab.example.com&repo_path=group%2Fwidgets",
    );
  });

  it("hides the repo browser command when workspace repo context is ambiguous", () => {
    const action = command("repo.browser.open");
    window.__middleman_config = {
      ui: {
        repo: { owner: "acme", name: "widgets" },
      },
    };
    setConfiguredRepos([
      configuredRepo({
        owner: "acme",
        name: "widgets",
        platformHost: "github.com",
      }),
      configuredRepo({
        owner: "acme",
        name: "widgets",
        platformHost: "ghe.example.com",
      }),
    ]);

    expect(action.when(ctx("workspaces"))).toBe(false);
  });
});
