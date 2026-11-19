import { describe, expect, it } from "vite-plus/test";
import type { IssueDetail, PullDetail } from "../api/types.js";
import { getRoute, replaceUrl } from "../stores/router.svelte.js";
import { publishWorkspaceTitle, routeTabTitle, workspaceTitle } from "./tab-title.svelte.js";

describe("tab titles", () => {
  it.each([
    ["/", "Activity"],
    ["/actions", "Actions"],
    ["/repos", "Repos"],
    ["/repo/browser?provider=github&repo_path=owner/repo", "owner/repo · Repos"],
    ["/pulls", "Pulls"],
    ["/issues", "Issues"],
    ["/docs", "Docs"],
    ["/workspaces", "Workspaces"],
    ["/terminal/a", "Workspace · Workspaces"],
    ["/settings", "Settings"],
    ["/project-intake", "Add project"],
    ["/design-system", "Design system"],
    ["/worklists/3", "Worklists"],
    ["/queues", "Queues"],
    ["/m/activity", "Activity"],
    ["/m/pulls", "Pulls"],
    ["/m/issues", "Issues"],
    ["/m/workspaces", "Workspaces"],
    ["/m/worklists", "Worklists"],
    ["/m/workspaces/local/a", "Workspace · Workspaces"],
    ["/m/workspaces/local/a/item", "Workspace · Workspaces"],
    ["/focus/mrs", "Pulls"],
    ["/focus/issues", "Issues"],
  ])("names %s", (path, label) => {
    replaceUrl(path);
    expect(routeTabTitle(getRoute())).toBe(`${label} · Forge`);
  });

  it("uses matching details and labels loading or mismatched items", () => {
    const repo = { provider: "github", platform_host: "github.com", repo_path: "owner/repo" };
    const pull = {
      repo,
      repo_owner: "owner",
      repo_name: "repo",
      merge_request: { Number: 1, Title: "Fix login" },
    } as PullDetail;
    const issue = {
      repo,
      repo_owner: "owner",
      repo_name: "repo",
      issue: { Number: 2, Title: "Broken login" },
    } as IssueDetail;
    replaceUrl("/focus/pulls/github/owner/repo/1");
    expect(routeTabTitle(getRoute(), { pull })).toBe("Fix login (owner/repo#1) · Forge");
    expect(routeTabTitle(getRoute())).toBe("Pull request (owner/repo#1) · Forge");
    replaceUrl("/issues/github/owner/repo/2");
    expect(routeTabTitle(getRoute(), { issue })).toBe("Broken login (owner/repo#2) · Forge");
    replaceUrl("/issues/github/owner/repo/3");
    expect(routeTabTitle(getRoute(), { issue })).toBe("Issue (owner/repo#3) · Forge");
  });

  it.each([
    ["/pulls/github/owner/repo/1", "Pull request (owner/repo#1) · Forge"],
    ["/queues/github/owner/repo/1", "Pull request (owner/repo#1) · Forge"],
    ["/worklists/3?selected=issue%3A2&provider=github&repo_path=owner%2Frepo", "Issue (owner/repo#2) · Forge"],
  ])("names selected items on %s", (path, title) => {
    replaceUrl(path);
    expect(routeTabTitle(getRoute())).toBe(title);
  });

  it("names the selected Activity item", () => {
    expect(
      routeTabTitle(
        { page: "activity" },
        {
          activitySelection: {
            itemType: "issue",
            provider: "github",
            owner: "owner",
            name: "repo",
            repoPath: "owner/repo",
            number: 2,
          },
        },
      ),
    ).toBe("Issue (owner/repo#2) · Forge");
  });

  it("keeps workspace names keyed by host and ID", () => {
    publishWorkspaceTitle("remote", "a", "Fix login");
    replaceUrl("/terminal/fleet/remote/a");
    expect(routeTabTitle(getRoute(), { workspaceName: workspaceTitle("remote", "a") })).toBe(
      "Fix login · Workspaces · Forge",
    );
    expect(workspaceTitle(undefined, "a")).toBeUndefined();
    expect(workspaceTitle("remote", "b")).toBeUndefined();
    publishWorkspaceTitle("remote", "a", null);
    expect(workspaceTitle("remote", "a")).toBeUndefined();
  });
});
