import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../../api/types.js";
import { type LabelFilters } from "../../utils/label-filter.js";

const activityState = vi.hoisted(() => ({
  items: [] as ActivityItem[],
  itemActivity: [] as ActivitySubject[],
  workspaceActivity: [] as WorkspaceActivitySubject[],
  enabledItemTypes: new Set<"pr" | "issue">(["pr", "issue"]),
  bots: undefined as "require" | "exclude" | undefined,
  pullAttributeFilters: [] as string[],
  labelFilters: new Map() as LabelFilters,
  itemActivityCapped: false,
  activityCapped: false,
  providerAvailable: true,
  overdue: 0,
  progress: undefined as string | undefined,
  repoErrors: [] as { repository: string; host: string; error: string; since: string }[],
}));

const runtime = vi.hoisted(() => ({
  runCommand: vi.fn(() => ({ interrupt: vi.fn() })),
}));

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => runtime,
}));

vi.mock("../../stores/router.svelte.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/router.svelte.ts")>()),
  getPage: () => "activity",
  navigate: vi.fn(),
}));

vi.mock("../../context.js", () => ({
  getStores: () => ({
    activity: {
      getActivityItems: () => activityState.items,
      getItemActivity: () => activityState.itemActivity,
      getWorkspaceActivity: () => activityState.workspaceActivity,
      getEnabledItemTypes: () => activityState.enabledItemTypes,
      getActivityState: () => "all",
      getBots: () => activityState.bots,
      getPullAttributeFilters: () => activityState.pullAttributeFilters,
      getHideDefaultBranchActivity: () => false,
      getHideBotAuthoredIssues: () => false,
      getLabelFilters: () => activityState.labelFilters,
      getViewMode: () => "flat",
      isActivityCapped: () => activityState.activityCapped,
      isItemActivityCapped: () => activityState.itemActivityCapped,
    },
    pulls: { getFilteredPulls: () => [] },
    issues: { getFilteredIssues: () => [] },
    settings: { getAirplaneMode: () => false },
    sync: {
      getSyncState: () => ({
        running: activityState.progress !== undefined,
        progress: activityState.progress,
        detail_refresh_overdue: activityState.overdue,
        repo_errors: activityState.repoErrors,
      }),
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
    activityState.bots = undefined;
    activityState.pullAttributeFilters = [];
    activityState.labelFilters = new Map();
    activityState.itemActivityCapped = false;
    activityState.activityCapped = false;
    activityState.providerAvailable = true;
    activityState.overdue = 0;
    activityState.progress = undefined;
    activityState.repoErrors = [];
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

  it.each(["parents", "events"])("marks flat activity totals as incomplete when %s are capped", (kind) => {
    activityState.itemActivity = Array.from({ length: 200 }, (_, index) =>
      parentSubject(index < 100 ? "pr" : "issue", index + 1),
    );
    activityState.itemActivityCapped = kind === "parents";
    activityState.activityCapped = kind === "events";
    render(StatusBar);
    expect(screen.getByText("100+ PRs")).toBeTruthy();
    expect(screen.getByText("100+ issues")).toBeTruthy();
    expect(screen.getByText("1+ repos")).toBeTruthy();
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
    activityState.bots = "exclude";
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

  it("counts only pull requests the From fork and Has workspace filters keep", () => {
    activityState.itemActivity = [
      { ...parentSubject("pr", 7), head_repo_kind: "fork" },
      { ...parentSubject("pr", 9), head_repo_kind: "same_repo" },
      parentSubject("issue", 10),
    ];
    activityState.workspaceActivity = [{ ...workspaceSubject("pr", 11), head_repo_kind: "fork" }];
    activityState.pullAttributeFilters = ["not_fork"];

    render(StatusBar);

    expect(screen.getByText("1 PRs")).toBeTruthy();
    expect(screen.getByText("1 issues")).toBeTruthy();

    cleanup();
    activityState.pullAttributeFilters = ["has_workspace"];
    render(StatusBar);

    expect(screen.getByText("1 PRs")).toBeTruthy();
    expect(screen.getByText("1 issues")).toBeTruthy();
  });

  it("shows hub outage separately from the local event stream", () => {
    activityState.providerAvailable = false;

    render(StatusBar);

    expect(screen.getByText("provider unavailable")).toBeTruthy();
    expect(screen.getByTitle("This Forge spoke cannot reach its federation hub")).toBeTruthy();
    expect(screen.queryByText(/synced/)).toBeNull();
  });

  it("shows missed daily checks instead of implying all items are fresh", () => {
    activityState.overdue = 12;
    render(StatusBar);
    expect(screen.getByText("12 overdue checks")).toBeTruthy();
    expect(screen.getByTitle(/not checked in 24 hours/)).toBeTruthy();
  });

  it("names repositories whose sync keeps failing while a pass runs", async () => {
    const { navigate } = await import("../../stores/router.svelte.ts");
    activityState.progress = "refreshing details";
    activityState.repoErrors = [
      {
        repository: "acme/gone",
        host: "github.com",
        error: "resolve repo identity acme/gone: 404 Not Found",
        since: "2026-10-28T12:00:00Z",
      },
    ];

    render(StatusBar);

    const indicator = screen.getByRole("button", { name: "1 repo failing" });
    expect(indicator.getAttribute("title")).toContain(
      "github.com/acme/gone: resolve repo identity acme/gone: 404 Not Found",
    );
    expect(screen.getByText("syncing (refreshing details)")).toBeTruthy();
    indicator.click();
    expect(navigate).toHaveBeenCalledWith("/settings?section=settings-repositories");
  });
});
