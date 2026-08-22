import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivityItem, ActivitySubject, WorkspaceActivitySubject } from "../api/types.js";
import MobileActivityView from "./MobileActivityViewRuntimeHarness.svelte";

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
const itemActivity = vi.hoisted(() => ({ value: [] as ActivitySubject[] }));
const workspaceActivity = vi.hoisted(() => ({ value: [] as WorkspaceActivitySubject[] }));
const onSelectItem = vi.hoisted(() => vi.fn());
const hideClosedMerged = vi.hoisted(() => ({ value: false }));
const hideBots = vi.hoisted(() => ({ value: false }));
const useWorkspaceActivityForRecency = vi.hoisted(() => ({ value: false }));
const hideOrgName = vi.hoisted(() => ({ value: false }));
const showNotifications = vi.hoisted(() => ({ value: true }));
const activityCapped = vi.hoisted(() => ({ value: false }));
const itemActivityCapped = vi.hoisted(() => ({ value: false }));
const activityLoading = vi.hoisted(() => ({ value: false }));
const activityError = vi.hoisted(() => ({ value: null as string | null }));
const showFlash = vi.hoisted(() => vi.fn());
const involvesMe = vi.hoisted(() => ({ value: false }));
const enabledEvents = vi.hoisted(() => ({
  value: new Set(["comment", "review", "commit", "force_push"]),
}));
const enabledItemTypes = vi.hoisted(() => ({
  value: new Set<"pr" | "issue">(["pr", "issue"]),
}));
const setEnabledEvents = vi.hoisted(() =>
  vi.fn((events: Set<string>) => {
    enabledEvents.value = events;
  }),
);
const setHideClosedMerged = vi.hoisted(() =>
  vi.fn((value: boolean) => {
    hideClosedMerged.value = value;
  }),
);
const setEnabledItemTypes = vi.hoisted(() =>
  vi.fn((itemTypes: Set<"pr" | "issue">) => {
    enabledItemTypes.value = itemTypes;
  }),
);
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
const loadActivity = vi.hoisted(() => vi.fn(async () => undefined));
const setFullEventProjectionRequired = vi.hoisted(() => vi.fn());
const setActivityPageLimit = vi.hoisted(() => vi.fn());
const loadThreadPreview = vi.hoisted(() => vi.fn());
const selectedAuthor = vi.hoisted(() => ({ value: undefined as string | undefined }));
const setActivityAuthor = vi.hoisted(() =>
  vi.fn((author: string | undefined) => {
    selectedAuthor.value = author;
  }),
);

function stubIntersectionObserver(): Map<Element, IntersectionObserverCallback> {
  const observed = new Map<Element, IntersectionObserverCallback>();
  class IntersectionObserverStub {
    constructor(private readonly callback: IntersectionObserverCallback) {}
    observe(target: Element): void {
      observed.set(target, this.callback);
    }
    disconnect(): void {}
    unobserve(): void {}
    takeRecords(): IntersectionObserverEntry[] {
      return [];
    }
    readonly root = null;
    readonly rootMargin = "0px";
    readonly thresholds = [0];
  }
  vi.stubGlobal("IntersectionObserver", IntersectionObserverStub);
  return observed;
}

vi.mock("../stores/flash.svelte.js", () => ({ showFlash }));

vi.mock("../context.js", () => ({
  getStores: () => ({
    activity: {
      initializeFromMount: vi.fn(),
      loadActivity,
      startActivityPolling: vi.fn(),
      stopActivityPolling: vi.fn(),
      getActivitySearch: () => "",
      getActivityAuthor: () => selectedAuthor.value,
      getActivityAuthors: () => ["Alice", "Bob"],
      isActivityAuthorsLoading: () => false,
      getActivityAuthorsError: () => null,
      getActivityItems: () => items.value,
      getItemActivity: () => itemActivity.value,
      getWorkspaceActivity: () => workspaceActivity.value,
      getActivityError: () => activityError.value,
      getTimeRange: () => "7d",
      getEnabledItemTypes: () => enabledItemTypes.value,
      getEnabledEvents: () => enabledEvents.value,
      getShowNotifications: () => showNotifications.value,
      getInvolvesMe: () => involvesMe.value,
      getHideClosedMerged: () => hideClosedMerged.value,
      getHideBots: () => hideBots.value,
      getUseWorkspaceActivityForRecency: () => useWorkspaceActivityForRecency.value,
      getHideDefaultBranchActivity: () => false,
      isActivityLoading: () => activityLoading.value,
      isActivityCapped: () => activityCapped.value,
      isItemActivityCapped: () => itemActivityCapped.value,
      setActivityFilterTypes: vi.fn(),
      setActivitySearch: vi.fn(),
      setActivityAuthor,
      setTimeRange: vi.fn(),
      setEnabledEvents,
      setEnabledItemTypes,
      setHideClosedMerged,
      setShowNotifications,
      setInvolvesMe: vi.fn((value: boolean) => {
        involvesMe.value = value;
      }),
      setFullEventProjectionRequired,
      setActivityPageLimit,
      loadThreadPreview,
      markNotificationSeen,
      setHideBots: vi.fn(),
      setHideDefaultBranchActivity: vi.fn(),
      syncToURL: vi.fn(),
    },
    settings: {
      getConfiguredRepos: () => [
        {
          provider: "github",
          platform_host: "github.com",
          owner: "acme",
          name: "api",
          repo_path: "acme/api",
          platform_repo_id: "R_api",
          is_glob: false,
          matched_repo_count: 1,
          hidden_from_ui: false,
        },
      ],
      getRepoPresets: () => [
        {
          name: "Backend",
          repos: [
            {
              provider: "github",
              platform_host: "github.com",
              platform_repo_id: "R_api",
              repo_path: "acme/api",
            },
          ],
        },
      ],
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

beforeEach(() => {
  hideBots.value = false;
  useWorkspaceActivityForRecency.value = false;
});

describe("MobileActivityView branch activity", () => {
  beforeEach(() => {
    items.value = [branchActivityItem("branch-commit")];
    itemActivity.value = [];
    workspaceActivity.value = [];
    hideOrgName.value = false;
    hideClosedMerged.value = false;
    activityCapped.value = false;
    itemActivityCapped.value = false;
    activityLoading.value = false;
    activityError.value = null;
    enabledEvents.value = new Set(["comment", "review", "commit", "force_push"]);
    enabledItemTypes.value = new Set(["pr", "issue"]);
    onSelectItem.mockClear();
    setEnabledItemTypes.mockClear();
    setHideOrgName.mockClear();
    selectedAuthor.value = undefined;
    setActivityAuthor.mockClear();
    setEnabledEvents.mockClear();
    setHideClosedMerged.mockClear();
    loadActivity.mockClear();
    setFullEventProjectionRequired.mockClear();
    setActivityPageLimit.mockClear();
    loadThreadPreview.mockClear();
    showFlash.mockClear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("keeps search and icon-only filters in one compact toolbar", () => {
    render(MobileActivityView, { props: { onSelectItem } });

    const search = screen.getByPlaceholderText("Search activity");
    const filters = screen.getByRole("button", { name: "Filters" });
    const toolbar = search.closest(".mobile-triage-search-bar");

    expect(toolbar).toBeTruthy();
    expect(filters.closest(".mobile-triage-search-bar")).toBe(toolbar);
    expect(filters.textContent?.trim()).toBe("");
    expect(filters.querySelector("svg")).toBeTruthy();
  });

  it("exposes the desktop event-type filters", async () => {
    render(MobileActivityView, { props: { onSelectItem } });

    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    const comments = screen.getByRole<HTMLInputElement>("switch", { name: "Comments" });
    expect(comments.checked).toBe(true);

    await fireEvent.click(comments);

    expect([...setEnabledEvents.mock.calls[0]![0]]).toEqual(["review", "commit", "force_push"]);
  });

  it("uses the shared repository picker inside the filter panel", async () => {
    const onRepoChange = vi.fn();
    render(MobileActivityView, { props: { onSelectItem, onRepoChange } });

    expect(screen.queryByRole("button", { name: "Select repository: Global" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    const repoPicker = screen.getByRole("button", { name: "Select repository: Global" });
    const filterPanel = screen.getByLabelText("Activity filters");
    expect(filterPanel.firstElementChild?.contains(repoPicker)).toBe(true);
    const authorPicker = screen.getByRole("button", { name: "Filter authors" });
    const authorRow = authorPicker.closest(".mobile-author-filter");
    expect(authorRow?.querySelector(".mobile-author-icon")).toBeTruthy();
    expect(authorRow?.textContent).toContain("All authors");
    await fireEvent.click(repoPicker);
    loadActivity.mockClear();
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Backend" }));

    expect(onRepoChange).toHaveBeenCalledWith("github|github.com/acme/api");
    expect(loadActivity).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "Save preset" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete preset Backend" })).toBeNull();
  });

  it("exposes closed and merged visibility", async () => {
    render(MobileActivityView, { props: { onSelectItem } });

    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    const control = screen.getByRole<HTMLInputElement>("switch", { name: "Hide closed/merged" });
    expect(control.checked).toBe(false);

    await fireEvent.click(control);

    expect(setHideClosedMerged).toHaveBeenCalledWith(true);
  });

  it("renders branch activity without a fake PR or issue number", () => {
    const { container } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    const article = container.querySelector("article");
    expect(article?.querySelector(".kit-card--raised")).toBeTruthy();
    expect(article?.textContent).toContain("Refresh cache warmer");
    expect(article?.textContent).toContain("main");
    expect(article?.textContent).toContain("a1b2c3d");
    expect(article?.textContent).not.toContain("#0");
    expect(article?.querySelector(".chip--kind-pr")).toBeNull();
    expect(article?.querySelector(".chip--kind-issue")).toBeNull();
  });

  it("uses a bounded projection for its mounted lifetime", () => {
    const view = render(MobileActivityView, { props: { onSelectItem } });

    expect(setActivityPageLimit).toHaveBeenCalledWith(30);
    expect(setActivityPageLimit.mock.invocationCallOrder[0]).toBeLessThan(
      loadActivity.mock.invocationCallOrder[0] ?? Number.POSITIVE_INFINITY,
    );
    expect(setFullEventProjectionRequired).not.toHaveBeenCalled();

    view.unmount();
    expect(setActivityPageLimit).toHaveBeenLastCalledWith(undefined);
  });

  it("autoloads one direct-activity chunk when the end sentinel enters the viewport", async () => {
    const observed = stubIntersectionObserver();
    activityCapped.value = true;
    activityLoading.value = true;
    const { container } = render(MobileActivityView, { props: { onSelectItem } });
    const sentinel = container.querySelector(".mobile-activity-loading-sentinel");
    expect(sentinel).toBeTruthy();
    const notify = observed.get(sentinel!);
    expect(notify).toBeTruthy();

    notify!([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);

    expect(setActivityPageLimit).toHaveBeenLastCalledWith(60);
    expect(loadActivity).toHaveBeenCalledTimes(2);

    notify!([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);
    expect(loadActivity).toHaveBeenCalledTimes(2);

    activityLoading.value = false;
    const viewport = sentinel!.closest(".kit-scrollbox__viewport");
    expect(viewport).toBeTruthy();
    await fireEvent.touchStart(viewport!);
    expect(setActivityPageLimit).toHaveBeenLastCalledWith(90);
    expect(loadActivity).toHaveBeenCalledTimes(3);
  });

  it("renders the next parent chunk after autoloading it", async () => {
    const observed = stubIntersectionObserver();
    items.value = Array.from({ length: 31 }, (_, index) =>
      branchActivityItem(`branch-commit-${index + 1}`, {
        body_preview: `Branch activity ${index + 1}`,
        branch_name: `branch-${index + 1}`,
      }),
    );
    itemActivityCapped.value = true;
    const { container } = render(MobileActivityView, { props: { onSelectItem } });
    expect(container.querySelectorAll("article")).toHaveLength(30);

    const sentinel = container.querySelector(".mobile-activity-loading-sentinel");
    expect(sentinel).toBeTruthy();
    observed.get(sentinel!)?.([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);

    await waitFor(() => expect(container.querySelectorAll("article")).toHaveLength(31));
  });

  it("does not render truncation warnings or a manual load control", () => {
    activityCapped.value = true;
    itemActivityCapped.value = true;

    render(MobileActivityView, { props: { onSelectItem } });

    expect(screen.queryByText(/most recent activity/)).toBeNull();
    expect(screen.queryByText(/most recently active pull requests and issues/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Load 30 more" })).toBeNull();
  });

  it("reports connection errors through the shared Kit UI warning banner", async () => {
    activityError.value = "Could not reach Kenn Forge";

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    await waitFor(() => {
      expect(showFlash).toHaveBeenCalledWith("Could not reach Kenn Forge", { tone: "warning", durationMs: 8_000 });
    });
    expect(container.querySelector(".mobile-activity-error")).toBeNull();
  });

  it("exposes independent PR and issue toggles", async () => {
    render(MobileActivityView, {
      props: { onSelectItem },
    });

    expect(screen.queryByRole("switch", { name: "PRs" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: /^Filters/ }));
    const prs = screen.getByRole("switch", { name: "PRs" });
    const issues = screen.getByRole("switch", { name: "Issues" });
    expect((prs as HTMLInputElement).checked).toBe(true);
    expect((issues as HTMLInputElement).checked).toBe(true);

    await fireEvent.click(prs);
    expect(setEnabledItemTypes).toHaveBeenCalledOnce();
    expect([...setEnabledItemTypes.mock.calls[0]![0]]).toEqual(["issue"]);
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

  it("uses shared timeline anatomy and semantic event tones", () => {
    items.value = [
      branchActivityItem("force-push", {
        activity_type: "default_branch_force_push",
        before_sha: "1111111111111111111111111111111111111111",
        after_sha: "2222222222222222222222222222222222222222",
        created_at: "2026-04-27T13:00:00Z",
      }),
      branchActivityItem("commit", {
        created_at: "2026-04-27T12:00:00Z",
      }),
    ];

    render(MobileActivityView, {
      props: { onSelectItem },
    });

    const timeline = screen.getByRole("list", {
      name: "Recent activity for 1111111 -> 2222222",
    });
    const timelineItems = within(timeline).getAllByRole("listitem");
    expect(timelineItems).toHaveLength(2);
    expect(timelineItems[0]?.classList.contains("kit-timeline-item--tone-danger")).toBe(true);
    expect(timelineItems[1]?.classList.contains("kit-timeline-item--tone-success")).toBe(true);
  });

  it("exposes a mobile hide org toggle", async () => {
    const { getByRole } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    await fireEvent.click(getByRole("button", { name: /^Filters/ }));
    const button = getByRole<HTMLInputElement>("switch", { name: "Hide org" });
    expect(button.checked).toBe(false);

    await fireEvent.click(button);

    expect(setHideOrgName).toHaveBeenCalledWith(true);
  });

  it("keeps author filtering in the collapsed filter panel and summarizes it without a chip row", async () => {
    render(MobileActivityView, { props: { onSelectItem } });

    const filters = screen.getByRole("button", { name: /^Filters/ });
    expect(filters.getAttribute("aria-expanded")).toBe("false");
    expect(screen.getByPlaceholderText("Search activity")).toBeTruthy();

    await fireEvent.click(filters);
    await fireEvent.click(screen.getByRole("button", { name: "Filter authors" }));
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Alice" }));

    expect(setActivityAuthor).toHaveBeenCalledWith("Alice");

    cleanup();
    selectedAuthor.value = "Alice";
    render(MobileActivityView, { props: { onSelectItem } });
    expect(screen.getByRole("button", { name: /Filters.*Alice/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear author filter Alice" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /Filters.*Alice/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Filter authors" }));
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Anyone" }));
    expect(setActivityAuthor).toHaveBeenLastCalledWith(undefined);
  });

  it("does not select a PR or issue when tapping a branch event", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);

    render(MobileActivityView, {
      props: { onSelectItem },
    });

    const event = screen.getByRole("button", { name: /Commit.*a1b2c3d.*Alice Example/ });
    await fireEvent.click(event);

    expect(onSelectItem).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledWith(
      "https://github.com/acme/widgets/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
      "_blank",
      "noopener",
    );
    open.mockRestore();
  });
});

function pullActivityItem(id: string, title: string, createdAt: string, number: number): ActivityItem {
  return {
    ...branchActivityItem(id),
    activity_type: "comment",
    body_preview: "Looks good",
    branch_name: "",
    commit_sha: "",
    created_at: createdAt,
    item_number: number,
    item_state: "open",
    item_title: title,
    item_type: "pr",
    item_url: `https://github.com/acme/widgets/pull/${number}`,
    activity_url: "",
  } as ActivityItem;
}

function workspaceSubject(
  number: number,
  title: string,
  activityAt: string,
  itemType: "pr" | "issue" = "pr",
): WorkspaceActivitySubject {
  return {
    activity_at: activityAt,
    item_author: "alice",
    item_number: number,
    item_state: "open",
    item_title: title,
    item_type: itemType,
    item_url: `https://github.com/acme/widgets/${itemType === "pr" ? "pull" : "issues"}/${number}`,
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
    workspace: { id: `workspace-${number}`, status: "ready" },
  } as WorkspaceActivitySubject;
}

function itemSubject(number: number, title: string, activityAt: string): ActivitySubject {
  const { workspace: _workspace, ...subject } = workspaceSubject(number, title, activityAt);
  return subject;
}

describe("MobileActivityView workspace activity", () => {
  beforeEach(() => {
    useWorkspaceActivityForRecency.value = true;
    items.value = [];
    itemActivity.value = [];
    workspaceActivity.value = [];
    hideClosedMerged.value = false;
    enabledItemTypes.value = new Set(["pr", "issue"]);
    onSelectItem.mockClear();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("orders an existing thread by its newer workspace activity", () => {
    items.value = [
      pullActivityItem("active", "Workspace-active pull", "2026-04-27T12:00:00Z", 1),
      pullActivityItem("provider", "Provider-newer pull", "2026-04-27T13:00:00Z", 2),
    ];
    workspaceActivity.value = [workspaceSubject(1, "Workspace-active pull", "2026-04-27T14:00:00Z")];

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    const cards = Array.from(container.querySelectorAll(".mobile-activity-card__title"));
    expect(cards.map((card) => card.textContent?.trim())).toEqual(["Workspace-active pull", "Provider-newer pull"]);
  });

  it("orders a thread by parent recency when its newer event is filtered", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-27T14:05:00Z"));
    items.value = [
      {
        ...pullActivityItem("older-visible-event", "Recently committed pull", "2026-04-27T12:00:00Z", 1),
        item_last_activity_at: "2026-04-27T14:00:00Z",
      },
      {
        ...pullActivityItem("newer-visible-event", "Recently commented pull", "2026-04-27T13:00:00Z", 2),
        item_last_activity_at: "2026-04-27T13:00:00Z",
      },
    ];

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    const cards = Array.from(container.querySelectorAll(".mobile-activity-card__title"));
    expect(cards.map((card) => card.textContent?.trim())).toEqual([
      "Recently committed pull",
      "Recently commented pull",
    ]);
    expect(container.querySelector(".mobile-activity-card time")?.textContent).toBe("5m ago");
    expect(container.querySelector(".mobile-activity-events time")?.textContent).toBe("2h ago");
  });

  it("renders a workspace-only subject without inventing a timeline event", async () => {
    workspaceActivity.value = [workspaceSubject(7, "Workspace-only pull", "2026-04-27T14:00:00Z")];

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    expect(screen.getByText("Workspace-only pull")).toBeTruthy();
    expect(screen.getByText("Recent activity")).toBeTruthy();
    expect(screen.getByLabelText("Workspace attached (ready)")).toBeTruthy();
    expect(container.querySelector(".mobile-activity-events")).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /Workspace-only pull/ }));
    expect(onSelectItem).toHaveBeenCalledWith(
      expect.objectContaining({
        activity_type: "workspace",
        item_number: 7,
        item_type: "pr",
      }),
    );
  });

  it("hides cached workspace-only subjects when workspace recency is disabled", () => {
    useWorkspaceActivityForRecency.value = false;
    workspaceActivity.value = [workspaceSubject(7, "Workspace-only pull", "2026-04-27T14:00:00Z")];

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    expect(container.textContent).not.toContain("Workspace-only pull");
  });

  it("renders a recently active parent without inventing a timeline event", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-27T14:05:00Z"));
    itemActivity.value = [itemSubject(8, "Old pull with recent hidden activity", "2026-04-27T14:00:00Z")];

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    expect(screen.getByText("Old pull with recent hidden activity")).toBeTruthy();
    expect(screen.getByText("Recent activity")).toBeTruthy();
    expect(container.querySelector(".mobile-activity-card time")?.textContent).toBe("5m ago");
    expect(container.querySelector(".mobile-activity-events")).toBeNull();
  });

  it("hides bot-authored parent and workspace-only cards", () => {
    itemActivity.value = [
      {
        ...itemSubject(8, "Bot-authored parent", "2026-04-27T14:00:00Z"),
        item_author: "renovate[bot]",
      },
    ];
    workspaceActivity.value = [
      {
        ...workspaceSubject(9, "Bot-authored workspace", "2026-04-27T13:00:00Z"),
        item_author: "release-bot",
      },
    ];
    hideBots.value = true;

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    expect(container.textContent).not.toContain("Bot-authored parent");
    expect(container.textContent).not.toContain("Bot-authored workspace");
  });

  it("disambiguates repository labels for parent-only cards", () => {
    hideOrgName.value = true;
    itemActivity.value = [
      {
        ...itemSubject(8, "First parent", "2026-04-27T14:00:00Z"),
        repo: {
          provider: "github",
          platform_host: "github.com",
          platform_repo_id: "repo-acme-widgets",
          owner: "acme",
          name: "widgets",
          repo_path: "acme/widgets",
        },
      },
      {
        ...itemSubject(9, "Second parent", "2026-04-27T13:00:00Z"),
        repo_owner: "platform",
        repo: {
          provider: "gitlab",
          platform_host: "gitlab.example.com",
          platform_repo_id: "repo-platform-widgets",
          owner: "platform",
          name: "widgets",
          repo_path: "platform/widgets",
        },
      },
    ];

    const { container } = render(MobileActivityView, { props: { onSelectItem } });

    const repoLabels = Array.from(container.querySelectorAll(".mobile-activity-card__meta span:first-child")).map(
      (element) => element.textContent?.trim(),
    );
    expect(repoLabels).toEqual(["acme/widgets", "platform/widgets"]);
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
    itemActivity.value = [];
    workspaceActivity.value = [];
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

    render(MobileActivityView, {
      props: { onSelectItem },
    });

    expect(screen.getByText("Review requested", { selector: "strong" })).toBeTruthy();
  });

  it("hides notifications through a mobile toggle wired to the store", async () => {
    items.value = [notificationItem("1", "Review me", "open")];

    const { getByRole } = render(MobileActivityView, {
      props: { onSelectItem },
    });

    await fireEvent.click(getByRole("button", { name: /^Filters/ }));
    const button = getByRole<HTMLInputElement>("switch", { name: "Notifications" });
    expect(button.checked).toBe(true);

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
    itemActivity.value = [];
    workspaceActivity.value = [];
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
