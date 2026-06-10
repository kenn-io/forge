import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import type { ComponentProps } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type {
  KataProjectSummary,
  KataRecurrence,
  KataTaskAPI,
  KataTaskDetail,
  KataTaskViewResponse,
} from "../../api/kata/taskTypes.js";
import type { MessageLinkRef } from "../../messages/types";

import KataIssueDetail from "./KataIssueDetail.svelte";

type KataIssueDetailProps = ComponentProps<typeof KataIssueDetail>;

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
      body: "Initial body",
      status: "open",
      metadata: { checklist: [{ id: "item-1", text: "Send", done: false }] },
      revision: 1,
      author: "wes",
      created_at: "2026-06-01T12:00:00Z",
      updated_at: "2026-06-01T12:00:00Z",
      ...overrides,
    },
    comments: [],
    labels: [{ issue_id: 1, label: "review", author: "wes", created_at: "2026-06-01T12:00:00Z" }],
    links: [],
  };
}

function makeProject(uid: string, name: string, role = ""): KataProjectSummary {
  return {
    id: uid === "project-1" ? 1 : 2,
    uid,
    name,
    metadata: role ? { role } : {},
    open_count: 1,
    revision: 1,
    created_at: "2026-06-01T12:00:00Z",
  };
}

function makeRecurrence(): KataRecurrence {
  return {
    id: 9,
    uid: "recurrence-9",
    project_id: 1,
    rrule: "FREQ=WEEKLY",
    dtstart: "2026-06-01",
    timezone: "UTC",
    template_title: "Ship the thing",
    template_body: "",
    template_labels: [],
    template_metadata: {},
    next_occurrence_key: "2026-06-08",
    author: "wes",
    revision: 1,
    created_at: "2026-06-01T12:00:00Z",
    updated_at: "2026-06-01T12:00:00Z",
  };
}

function makeMessageLink(overrides: Partial<MessageLinkRef> = {}): MessageLinkRef {
  return {
    message_id: 1001,
    conversation_id: 1001,
    subject: "Project sync",
    from: "alice@example.com",
    sent_at: "2026-05-15T09:00:00Z",
    added_at: "2026-05-18T00:00:00Z",
    ...overrides,
  };
}

function makeView(): KataTaskViewResponse {
  return {
    view: "today",
    fetched_at: "2026-06-01T12:00:00Z",
    groups: [],
  };
}

function makeAPI(): KataTaskAPI {
  return {
    search: vi.fn(async () => ({
      filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
      issues: [],
      fetched_at: "2026-06-01T12:00:00Z",
    })),
    issue: vi.fn(async () => makeIssue()),
  } as unknown as KataTaskAPI;
}

function renderDetail(props: Partial<KataIssueDetailProps> = {}) {
  return render(KataIssueDetail, {
    props: {
      issue: makeIssue({ recurrence_id: 9 }),
      events: [],
      currentView: makeView(),
      api: makeAPI(),
      activeDaemonId: "home",
      projects: [makeProject("project-1", "Inbox", "inbox"), makeProject("project-2", "Roadmap")],
      ownerOptions: [],
      messageLinks: [],
      unlinkBusyIds: new Set<number>(),
      unlinkError: null,
      selectedRecurrences: [makeRecurrence()],
      checklistRevealed: false,
      onMoveIssue: vi.fn(async () => {}),
      onPatchMetadata: vi.fn(async () => true),
      onAddComment: vi.fn(async () => true),
      onEditIssue: vi.fn(async () => true),
      onAssignOwner: vi.fn(async () => true),
      onUnassignOwner: vi.fn(async () => true),
      onSetPriority: vi.fn(async () => true),
      onAddLabel: vi.fn(async () => true),
      onRemoveLabel: vi.fn(),
      onOpenMessage: undefined,
      onUnlinkMessage: vi.fn(),
      onRevealChecklist: vi.fn(),
      onCreateRecurrence: vi.fn(),
      onEditRecurrence: vi.fn(),
      onDeleteRecurrence: vi.fn(),
      onCloseIssue: vi.fn(async () => true),
      onReopenIssue: vi.fn(),
      onDeleteIssue: vi.fn(async () => true),
      onSelectIssue: vi.fn(),
      ...props,
    },
  });
}

describe("KataIssueDetail", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders the selected issue shell and composed sections", () => {
    renderDetail();

    const detail = screen.getByRole("region", { name: "Task detail" });
    expect(within(detail).getByRole("heading", { name: "Ship the thing" })).toBeTruthy();
    expect(within(detail).getByText("INBOX-1")).toBeTruthy();
    expect(within(detail).getByText("Initial body")).toBeTruthy();
    expect(within(detail).getByRole("region", { name: "Checklist" })).toBeTruthy();
    expect(within(detail).getByRole("heading", { name: "Recurring" })).toBeTruthy();
    expect(within(detail).getByRole("heading", { name: "Comments" })).toBeTruthy();
  });

  it("edits title and description through the issue edit callback", async () => {
    const onEditIssue = vi.fn(async () => true);
    renderDetail({ onEditIssue });

    await fireEvent.click(screen.getByRole("button", { name: "Edit title" }));
    await fireEvent.input(screen.getByLabelText("Edit title"), {
      target: { value: "Updated title" },
    });
    await fireEvent.keyDown(screen.getByLabelText("Edit title"), { key: "Enter" });

    expect(onEditIssue).toHaveBeenCalledWith("issue-1", { title: "Updated title" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit description" }));
    await fireEvent.input(screen.getByLabelText("Edit description"), {
      target: { value: "Updated body" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onEditIssue).toHaveBeenCalledWith("issue-1", { body: "Updated body" });
  });

  it("moves to a non-inbox project from the crumb picker", async () => {
    const onMoveIssue = vi.fn(async () => {});
    renderDetail({ onMoveIssue });

    await fireEvent.click(screen.getByRole("button", { name: /Move issue from Inbox/ }));
    await fireEvent.keyDown(screen.getByRole("combobox", { name: "Move issue project" }), { key: "Enter" });

    expect(onMoveIssue).toHaveBeenCalledWith("project-2");
  });

  it("falls back to project UID when the issue omits project name", () => {
    renderDetail({
      issue: makeIssue({
        project_id: 1,
        project_uid: "project-2",
        project_name: "",
      }),
    });

    expect(screen.getByRole("button", { name: "Move issue from Roadmap" }).textContent).toContain("Roadmap");
  });

  it("renders linked messages through the detail composition", () => {
    renderDetail({
      messageLinks: [makeMessageLink({ subject: "Lease renewal", from: "alice@example.com" })],
    });

    const linked = screen.getByRole("region", { name: "Linked messages" });
    expect(within(linked).getByText("Lease renewal")).toBeTruthy();
    expect(within(linked).getByText("alice@example.com")).toBeTruthy();
  });

  it("opens and unlinks linked messages through detail callbacks", async () => {
    const link = makeMessageLink({ message_id: 2001, subject: "Lease renewal" });
    const onOpenMessage = vi.fn();
    const onUnlinkMessage = vi.fn();
    renderDetail({
      messageLinks: [link],
      onOpenMessage,
      onUnlinkMessage,
    });

    await fireEvent.click(screen.getByTitle("Open alice@example.com - Lease renewal"));
    expect(onOpenMessage).toHaveBeenCalledWith(link);

    await fireEvent.click(screen.getByRole("button", { name: "Unlink Lease renewal" }));
    expect(onUnlinkMessage).toHaveBeenCalledWith(link);
  });
});
