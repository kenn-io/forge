import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type {
  KataTaskAPI,
  KataTaskDetail,
  KataTaskEvent,
  KataTaskLink,
  KataTaskSummary,
  KataTaskViewResponse,
} from "../../api/kata/taskTypes.js";
import { createKataLinkFilters } from "../../features/kata/kataLinkFilters.js";

import KataIssueDiscussion from "./KataIssueDiscussion.svelte";

function makeIssue(overrides: Partial<KataTaskDetail["issue"]> = {}): KataTaskDetail {
  return {
    issue: {
      id: 1,
      uid: "issue-1",
      project_id: 1,
      project_uid: "project-1",
      project_name: "Inbox",
      short_id: "I-1",
      qualified_id: "INBOX-1",
      title: "Ship the thing",
      body: "Body",
      status: "open",
      metadata: {},
      revision: 1,
      author: "wes",
      created_at: "2026-06-01T12:00:00Z",
      updated_at: "2026-06-01T12:00:00Z",
      ...overrides,
    },
    comments: [
      {
        id: 11,
        issue_id: 1,
        author: "wes",
        body: "First comment",
        created_at: "2026-06-01T12:30:00Z",
      },
    ],
    labels: [],
    links: [
      {
        id: 3,
        project_id: 1,
        from: { uid: "issue-1", short_id: "I-1" },
        to: { uid: "issue-2", short_id: "I-2" },
        type: "related",
        author: "wes",
        created_at: "2026-06-01T12:20:00Z",
      },
    ],
  };
}

function makeEvent(): KataTaskEvent {
  return {
    event_id: 7,
    event_uid: "event-7",
    origin_instance_uid: "instance-1",
    type: "issue.commented",
    project_id: 1,
    project_uid: "project-1",
    project_name: "Inbox",
    issue_id: 1,
    issue_uid: "issue-1",
    issue_short_id: "I-1",
    actor: "wes",
    created_at: "2026-06-01T12:31:00Z",
  };
}

function makeView(): KataTaskViewResponse {
  return {
    view: "today",
    fetched_at: "2026-06-01T12:00:00Z",
    groups: [
      {
        id: "today",
        title: "Today",
        issues: [
          {
            id: 2,
            uid: "issue-2",
            project_id: 1,
            project_uid: "project-1",
            project_name: "Inbox",
            short_id: "I-2",
            qualified_id: "INBOX-2",
            title: "Linked task",
            status: "open",
            metadata: {},
            revision: 1,
            author: "wes",
            created_at: "2026-06-01T12:00:00Z",
            updated_at: "2026-06-01T12:00:00Z",
          },
        ],
      },
    ],
  };
}

function makeAPI(
  options: {
    searchIssues?: KataTaskSummary[];
    issueDetails?: Record<string, KataTaskDetail>;
  } = {},
): KataTaskAPI {
  return {
    search: vi.fn(async () => ({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      issues: options.searchIssues ?? [],
      fetched_at: "2026-06-01T12:00:00Z",
    })),
    issue: vi.fn(
      async (uid: string) => options.issueDetails?.[uid] ?? makeIssue({ uid, short_id: uid, title: "Hydrated task" }),
    ),
  } as unknown as KataTaskAPI;
}

function taskLink(id: number, peerUID: string, peerShortID: string, type: KataTaskLink["type"]): KataTaskLink {
  return {
    id,
    project_id: 1,
    from: { uid: "issue-1", short_id: "I-1" },
    to: { uid: peerUID, short_id: peerShortID },
    type,
    author: "wes",
    created_at: "2026-06-01T12:20:00Z",
  };
}

function searchTask(overrides: Partial<KataTaskSummary> = {}): KataTaskSummary {
  return {
    id: 2,
    uid: "issue-2",
    project_id: 1,
    project_uid: "project-1",
    project_name: "Inbox",
    short_id: "I-2",
    qualified_id: "INBOX-2",
    title: "Linked task",
    status: "open",
    metadata: {},
    revision: 1,
    author: "wes",
    created_at: "2026-06-01T12:00:00Z",
    updated_at: "2026-06-01T12:00:00Z",
    ...overrides,
  };
}

describe("KataIssueDiscussion", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("submits comments and related links for the selected issue", async () => {
    const onAddComment = vi.fn(async () => true);
    const onEditIssue = vi.fn(async () => true);

    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [makeEvent()],
        currentView: makeView(),
        api: makeAPI(),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment,
        onEditIssue,
        onSelectIssue: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Comment"), {
      target: { value: "Looks good" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    expect(onAddComment).toHaveBeenCalledWith("issue-1", "Looks good");

    await fireEvent.input(screen.getByLabelText("Related issue"), {
      target: { value: "I-9" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Link" }));

    expect(onEditIssue).toHaveBeenCalledWith("issue-1", { links_delta: { add_related: ["I-9"] } });
  });

  it("renders linked task state and event history", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-06-01T13:30:00Z"));
    const onSelectIssue = vi.fn();

    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [makeEvent()],
        currentView: makeView(),
        api: makeAPI(),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue,
      },
    });

    expect(screen.getByText("First comment")).toBeTruthy();
    expect(screen.getByText("1h ago")).toBeTruthy();
    expect(screen.getByText("commented")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Linked task/ })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /Linked task/ }));

    expect(onSelectIssue).toHaveBeenCalledWith("issue-2");

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Linked task/ })).toBeTruthy();
    });
  });

  it("preserves the comment draft when submission fails", async () => {
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        currentView: makeView(),
        api: makeAPI(),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => false),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Comment"), {
      target: { value: "Keep this reply" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    expect((screen.getByLabelText("Comment") as HTMLTextAreaElement).value).toBe("Keep this reply");
  });

  it("inserts task references from the comment composer", async () => {
    const api = makeAPI({ searchIssues: [searchTask({ short_id: "pay-rent", title: "Pay rent" })] });
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        currentView: makeView(),
        api,
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const composer = screen.getByLabelText("Comment") as HTMLTextAreaElement;
    await fireEvent.input(composer, { target: { value: "see #" } });

    await waitFor(() => {
      expect(screen.getByRole("listbox", { name: "Insert reference" })).toBeTruthy();
    });
    expect(api.search).toHaveBeenLastCalledWith({
      scope: { kind: "all" },
      status: "open",
      owner: "",
      label: "",
      query: "",
    });

    await fireEvent.keyDown(composer, { key: "Enter" });

    await waitFor(() => {
      expect(composer.value).toBe("see #pay-rent ");
    });
    expect(screen.queryByRole("listbox", { name: "Insert reference" })).toBeNull();
  });

  it("qualifies ambiguous task references even when the peer is outside the visible limit", async () => {
    const sharedHome = searchTask({
      uid: "issue-home",
      short_id: "shared-1",
      qualified_id: "Inbox#shared-1",
      title: "Home shared",
    });
    const filler = Array.from({ length: 7 }, (_, index) =>
      searchTask({
        uid: `issue-filler-${index}`,
        short_id: `filler-${index}`,
        qualified_id: `Inbox#filler-${index}`,
        title: `Filler ${index}`,
      }),
    );
    const sharedWork = searchTask({
      uid: "issue-work",
      short_id: "shared-1",
      qualified_id: "Work#shared-1",
      title: "Work shared",
    });
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        currentView: makeView(),
        api: makeAPI({ searchIssues: [sharedHome, ...filler, sharedWork] }),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const composer = screen.getByLabelText("Comment") as HTMLTextAreaElement;
    await fireEvent.input(composer, { target: { value: "see #shared" } });

    await waitFor(() => {
      expect(screen.getByRole("listbox", { name: "Insert reference" })).toBeTruthy();
    });
    expect(screen.queryByText("Work shared")).toBeNull();

    await fireEvent.keyDown(composer, { key: "Enter" });

    await waitFor(() => {
      expect(composer.value).toBe("see #Inbox#shared-1 ");
    });
  });

  it("closes the comment reference menu without changing the draft", async () => {
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        currentView: makeView(),
        api: makeAPI({ searchIssues: [searchTask({ short_id: "rent", title: "Rent" })] }),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const composer = screen.getByLabelText("Comment") as HTMLTextAreaElement;
    await fireEvent.input(composer, { target: { value: "see #r" } });

    await waitFor(() => {
      expect(screen.getByRole("listbox", { name: "Insert reference" })).toBeTruthy();
    });

    await fireEvent.keyDown(composer, { key: "Escape" });

    expect(screen.queryByRole("listbox", { name: "Insert reference" })).toBeNull();
    expect(composer.value).toBe("see #r");
  });

  it("renders task events as user-facing labels", () => {
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [
          {
            ...makeEvent(),
            type: "issue.links_changed",
            payload: {
              blocks_added: [{ uid: "issue-late-fee", short_id: "late" }],
              related_removed: ["foo", "bar"],
            },
          },
        ],
        currentView: makeView(),
        api: makeAPI(),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    expect(screen.getByText("+blocks · -related (2)")).toBeTruthy();
    expect(screen.queryByText("issue.links_changed")).toBeNull();
  });

  it("hydrates link rows with peer task titles outside the current view", async () => {
    const issue = makeIssue({
      uid: "issue-1",
      short_id: "I-1",
    });
    issue.links = [
      {
        id: 1,
        project_id: 1,
        from: { uid: "issue-1", short_id: "I-1" },
        to: { uid: "issue-peer", short_id: "P-1" },
        type: "related",
        author: "wes",
        created_at: "2026-06-01T12:00:00Z",
      },
    ];
    const api = makeAPI({
      issueDetails: {
        "issue-peer": makeIssue({
          uid: "issue-peer",
          short_id: "P-1",
          title: "Hydrated peer task",
        }),
      },
    });
    render(KataIssueDiscussion, {
      props: {
        issue,
        events: [],
        currentView: { groups: [] },
        api,
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const links = screen.getByRole("region", { name: "Links" });
    await waitFor(() => {
      expect(within(links).getByText("Hydrated peer task")).toBeTruthy();
    });
    expect(api.issue).toHaveBeenCalledWith("issue-peer");
  });

  it("shows only open peers by default and reports the filtered count", async () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-open", "open", "related"), taskLink(2, "issue-closed", "closed", "blocks")];
    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: { groups: [] },
        api: makeAPI({
          issueDetails: {
            "issue-open": makeIssue({ uid: "issue-open", short_id: "open", title: "Open peer", status: "open" }),
            "issue-closed": makeIssue({
              uid: "issue-closed",
              short_id: "closed",
              title: "Closed peer",
              status: "closed",
            }),
          },
        }),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    await waitFor(() => expect(screen.getByRole("button", { name: /Open peer/ })).toBeTruthy());
    expect(screen.queryByRole("button", { name: /Closed peer/ })).toBeNull();
    const links = screen.getByRole("region", { name: "Links" });
    expect(within(links).getByText("1 / 2")).toBeTruthy();
    expect(links.querySelector(".state-badge")).toBeNull();
  });

  it("shows state chips when both task-state filters are enabled", async () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-open", "open", "related"), taskLink(2, "issue-closed", "closed", "blocks")];
    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: { groups: [] },
        api: makeAPI({
          issueDetails: {
            "issue-open": makeIssue({ uid: "issue-open", short_id: "open", title: "Open peer", status: "open" }),
            "issue-closed": makeIssue({
              uid: "issue-closed",
              short_id: "closed",
              title: "Closed peer",
              status: "closed",
            }),
          },
        }),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("all"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const links = screen.getByRole("region", { name: "Links" });
    await waitFor(() => expect(within(links).getByRole("button", { name: /Open peer open/ })).toBeTruthy());
    expect(within(links).getByRole("button", { name: /Closed peer closed/ })).toBeTruthy();
    expect(within(links).getByText("Open").closest(".state-badge")).toBeTruthy();
    expect(within(links).getByText("Closed").closest(".state-badge")).toBeTruthy();
  });

  it("shows state chips when both filters are enabled even if every peer is open", async () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-open", "open", "related")];
    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: { groups: [] },
        api: makeAPI({
          issueDetails: {
            "issue-open": makeIssue({ uid: "issue-open", short_id: "open", title: "Only open peer", status: "open" }),
          },
        }),
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("all"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const links = screen.getByRole("region", { name: "Links" });
    await waitFor(() => expect(within(links).getByRole("button", { name: /Only open peer open/ })).toBeTruthy());
    expect(within(links).getByText("Open").closest(".state-badge")).toBeTruthy();
  });

  it("filters directional relationship types and shows the filtered-empty message", async () => {
    const selected = makeIssue();
    selected.links = [
      taskLink(1, "issue-related", "related", "related"),
      taskLink(2, "issue-blocked", "blocked", "blocks"),
    ];
    const filters = createKataLinkFilters("all");
    filters.relations.related = false;
    filters.relations.blocks = false;
    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: { groups: [] },
        api: makeAPI({
          issueDetails: {
            "issue-related": makeIssue({ uid: "issue-related", short_id: "related", title: "Related peer" }),
            "issue-blocked": makeIssue({ uid: "issue-blocked", short_id: "blocked", title: "Blocked peer" }),
          },
        }),
        activeDaemonId: "home",
        linkFilters: filters,
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const links = screen.getByRole("region", { name: "Links" });
    await waitFor(() => expect(within(links).getByText("No links match these filters.")).toBeTruthy());
    expect(within(links).queryByText("No links.")).toBeNull();
    expect(within(links).getByText("0 / 2")).toBeTruthy();
  });

  it("shows a resolving state while a single-state filter waits for peer status", () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-pending", "pending", "related")];
    const api = makeAPI();
    vi.mocked(api.issue).mockImplementation(() => new Promise<KataTaskDetail>(() => {}));

    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: { groups: [] },
        api,
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    expect(within(screen.getByRole("region", { name: "Links" })).getByText("Resolving linked tasks...")).toBeTruthy();
  });

  it("does not show resolving state for a pending disabled relationship", () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-pending", "pending", "related")];
    const api = makeAPI();
    vi.mocked(api.issue).mockImplementation(() => new Promise<KataTaskDetail>(() => {}));
    const filters = createKataLinkFilters("open");
    filters.relations.related = false;

    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: { groups: [] },
        api,
        activeDaemonId: "home",
        linkFilters: filters,
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const links = screen.getByRole("region", { name: "Links" });
    expect(within(links).getByText("No links match these filters.")).toBeTruthy();
    expect(within(links).queryByText(/Resolving/)).toBeNull();
  });

  it("shows the filtered-empty state when both task states are disabled", () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-open", "open", "related")];
    const filters = createKataLinkFilters("all");
    filters.statuses.open = false;
    filters.statuses.closed = false;

    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: makeView(),
        api: makeAPI(),
        activeDaemonId: "home",
        linkFilters: filters,
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    expect(
      within(screen.getByRole("region", { name: "Links" })).getByText("No links match these filters."),
    ).toBeTruthy();
  });

  it("keeps failed peer state discoverable in a single-state view", async () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-missing", "missing", "related")];
    const api = makeAPI();
    vi.mocked(api.issue).mockRejectedValue(new Error("unavailable"));

    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: { groups: [] },
        api,
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const links = screen.getByRole("region", { name: "Links" });
    await waitFor(() => expect(within(links).getByTitle("Task state unavailable")).toBeTruthy());
    expect(within(links).getByRole("button", { name: /missing state unavailable/ })).toBeTruthy();
  });

  it("uses current-view peer summaries without redundant detail requests", () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-2", "I-2", "related")];
    const api = makeAPI();

    render(KataIssueDiscussion, {
      props: {
        issue: selected,
        events: [],
        currentView: makeView(),
        api,
        activeDaemonId: "home",
        linkFilters: createKataLinkFilters("open"),
        onLinkFiltersChange: vi.fn(),
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    expect(screen.getByRole("button", { name: /Linked task/ })).toBeTruthy();
    expect(api.issue).not.toHaveBeenCalled();
  });

  it("retries only failed peers when a revised selected detail refreshes", async () => {
    const selected = makeIssue();
    selected.links = [taskLink(1, "issue-stable", "stable", "related"), taskLink(2, "issue-retry", "retry", "related")];
    const api = makeAPI();
    let retryCalls = 0;
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      if (uid === "issue-stable") {
        return makeIssue({ uid, short_id: "stable", title: "Stable peer" });
      }
      retryCalls += 1;
      if (retryCalls === 1) throw new Error("temporary");
      return makeIssue({ uid, short_id: "retry", title: "Recovered peer" });
    });
    const props = {
      issue: selected,
      events: [],
      currentView: { groups: [] },
      api,
      activeDaemonId: "home",
      linkFilters: createKataLinkFilters("all"),
      onLinkFiltersChange: vi.fn(),
      onAddComment: vi.fn(async () => true),
      onEditIssue: vi.fn(async () => true),
      onSelectIssue: vi.fn(),
    };
    const { rerender } = render(KataIssueDiscussion, { props });
    const links = screen.getByRole("region", { name: "Links" });
    await waitFor(() => expect(within(links).getByRole("button", { name: /Stable peer open/ })).toBeTruthy());
    await waitFor(() => expect(within(links).getByTitle("Task state unavailable")).toBeTruthy());

    await rerender({
      ...props,
      issue: { ...selected, issue: { ...selected.issue, revision: selected.issue.revision + 1 } },
    });

    await waitFor(() => expect(within(links).getByRole("button", { name: /Recovered peer open/ })).toBeTruthy());
    expect(api.issue).toHaveBeenCalledTimes(3);
    expect(vi.mocked(api.issue).mock.calls.filter(([uid]) => uid === "issue-stable")).toHaveLength(1);
    expect(vi.mocked(api.issue).mock.calls.filter(([uid]) => uid === "issue-retry")).toHaveLength(2);
  });
});
