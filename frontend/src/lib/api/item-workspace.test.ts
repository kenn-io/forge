import { describe, expect, it } from "vite-plus/test";
import type { FleetWorkspaceSummary } from "./fleet-snapshot.js";
import { fleetWorkspaceIsForItem } from "./item-workspace.js";
import { repositoryIdKey } from "./repository-key.js";

function listed(owner: string, name: string, repoId: number | undefined): FleetWorkspaceSummary {
  return {
    id: "ws-remote",
    status: "ready",
    fleet_host_key: "b".repeat(32),
    item_type: "pull_request",
    item_number: 7,
    platform_host: "github.com",
    repo_owner: owner,
    repo_name: name,
    repo: { provider: "github", platform_host: "github.com", platform_repo_id: repoId, owner, name },
  } as unknown as FleetWorkspaceSummary;
}

function request(repoId: number | undefined) {
  return {
    kind: "pull" as const,
    repo: {
      provider: "github",
      platformHost: "github.com",
      repositoryKey: repoId === undefined ? undefined : repositoryIdKey(repoId),
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
    },
    number: 7,
  };
}

describe("fleetWorkspaceIsForItem", () => {
  it("finds a workspace listed under the repository's old name when its ID matches", () => {
    expect(fleetWorkspaceIsForItem(listed("acme", "gadget", 1001), request(1001))).toBe(true);
  });

  it("matches by owner and name when either side has no repository ID", () => {
    expect(fleetWorkspaceIsForItem(listed("acme", "widget", undefined), request(1001))).toBe(true);
    expect(fleetWorkspaceIsForItem(listed("acme", "widget", 1001), request(undefined))).toBe(true);
    expect(fleetWorkspaceIsForItem(listed("acme", "gadget", undefined), request(1001))).toBe(false);
  });
});
