import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const issueRoute = "/host/ghe.example.com/issues/github/acme/widget/7";

async function importRouterAt(path: string) {
  vi.resetModules();
  window.history.replaceState(null, "", path);
  return import("./router.svelte.js");
}

describe("router initialization", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
  });

  afterEach(() => {
    delete window.__kenn_forge_config;
    delete window.__BASE_PATH__;
    vi.restoreAllMocks();
    window.sessionStorage.clear();
    window.history.replaceState(null, "", "/");
    vi.resetModules();
  });

  it("withBasePath prefixes hrefs when mounted under a base path", async () => {
    window.__BASE_PATH__ = "/kenn-forge/";
    const { withBasePath } = await importRouterAt("/kenn-forge/docs");
    expect(withBasePath("/docs?folder=notes&doc=README.md")).toBe("/kenn-forge/docs?folder=notes&doc=README.md");
  });

  it("withBasePath is a no-op at the root base path", async () => {
    window.__BASE_PATH__ = "/";
    const { withBasePath } = await importRouterAt("/docs");
    expect(withBasePath("/docs?folder=notes")).toBe("/docs?folder=notes");
  });

  it("preserves provider issue route state on initial load", async () => {
    const { getRoute } = await importRouterAt(issueRoute);

    expect(getRoute()).toEqual({
      page: "issues",
      selected: {
        provider: "github",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
        platformHost: "ghe.example.com",
      },
    });
  });

  it("preserves kata route state on initial load", async () => {
    const { getRoute, getPage } = await importRouterAt("/kata?issue=issue-email-susan");

    expect(getRoute()).toEqual({ page: "kata", issue: "issue-email-susan" });
    expect(getPage()).toBe("kata");
  });

  it("preserves kata view and scope route state on initial load", async () => {
    const { getRoute, getPage } = await importRouterAt("/kata?view=inbox&scope=project-kata");

    expect(getRoute()).toEqual({ page: "kata", view: "inbox", scope: "project-kata" });
    expect(getPage()).toBe("kata");
  });

  it("uses embed initialRoute before the first app render", async () => {
    window.__kenn_forge_config = {
      embed: {
        initialRoute: "/workspaces/embed/detail/gitlab/pr/git.example.com/42" + "?repo_path=group%2Fproject",
      },
    };
    const { getRoute } = await importRouterAt("/");

    expect(getRoute()).toEqual({
      page: "embed-workspace-detail",
      provider: "gitlab",
      itemType: "pr",
      platformHost: "git.example.com",
      repoPath: "group/project",
      owner: "group",
      name: "project",
      number: 42,
    });
    expect(window.location.pathname + window.location.search).toBe(
      "/workspaces/embed/detail/gitlab/pr/git.example.com/42" + "?repo_path=group%2Fproject",
    );
  });

  it("defaults the last workspace route to /workspaces on initial load", async () => {
    const { getLastWorkspaceRoute } = await importRouterAt("/");

    expect(getLastWorkspaceRoute()).toBe("/workspaces");
  });

  it("seeds the last workspace route from an initial load on a terminal route", async () => {
    const { getLastWorkspaceRoute } = await importRouterAt("/terminal/ws-seed");

    expect(getLastWorkspaceRoute()).toBe("/terminal/ws-seed");
  });

  it("restores the last Activity route after reloading on Workspaces", async () => {
    const activityRoute = "/?types=new_pr,comment,review,force_push,notification";
    const router = await importRouterAt(activityRoute);
    router.navigate("/workspaces");

    const reloadedRouter = await importRouterAt("/workspaces");

    expect(reloadedRouter.getLastActivityRoute()).toBe(activityRoute);
  });

  it.each(["/workspaces", "/unexpected", "//", "///?types=new_pr", "//example.com/?types=new_pr"])(
    "ignores an invalid stored Activity route: %s",
    async (storedRoute) => {
      window.sessionStorage.setItem("kenn-forge:last-activity-route", storedRoute);

      const router = await importRouterAt("/workspaces");

      expect(router.getLastActivityRoute()).toBe("/");
    },
  );

  it("uses the default Activity route when session storage reads are blocked", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    const router = await importRouterAt("/workspaces");

    expect(router.getLastActivityRoute()).toBe("/");
  });

  it("keeps the in-memory Activity route when session storage writes are blocked", async () => {
    const activityRoute = "/?types=new_pr,comment,review,force_push,notification";
    const router = await importRouterAt(activityRoute);
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    expect(() => router.navigate("/workspaces")).not.toThrow();
    expect(router.getLastActivityRoute()).toBe(activityRoute);
  });

  it("preserves provider issue route state on popstate", async () => {
    const { getRoute } = await importRouterAt("/issues");

    window.history.pushState(null, "", issueRoute);
    window.dispatchEvent(new PopStateEvent("popstate"));

    expect(getRoute()).toEqual({
      page: "issues",
      selected: {
        provider: "github",
        owner: "acme",
        name: "widget",
        repoPath: "acme/widget",
        number: 7,
        platformHost: "ghe.example.com",
      },
    });
  });
});
