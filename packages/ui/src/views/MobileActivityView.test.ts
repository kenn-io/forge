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
const hideOrgName = vi.hoisted(() => ({ value: false }));
const setHideOrgName = vi.hoisted(() =>
  vi.fn((value: boolean) => {
    hideOrgName.value = value;
  }),
);

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
      getHideClosedMerged: () => false,
      getHideBots: () => false,
      getHideDefaultBranchActivity: () => false,
      isActivityLoading: () => false,
      isActivityCapped: () => false,
      setActivityFilterTypes: vi.fn(),
      setActivitySearch: vi.fn(),
      setTimeRange: vi.fn(),
      setItemFilter: vi.fn(),
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

  it("uses the full hosted repo path by default", () => {
    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const repoLabel = container.querySelector(".mobile-activity-card__meta span");
    expect(repoLabel?.textContent).toBe("github.com/acme/widgets");
  });

  it("respects hide org name in mobile activity cards", () => {
    hideOrgName.value = true;

    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const repoLabel = container.querySelector(".mobile-activity-card__meta span");
    expect(repoLabel?.textContent).toBe("widgets");
    expect(container.textContent).not.toContain("github.com/acme/widgets");
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
