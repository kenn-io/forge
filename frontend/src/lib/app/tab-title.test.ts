import { describe, expect, it } from "vite-plus/test";
import type { IssueDetail, PullDetail } from "../api/types.js";
import { mockIssueFixture, mockPullFixture } from "../../test/mockApiFetch.js";
import { getRoute, replaceUrl } from "../stores/router.svelte.js";
import { routeTabTitle } from "./tab-title.svelte.js";

describe("tab titles", () => {
  it.each([
    ["/", "Activity"],
    ["/actions", "Actions"],
    ["/repos", "Repos"],
    ["/repo/browser?provider=github&repo_path=owner/repo", "Repos"],
    ["/pulls", "Pulls"],
    ["/issues", "Issues"],
    ["/docs", "Docs"],
    ["/workspaces", "Workspaces"],
    ["/terminal/a", "Workspace"],
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
    ["/m/workspaces/local/a", "Workspace"],
    ["/m/workspaces/local/a/item", "Linked item · Workspaces"],
    ["/focus/mrs", "Pulls"],
    ["/focus/issues", "Issues"],
  ])("names %s", (path, label) => {
    replaceUrl(path);
    expect(routeTabTitle(getRoute())).toBe(`${label} · Forge`);
  });

  it("uses a readable workspace name", () => {
    const workspace = { hostKey: "host-a", id: "ws", name: "feature/auth" };
    replaceUrl("/terminal/fleet/host-a/ws");
    expect(routeTabTitle(getRoute(), { workspace })).toBe("feature/auth · Workspaces · Forge");
    replaceUrl("/m/workspaces/fleet/host-a/ws/item");
    expect(routeTabTitle(getRoute(), { workspace })).toBe("Linked item (feature/auth) · Workspaces · Forge");
  });

  it("uses matching details and labels loading or mismatched items", () => {
    const repo = { provider: "github", platform_host: "github.com", repo_path: "owner/repo" };
    const pull = {
      repo,
      repo_owner: "owner",
      repo_name: "repo",
      merge_request: { ...mockPullFixture(42), Number: 1, Title: "Fix login" },
      platform_host: "github.com",
      platform_head_sha: "",
      platform_base_sha: "",
      reviewed_head_sha: "",
      diff_head_sha: "",
      merge_base_sha: "",
      head_repo_kind: "same_repo",
      detail_loaded: true,
      deferred_merge_pending: false,
      events: [],
      worktree_links: [],
      workflow_approval: { checked: false, count: 0, required: false },
    } satisfies PullDetail;
    const issue = {
      repo,
      repo_owner: "owner",
      repo_name: "repo",
      issue: { ...mockIssueFixture(7), Number: 2, Title: "Broken login" },
      platform_host: "github.com",
      detail_loaded: true,
      events: [],
    } satisfies IssueDetail;
    replaceUrl("/focus/pulls/github/owner/repo/1");
    expect(routeTabTitle(getRoute(), { pull })).toBe("Fix login (owner/repo#1) · Forge");
    expect(routeTabTitle(getRoute(), { pull: { ...pull, merge_request: { ...pull.merge_request, Number: 2 } } })).toBe(
      "Pull request (owner/repo#1) · Forge",
    );
    replaceUrl("/issues/github/owner/repo/2");
    expect(routeTabTitle(getRoute(), { issue })).toBe("Broken login (owner/repo#2) · Forge");
    replaceUrl("/issues/github/owner/repo/3");
    expect(routeTabTitle(getRoute(), { issue })).toBe("Issue (owner/repo#3) · Forge");
  });

  it.each([
    ["/pulls/github/owner/repo/1", "Pull request (owner/repo#1) · Forge"],
    ["/pulls/gitlab/group/project/42", "Pull request (group/project!42) · Forge"],
    ["/queues/github/owner/repo/1", "Pull request (owner/repo#1) · Forge"],
    ["/worklists/3?selected=issue%3A2&provider=github&repo_path=owner%2Frepo", "Issue (owner/repo#2) · Forge"],
    [
      "/",
      "Issue (owner/repo#2) · Forge",
      {
        activitySelection: {
          itemType: "issue" as const,
          provider: "github",
          owner: "owner",
          name: "repo",
          repoPath: "owner/repo",
          number: 2,
        },
      },
    ],
  ])("names selected items on %s", (path, title, options) => {
    replaceUrl(path);
    expect(routeTabTitle(getRoute(), options)).toBe(title);
  });

  it("names a linked workspace item", () => {
    const item = {
      itemType: "pr",
      provider: "github",
      owner: "owner",
      name: "repo",
      repoPath: "owner/repo",
      number: 1,
    } as const;
    const workspace = { hostKey: undefined, id: "a", name: "Workspace", item };
    const route = { page: "mobile-workspace-item", workspaceId: "a", tab: "files" } as const;
    expect(routeTabTitle(route, { workspace })).toBe("Pull request (owner/repo#1) · Forge");
  });
});
