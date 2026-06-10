import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import MessagesList from "./MessagesList.svelte";
import type { MessageSummary } from "../../api/messages/types";

afterEach(cleanup);

// Synthetic fixtures - no real message source data.
function makeMsg(overrides: Partial<MessageSummary> & Pick<MessageSummary, "id">): MessageSummary {
  return {
    conversation_id: overrides.id,
    subject: `Subject ${overrides.id}`,
    from: "alice@example.com",
    to: ["bob@example.com"],
    cc: [],
    bcc: [],
    sent_at: "2026-05-15T09:00:00Z",
    snippet: `Snippet for message ${overrides.id}.`,
    labels: ["Inbox"],
    has_attachments: false,
    size_bytes: 1024,
    deleted_at: null,
    ...overrides,
  };
}

const MSG_A = makeMsg({ id: 1, subject: "Alpha message" });
const MSG_B = makeMsg({ id: 2, subject: "Beta message" });
const MSG_C = makeMsg({ id: 3, subject: "Gamma message" });

const THREE_MSGS = [MSG_A, MSG_B, MSG_C];

function renderList(
  messages: readonly MessageSummary[] = THREE_MSGS,
  opts: {
    selectedID?: number | null;
    loading?: boolean;
    onSelect?: (id: number) => void;
  } = {},
) {
  const onSelect = opts.onSelect ?? vi.fn();
  const result = render(MessagesList, {
    props: {
      messages,
      selectedID: opts.selectedID ?? null,
      loading: opts.loading ?? false,
      onSelect,
    },
  });
  return { ...result, onSelect };
}

function getRows(): HTMLButtonElement[] {
  return Array.from(document.querySelectorAll<HTMLButtonElement>("button.row"));
}

/** Fire a keydown on the currently-focused element (or a given target). */
function keydown(key: string, target?: Element, extra: Partial<KeyboardEventInit> = {}) {
  const el = target ?? document.activeElement ?? document.body;
  return fireEvent.keyDown(el, { key, ...extra });
}

// ------------------------------------------------------------------ loading state

describe("MessagesList - loading state", () => {
  test("shows skeleton rows, no message rows when loading=true", () => {
    renderList([], { loading: true });
    const skeletons = document.querySelectorAll(".skeleton-row");
    expect(skeletons.length).toBeGreaterThanOrEqual(3);
    expect(getRows()).toHaveLength(0);
  });
});

// ------------------------------------------------------------------ empty state

describe("MessagesList - empty state", () => {
  test("shows empty message when messages is empty and not loading", () => {
    renderList([], { loading: false });
    expect(screen.getByText("No messages match your search.")).toBeTruthy();
    expect(getRows()).toHaveLength(0);
  });
});

// ------------------------------------------------------------------ row rendering

describe("MessagesList - row rendering", () => {
  test("renders one row per message", () => {
    renderList();
    expect(getRows()).toHaveLength(3);
  });

  test("each row contains sender, subject, and snippet text", () => {
    renderList([MSG_A]);
    const row = getRows()[0]!;
    expect(row.textContent).toContain("alice@example.com");
    expect(row.textContent).toContain("Alpha message");
    expect(row.textContent).toContain("Snippet for message 1.");
  });

  test("paperclip icon appears only when has_attachments is true", () => {
    const withClip = makeMsg({ id: 10, has_attachments: true });
    const withoutClip = makeMsg({ id: 11, has_attachments: false });
    renderList([withClip, withoutClip]);
    const allRows = getRows();
    const rowClip = allRows[0]!;
    const rowNone = allRows[1]!;
    expect(rowClip.querySelector('[aria-label="Has attachments"]')).toBeTruthy();
    expect(rowNone.querySelector('[aria-label="Has attachments"]')).toBeNull();
  });

  test("selected row has aria-current=true", () => {
    renderList(THREE_MSGS, { selectedID: 2 });
    const allRows = getRows();
    expect(allRows[1]!.getAttribute("aria-current")).toBe("true");
    expect(allRows[0]!.getAttribute("aria-current")).toBeNull();
  });
});

// ------------------------------------------------------------------ click selection

describe("MessagesList - click selection", () => {
  test("clicking a row fires onSelect with that message id", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    await fireEvent.click(allRows[1]!);
    expect(onSelect).toHaveBeenCalledOnce();
    expect(onSelect).toHaveBeenCalledWith(2);
  });
});

// ------------------------------------------------------------------ keyboard navigation

describe("MessagesList - keyboard navigation", () => {
  test("j moves focus down and auto-selects the new row", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[0]!.focus();

    keydown("j", allRows[0]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[1]);
    expect(onSelect).toHaveBeenCalledWith(2);
  });

  test("ArrowDown moves focus down and auto-selects the new row", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[0]!.focus();

    keydown("ArrowDown", allRows[0]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[1]);
    expect(onSelect).toHaveBeenCalledWith(2);
  });

  test("k moves focus up and auto-selects the new row", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[1]!.focus();

    keydown("k", allRows[1]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[0]);
    expect(onSelect).toHaveBeenCalledWith(1);
  });

  test("ArrowUp moves focus up and auto-selects the new row", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[2]!.focus();

    keydown("ArrowUp", allRows[2]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[1]);
    expect(onSelect).toHaveBeenCalledWith(2);
  });

  test("Home jumps to first row and auto-selects it", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[2]!.focus();

    keydown("Home", allRows[2]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[0]);
    expect(onSelect).toHaveBeenCalledWith(1);
  });

  test("End jumps to last row and auto-selects it", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[0]!.focus();

    keydown("End", allRows[0]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[2]);
    expect(onSelect).toHaveBeenCalledWith(3);
  });

  test("j on last row stays put and does not fire onSelect", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[2]!.focus();

    keydown("j", allRows[2]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[2]);
    expect(onSelect).not.toHaveBeenCalled();
  });

  test("k on first row stays put and does not fire onSelect", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[0]!.focus();

    keydown("k", allRows[0]!);
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[0]);
    expect(onSelect).not.toHaveBeenCalled();
  });

  test("Enter on focused row fires onSelect with current id (idempotent)", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[1]!.focus();

    keydown("Enter", allRows[1]!);
    await Promise.resolve();

    expect(onSelect).toHaveBeenCalledWith(2);
  });

  test("meta+j does not trigger navigation", async () => {
    const onSelect = vi.fn();
    renderList(THREE_MSGS, { onSelect });
    const allRows = getRows();
    allRows[0]!.focus();

    keydown("j", allRows[0]!, { metaKey: true });
    await Promise.resolve();

    expect(document.activeElement).toBe(allRows[0]);
    expect(onSelect).not.toHaveBeenCalled();
  });
});
