import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";
import {
  navigate,
  getRoute,
  getDetailTab,
  isDiffView,
  getSelectedPRFromRoute,
  getPage,
} from "./router.svelte.js";

const prRoute = "/pulls/github/acme/widgets/42";
const prFilesRoute = "/pulls/github/acme/widgets/42/files";
const focusPrRoute = "/focus/pulls/github/acme/widgets/42";
const focusPrFilesRoute = "/focus/pulls/github/acme/widgets/42/files";
const prRef = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 42,
};

describe("router /pulls files route", () => {
  beforeEach(() => {
    navigate("/pulls");
  });

  it("parses provider pull files route as list view with files tab", () => {
    navigate(prFilesRoute);
    expect(getRoute()).toEqual({
      page: "pulls",
      view: "list",
      selected: prRef,
      tab: "files",
    });
  });

  it("getDetailTab returns files for files route", () => {
    navigate(prFilesRoute);
    expect(getDetailTab()).toBe("files");
  });

  it("getDetailTab returns conversation for non-files PR route", () => {
    navigate(prRoute);
    expect(getDetailTab()).toBe("conversation");
  });

  it("isDiffView returns true for files route", () => {
    navigate(prFilesRoute);
    expect(isDiffView()).toBe(true);
  });

  it("isDiffView returns false for conversation route", () => {
    navigate(prRoute);
    expect(isDiffView()).toBe(false);
  });

  it("isDiffView returns false for board view", () => {
    navigate("/pulls/board");
    expect(isDiffView()).toBe(false);
  });

  it("getSelectedPRFromRoute returns PR for files route", () => {
    navigate(prFilesRoute);
    expect(getSelectedPRFromRoute()).toEqual(prRef);
  });

  it("getSelectedPRFromRoute returns PR for conversation route", () => {
    navigate(prRoute);
    expect(getSelectedPRFromRoute()).toEqual(prRef);
  });

  it("getSelectedPRFromRoute returns null for pull list without selection", () => {
    navigate("/pulls");
    expect(getSelectedPRFromRoute()).toBeNull();
  });

  it("parses focus provider pull files route as focused PR with files tab", () => {
    navigate(focusPrFilesRoute);
    expect(getRoute()).toEqual({
      page: "focus",
      itemType: "pr",
      ...prRef,
      tab: "files",
    });
  });

  it("parses hosted focus provider pull files route as focused PR with files tab", () => {
    navigate("/focus/host/ghe.example.com/pulls/github/acme/widgets/42/files");
    expect(getRoute()).toEqual({
      page: "focus",
      itemType: "pr",
      provider: "github",
      platformHost: "ghe.example.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 42,
      tab: "files",
    });
  });

  it("focus PR files route participates in detail-tab helpers", () => {
    navigate(focusPrFilesRoute);
    expect(getDetailTab()).toBe("files");
    expect(isDiffView()).toBe(true);
  });

  it("focus PR conversation route remains the conversation tab", () => {
    navigate(focusPrRoute);
    expect(getDetailTab()).toBe("conversation");
    expect(isDiffView()).toBe(false);
  });

  it("getPage returns pulls for files route", () => {
    navigate(prFilesRoute);
    expect(getPage()).toBe("pulls");
  });
});

describe("router basic routes", () => {
  it("parses /design-system", () => {
    navigate("/design-system");
    expect(getRoute()).toEqual({ page: "design-system" });
    expect(getPage()).toBe("design-system");
  });

  it("parses /pulls as list view", () => {
    navigate("/pulls");
    expect(getRoute()).toEqual({ page: "pulls", view: "list" });
  });

  it("parses /pulls/board as board view", () => {
    navigate("/pulls/board");
    expect(getRoute()).toEqual({ page: "pulls", view: "board" });
  });

  it("does not parse legacy owner/name pull detail routes", () => {
    navigate("/pulls/org/repo/7");
    expect(getRoute()).toEqual({ page: "pulls", view: "list" });
  });

  it("does not parse old query-param pull detail routes", () => {
    navigate("/pulls/detail?provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&number=42");
    expect(getRoute()).toEqual({ page: "pulls", view: "list" });
  });

  it("parses provider pull routes with escaped nested repo paths", () => {
    navigate(
      "/host/gitlab.example.com%3A8443/pulls/gitlab/Group%2FSubGroup%2FSubGroup%202/My_Project.v2/12",
    );

    expect(getRoute()).toEqual({
      page: "pulls",
      view: "list",
      selected: {
        owner: "Group/SubGroup/SubGroup 2",
        name: "My_Project.v2",
        provider: "gitlab",
        platformHost: "gitlab.example.com:8443",
        repoPath: "Group/SubGroup/SubGroup 2/My_Project.v2",
        number: 12,
      },
    });
  });

  it("uses public Forgejo and Gitea hosts for provider pull routes", () => {
    navigate("/pulls/forgejo/forgejo/forgejo/12/files");
    expect(getRoute()).toEqual({
      page: "pulls",
      view: "list",
      selected: {
        owner: "forgejo",
        name: "forgejo",
        provider: "forgejo",
        platformHost: "codeberg.org",
        repoPath: "forgejo/forgejo",
        number: 12,
      },
      tab: "files",
    });

    navigate("/pulls/gitea/gitea/tea/3");
    expect(getRoute()).toEqual({
      page: "pulls",
      view: "list",
      selected: {
        owner: "gitea",
        name: "tea",
        provider: "gitea",
        platformHost: "gitea.com",
        repoPath: "gitea/tea",
        number: 3,
      },
    });
  });

  it("parses provider pull files routes with escaped nested repo paths", () => {
    navigate(
      "/host/gitlab.example.com%3A8443/pulls/gitlab/Group%2FSubGroup%2FSubGroup%202/My_Project.v2/12/files",
    );

    expect(getRoute()).toEqual({
      page: "pulls",
      view: "list",
      selected: {
        owner: "Group/SubGroup/SubGroup 2",
        name: "My_Project.v2",
        provider: "gitlab",
        platformHost: "gitlab.example.com:8443",
        repoPath: "Group/SubGroup/SubGroup 2/My_Project.v2",
        number: 12,
      },
      tab: "files",
    });
  });

  it("parses / as activity", () => {
    navigate("/");
    expect(getRoute()).toEqual({ page: "activity" });
  });

  it("parses /repos", () => {
    navigate("/repos");
    expect(getRoute()).toEqual({ page: "repos" });
  });

  it("parses /repos/", () => {
    navigate("/repos/");
    expect(getRoute()).toEqual({ page: "repos" });
  });

  it("does not parse legacy issue detail routes", () => {
    navigate("/issues/org/repo/3");
    expect(getRoute()).toEqual({ page: "issues" });
  });

  it("does not parse old query-param issue detail routes", () => {
    navigate("/issues/detail?provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&number=42");
    expect(getRoute()).toEqual({ page: "issues" });
  });

  it("parses provider issue routes with special characters in repo paths", () => {
    navigate(
      "/host/gitlab.example.test%3A8443/issues/gitlab/Team%20One%2FSub%20Team/project%2B%231/7",
    );

    expect(getRoute()).toEqual({
      page: "issues",
      selected: {
        owner: "Team One/Sub Team",
        name: "project+#1",
        provider: "gitlab",
        platformHost: "gitlab.example.test:8443",
        repoPath: "Team One/Sub Team/project+#1",
        number: 7,
      },
    });
  });

  it("parses focus provider pull and issue routes", () => {
    navigate("/focus/pulls/github/acme/widgets/42");
    expect(getRoute()).toEqual({
      page: "focus",
      itemType: "pr",
      ...prRef,
    });

    navigate("/focus/host/ghe.example.com/issues/github/acme/widgets/7");
    expect(getRoute()).toEqual({
      page: "focus",
      itemType: "issue",
      provider: "github",
      platformHost: "ghe.example.com",
      owner: "acme",
      name: "widgets",
      repoPath: "acme/widgets",
      number: 7,
    });
  });

  it("does not parse old query-param focus detail routes", () => {
    navigate("/focus/pr?provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&number=42");
    expect(getRoute()).toEqual({ page: "activity" });
  });

  it("treats legacy /workspaces/panel routes as workspaces page", () => {
    navigate("/workspaces/panel/github.com/acme/widgets/42");
    expect(getRoute()).toEqual({ page: "workspaces" });
    expect(getPage()).toBe("workspaces");
  });
});

describe("router embed-workspace routes", () => {
  it("parses /workspaces/embed/list", () => {
    navigate("/workspaces/embed/list");
    expect(getRoute()).toEqual({ page: "embed-workspace-list" });
    expect(getPage()).toBe("embed-workspace-list");
  });

  it("parses /workspaces/embed/terminal without an id", () => {
    navigate("/workspaces/embed/terminal");
    expect(getRoute()).toEqual({
      page: "embed-workspace-terminal",
      workspaceId: "",
    });
  });

  it("parses /workspaces/embed/terminal/:workspaceId", () => {
    navigate("/workspaces/embed/terminal/abc-123");
    expect(getRoute()).toEqual({
      page: "embed-workspace-terminal",
      workspaceId: "abc-123",
    });
  });

  it("parses /workspaces/embed/detail/:provider/pr/:host/:number with repo_path", () => {
    navigate(
      "/workspaces/embed/detail/github/pr/github.com/42?repo_path=acme%2Fwidgets",
    );
    expect(getRoute()).toEqual({
      page: "embed-workspace-detail",
      provider: "github",
      itemType: "pr",
      platformHost: "github.com",
      repoPath: "acme/widgets",
      owner: "acme",
      name: "widgets",
      number: 42,
    });
  });

  it("parses legacy /workspaces/embed/detail/:type/:host/:owner/:name/:number", () => {
    navigate(
      "/workspaces/embed/detail/issue/gitlab.example.com/acme/widgets/7?tab=issue",
    );
    expect(getRoute()).toEqual({
      page: "embed-workspace-detail",
      provider: "gitlab",
      itemType: "issue",
      platformHost: "gitlab.example.com",
      repoPath: "acme/widgets",
      owner: "acme",
      name: "widgets",
      number: 7,
      tab: "issue",
    });
  });

  it("keeps legacy GitHub Enterprise embed detail URLs on GitHub", () => {
    navigate(
      "/workspaces/embed/detail/pr/ghe.example.com/acme/widgets/42",
    );
    expect(getRoute()).toEqual({
      page: "embed-workspace-detail",
      provider: "github",
      itemType: "pr",
      platformHost: "ghe.example.com",
      repoPath: "acme/widgets",
      owner: "acme",
      name: "widgets",
      number: 42,
    });
  });

  it("parses legacy provider-explicit detail path without repo_path", () => {
    navigate(
      "/workspaces/embed/detail/github/pr/github.com/acme/widgets/42?branch=main",
    );
    expect(getRoute()).toEqual({
      page: "embed-workspace-detail",
      provider: "github",
      itemType: "pr",
      platformHost: "github.com",
      repoPath: "acme/widgets",
      owner: "acme",
      name: "widgets",
      number: 42,
      branch: "main",
    });
  });

  it("parses /workspaces/embed/detail with branch and tab query", () => {
    navigate(
      "/workspaces/embed/detail/gitlab/issue/git.example.com/7" +
        "?repo_path=org%2Fteam%2Frepo&branch=feature%2Fx&tab=reviews",
    );
    expect(getRoute()).toEqual({
      page: "embed-workspace-detail",
      provider: "gitlab",
      itemType: "issue",
      platformHost: "git.example.com",
      repoPath: "org/team/repo",
      owner: "org/team",
      name: "repo",
      number: 7,
      branch: "feature/x",
      tab: "reviews",
    });
  });

  it("ignores unknown tab values on the detail route", () => {
    navigate(
      "/workspaces/embed/detail/github/pr/github.com/1?repo_path=o%2Fn&tab=garbage",
    );
    const route = getRoute();
    expect(route).toEqual({
      page: "embed-workspace-detail",
      provider: "github",
      itemType: "pr",
      platformHost: "github.com",
      repoPath: "o/n",
      owner: "o",
      name: "n",
      number: 1,
    });
  });

  it("parses /workspaces/embed/empty/:reason for each known reason", () => {
    for (const reason of [
      "noSelection",
      "noRepo",
      "noWorkspace",
    ] as const) {
      navigate(`/workspaces/embed/empty/${reason}`);
      expect(getRoute()).toEqual({
        page: "embed-workspace-empty",
        reason,
      });
    }
  });

  it("falls back to the standalone workspaces page for unknown empty reasons", () => {
    navigate("/workspaces/embed/empty/garbage");
    expect(getRoute()).toEqual({ page: "workspaces" });
  });

  it("parses /workspaces/embed/first-run", () => {
    navigate("/workspaces/embed/first-run");
    expect(getRoute()).toEqual({ page: "embed-workspace-first-run" });
    expect(getPage()).toBe("embed-workspace-first-run");
  });

  it("parses /workspaces/embed/project/:project_id", () => {
    navigate("/workspaces/embed/project/prj_abc123");
    expect(getRoute()).toEqual({
      page: "embed-workspace-project",
      projectId: "prj_abc123",
    });
    expect(getPage()).toBe("embed-workspace-project");
  });

  it("falls back to the standalone workspaces page for unknown project_id shapes", () => {
    navigate("/workspaces/embed/project/has slash/extra");
    expect(getRoute()).toEqual({ page: "workspaces" });
  });
});

describe("router navigation events", () => {
  beforeEach(() => {
    navigate("/pulls");
  });

  afterEach(() => {
    delete (window as unknown as { __middleman_config?: unknown }).__middleman_config;
    (window as unknown as { __middleman_notify_config_changed?: () => void })
      .__middleman_notify_config_changed?.();
  });

  function installOnNavigate(spy: ReturnType<typeof vi.fn>): void {
    (window as unknown as { __middleman_config?: unknown }).__middleman_config = { onNavigate: spy };
    (window as unknown as { __middleman_notify_config_changed?: () => void })
      .__middleman_notify_config_changed?.();
  }

  it("fires onNavigate with pull payload for files route", () => {
    const spy = vi.fn();
    installOnNavigate(spy);

    navigate(prFilesRoute);

    expect(spy).toHaveBeenCalled();
    const payload = spy.mock.calls[spy.mock.calls.length - 1]![0];
    expect(payload.type).toBe("pull");
    expect(payload.focus).toBe(false);
    expect(payload.owner).toBe("acme");
    expect(payload.name).toBe("widgets");
    expect(payload.number).toBe(42);
  });

  it("fires onNavigate with pull payload for conversation route", () => {
    const spy = vi.fn();
    installOnNavigate(spy);

    navigate(prRoute);

    const payload = spy.mock.calls[spy.mock.calls.length - 1]![0];
    expect(payload.type).toBe("pull");
    expect(payload.owner).toBe("acme");
    expect(payload.name).toBe("widgets");
    expect(payload.number).toBe(42);
  });

  it("fires onNavigate without owner/name/number for /pulls list", () => {
    const spy = vi.fn();
    installOnNavigate(spy);

    navigate("/pulls");

    const payload = spy.mock.calls[spy.mock.calls.length - 1]![0];
    expect(payload.type).toBe("pull");
    expect(payload.owner).toBeUndefined();
    expect(payload.number).toBeUndefined();
  });

  it("maps /design-system to activity navigation events", () => {
    const spy = vi.fn();
    installOnNavigate(spy);

    navigate("/design-system");

    const payload = spy.mock.calls[spy.mock.calls.length - 1]![0];
    expect(payload.type).toBe("activity");
    expect(payload.view).toBe("/design-system");
  });

  it("maps every embed-workspace route to a workspaces navigation event", () => {
    const spy = vi.fn();
    installOnNavigate(spy);

    const embedPaths = [
      "/workspaces/embed/list",
      "/workspaces/embed/terminal/ws-1",
      "/workspaces/embed/detail/github/pr/github.com/42?repo_path=acme%2Fwidget",
      "/workspaces/embed/empty/noWorkspace",
      "/workspaces/embed/first-run",
      "/workspaces/embed/project/prj_abc123",
    ];

    for (const path of embedPaths) {
      spy.mockClear();
      navigate(path);
      const payload = spy.mock.calls[spy.mock.calls.length - 1]![0];
      expect(payload.type, `type for ${path}`).toBe("workspaces");
      expect(payload.focus, `focus for ${path}`).toBe(false);
    }
  });
});

describe("router window bridges", () => {
  beforeEach(() => {
    navigate("/pulls");
  });

  it("exposes __middleman_navigate_to_route as a window global", () => {
    const bridge = (window as unknown as {
      __middleman_navigate_to_route?: (route: string) => void;
    }).__middleman_navigate_to_route;
    expect(typeof bridge).toBe("function");
  });

  it("__middleman_navigate_to_route updates the SPA route", () => {
    const bridge = (window as unknown as {
      __middleman_navigate_to_route: (route: string) => void;
    }).__middleman_navigate_to_route;

    bridge("/workspaces/embed/first-run");
    expect(getRoute()).toEqual({ page: "embed-workspace-first-run" });
    expect(getPage()).toBe("embed-workspace-first-run");

    bridge("/workspaces/embed/project/prj_xyz");
    expect(getRoute()).toEqual({
      page: "embed-workspace-project",
      projectId: "prj_xyz",
    });
  });
});
