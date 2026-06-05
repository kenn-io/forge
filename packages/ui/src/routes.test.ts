import { describe, expect, it } from "vite-plus/test";
import {
  buildFocusIssueRoute,
  buildFocusListRoute,
  buildFocusPullRequestFilesRoute,
  buildFocusPullRequestRoute,
  buildIssueRoute,
  buildProviderIssueRoute,
  buildProviderPullRequestFilesRoute,
  buildProviderPullRequestRoute,
  buildPullRequestFilesRoute,
  buildPullRequestRoute,
  buildRoutedItemRoute,
} from "./routes.js";

const githubWidgets = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
} as const;

describe("route item builders", () => {
  it("builds pull request conversation and files routes from provider repo identity", () => {
    const ref = { ...githubWidgets, number: 42 };

    expect(buildPullRequestRoute(ref)).toBe("/pulls/github/acme/widgets/42");
    expect(buildPullRequestFilesRoute(ref)).toBe("/pulls/github/acme/widgets/42/files");
  });

  it("builds issue routes with encoded platform hosts", () => {
    expect(
      buildIssueRoute({
        ...githubWidgets,
        platformHost: "ghe.example.com/team one",
        number: 7,
      }),
    ).toBe("/host/ghe.example.com%2Fteam%20one/issues/github/acme/widgets/7");
  });

  it("omits absent issue platform host query strings", () => {
    expect(
      buildIssueRoute({
        provider: "github",
        owner: "acme",
        name: "widgets",
        repoPath: "acme/widgets",
        number: 7,
      }),
    ).toBe("/issues/github/acme/widgets/7");
  });

  it("builds provider repo-path routes with escaped refs", () => {
    const deep = {
      provider: "gitlab",
      platformHost: "gitlab.example.com:8443",
      repoPath: "Group/SubGroup/SubGroup 2/My_Project.v2",
      number: 12,
    };

    expect(buildProviderPullRequestRoute(deep)).toBe(
      "/host/gitlab.example.com%3A8443/pulls/gitlab/Group%2FSubGroup%2FSubGroup%202/My_Project.v2/12",
    );
    expect(buildProviderPullRequestFilesRoute(deep)).toBe(
      "/host/gitlab.example.com%3A8443/pulls/gitlab/Group%2FSubGroup%2FSubGroup%202/My_Project.v2/12/files",
    );
    expect(buildProviderIssueRoute(deep)).toBe(
      "/host/gitlab.example.com%3A8443/issues/gitlab/Group%2FSubGroup%2FSubGroup%202/My_Project.v2/12",
    );
  });

  it("builds focus item and list routes", () => {
    expect(
      buildFocusPullRequestRoute({
        ...githubWidgets,
        number: 42,
      }),
    ).toBe("/focus/pulls/github/acme/widgets/42");
    expect(
      buildFocusPullRequestFilesRoute({
        ...githubWidgets,
        number: 42,
      }),
    ).toBe("/focus/pulls/github/acme/widgets/42/files");
    expect(
      buildFocusIssueRoute({
        ...githubWidgets,
        platformHost: "ghe.example.com",
        number: 7,
      }),
    ).toBe("/focus/host/ghe.example.com/issues/github/acme/widgets/7");
    expect(buildFocusListRoute({ itemType: "mrs" })).toBe("/focus/mrs");
    expect(buildFocusListRoute({ itemType: "issues" })).toBe("/focus/issues");
    expect(buildFocusListRoute({ itemType: "mrs", repo: "acme/widgets" })).toBe("/focus/mrs?repo=acme%2Fwidgets");
  });

  it("builds routed item routes for normal and focus surfaces", () => {
    const pr = {
      itemType: "pr",
      ...githubWidgets,
      number: 42,
    } as const;
    const issue = {
      itemType: "issue",
      ...githubWidgets,
      platformHost: "ghe.example.com",
      number: 7,
    } as const;

    expect(buildRoutedItemRoute(pr)).toBe("/pulls/github/acme/widgets/42");
    expect(buildRoutedItemRoute(issue)).toBe("/host/ghe.example.com/issues/github/acme/widgets/7");
    expect(buildRoutedItemRoute(pr, { focus: true })).toBe("/focus/pulls/github/acme/widgets/42");
    expect(buildRoutedItemRoute(issue, { focus: true })).toBe(
      "/focus/host/ghe.example.com/issues/github/acme/widgets/7",
    );
  });
});
