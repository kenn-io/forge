import { describe, expect, it } from "vite-plus/test";
import type { IssueDetail, PullDetail } from "../api/types.js";
import { getRoute, replaceUrl } from "../stores/router.svelte.js";
import { routeTabTitle } from "./tab-title.svelte.js";

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
    ["/terminal/a", "Workspace (a) · Workspaces"],
    ["/settings", "Settings"],
    ["/project-intake", "Add project"],
    ["/design-system", "Design system"],
    ["/worklists/3", "Worklist (3) · Worklists"],
    ["/queues", "Queues"],
    ["/m/activity", "Activity"],
    ["/m/pulls", "Pulls"],
    ["/m/issues", "Issues"],
    ["/m/workspaces", "Workspaces"],
    ["/m/worklists", "Worklists"],
    ["/m/workspaces/local/a", "Workspace (a) · Workspaces"],
    ["/m/workspaces/local/a/item", "Linked item (a) · Workspaces"],
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

  it.each([
    ["/docs?folder=notes&doc=project%2Fplan.md", "project/plan.md · notes · Docs · Forge"],
    ["/docs?folder=notes", "notes · Docs · Forge"],
    [
      "/repo/browser?provider=github&repo_path=owner%2Frepo&ref_name=main&path=src%2Fapp.ts",
      "src/app.ts · main · owner/repo · Repos · Forge",
    ],
    ["/m/workspaces/fleet/host-a/ws/item/files", "Linked item (host-a/ws) · Workspaces · Forge"],
    ["/repo/browser?provider=github&repo_path=owner%2Frepo&ref_sha=abc123", "abc123 · owner/repo · Repos · Forge"],
  ])("names the selected screen on %s", (path, title) => {
    replaceUrl(path);
    expect(routeTabTitle(getRoute())).toBe(title);
  });

  it("shortens a repository commit SHA", () => {
    replaceUrl("/repo/browser?provider=github&repo_path=owner%2Frepo&ref_sha=0123456789abcdef0123456789abcdef01234567");
    expect(routeTabTitle(getRoute())).toBe("0123456789ab · owner/repo · Repos · Forge");
  });

  it("uses the GitLab merge request number marker", () => {
    replaceUrl("/pulls/gitlab/group/project/42");
    expect(routeTabTitle(getRoute())).toBe("Pull request (group/project!42) · Forge");
  });

  it("names only the matching worklist", () => {
    replaceUrl("/worklists/3");
    expect(routeTabTitle(getRoute(), { worklists: [{ id: 4, title: "Other" }] })).toBe(
      "Worklist (3) · Worklists · Forge",
    );
    expect(routeTabTitle(getRoute(), { worklists: [{ id: 3, title: "My work" }] })).toBe("My work · Worklists · Forge");
    replaceUrl("/m/worklists/3");
    expect(routeTabTitle(getRoute(), { worklists: [{ id: 3, title: "My work" }] })).toBe("My work · Worklists · Forge");
  });

  it("names an Activity commit", () => {
    expect(
      routeTabTitle({ page: "activity" }, { activityCommit: { title: "Fix startup", repoPath: "owner/repo" } }),
    ).toBe("Fix startup (owner/repo) · Forge");
  });

  it("names a linked workspace item with a matching detail or typed fallback", () => {
    const item = {
      itemType: "pr",
      provider: "github",
      owner: "owner",
      name: "repo",
      repoPath: "owner/repo",
      number: 1,
    } as const;
    const workspace = { page: "mobile-workspace-item", hostKey: undefined, id: "a", name: "Workspace", item } as const;
    const route = { page: "mobile-workspace-item", workspaceId: "a", tab: "files" } as const;
    const pull = {
      repo: { provider: "github", platform_host: "github.com", repo_path: "owner/repo" },
      repo_owner: "owner",
      repo_name: "repo",
      merge_request: { Number: 1, Title: "Fix login" },
    } as PullDetail;
    expect(routeTabTitle(route, { workspace, pull })).toBe("Fix login (owner/repo#1) · Forge");
    expect(routeTabTitle(route, { workspace })).toBe("Pull request (owner/repo#1) · Forge");
    const issue = {
      repo: pull.repo,
      repo_owner: "owner",
      repo_name: "repo",
      issue: { Number: 1, Title: "Broken login" },
    } as IssueDetail;
    const issueWorkspace = { ...workspace, item: { ...item, itemType: "issue" as const } };
    expect(routeTabTitle(route, { workspace: issueWorkspace, issue })).toBe("Broken login (owner/repo#1) · Forge");
    expect(routeTabTitle(route, { workspace: issueWorkspace, pull })).toBe("Issue (owner/repo#1) · Forge");
  });

  it.each(["/worklists/import", "/worklists/import#v9.abc"])("names worklist import %s", (path) => {
    replaceUrl(path);
    expect(routeTabTitle(getRoute())).toBe("Import worklist · Worklists · Forge");
  });
});
