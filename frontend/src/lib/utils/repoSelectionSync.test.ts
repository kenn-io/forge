import { describe, expect, it } from "vite-plus/test";
import { globalRepoForSelectedRoute } from "./repoSelectionSync.js";
import type { Route } from "../stores/router.svelte.ts";

const prSelected = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "tools",
  repoPath: "acme/tools",
  number: 42,
};

const issueSelected = {
  provider: "gitlab",
  platformHost: "gitlab.example.com",
  owner: "team",
  name: "infra",
  repoPath: "team/infra",
  number: 7,
};

describe("globalRepoForSelectedRoute", () => {
  it("returns provider|platformHost/repoPath for a pulls route with a selected PR", () => {
    const route: Route = {
      page: "pulls",
      view: "list",
      selected: prSelected,
    };
    expect(globalRepoForSelectedRoute(route)).toBe("github|github.com/acme/tools");
  });

  it("returns provider|platformHost/repoPath for an issues route with a selected issue", () => {
    const route: Route = {
      page: "issues",
      selected: issueSelected,
    };
    expect(globalRepoForSelectedRoute(route)).toBe("gitlab|gitlab.example.com/team/infra");
  });

  it("returns provider|platformHost/repoPath for a focus PR route", () => {
    const route: Route = {
      page: "focus",
      itemType: "pr",
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "tools",
      repoPath: "acme/tools",
      number: 42,
    };
    expect(globalRepoForSelectedRoute(route)).toBe("github|github.com/acme/tools");
  });

  it("returns provider|platformHost/repoPath for a focus issue route", () => {
    const route: Route = {
      page: "focus",
      itemType: "issue",
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "team",
      name: "infra",
      repoPath: "team/infra",
      number: 7,
    };
    expect(globalRepoForSelectedRoute(route)).toBe("gitlab|gitlab.example.com/team/infra");
  });

  it("keeps nested repo paths intact", () => {
    const route: Route = {
      page: "issues",
      selected: {
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "Group/SubGroup",
        name: "Project.Special",
        repoPath: "Group/SubGroup/Project.Special",
        number: 17,
      },
    };
    expect(globalRepoForSelectedRoute(route)).toBe("gitlab|gitlab.example.com/Group/SubGroup/Project.Special");
  });

  it("keeps same-host same-path provider collisions distinct", () => {
    const githubRoute: Route = {
      page: "issues",
      selected: {
        provider: "github",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
        number: 101,
      },
    };
    const giteaRoute: Route = {
      page: "issues",
      selected: {
        provider: "gitea",
        platformHost: "github.com",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
        number: 901,
      },
    };

    expect(globalRepoForSelectedRoute(githubRoute)).toBe("github|github.com/acme/widgets");
    expect(globalRepoForSelectedRoute(giteaRoute)).toBe("gitea|github.com/acme/widgets");
  });

  it("returns undefined for a pulls list route without a selection", () => {
    const route: Route = { page: "pulls", view: "list" };
    expect(globalRepoForSelectedRoute(route)).toBeUndefined();
  });

  it("returns undefined for an issues list route without a selection", () => {
    const route: Route = { page: "issues" };
    expect(globalRepoForSelectedRoute(route)).toBeUndefined();
  });

  it("returns undefined for activity, repos, settings, reviews, workspaces", () => {
    const pages: Route[] = [
      { page: "activity" },
      { page: "repos" },
      { page: "settings" },
      { page: "reviews" },
      { page: "workspaces" },
    ];
    for (const route of pages) {
      expect(globalRepoForSelectedRoute(route)).toBeUndefined();
    }
  });

  it("returns undefined for focus list-only routes (mrs/issues without a specific item)", () => {
    expect(
      globalRepoForSelectedRoute({
        page: "focus",
        itemType: "mrs",
      }),
    ).toBeUndefined();
    expect(
      globalRepoForSelectedRoute({
        page: "focus",
        itemType: "issues",
      }),
    ).toBeUndefined();
  });

  it("throws when platformHost is missing on the selected item", () => {
    const route: Route = {
      page: "pulls",
      view: "list",
      selected: {
        provider: "custom",
        owner: "acme",
        name: "tools",
        repoPath: "acme/tools",
        number: 42,
      },
    };
    expect(() => globalRepoForSelectedRoute(route)).toThrow("selected route is missing platformHost");
  });

  it("throws when provider is missing on the selected item", () => {
    const route = {
      page: "pulls",
      view: "list",
      selected: {
        platformHost: "github.com",
        owner: "acme",
        name: "tools",
        repoPath: "acme/tools",
        number: 42,
      },
    } as unknown as Route;
    expect(() => globalRepoForSelectedRoute(route)).toThrow("selected route is missing provider");
  });
});
