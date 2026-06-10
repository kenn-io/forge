import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";

import { QUICK_VIEWS, type SavedSearch } from "../../messages/savedSearches";
import MessagesSavedViews from "./MessagesSavedViews.svelte";

afterEach(cleanup);

interface Props {
  quickViews: typeof QUICK_VIEWS;
  savedSearches: SavedSearch[];
  currentQuery: string;
  onApply: (query: string) => void;
  onSave: (name: string, query: string) => void;
  onDelete: (name: string) => void;
}

function defaults(overrides: Partial<Props> = {}): Props {
  return {
    quickViews: QUICK_VIEWS,
    savedSearches: [],
    currentQuery: "",
    onApply: vi.fn(),
    onSave: vi.fn(),
    onDelete: vi.fn(),
    ...overrides,
  };
}

describe("MessagesSavedViews quick views", () => {
  test("renders the canned views", () => {
    render(MessagesSavedViews, { props: defaults() });

    expect(screen.getByRole("button", { name: "Inbox" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Has attachments" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Recent" })).toBeTruthy();
  });

  test("clicking a quick view applies its query", async () => {
    const props = defaults();
    render(MessagesSavedViews, { props });

    await fireEvent.click(screen.getByRole("button", { name: "Inbox" }));

    expect(props.onApply).toHaveBeenCalledWith("label:Inbox");
  });

  test("active highlight follows currentQuery via aria-pressed", () => {
    render(MessagesSavedViews, { props: defaults({ currentQuery: "label:Inbox" }) });

    expect(screen.getByRole("button", { name: "Inbox" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Has attachments" }).getAttribute("aria-pressed")).toBe("false");
  });
});

describe("MessagesSavedViews saved searches", () => {
  test("renders the empty state when there are no saved searches", () => {
    render(MessagesSavedViews, { props: defaults() });

    expect(screen.getByText("No saved searches yet.")).toBeTruthy();
  });

  test("renders saved entries with apply and delete affordances", async () => {
    const props = defaults({
      savedSearches: [{ name: "Boss messages", query: "from:boss@example.com" }],
    });
    render(MessagesSavedViews, { props });

    await fireEvent.click(screen.getByRole("button", { name: "Boss messages" }));
    expect(props.onApply).toHaveBeenCalledWith("from:boss@example.com");

    await fireEvent.click(screen.getByRole("button", { name: "Delete saved search Boss messages" }));
    expect(props.onDelete).toHaveBeenCalledWith("Boss messages");
  });

  test("save current search is disabled when the query is empty or whitespace", () => {
    render(MessagesSavedViews, { props: defaults({ currentQuery: "   " }) });

    const btn = screen.getByRole("button", { name: "Save current search" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });

  test("clicking save reveals an input prefilled with the current query", async () => {
    render(MessagesSavedViews, { props: defaults({ currentQuery: "label:Inbox" }) });

    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));

    const input = screen.getByRole("textbox", { name: "Saved search name" }) as HTMLInputElement;
    expect(input.value).toBe("label:Inbox");
  });

  test("Enter saves the raw draft name and current query", async () => {
    const props = defaults({ currentQuery: "from:boss@example.com" });
    render(MessagesSavedViews, { props });

    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    const input = screen.getByRole("textbox", { name: "Saved search name" });
    await fireEvent.input(input, { target: { value: "Boss messages" } });
    await fireEvent.keyDown(input, { key: "Enter" });

    expect(props.onSave).toHaveBeenCalledWith("Boss messages", "from:boss@example.com");
  });

  test("Escape cancels save without firing onSave and unmounts the input", async () => {
    const props = defaults({ currentQuery: "label:Inbox" });
    render(MessagesSavedViews, { props });

    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    const input = screen.getByRole("textbox", { name: "Saved search name" });
    await fireEvent.keyDown(input, { key: "Escape" });

    expect(props.onSave).not.toHaveBeenCalled();
    expect(screen.queryByRole("textbox", { name: "Saved search name" })).toBeNull();
  });
});
