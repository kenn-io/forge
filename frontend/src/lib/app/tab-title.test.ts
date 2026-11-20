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
    ["/m/workspaces/local/a/item", "Workspace (a) · Conversation · Workspaces"],
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
    ["/m/workspaces/fleet/host-a/ws/item/files", "Workspace (host-a/ws) · Files · Workspaces · Forge"],
    ["/repo/browser?provider=github&repo_path=owner%2Frepo&ref_sha=abc123", "abc123 · owner/repo · Repos · Forge"],
  ])("names the selected screen on %s", (path, title) => {
    replaceUrl(path);
    expect(routeTabTitle(getRoute())).toBe(title);
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

  it("keeps workspace names keyed by host and ID", () => {
    const release = publishWorkspaceTitle("remote", "a", "Fix login");
    replaceUrl("/terminal/fleet/remote/a");
    expect(routeTabTitle(getRoute(), { workspaceName: workspaceTitle("remote", "a") })).toBe(
      "Fix login · Workspaces · Forge",
    );
    expect(workspaceTitle(undefined, "a")).toBeUndefined();
    expect(workspaceTitle("remote", "b")).toBeUndefined();
    replaceUrl("/m/workspaces/fleet/remote/a/item/files");
    expect(routeTabTitle(getRoute(), { workspaceName: workspaceTitle("remote", "a") })).toBe(
      "Fix login · Files · Workspaces · Forge",
    );
    const releaseSecond = publishWorkspaceTitle("remote", "a", "Updated title");
    release();
    expect(workspaceTitle("remote", "a")).toBe("Updated title");
    releaseSecond();
    expect(workspaceTitle("remote", "a")).toBeUndefined();
  });
});
