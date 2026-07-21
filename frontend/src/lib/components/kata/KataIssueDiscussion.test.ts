import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { KataTaskReference, KataTaskReferenceResponse, KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
import type { KataTaskDetail, KataTaskEvent, KataTaskViewResponse } from "../../api/kata/taskTypes.js";

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

function referenceResponse(references: KataTaskReference[]): KataTaskReferenceResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    generation: 7,
    invalidation_epoch: 2,
    fetched_at: "2026-06-01T12:00:00Z",
    references,
  };
}

function makeSearch(references: KataTaskReference[] = []): KataTaskReferenceSearch {
  return vi.fn(async () => referenceResponse(references));
}

function searchTask(overrides: Partial<KataTaskReference> = {}): KataTaskReference {
  return {
    uid: "issue-2",
    project_id: 1,
    project_uid: "project-1",
    project_name: "Inbox",
    short_id: "I-2",
    qualified_id: "INBOX-2",
    reference: "I-2",
    title: "Linked task",
    ...overrides,
  };
}

describe("KataIssueDiscussion", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("renders linked peer titles from the accepted snapshot catalog", () => {
    const linkedPeer = makeView().groups[0]!.issues[0]!;
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        issueCatalog: [makeIssue().issue, linkedPeer],
        searchReferences: makeSearch(),
        activeDaemonId: "home",
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    expect(screen.getByRole("button", { name: /Linked task/ })).toBeTruthy();
  });

  it("submits comments and related links for the selected issue", async () => {
    const onAddComment = vi.fn(async () => true);
    const onEditIssue = vi.fn(async () => true);

    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [makeEvent()],
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch(),
        activeDaemonId: "home",
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
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch(),
        activeDaemonId: "home",
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
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch(),
        activeDaemonId: "home",
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

  it("resets an acknowledged comment only after accepted replacement authority", async () => {
    const view = render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch(),
        activeDaemonId: "home",
        draftResetGeneration: 0,
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Related issue"), {
      target: { value: "I-9" },
    });
    await fireEvent.input(screen.getByLabelText("Comment"), {
      target: { value: "Accepted reply" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    expect((screen.getByLabelText("Comment") as HTMLTextAreaElement).value).toBe("Accepted reply");
    expect((screen.getByLabelText("Related issue") as HTMLInputElement).value).toBe("I-9");

    await view.rerender({ draftResetGeneration: 1 });

    expect((screen.getByLabelText("Comment") as HTMLTextAreaElement).value).toBe("");
    expect((screen.getByLabelText("Related issue") as HTMLInputElement).value).toBe("I-9");
  });

  it("resets an acknowledged related link only after accepted replacement authority", async () => {
    const view = render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch(),
        activeDaemonId: "home",
        draftResetGeneration: 0,
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Comment"), {
      target: { value: "Keep this unrelated reply" },
    });
    await fireEvent.input(screen.getByLabelText("Related issue"), {
      target: { value: "I-9" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Link" }));

    expect((screen.getByLabelText("Related issue") as HTMLInputElement).value).toBe("I-9");
    expect((screen.getByLabelText("Comment") as HTMLTextAreaElement).value).toBe("Keep this unrelated reply");

    await view.rerender({ draftResetGeneration: 1 });

    expect((screen.getByLabelText("Related issue") as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText("Comment") as HTMLTextAreaElement).value).toBe("Keep this unrelated reply");
  });

  it("preserves a newer comment draft when the acknowledged comment reset arrives", async () => {
    const view = render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch(),
        activeDaemonId: "home",
        draftResetGeneration: 0,
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Comment"), {
      target: { value: "Accepted reply" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    await fireEvent.input(screen.getByLabelText("Comment"), {
      target: { value: "Newer reply" },
    });

    await view.rerender({ draftResetGeneration: 1 });

    expect((screen.getByLabelText("Comment") as HTMLTextAreaElement).value).toBe("Newer reply");
  });

  it("inserts task references from the comment composer", async () => {
    const searchReferences = makeSearch([
      searchTask({ short_id: "pay-rent", reference: "pay-rent", title: "Pay rent" }),
    ]);
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences,
        activeDaemonId: "home",
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
    expect(searchReferences).toHaveBeenLastCalledWith("", { daemon_id: "home", limit: 20 });

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
      reference: "Inbox#shared-1",
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
      reference: "Work#shared-1",
      title: "Work shared",
    });
    render(KataIssueDiscussion, {
      props: {
        issue: makeIssue(),
        events: [],
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch([sharedHome, ...filler, sharedWork]),
        activeDaemonId: "home",
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
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch([searchTask({ short_id: "rent", reference: "rent", title: "Rent" })]),
        activeDaemonId: "home",
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
        issueCatalog: makeView().groups.flatMap((group) => group.issues),
        searchReferences: makeSearch(),
        activeDaemonId: "home",
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    expect(screen.getByText("+blocks · -related (2)")).toBeTruthy();
    expect(screen.queryByText("issue.links_changed")).toBeNull();
  });

  it("does not issue detail reads for link rows outside the current view", () => {
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
    const searchReferences = makeSearch();
    render(KataIssueDiscussion, {
      props: {
        issue,
        events: [],
        issueCatalog: [],
        searchReferences,
        activeDaemonId: "home",
        onAddComment: vi.fn(async () => true),
        onEditIssue: vi.fn(async () => true),
        onSelectIssue: vi.fn(),
      },
    });

    const links = screen.getByRole("region", { name: "Links" });
    expect(within(links).getByText("P-1")).toBeTruthy();
    expect(within(links).queryByText("Hydrated peer task")).toBeNull();
    expect(searchReferences).not.toHaveBeenCalled();
  });
});
