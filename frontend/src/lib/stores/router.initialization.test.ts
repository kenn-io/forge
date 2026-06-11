import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const issueRoute = "/host/ghe.example.com/issues/github/acme/widget/7";

async function importRouterAt(path: string) {
  vi.resetModules();
  window.history.replaceState(null, "", path);
  return import("./router.svelte.js");
}

describe("router initialization", () => {
  afterEach(() => {
    delete window.__middleman_config;
    delete window.__BASE_PATH__;
    window.history.replaceState(null, "", "/");
    vi.resetModules();
  });

  it("withBasePath prefixes hrefs when mounted under a base path", async () => {
    window.__BASE_PATH__ = "/middleman/";
    const { withBasePath } = await importRouterAt("/middleman/docs");
    expect(withBasePath("/docs?folder=notes&doc=README.md")).toBe("/middleman/docs?folder=notes&doc=README.md");
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

  it("preserves messages route state on initial load", async () => {
    const { getRoute, getPage } = await importRouterAt("/messages?q=from%3Aops&message=7");

    expect(getRoute()).toEqual({
      page: "messages",
      q: "from:ops",
      message: "7",
    });
    expect(getPage()).toBe("messages");
  });

  it("uses embed initialRoute before the first app render", async () => {
    window.__middleman_config = {
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
