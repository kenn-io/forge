import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem } from "../api/types.js";
import MobileActivityView from "./MobileActivityView.svelte";

function branchActivityItem(id: string, overrides: Partial<ActivityItem> = {}): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type: "default_branch_commit",
    author: "alice",
    author_name: "Alice Example",
    body_preview: "Refresh cache warmer",
    branch_name: "main",
    commit_sha: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
    committed_at: "2026-04-27T12:00:00Z",
    created_at: "2026-04-27T12:00:00Z",
    item_number: 0,
    item_state: "",
    item_title: "",
    item_type: "",
    item_url: "",
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
    activity_url: "https://github.com/acme/widgets/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
    ...overrides,
  } as ActivityItem;
}

const items = vi.hoisted(() => ({ value: [] as ActivityItem[] }));
const onSelectItem = vi.hoisted(() => vi.fn());
const hideClosedMerged = vi.hoisted(() => ({ value: false }));
const hideOrgName = vi.hoisted(() => ({ value: false }));
const showNotifications = vi.hoisted(() => ({ value: true }));
const setHideOrgName = vi.hoisted(() =>
  vi.fn((value: boolean) => {
    hideOrgName.value = value;
  }),
);
const setShowNotifications = vi.hoisted(() =>
  vi.fn((value: boolean) => {
    showNotifications.value = value;
  }),
);
const markNotificationSeen = vi.hoisted(() => vi.fn(async () => undefined));

vi.mock("../context.js", () => ({
  getStores: () => ({
    activity: {
      initializeFromMount: vi.fn(),
      loadActivity: vi.fn(async () => undefined),
      startActivityPolling: vi.fn(),
      stopActivityPolling: vi.fn(),
      getActivitySearch: () => "",
      getActivityItems: () => items.value,
      getActivityError: () => null,
      getTimeRange: () => "7d",
      getItemFilter: () => "all",
      getEnabledEvents: () => new Set(["comment", "review", "commit", "force_push"]),
      getShowNotifications: () => showNotifications.value,
      getHideClosedMerged: () => hideClosedMerged.value,
      getHideBots: () => false,
      getHideDefaultBranchActivity: () => false,
      isActivityLoading: () => false,
      isActivityCapped: () => false,
      setActivityFilterTypes: vi.fn(),
      setActivitySearch: vi.fn(),
      setTimeRange: vi.fn(),
      setItemFilter: vi.fn(),
      setShowNotifications,
      markNotificationSeen,
      setHideBots: vi.fn(),
      setHideDefaultBranchActivity: vi.fn(),
      syncToURL: vi.fn(),
    },
    settings: {
      getConfiguredRepos: () => [],
      isSettingsLoaded: () => true,
      hasConfiguredRepos: () => true,
    },
    sync: {
      subscribeSyncComplete: vi.fn(() => () => undefined),
    },
    grouping: {
      getHideOrgName: () => hideOrgName.value,
      setHideOrgName,
    },
  }),
}));

describe("MobileActivityView branch activity", () => {
  beforeEach(() => {
    items.value = [branchActivityItem("branch-commit")];
    hideOrgName.value = false;
    hideClosedMerged.value = false;
    onSelectItem.mockClear();
    setHideOrgName.mockClear();
  });

  afterEach(() => {
    cleanup();
  });

  it("renders branch activity without a fake PR or issue number", () => {
    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const card = container.querySelector(".mobile-activity-card");
    expect(card?.textContent).toContain("Refresh cache warmer");
    expect(card?.textContent).toContain("main");
    expect(card?.textContent).toContain("a1b2c3d");
    expect(card?.textContent).not.toContain("#0");
    expect(card?.querySelector(".chip--kind-pr")).toBeNull();
    expect(card?.querySelector(".chip--kind-issue")).toBeNull();
  });

  it("uses the shared repo path by default", () => {
    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const repoLabel = container.querySelector(".mobile-activity-card__meta span");
    expect(repoLabel?.textContent).toBe("acme/widgets");
  });

  it("respects hide org name in mobile activity cards", () => {
    hideOrgName.value = true;

    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const repoLabel = container.querySelector(".mobile-activity-card__meta span");
    expect(repoLabel?.textContent).toBe("widgets");
    expect(container.textContent).not.toContain("acme/widgets");
  });

  it("keeps hidden-org mobile activity repo labels distinguishable", () => {
    hideOrgName.value = true;
    items.value = [
      branchActivityItem("acme-widgets"),
      branchActivityItem("platform-widgets", {
        id: "platform-widgets",
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
    ];

    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const repoLabels = Array.from(container.querySelectorAll(".mobile-activity-card__meta span:first-child")).map(
      (el) => el.textContent?.trim(),
    );
    expect(repoLabels).toEqual(["acme/widgets", "platform/widgets"]);
  });

  it("exposes a mobile hide org toggle", async () => {
    const { getByRole } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const button = getByRole("button", { name: "Hide org" });
    expect(button.getAttribute("aria-pressed")).toBe("false");

    await fireEvent.click(button);

    expect(setHideOrgName).toHaveBeenCalledWith(true);
  });

  it("does not select a PR or issue when tapping a branch event", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);

    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const event = container.querySelector(".mobile-activity-event");
    expect(event).not.toBeNull();
    await fireEvent.click(event!);

    expect(onSelectItem).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith(
      "https://github.com/acme/widgets/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
      "_blank",
      "noopener",
    );
    open.mockRestore();
  });
});

function notificationItem(id: string, title: string, subjectState: string): ActivityItem {
  return {
    id,
    cursor: id,
    activity_type: "notification",
    author: "carol",
    body_preview: "review_requested",
    created_at: "2026-04-27T12:00:00Z",
    // Notifications carry unread/read in item_state, never a lifecycle state;
    // the linked PR's lifecycle rides in subject_state.
    item_number: Number(id),
    item_state: "unread",
    subject_state: subjectState,
    item_title: title,
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${id}`,
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
  } as ActivityItem;
}

describe("MobileActivityView notifications", () => {
  beforeEach(() => {
    hideClosedMerged.value = false;
    showNotifications.value = true;
    onSelectItem.mockClear();
    setShowNotifications.mockClear();
    markNotificationSeen.mockClear();
  });

  afterEach(() => {
    cleanup();
  });

  it("labels notification events by their reason, not the raw type", () => {
    items.value = [notificationItem("1", "Review me", "open")];

    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const event = container.querySelector(".mobile-activity-event__body strong");
    expect(event?.textContent).toBe("Review requested");
  });

  it("hides notifications through a mobile toggle wired to the store", async () => {
    items.value = [notificationItem("1", "Review me", "open")];

    const { getByRole } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const button = getByRole("button", { name: "Hide notifications" });
    expect(button.getAttribute("aria-pressed")).toBe("false");

    await fireEvent.click(button);

    expect(setShowNotifications).toHaveBeenCalledWith(false);
  });

  it("marks an unread notification seen from a touch control without navigating", async () => {
    items.value = [notificationItem("1", "Review me", "open")];

    const { getByRole } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const seen = getByRole("button", { name: "Mark notification seen" });
    await fireEvent.click(seen);

    expect(markNotificationSeen).toHaveBeenCalledTimes(1);
    expect(markNotificationSeen.mock.calls[0]![0]).toMatchObject({ id: "1" });
    // The seen control is a sibling, so tapping it must not open the item.
    expect(onSelectItem).not.toHaveBeenCalled();
  });
});

describe("MobileActivityView hide closed/merged", () => {
  beforeEach(() => {
    hideClosedMerged.value = false;
    showNotifications.value = true;
    onSelectItem.mockClear();
  });

  afterEach(() => {
    cleanup();
  });

  it("hides notifications on merged/closed subjects but keeps open ones", () => {
    hideClosedMerged.value = true;
    items.value = [
      notificationItem("1", "Open subject", "open"),
      notificationItem("2", "Merged subject", "merged"),
      notificationItem("3", "Closed subject", "closed"),
    ];

    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    // A notifications-only mobile feed has no sibling PR row, yet the
    // merged/closed notifications are dropped because the filter reads
    // subject_state, not the notification's unread/read item_state.
    expect(container.textContent).toContain("Open subject");
    expect(container.textContent).not.toContain("Merged subject");
    expect(container.textContent).not.toContain("Closed subject");
  });

  it("keeps every notification when hide closed/merged is off", () => {
    items.value = [notificationItem("1", "Open subject", "open"), notificationItem("2", "Merged subject", "merged")];

    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    expect(container.textContent).toContain("Open subject");
    expect(container.textContent).toContain("Merged subject");
  });
});
