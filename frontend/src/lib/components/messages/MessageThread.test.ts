import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";

import type { MessageDetailData, MessageSummary } from "../../api/messages/types";
import MessageThread from "./MessageThread.svelte";

afterEach(cleanup);

function summary(
  id: number,
  conversationID: number,
  sentAt: string,
  subject = `m${id}`,
  from = "alice@example.com",
): MessageSummary {
  return {
    id,
    conversation_id: conversationID,
    subject,
    from,
    to: ["bob@example.com"],
    cc: [],
    bcc: [],
    sent_at: sentAt,
    snippet: `snip ${id}`,
    labels: [],
    has_attachments: false,
    size_bytes: 1024,
    deleted_at: null,
  };
}

function detail(overrides: Partial<MessageDetailData> = {}): MessageDetailData {
  return {
    id: 1002,
    conversation_id: 1001,
    subject: "Project sync",
    from: "bob@example.com",
    to: ["alice@example.com"],
    cc: [],
    bcc: [],
    sent_at: "2026-05-15T10:00:00Z",
    snippet: "thanks",
    labels: [],
    has_attachments: false,
    size_bytes: 1024,
    deleted_at: null,
    body: "Following up.",
    attachments: [],
    ...overrides,
  };
}

const fixedProps = {
  permalinkOf: (id: number) => `messages:msgvault:${id}`,
  remoteImageURL: (id: number, token: string, index: string) =>
    `/api/v1/msgvault/messages/${id}/remote-image/${token}/${index}`,
  loadingDetail: false,
  detailError: null,
  loadingThread: false,
  threadError: null,
};

function renderThread(
  props: Partial<{
    detail: MessageDetailData | null;
    thread: MessageSummary[] | null;
    selectedMessageId: number | null;
    onSelectMessage: (id: number) => void;
    loadingDetail: boolean;
    detailError: string | null;
    loadingThread: boolean;
    threadError: string | null;
  }>,
) {
  return render(MessageThread, {
    props: {
      detail: null,
      thread: null,
      selectedMessageId: null,
      onSelectMessage: () => {},
      ...fixedProps,
      ...props,
    },
  });
}

describe("MessageThread stack mode", () => {
  const thread = [
    summary(1001, 1001, "2026-05-15T09:00:00Z", "Project sync"),
    summary(1002, 1001, "2026-05-15T10:00:00Z", "Re: Project sync", "bob@example.com"),
    summary(1003, 1001, "2026-05-15T14:30:00Z", "Re: Project sync"),
  ];

  test("renders the thread header row with selected subject and message count", () => {
    renderThread({ detail: detail({ id: 1002, subject: "Re: Project sync" }), thread, selectedMessageId: 1002 });

    expect(screen.getByRole("heading", { name: /Re: Project sync/ })).toBeTruthy();
    expect(screen.getByText(/3\s+msgs?/i)).toBeTruthy();
  });

  test("renders one collapsed peer row per non-selected message", () => {
    renderThread({ detail: detail({ id: 1002 }), thread, selectedMessageId: 1002 });

    expect(screen.getAllByRole("button", { name: /open message/i })).toHaveLength(2);
  });

  test("clicking a collapsed peer selects that message", async () => {
    const onSelectMessage = vi.fn();
    renderThread({ detail: detail({ id: 1002 }), thread, selectedMessageId: 1002, onSelectMessage });

    const peers = screen.getAllByRole("button", { name: /open message/i });
    await fireEvent.click(peers[0]!);

    expect(onSelectMessage).toHaveBeenCalledTimes(1);
    expect(typeof onSelectMessage.mock.calls[0]?.[0]).toBe("number");
    expect([1001, 1003]).toContain(onSelectMessage.mock.calls[0]?.[0]);
  });

  test("mounts MessageDetail in compact mode for the open card", () => {
    const { container } = renderThread({
      detail: detail({ id: 1002 }),
      thread,
      selectedMessageId: 1002,
    });

    const messageDetail = container.querySelector(".messages-detail");
    expect(messageDetail).not.toBeNull();
    expect(messageDetail?.classList.contains("compact")).toBe(true);
  });

  test("ignores stale detail when picking the thread-header subject", () => {
    renderThread({
      detail: detail({ id: 1003, subject: "STALE - previous subject" }),
      thread,
      selectedMessageId: 1002,
    });

    expect(screen.getByRole("heading", { name: /Re: Project sync/ })).toBeTruthy();
    expect(screen.queryByText(/STALE/)).toBeNull();
  });

  test("multi-message thread without a matching selected row falls through to non-stack mode", () => {
    const { container } = renderThread({
      detail: detail({ id: 9999, conversation_id: 50, subject: "Other conv" }),
      thread,
      selectedMessageId: 9999,
      loadingThread: true,
    });

    expect(screen.queryAllByRole("button", { name: /open message/i })).toHaveLength(0);
    expect(screen.queryByText(/\bmsgs?\b/i)).toBeNull();
    const messageDetail = container.querySelector(".messages-detail");
    expect(messageDetail?.classList.contains("compact")).toBe(false);
    expect(screen.getByRole("heading", { level: 1, name: "Other conv" })).toBeTruthy();
  });

  test("stack mode renders the header even when the selected subject is empty", () => {
    const emptySubjectThread = [
      summary(1001, 1001, "2026-05-15T09:00:00Z", ""),
      summary(1002, 1001, "2026-05-15T10:00:00Z", "", "bob@example.com"),
      summary(1003, 1001, "2026-05-15T14:30:00Z", ""),
    ];

    renderThread({
      detail: detail({ id: 1002, subject: "" }),
      thread: emptySubjectThread,
      selectedMessageId: 1002,
    });

    expect(screen.getByText(/3\s+msgs?/i)).toBeTruthy();
  });
});

describe("MessageThread singleton mode", () => {
  const thread = [summary(202, 102, "2026-05-15T07:00:00Z", "Vacation OOO", "bob@example.com")];

  test("renders MessageDetail in non-compact mode with no thread header or peers", () => {
    const { container } = renderThread({
      detail: detail({ id: 202, conversation_id: 102, subject: "Vacation OOO" }),
      thread,
      selectedMessageId: 202,
    });

    const messageDetail = container.querySelector(".messages-detail");
    expect(messageDetail).not.toBeNull();
    expect(messageDetail?.classList.contains("compact")).toBe(false);
    expect(screen.getByRole("heading", { level: 1, name: "Vacation OOO" })).toBeTruthy();
    expect(screen.queryAllByRole("button", { name: /open message/i })).toHaveLength(0);
    expect(screen.queryByText(/\bmsgs?\b/i)).toBeNull();
  });

  test("treats a one-message thread with a non-matching selected id as loading", () => {
    const stale = [summary(999, 50, "2026-05-15T07:00:00Z")];
    const { container } = renderThread({
      detail: detail({ id: 1234, conversation_id: 60 }),
      thread: stale,
      selectedMessageId: 1234,
      loadingThread: true,
    });

    expect(screen.queryByText(/\bmsg\b/i)).toBeNull();
    expect(screen.queryAllByRole("button", { name: /open message/i })).toHaveLength(0);
    const messageDetail = container.querySelector(".messages-detail");
    expect(messageDetail?.classList.contains("compact")).toBe(false);
  });
});

describe("MessageThread loading mode", () => {
  test("renders MessageDetail in non-compact mode while the thread is fetched", () => {
    const { container } = renderThread({
      detail: detail({ id: 1002, subject: "Project sync" }),
      thread: null,
      selectedMessageId: 1002,
      loadingThread: true,
    });

    const messageDetail = container.querySelector(".messages-detail");
    expect(messageDetail?.classList.contains("compact")).toBe(false);
    expect(screen.getByRole("heading", { level: 1, name: "Project sync" })).toBeTruthy();
  });
});

describe("MessageThread error mode", () => {
  test("renders MessageDetail non-compact and shows the inline notice", () => {
    const { container } = renderThread({
      detail: detail({ id: 1002, subject: "Project sync" }),
      thread: null,
      selectedMessageId: 1002,
      threadError: "Couldn't load conversation context.",
    });

    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/Couldn't load conversation context/i);
    const messageDetail = container.querySelector(".messages-detail");
    expect(messageDetail?.classList.contains("compact")).toBe(false);
    expect(screen.getByRole("heading", { level: 1, name: "Project sync" })).toBeTruthy();
  });
});

describe("MessageThread window behavior", () => {
  test("a long thread renders only the selected-inclusive 50-window with a truncation banner", () => {
    const sorted: MessageSummary[] = Array.from({ length: 60 }, (_, i) => {
      const base = Date.UTC(2026, 4, 15, 0, 0, 0);
      return summary(i + 1, 9, new Date(base + i * 60_000).toISOString(), "Long thread");
    });

    renderThread({
      detail: detail({ id: 60, conversation_id: 9, subject: "Long thread" }),
      thread: sorted,
      selectedMessageId: 60,
    });

    expect(screen.getAllByRole("button", { name: /open message/i })).toHaveLength(49);
    expect(screen.getByText(/Showing 50 of 60 messages\. The selected message is included\./)).toBeTruthy();
  });

  test("when selected sits outside the recent 50, it is still in the window", () => {
    const sorted: MessageSummary[] = Array.from({ length: 60 }, (_, i) => {
      const base = Date.UTC(2026, 4, 15, 0, 0, 0);
      return summary(i + 1, 9, new Date(base + i * 60_000).toISOString(), "Long thread");
    });

    renderThread({
      detail: detail({ id: 5, conversation_id: 9, subject: "Long thread" }),
      thread: sorted,
      selectedMessageId: 5,
    });

    expect(screen.getAllByRole("button", { name: /open message/i })).toHaveLength(49);
    expect(screen.getByText(/Showing 50 of 60 messages\. The selected message is included\./)).toBeTruthy();
  });
});
