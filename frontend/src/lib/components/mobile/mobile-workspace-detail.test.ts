import { describe, expect, it } from "vite-plus/test";
import type { WorkspaceDetail } from "../terminal/workspace-detail.js";
import { mobileWorkspaceIdentity, mobileWorkspaceLinkedItem } from "./mobile-workspace-detail.js";

function detail(itemType: WorkspaceDetail["item_type"], number: number): WorkspaceDetail {
  return {
    id: "ws-1",
    associated_pr_number: itemType === "adhoc" ? 99 : null,
    created_at: "2026-08-11T12:00:00Z",
    enrichment_status: "fresh",
    git_head_ref: "feature/mobile",
    item_number: number,
    item_type: itemType,
    platform_host: "github.com",
    repo_name: "widgets",
    repo_owner: "acme",
    status: "ready",
    tmux_session: "tmux-ws-1",
    worktree_path: "/tmp/ws-1",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
  };
}

describe("mobile workspace linked item", () => {
  it("resolves owned issues, pull requests, and associated ad-hoc pull requests", () => {
    expect(mobileWorkspaceLinkedItem(detail("issue", 7))).toEqual({ itemType: "issue", number: 7 });
    expect(mobileWorkspaceLinkedItem(detail("pull_request", 8))).toEqual({ itemType: "pr", number: 8 });
    expect(mobileWorkspaceLinkedItem(detail("adhoc", 0))).toEqual({ itemType: "pr", number: 99 });
  });

  it("hides missing linked items", () => {
    expect(mobileWorkspaceLinkedItem({ ...detail("kata_task", 7), associated_pr_number: 99 })).toBeNull();
  });

  it("keeps repository, branch, and optional Fleet identity visible", () => {
    const workspace = detail("pull_request", 7);
    expect(mobileWorkspaceIdentity(workspace)).toBe("acme/widgets · feature/mobile");
    expect(mobileWorkspaceIdentity(workspace, "dev-laptop")).toBe("acme/widgets · feature/mobile · dev-laptop");
  });
});
