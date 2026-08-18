import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../api/types.js";
import ActivityThreaded from "./ActivityThreaded.svelte";

function activityItem(id: string, overrides: Partial<ActivityItem> = {}): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type: "comment",
    author: "alice",
    body_preview: "",
    created_at: "2026-04-27T12:00:00Z",
    item_number: 1,
    item_state: "open",
    item_title: "Add widget caching layer",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/1",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widgets",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
    ...overrides,
  };
}

function branchActivityItem(id: string, overrides: Partial<ActivityItem> = {}): ActivityItem {
  return activityItem(id, {
    activity_type: "default_branch_commit",
    author: "alice",
    author_name: "Alice Example",
    body_preview: "Refresh cache warmer",
    branch_name: "main",
    commit_sha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
    committed_at: "2026-04-27T12:00:00Z",
    item_number: 0,
    item_state: "",
    item_title: "",
    item_type: "",
    item_url: "",
    activity_url: "https://github.com/acme/widgets/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
    ...overrides,
  });
}

function workspaceSubject(overrides: Partial<WorkspaceActivitySubject> = {}): WorkspaceActivitySubject {
  return {
    activity_at: "2026-04-27T14:00:00Z",
    item_author: "workspace-author",
    item_number: 7,
    item_state: "open",
    item_title: "Keep the agent's work visible",
    item_type: "pr",
    item_url: "https://github.com/acme/widgets/pull/7",
    platform_host: "github.com",
    repo: activityItem("repo-source").repo!,
    repo_name: "widgets",
    repo_owner: "acme",
    workspace: { id: "ws-pr-7", status: "ready" },
    ...overrides,
  };
}

function itemSubject(overrides: Partial<ActivitySubject> = {}): ActivitySubject {
  const { workspace: _workspace, ...subject } = workspaceSubject();
  return {
    ...subject,
    item_number: 8,
    item_title: "Old pull with recent hidden activity",
    item_url: "https://github.com/acme/widgets/pull/8",
    ...overrides,
  };
}

const expanded = vi.hoisted(() => ({ value: true }));
const groupByRepo = vi.hoisted(() => ({ value: false }));
const hideOrgName = vi.hoisted(() => ({ value: false }));
const rollUpCommits = vi.hoisted(() => ({ value: false }));
const toggleThreadItem = vi.hoisted(() => vi.fn());
const markNotificationSeen = vi.hoisted(() => vi.fn(async () => undefined));

vi.mock("../context.js", () => ({
  getStores: () => ({
    grouping: {
      getGroupByRepo: () => groupByRepo.value,
      getHideOrgName: () => hideOrgName.value,
    },
    activity: {
      isThreadItemExpanded: () => expanded.value,
      toggleThreadItem,
      getRollUpCommits: () => rollUpCommits.value,
      markNotificationSeen,
    },
  }),
}));

describe("ActivityThreaded collapse", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    expanded.value = true;
    groupByRepo.value = false;
    hideOrgName.value = false;
    rollUpCommits.value = false;
    toggleThreadItem.mockClear();
  });

  it("shows events when the item is expanded", () => {
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    expect(container.querySelectorAll(".event-row").length).toBeGreaterThan(0);
  });

  it("mounts large collapsed projections in bounded batches", async () => {
    vi.useFakeTimers();
    const subjects = Array.from({ length: 51 }, (_, index) =>
      itemSubject({ item_number: index + 1, item_title: `Projection item ${index + 1}` }),
    );
    const { container } = render(ActivityThreaded, {
      props: { items: [], itemActivity: subjects, onSelectItem: undefined },
    });

    expect(container.querySelectorAll(".item-row")).toHaveLength(25);
    await vi.runAllTimersAsync();
    expect(container.querySelectorAll(".item-row")).toHaveLength(51);
  });

  it("keeps mounted rows visible when activity updates the projection", async () => {
    vi.useFakeTimers();
    const subjects = Array.from({ length: 51 }, (_, index) =>
      itemSubject({ item_number: index + 1, item_title: `Projection item ${index + 1}` }),
    );
    const { container, rerender } = render(ActivityThreaded, {
      props: { items: [], itemActivity: subjects, onSelectItem: undefined },
    });
    await vi.runAllTimersAsync();
    expect(container.querySelectorAll(".item-row")).toHaveLength(51);

    await rerender({
      items: [],
      itemActivity: [
        itemSubject({
          activity_at: "2026-04-27T15:00:00Z",
          item_number: 52,
          item_title: "Newly reconciled item",
        }),
        ...subjects,
      ],
      onSelectItem: undefined,
    });

    expect(container.querySelectorAll(".item-row")).toHaveLength(51);
    await vi.runAllTimersAsync();
    expect(container.querySelectorAll(".item-row")).toHaveLength(52);
  });

  it("renders a workspace-active subject with no provider events as an unexpandable group", async () => {
    const onSelectItem = vi.fn();
    const { container, getByLabelText } = render(ActivityThreaded, {
      props: {
        items: [],
        workspaceActivity: [workspaceSubject()],
        onSelectItem,
      },
    });

    const row = container.querySelector(".item-row");
    expect(row?.textContent).toContain("Keep the agent's work visible");
    expect(row?.textContent).toContain("workspace-author");
    expect(container.querySelector(".thread-caret")).toBeNull();
    expect(container.querySelector(".event-row")).toBeNull();
    expect(getByLabelText("Workspace attached (ready)")).toBeTruthy();

    await fireEvent.click(row!);
    expect(onSelectItem).toHaveBeenCalledWith(expect.objectContaining({ item_number: 7, item_type: "pr" }));
  });

  it("renders a recently active parent as a lazily expandable row without inventing a visible event", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-27T14:05:00Z"));
    const { container } = render(ActivityThreaded, {
      props: {
        items: [],
        itemActivity: [itemSubject()],
        onSelectItem: undefined,
      },
    });

    const row = container.querySelector(".item-row");
    expect(row?.textContent).toContain("Old pull with recent hidden activity");
    expect(row?.querySelector(".cell--time")?.textContent).toBe("5m ago");
    expect(container.querySelector(".thread-caret")).not.toBeNull();
    expect(container.querySelector(".event-row")).toBeNull();
  });

  it("keeps the event actor as thread author when the parent summary has no author", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [activityItem("authorless-parent-event", { item_number: 8, item_author: "", author: "alice" })],
        itemActivity: [itemSubject({ item_author: undefined })],
        onSelectItem: undefined,
      },
    });

    expect(container.querySelector(".item-row .cell--author")?.textContent).toBe("alice");
  });

  it("sorts existing threads by workspace activity without adding an event", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("provider-newer", {
            item_number: 2,
            item_title: "Provider-only thread",
            created_at: "2026-04-27T13:00:00Z",
          }),
          activityItem("workspace-older-event", {
            item_number: 7,
            item_title: "Workspace-active thread",
            created_at: "2026-04-27T12:00:00Z",
          }),
        ],
        workspaceActivity: [workspaceSubject()],
        onSelectItem: undefined,
      },
    });

    const rows = Array.from(container.querySelectorAll(".item-row:not(.branch-activity-row)"));
    expect(rows[0]?.textContent).toContain("Workspace-active thread");
    expect(container.querySelectorAll(".event-row")).toHaveLength(2);
  });

  it("sorts and timestamps threads by parent recency when newer events are filtered", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-27T14:05:00Z"));
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("older-visible-event", {
            item_number: 1,
            item_title: "Recently committed pull",
            created_at: "2026-04-27T12:00:00Z",
            item_last_activity_at: "2026-04-27T14:00:00Z",
          }),
          activityItem("newer-visible-event", {
            item_number: 2,
            item_title: "Recently commented pull",
            created_at: "2026-04-27T13:00:00Z",
            item_last_activity_at: "2026-04-27T13:00:00Z",
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const rows = Array.from(container.querySelectorAll(".item-row:not(.branch-activity-row)"));
    expect(rows[0]?.textContent).toContain("Recently committed pull");
    expect(rows[0]?.querySelector(".cell--time")?.textContent).toBe("5m ago");
    expect(container.querySelector(".event-row .event-time")?.textContent).toBe("2h ago");
  });

  it("hides events but keeps the item row when collapsed", () => {
    expanded.value = false;
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    expect(container.querySelectorAll(".event-row")).toHaveLength(0);
    expect(container.querySelectorAll(".item-row")).toHaveLength(1);
  });

  it("toggles the item on caret click without selecting the row", async () => {
    const onSelectItem = vi.fn();
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem },
    });
    const caret = container.querySelector(".thread-caret");
    expect(caret).not.toBeNull();
    await fireEvent.click(caret!);
    expect(toggleThreadItem).toHaveBeenCalledTimes(1);
    expect(toggleThreadItem).toHaveBeenCalledWith("github|github.com|acme/widgets:pr:1");
    expect(onSelectItem).not.toHaveBeenCalled();
  });

  it("renders the repo chip label in non-grouped mode", () => {
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    const label = container.querySelector(".repo-chip.repo-tag .repo-chip__label");
    expect(label?.textContent).toBe("acme/widgets");
  });

  it("renders branch activity as top-level rows interleaved with item threads", async () => {
    rollUpCommits.value = true;

    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          branchActivityItem("c4", {
            created_at: "2026-04-27T12:04:00Z",
            commit_sha: "d1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
          }),
          branchActivityItem("c3", {
            created_at: "2026-04-27T12:03:00Z",
            commit_sha: "c1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
          }),
          branchActivityItem("c2", {
            created_at: "2026-04-27T12:02:00Z",
            commit_sha: "b1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
          }),
          activityItem("pr-comment", {
            created_at: "2026-04-27T12:01:30Z",
          }),
          branchActivityItem("c1", {
            created_at: "2026-04-27T12:01:00Z",
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const rows = Array.from(container.querySelectorAll(".item-row"));
    expect(rows).toHaveLength(3);
    expect(rows[0]?.textContent).toContain("3 commits");
    expect(rows[1]?.textContent).toContain("Add widget caching layer");
    expect(rows[2]?.textContent).toContain("Refresh cache warmer");
    expect(container.textContent).not.toContain("main updates on acme/widgets");
    expect(container.textContent).not.toContain("#0");
    expect(container.querySelector(".branch-activity-row .thread-caret")).toBeNull();
  });

  it("shows default-branch commits as individual rows when commit roll-up is off", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          branchActivityItem("c3", {
            created_at: "2026-04-27T12:03:00Z",
            body_preview: "Ship direct main commit 3",
            commit_sha: "3333333333333333333333333333333333333333",
          }),
          branchActivityItem("c2", {
            created_at: "2026-04-27T12:02:00Z",
            body_preview: "Ship direct main commit 2",
            commit_sha: "2222222222222222222222222222222222222222",
          }),
          branchActivityItem("c1", {
            created_at: "2026-04-27T12:01:00Z",
            body_preview: "Ship direct main commit 1",
            commit_sha: "1111111111111111111111111111111111111111",
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const rows = Array.from(container.querySelectorAll(".branch-activity-row"));
    expect(rows).toHaveLength(3);
    expect(rows[0]?.textContent).toContain("Ship direct main commit 3");
    expect(rows[1]?.textContent).toContain("Ship direct main commit 2");
    expect(rows[2]?.textContent).toContain("Ship direct main commit 1");
    expect(container.textContent).not.toContain("3 commits");
  });

  it("labels commit rows without the branch type or duplicated commit text", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem: undefined,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    expect(row).not.toBeNull();
    expect(row?.textContent).toContain("Commit");
    expect(row?.textContent).toContain("acme/widgets");
    expect(row?.textContent).toContain("main");
    expect(row?.textContent).toContain("Refresh cache warmer");
    expect(row?.textContent).not.toContain("Branch");
    expect(row?.textContent).not.toContain("Commit Commit");
    expect(row?.textContent).not.toContain("a1b2c3d");
  });

  it("selects default branch commit rows for an in-app diff", async () => {
    const onSelectItem = vi.fn();
    const onSelectBranchCommit = vi.fn();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);

    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem,
        onSelectBranchCommit,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);

    expect(onSelectItem).not.toHaveBeenCalled();
    expect(onSelectBranchCommit).toHaveBeenCalledWith(
      expect.objectContaining({
        activity_type: "default_branch_commit",
        commit_sha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
      }),
    );
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("highlights the selected default branch commit row", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem: undefined,
        selectedBranchCommit: {
          provider: "github",
          platformHost: "github.com",
          repoPath: "acme/widgets",
          owner: "acme",
          name: "widgets",
          commitSha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
        },
      },
    });

    expect(container.querySelector(".branch-activity-row.selected")).not.toBeNull();
  });

  it("shows the PR author on the item row, not the latest actor", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("c-late", {
            created_at: "2026-04-27T13:00:00Z",
            author: "bob",
            author_name: "Bob Example",
            item_author: "prauthor",
          }),
          activityItem("c-early", {
            created_at: "2026-04-27T12:00:00Z",
            author: "alice",
            author_name: "Alice Example",
            item_author: "prauthor",
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const row = container.querySelector(".item-row:not(.branch-activity-row)");
    expect(row).not.toBeNull();
    const authorCell = row?.querySelector(".cell--author");
    expect(authorCell?.textContent?.trim()).toBe("prauthor");

    // Expanded event rows still attribute each event to its own actor.
    const eventAuthors = Array.from(container.querySelectorAll(".event-row .event-author")).map((el) =>
      el.textContent?.trim(),
    );
    expect(eventAuthors).toEqual(["Bob Example", "Alice Example"]);
  });

  it("shows a workspace indicator on item threads with attached workspaces", () => {
    const { getByLabelText } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("c1", {
            workspace: { id: "ws-pr-1", status: "ready" },
          }),
        ],
        onSelectItem: undefined,
      },
    });

    expect(getByLabelText("Workspace attached (ready)")).toBeTruthy();
  });

  it("shows the commit author on branch rows", () => {
    const { container } = render(ActivityThreaded, {
      props: {
        items: [branchActivityItem("c1")],
        onSelectItem: undefined,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    const authorCell = row?.querySelector(".cell--author");
    expect(authorCell?.textContent?.trim()).toBe("Alice Example");
  });

  it("shows just the repo name when hideOrgName is on", () => {
    hideOrgName.value = true;
    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });
    const label = container.querySelector(".repo-chip.repo-tag .repo-chip__label");
    expect(label?.textContent).toBe("widgets");
  });

  it("shows just the repo name in grouped headers when hideOrgName is on", () => {
    groupByRepo.value = true;
    hideOrgName.value = true;

    const { container } = render(ActivityThreaded, {
      props: { items: [activityItem("c1")], onSelectItem: undefined },
    });

    const repoName = container.querySelector(".repo-header .repo-name");
    expect(repoName?.textContent).toBe("widgets");
    expect(container.textContent).not.toContain("acme/widgets");
  });

  it("keeps hidden-org grouped activity headers distinguishable", () => {
    groupByRepo.value = true;
    hideOrgName.value = true;

    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          activityItem("acme-widgets"),
          activityItem("platform-widgets", {
            id: "platform-widgets",
            item_number: 2,
            repo_owner: "platform",
            repo_name: "widgets",
            repo: {
              provider: "gitlab",
              platform_host: "gitlab.example.com",
              owner: "platform",
              name: "widgets",
              repo_path: "platform/widgets",
            },
          }),
        ],
        onSelectItem: undefined,
      },
    });

    const repoNames = Array.from(container.querySelectorAll(".repo-header .repo-name")).map((el) =>
      el.textContent?.trim(),
    );
    expect(repoNames).toEqual(["acme/widgets", "platform/widgets"]);
  });

  it("keeps force-push rows as provider compare links", async () => {
    const onSelectBranchCommit = vi.fn();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);

    const { container } = render(ActivityThreaded, {
      props: {
        items: [
          branchActivityItem("force-1", {
            activity_type: "default_branch_force_push",
            activity_url: "https://github.com/acme/widgets/compare/aaaaaaaa...bbbbbbbb",
            before_sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            after_sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            body_preview: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -> bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            commit_sha: "",
          }),
        ],
        onSelectItem: undefined,
        onSelectBranchCommit,
      },
    });

    const row = container.querySelector(".branch-activity-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);

    expect(onSelectBranchCommit).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith(
      "https://github.com/acme/widgets/compare/aaaaaaaa...bbbbbbbb",
      "_blank",
      "noopener",
    );
    open.mockRestore();
  });
});

describe("ActivityThreaded notification events", () => {
  afterEach(() => {
    cleanup();
    expanded.value = true;
    markNotificationSeen.mockClear();
  });

  it("labels a notification event by its reason and marks it seen", async () => {
    const notif = activityItem("ntf:42", {
      activity_type: "notification",
      item_state: "unread",
      body_preview: "review_requested",
    });
    const { container, getByRole } = render(ActivityThreaded, {
      props: { items: [notif], onSelectItem: undefined },
    });

    expect(container.textContent).toContain("Review requested");
    const btn = getByRole("button", { name: "Mark notification seen" });
    await fireEvent.click(btn);
    expect(markNotificationSeen).toHaveBeenCalledTimes(1);
    expect(markNotificationSeen.mock.calls[0]![0]).toMatchObject({ id: "ntf:42" });
  });

  it("omits the seen control once a notification is read", () => {
    const notif = activityItem("ntf:43", {
      activity_type: "notification",
      item_state: "read",
      body_preview: "mention",
    });
    const { container, queryByRole } = render(ActivityThreaded, {
      props: { items: [notif], onSelectItem: undefined },
    });

    expect(container.textContent).toContain("Mentioned");
    expect(queryByRole("button", { name: "Mark notification seen" })).toBeNull();
  });

  it("opens the web URL for a notification without a PR/issue subject", async () => {
    const onSelectItem = vi.fn();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const notif = activityItem("ntf:44", {
      activity_type: "notification",
      item_state: "read",
      item_type: "release",
      item_number: 0,
      body_preview: "subscribed",
      activity_url: "https://github.com/acme/widgets/releases/tag/v1.2.3",
    });

    const { container } = render(ActivityThreaded, {
      props: { items: [notif], onSelectItem },
    });
    const row = container.querySelector(".event-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);

    expect(onSelectItem).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith("https://github.com/acme/widgets/releases/tag/v1.2.3", "_blank", "noopener");
    open.mockRestore();
  });

  it("opens the web URL when the top-level notification group row is clicked", async () => {
    const onSelectItem = vi.fn();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const notif = activityItem("ntf:45", {
      activity_type: "notification",
      item_state: "read",
      item_type: "release",
      item_number: 0,
      body_preview: "subscribed",
      activity_url: "https://github.com/acme/widgets/releases/tag/v2.0.0",
    });

    const { container } = render(ActivityThreaded, {
      props: { items: [notif], onSelectItem },
    });
    const row = container.querySelector(".item-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);

    // The top-level group row must not reopen the invalid #0 detail
    // drawer; it follows the provider URL like the expanded event row.
    expect(onSelectItem).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith("https://github.com/acme/widgets/releases/tag/v2.0.0", "_blank", "noopener");
    open.mockRestore();
  });
});
