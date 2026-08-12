import { describe, expect, it } from "vite-plus/test";
import type { WorkspaceListItem } from "../terminal/workspace-list-schema.js";
import { groupMobileWorkspaces, sortMobileWorkspaces, workspaceMatchesMobileSearch } from "./mobile-workspace-list.js";

function workspace(
  id: string,
  owner: string,
  name: string,
  createdAt: string,
  terminalActivity: string | null,
): WorkspaceListItem {
  return {
    id,
    created_at: createdAt,
    git_head_ref: id === "active" ? "feature/branch" : "main",
    item_number: id === "active" ? 42 : 7,
    item_type: "pull_request",
    platform_host: "github.com",
    repo_name: name,
    repo_owner: owner,
    status: "ready",
    tmux_activity_source: "unknown",
    tmux_last_output_at: terminalActivity,
    tmux_working: false,
    worktree_path: `/tmp/${id}`,
    mr_title: id === "active" ? "Feature branch" : "Newest work",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner,
      name,
      repo_path: `${owner}/${name}`,
    },
  };
}

describe("mobile workspace list model", () => {
  const items = [
    workspace("new", "acme", "widgets", "2026-08-11T12:00:00Z", null),
    workspace("active", "acme", "widgets", "2026-08-10T12:00:00Z", "2026-08-12T12:00:00Z"),
  ];

  it("sorts terminal activity with creation fallback", () => {
    expect(sortMobileWorkspaces(items, "activity").map((item) => item.id)).toEqual(["active", "new"]);
  });

  it("groups by stable repository identity", () => {
    const groups = groupMobileWorkspaces(items, true);
    expect(groups).toHaveLength(1);
    expect(groups[0]?.label).toBe("acme/widgets");
    expect(groups[0]?.items).toHaveLength(2);
  });

  it("searches title, branch, item number, repository, and Fleet host", () => {
    const remote = { ...items[0]!, fleet_host_key: "phone-dev" };
    expect(workspaceMatchesMobileSearch(items[1]!, "feature branch")).toBe(true);
    expect(workspaceMatchesMobileSearch(items[1]!, "#42")).toBe(true);
    expect(workspaceMatchesMobileSearch(remote, "phone-dev")).toBe(true);
    expect(workspaceMatchesMobileSearch(items[1]!, "unrelated")).toBe(false);
  });
});
