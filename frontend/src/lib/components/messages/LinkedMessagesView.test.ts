import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";

import type { LinkedMessageRow } from "../../messages/reverseLinks";
import LinkedMessagesView from "./LinkedMessagesView.svelte";

afterEach(cleanup);

function makeRow(messageId: number, subject: string, from: string, issueCount: number): LinkedMessageRow {
  return {
    message: {
      message_id: messageId,
      conversation_id: messageId,
      subject,
      from,
      sent_at: "2026-05-15T09:00:00Z",
      added_at: "2026-05-15T10:00:00Z",
    },
    issues: Array.from({ length: issueCount }, (_, i) => ({
      uid: `uid-${messageId}-${i}`,
      short_id: `${messageId}${i}`,
      qualified_id: `PROJ-${messageId}${i}`,
      title: `Issue ${messageId}-${i}`,
      status: "open" as const,
    })),
    most_recent_added_at: "2026-05-15T10:00:00Z",
  };
}

const oneRow = makeRow(1001, "Hello from Alice", "Alice <alice@example.com>", 2);

function renderView(
  opts: {
    rows?: LinkedMessageRow[] | null;
    loading?: boolean;
    error?: string | null;
    selectedMessageId?: number | null;
    onRefresh?: () => void;
    onSelectMessage?: (id: number) => void;
    onOpenIssue?: (uid: string) => void;
    MAX_ROWS?: number;
  } = {},
) {
  const onRefresh = opts.onRefresh ?? vi.fn();
  const onSelectMessage = opts.onSelectMessage ?? vi.fn();
  const onOpenIssue = opts.onOpenIssue ?? vi.fn();
  const result = render(LinkedMessagesView, {
    props: {
      rows: opts.rows !== undefined ? opts.rows : [],
      loading: opts.loading ?? false,
      error: opts.error ?? null,
      selectedMessageId: opts.selectedMessageId ?? null,
      onRefresh,
      onSelectMessage,
      onOpenIssue,
      ...(opts.MAX_ROWS !== undefined ? { MAX_ROWS: opts.MAX_ROWS } : {}),
    },
  });
  return { ...result, onRefresh, onSelectMessage, onOpenIssue };
}

describe("LinkedMessagesView loading state", () => {
  test("loading renders the loading state", () => {
    renderView({ rows: null, loading: true });

    const stateDiv = document.querySelector(".linked-state");
    expect(stateDiv).not.toBeNull();
    expect(stateDiv?.textContent).toContain("Loading...");
  });

  test("loading disables the refresh button", () => {
    renderView({ rows: null, loading: true });

    const btn = screen.getByRole("button", { name: /Loading/ }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });
});

describe("LinkedMessagesView empty state", () => {
  test("empty rows render the empty state", () => {
    renderView({ rows: [], loading: false });

    expect(screen.getByText("No linked messages yet.")).toBeTruthy();
  });
});

describe("LinkedMessagesView table", () => {
  test("one row renders subject, formatted sender, and issue chips", () => {
    renderView({ rows: [oneRow] });

    expect(screen.getByRole("columnheader", { name: "Linked tasks" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Hello from Alice" })).toBeTruthy();
    expect(screen.getByText("Alice")).toBeTruthy();
    const chips = document.querySelectorAll(".issue-chip");
    expect(chips).toHaveLength(2);
    expect(chips[0]?.textContent).toContain("PROJ-10010");
    expect(chips[1]?.textContent).toContain("PROJ-10011");
  });

  test("blank message subject renders a stable fallback label", () => {
    renderView({ rows: [makeRow(1002, "", "bob@example.com", 1)] });

    expect(screen.getByRole("button", { name: "(no subject)" })).toBeTruthy();
  });

  test("clicking a row subject selects the message", async () => {
    const onSelectMessage = vi.fn();
    renderView({ rows: [oneRow], onSelectMessage });

    await fireEvent.click(screen.getByRole("button", { name: "Hello from Alice" }));

    expect(onSelectMessage).toHaveBeenCalledOnce();
    expect(onSelectMessage).toHaveBeenCalledWith(1001);
  });

  test("clicking an issue chip opens the issue", async () => {
    const onOpenIssue = vi.fn();
    renderView({ rows: [oneRow], onOpenIssue });

    const chip = document.querySelector<HTMLButtonElement>(".issue-chip");
    expect(chip).not.toBeNull();
    await fireEvent.click(chip!);

    expect(onOpenIssue).toHaveBeenCalledOnce();
    expect(onOpenIssue).toHaveBeenCalledWith("uid-1001-0");
  });
});

describe("LinkedMessagesView refresh button", () => {
  test("refresh button fires onRefresh when not loading", async () => {
    const onRefresh = vi.fn();
    renderView({ rows: [], loading: false, onRefresh });

    await fireEvent.click(screen.getByRole("button", { name: "Refresh" }));

    expect(onRefresh).toHaveBeenCalledOnce();
  });

  test("refresh button is disabled while loading", () => {
    renderView({ rows: null, loading: true });

    const btn = document.querySelector<HTMLButtonElement>(".refresh");
    expect(btn?.disabled).toBe(true);
  });
});

describe("LinkedMessagesView error state", () => {
  test("non-null error renders role=alert with the message", () => {
    renderView({ rows: [], error: "Network timeout." });

    expect(screen.getByRole("alert").textContent).toContain("Network timeout.");
  });

  test("null error renders no alert", () => {
    renderView({ rows: [] });

    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("LinkedMessagesView failed first load", () => {
  test("rows=null, loading=false, error set shows recovery UI", () => {
    renderView({ rows: null, loading: false, error: "Initial load failed." });

    expect(screen.getByRole("alert").textContent).toContain("Initial load failed.");
    const btn = document.querySelector<HTMLButtonElement>(".refresh");
    expect(btn).toBeTruthy();
    expect(btn?.disabled).toBe(false);
    expect(document.querySelector(".linked-state")).toBeNull();
  });
});

describe("LinkedMessagesView truncation", () => {
  test("MAX_ROWS limits body rows and reports the visible count", () => {
    const rows = [
      makeRow(1, "Msg 1", "alice@example.com", 1),
      makeRow(2, "Msg 2", "bob@example.com", 1),
      makeRow(3, "Msg 3", "carol@example.com", 1),
      makeRow(4, "Msg 4", "alice@example.com", 1),
      makeRow(5, "Msg 5", "bob@example.com", 1),
    ];

    renderView({ rows, MAX_ROWS: 2 });

    expect(document.querySelectorAll("tbody tr")).toHaveLength(2);
    expect(screen.getByText(/Showing 2 of 5/)).toBeTruthy();
  });
});
