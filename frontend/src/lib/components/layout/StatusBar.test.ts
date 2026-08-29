import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../../api/types.js";

const activityState = vi.hoisted(() => ({
  items: [] as ActivityItem[],
  itemActivity: [] as ActivitySubject[],
  workspaceActivity: [] as WorkspaceActivitySubject[],
  enabledItemTypes: new Set<"pr" | "issue">(["pr", "issue"]),
  hideBots: false,
  providerAvailable: true,
}));

const runtime = vi.hoisted(() => ({
  runCommand: vi.fn(() => ({ interrupt: vi.fn() })),
}));

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => runtime,
}));

vi.mock("../../stores/router.svelte.ts", () => ({
  getPage: () => "activity",
}));

vi.mock("../../context.js", () => ({
  getStores: () => ({
    activity: {
      getActivityItems: () => activityState.items,
      getItemActivity: () => activityState.itemActivity,
      getWorkspaceActivity: () => activityState.workspaceActivity,
      getEnabledItemTypes: () => activityState.enabledItemTypes,
      getHideBots: () => activityState.hideBots,
    },
    pulls: { getPulls: () => [] },
    issues: { getIssues: () => [] },
    sync: {
      getSyncState: () => null,
      getRateLimits: () => ({ provider_pools: {}, local_ceilings: {} }),
      getProviderAvailable: () => activityState.providerAvailable,
    },
    events: {
      getConnectionState: () => "connected",
      getLastError: () => null,
      reconnect: vi.fn(),
    },
  }),
}));

import StatusBar from "./StatusBar.svelte";

const repo = {
  provider: "github",
  platform_host: "github.com",
  owner: "acme",
  name: "widgets",
  repo_path: "acme/widgets",
};

function parentSubject(itemType: "pr" | "issue", itemNumber: number, itemState = "open"): ActivitySubject {
  return {
    activity_at: "2026-08-14T12:00:00Z",
    item_number: itemNumber,
    item_state: itemState,
    item_title: `${itemType} ${itemNumber}`,
    item_type: itemType,
    item_url: `https://github.com/acme/widgets/${itemType === "pr" ? "pull" : "issues"}/${itemNumber}`,
    platform_host: "github.com",
    repo,
    repo_name: "widgets",
    repo_owner: "acme",
  } as ActivitySubject;
}

function workspaceSubject(itemType: "pr" | "issue", itemNumber: number): WorkspaceActivitySubject {
  return {
    ...parentSubject(itemType, itemNumber),
    workspace: { id: `workspace-${itemNumber}`, status: "ready" },
  } as WorkspaceActivitySubject;
}

function activityItem(itemType: "pr" | "issue", itemNumber: number, itemState = "open"): ActivityItem {
  return {
    ...parentSubject(itemType, itemNumber, itemState),
    id: `event-${itemType}-${itemNumber}`,
    cursor: `cursor-${itemType}-${itemNumber}`,
    activity_type: "comment",
    author: "alice",
    body_preview: "",
    created_at: "2026-08-14T12:00:00Z",
  } as ActivityItem;
}

describe("StatusBar Activity counts", () => {
  beforeEach(() => {
    activityState.items = [];
    activityState.itemActivity = [];
    activityState.workspaceActivity = [];
    activityState.enabledItemTypes = new Set(["pr", "issue"]);
    activityState.hideBots = false;
    activityState.providerAvailable = true;
    runtime.runCommand.mockClear();
  });

  afterEach(() => cleanup());

  it("counts open parent-only subjects from the authoritative snapshot", () => {
    activityState.itemActivity = [parentSubject("pr", 7), parentSubject("issue", 8), parentSubject("pr", 9, "merged")];

    render(StatusBar);

    expect(screen.getByText("1 PRs")).toBeTruthy();
    expect(screen.getByText("1 issues")).toBeTruthy();
    expect(screen.getByText("1 repos")).toBeTruthy();
  });

  it("deduplicates subjects represented by events, parents, and workspaces", () => {
    activityState.items = [activityItem("pr", 7)];
    activityState.itemActivity = [parentSubject("pr", 7)];
    activityState.workspaceActivity = [workspaceSubject("pr", 7), workspaceSubject("issue", 8)];

    render(StatusBar);

    expect(screen.getByText("1 PRs")).toBeTruthy();
    expect(screen.getByText("1 issues")).toBeTruthy();
    expect(screen.getByText("1 repos")).toBeTruthy();
  });

  it("uses authoritative parent lifecycle state over an event snapshot", () => {
    activityState.items = [activityItem("pr", 7)];
    activityState.itemActivity = [parentSubject("pr", 7, "merged")];

    render(StatusBar);

    expect(screen.getByText("0 PRs")).toBeTruthy();
    expect(screen.getByText("0 repos")).toBeTruthy();
  });

  it("excludes bot-authored parent and workspace-only subjects", () => {
    activityState.hideBots = true;
    activityState.itemActivity = [
      { ...parentSubject("pr", 7), item_author: "renovate[bot]" },
      { ...parentSubject("pr", 9), item_author: "alice" },
    ];
    activityState.workspaceActivity = [{ ...workspaceSubject("issue", 8), item_author: "release-bot" }];

    render(StatusBar);

    expect(screen.getByText("1 PRs")).toBeTruthy();
    expect(screen.getByText("0 issues")).toBeTruthy();
    expect(screen.getByText("1 repos")).toBeTruthy();
  });

  it("shows hub outage separately from the local event stream", () => {
    activityState.providerAvailable = false;

    render(StatusBar);

    expect(screen.getByText("provider unavailable")).toBeTruthy();
    expect(screen.getByTitle("This Forge spoke cannot reach its federation hub")).toBeTruthy();
    expect(screen.queryByText(/synced/)).toBeNull();
  });
});
