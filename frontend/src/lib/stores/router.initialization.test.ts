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
    window.history.replaceState(null, "", "/");
    vi.resetModules();
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
