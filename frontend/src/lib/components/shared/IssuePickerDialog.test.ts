import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { IssueSummary, KataAPI, SearchResponse } from "../../messages/types";
import IssuePickerDialog from "./IssuePickerDialog.svelte";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function fakeIssue(overrides: Partial<IssueSummary> = {}): IssueSummary {
  return {
    id: overrides.id ?? 100,
    uid: overrides.uid ?? "uid-100",
    short_id: overrides.short_id ?? "100",
    qualified_id: overrides.qualified_id ?? "Kata#100",
    title: overrides.title ?? "Issue one hundred",
    status: overrides.status ?? "open",
    metadata: overrides.metadata ?? {},
  };
}

function searchResponse(issues: IssueSummary[]): SearchResponse {
  return {
    filters: {
      scope: { kind: "all" },
      status: "open",
      owner: "",
      label: "",
      query: "",
    },
    issues,
    fetched_at: "2026-05-18T00:00:00Z",
  };
}

type KataSearchOnly = Pick<KataAPI, "search">;

function makeKata(search?: KataAPI["search"]): {
  kata: KataSearchOnly;
  spy: ReturnType<typeof vi.fn>;
} {
  const fallback: KataAPI["search"] = async () => searchResponse([]);
  const spy = vi.fn(search ?? fallback);
  return { kata: { search: spy }, spy };
}

interface RenderOpts {
  open?: boolean;
  kata?: KataSearchOnly;
  excludeIds?: ReadonlySet<number>;
  onPick?: (issue: { id: number; uid: string; qualified_id: string; title: string }) => void;
  onClose?: () => void;
}

function renderDialog(opts: RenderOpts = {}) {
  const onPick = opts.onPick ?? vi.fn();
  const onClose = opts.onClose ?? vi.fn();
  const kata = opts.kata ?? makeKata().kata;
  const result = render(IssuePickerDialog, {
    props: {
      open: opts.open ?? true,
      kata,
      onPick,
      onClose,
      ...(opts.excludeIds !== undefined ? { excludeIds: opts.excludeIds } : {}),
    },
  });
  return { ...result, onPick, onClose, kata };
}

function getSearchInput(): HTMLInputElement {
  return screen.getByPlaceholderText(/Title or qualified ID/i) as HTMLInputElement;
}

describe("IssuePickerDialog structure", () => {
  it("renders nothing when open=false", () => {
    renderDialog({ open: false });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByPlaceholderText(/Title or qualified ID/i)).toBeNull();
  });

  it("renders the search input and empty-state hint when open", () => {
    renderDialog();
    expect(screen.getByRole("dialog", { name: /Link to task/i })).toBeTruthy();
    expect(screen.getByLabelText(/Search tasks/i)).toBeTruthy();
    expect(getSearchInput()).toBeTruthy();
    expect(screen.getByText(/Type to search open tasks/i)).toBeTruthy();
  });
});

describe("IssuePickerDialog debounce", () => {
  it("coalesces rapid keystrokes into one search with the latest query", async () => {
    vi.useFakeTimers();
    const { kata, spy } = makeKata(async () => searchResponse([fakeIssue()]));
    renderDialog({ kata });

    const input = getSearchInput();
    await fireEvent.input(input, { target: { value: "a" } });
    await fireEvent.input(input, { target: { value: "ab" } });
    await fireEvent.input(input, { target: { value: "abc" } });

    expect(spy).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(250);
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy.mock.calls[0]?.[0]).toMatchObject({
      query: "abc",
      status: "open",
      scope: { kind: "all" },
    });
  });
});

describe("IssuePickerDialog results", () => {
  it("renders search results", async () => {
    vi.useFakeTimers();
    const issues = [
      fakeIssue({ id: 100, uid: "u100", qualified_id: "Kata#100", title: "First" }),
      fakeIssue({ id: 101, uid: "u101", qualified_id: "Kata#101", title: "Second" }),
      fakeIssue({ id: 102, uid: "u102", qualified_id: "Kata#102", title: "Third" }),
    ];
    const { kata } = makeKata(async () => searchResponse(issues));
    renderDialog({ kata });

    await fireEvent.input(getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => {
      expect(screen.getAllByRole("option")).toHaveLength(3);
    });
    expect(screen.getByRole("listbox", { name: /Matching tasks/i })).toBeTruthy();
    expect(screen.getByText("Kata#100")).toBeTruthy();
    expect(screen.getByText("Kata#101")).toBeTruthy();
    expect(screen.getByText("Kata#102")).toBeTruthy();
    expect(screen.getByText("First")).toBeTruthy();
  });

  it("hides excluded results", async () => {
    vi.useFakeTimers();
    const issues = [
      fakeIssue({ id: 100, uid: "u100", qualified_id: "Kata#100", title: "Keep me" }),
      fakeIssue({ id: 101, uid: "u101", qualified_id: "Kata#101", title: "Hide me" }),
    ];
    const { kata } = makeKata(async () => searchResponse(issues));
    renderDialog({ kata, excludeIds: new Set([101]) });

    await fireEvent.input(getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => {
      expect(screen.getAllByRole("option")).toHaveLength(1);
    });
    expect(screen.getByText("Kata#100")).toBeTruthy();
    expect(screen.queryByText("Kata#101")).toBeNull();
    expect(screen.queryByText("Hide me")).toBeNull();
  });

  it("shows no matches when every result is excluded", async () => {
    vi.useFakeTimers();
    const issues = [
      fakeIssue({ id: 100, uid: "u100", qualified_id: "Kata#100", title: "First" }),
      fakeIssue({ id: 101, uid: "u101", qualified_id: "Kata#101", title: "Second" }),
    ];
    const { kata } = makeKata(async () => searchResponse(issues));
    renderDialog({ kata, excludeIds: new Set([100, 101]) });

    await fireEvent.input(getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => {
      expect(screen.getByText(/No matches/i)).toBeTruthy();
    });
    expect(screen.queryByRole("option")).toBeNull();
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("discards stale slow results after a faster search wins", async () => {
    vi.useFakeTimers();
    let resolveFirst!: (value: SearchResponse) => void;
    const firstPending = new Promise<SearchResponse>((resolve) => {
      resolveFirst = resolve;
    });
    const firstIssues = [fakeIssue({ id: 200, uid: "u200", qualified_id: "Kata#200", title: "Stale" })];
    const secondIssues = [fakeIssue({ id: 300, uid: "u300", qualified_id: "Kata#300", title: "Fresh" })];
    let call = 0;
    const spy = vi.fn(async (): Promise<SearchResponse> => {
      call++;
      if (call === 1) return firstPending;
      return searchResponse(secondIssues);
    });
    const kata: KataSearchOnly = { search: spy };
    renderDialog({ kata });

    await fireEvent.input(getSearchInput(), { target: { value: "stale" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(spy).toHaveBeenCalledTimes(1);

    await fireEvent.input(getSearchInput(), { target: { value: "fresh" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(spy).toHaveBeenCalledTimes(2);
    await waitFor(() => screen.getByText("Fresh"));

    resolveFirst(searchResponse(firstIssues));
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(screen.queryByText("Stale")).toBeNull();
    expect(screen.getByText("Fresh")).toBeTruthy();
  });

  it("renders search errors as an alert and clears them with an empty query", async () => {
    vi.useFakeTimers();
    const spy = vi.fn().mockRejectedValueOnce(new Error("upstream down"));
    const kata: KataSearchOnly = { search: spy };
    renderDialog({ kata });

    const input = getSearchInput();
    await fireEvent.input(input, { target: { value: "anything" } });
    await vi.advanceTimersByTimeAsync(250);

    const alert = await waitFor(() => screen.getByRole("alert"));
    expect(alert.textContent).toContain("upstream down");

    await fireEvent.input(input, { target: { value: "" } });
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("IssuePickerDialog selection", () => {
  it("picks the selected issue", async () => {
    vi.useFakeTimers();
    const issues = [
      fakeIssue({ id: 100, uid: "u100", qualified_id: "Kata#100", title: "First" }),
      fakeIssue({ id: 101, uid: "u101", qualified_id: "Kata#101", title: "Second" }),
    ];
    const { kata } = makeKata(async () => searchResponse(issues));
    const onPick = vi.fn();
    renderDialog({ kata, onPick });

    await fireEvent.input(getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);
    const row = await waitFor(() => screen.getByRole("button", { name: /Kata#101.*Second/i }));
    await fireEvent.click(row);

    const linkBtn = screen.getByRole("button", { name: /^Link$/ }) as HTMLButtonElement;
    expect(linkBtn.disabled).toBe(false);
    await fireEvent.click(linkBtn);

    expect(onPick).toHaveBeenCalledWith({
      id: 101,
      uid: "u101",
      qualified_id: "Kata#101",
      title: "Second",
    });
  });

  it("keeps Link disabled until a row is selected", async () => {
    vi.useFakeTimers();
    const { kata } = makeKata(async () => searchResponse([fakeIssue()]));
    renderDialog({ kata });

    await fireEvent.input(getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => screen.getByRole("option"));
    const linkBtn = screen.getByRole("button", { name: /^Link$/ }) as HTMLButtonElement;
    expect(linkBtn.disabled).toBe(true);
  });

  it("clears a prior selection as soon as the query changes", async () => {
    vi.useFakeTimers();
    let call = 0;
    const spy = vi.fn(async () => {
      call++;
      return call === 1
        ? searchResponse([fakeIssue({ id: 100, uid: "u100", qualified_id: "Kata#100", title: "First" })])
        : searchResponse([fakeIssue({ id: 200, uid: "u200", qualified_id: "Kata#200", title: "Different" })]);
    });
    const kata: KataSearchOnly = { search: spy };
    renderDialog({ kata });

    await fireEvent.input(getSearchInput(), { target: { value: "first" } });
    await vi.advanceTimersByTimeAsync(250);
    await fireEvent.click(await waitFor(() => screen.getByRole("button", { name: /Kata#100.*First/i })));

    const linkBtn = screen.getByRole("button", { name: /^Link$/ }) as HTMLButtonElement;
    expect(linkBtn.disabled).toBe(false);

    await fireEvent.input(getSearchInput(), { target: { value: "diff" } });
    expect(linkBtn.disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(250);
    await waitFor(() => screen.getByText("Kata#200"));
    expect(linkBtn.disabled).toBe(true);
  });

  it("discards in-flight results when the query is cleared", async () => {
    vi.useFakeTimers();
    let resolvePending!: (value: SearchResponse) => void;
    const pending = new Promise<SearchResponse>((resolve) => {
      resolvePending = resolve;
    });
    const spy = vi.fn(async () => pending);
    const kata: KataSearchOnly = { search: spy };
    renderDialog({ kata });

    const input = getSearchInput();
    await fireEvent.input(input, { target: { value: "slow" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(spy).toHaveBeenCalledTimes(1);

    await fireEvent.input(input, { target: { value: "" } });
    expect(screen.getByText(/Type to search open tasks/i)).toBeTruthy();

    resolvePending(searchResponse([fakeIssue({ id: 999, uid: "u999", qualified_id: "Kata#999", title: "Stale" })]));
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(screen.queryByText("Stale")).toBeNull();
    expect(screen.queryByText("Kata#999")).toBeNull();
  });

  it("filters excluded issues before applying the result cap", async () => {
    vi.useFakeTimers();
    const issues: IssueSummary[] = [];
    for (let i = 1; i <= 25; i++) {
      issues.push(fakeIssue({ id: i, uid: `u${i}`, qualified_id: `Kata#${i}`, title: `Excluded ${i}` }));
    }
    issues.push(fakeIssue({ id: 999, uid: "u999", qualified_id: "Kata#999", title: "Visible" }));
    const excludeIds = new Set<number>();
    for (let i = 1; i <= 25; i++) excludeIds.add(i);

    const { kata } = makeKata(async () => searchResponse(issues));
    renderDialog({ kata, excludeIds });

    await fireEvent.input(getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => expect(screen.getByText("Kata#999")).toBeTruthy());
    expect(screen.getByText("Visible")).toBeTruthy();
  });
});

describe("IssuePickerDialog close and reset paths", () => {
  it("calls onClose from Cancel and Escape", async () => {
    const { onClose } = renderDialog();
    await fireEvent.click(screen.getByRole("button", { name: /Cancel/i }));
    expect(onClose).toHaveBeenCalledTimes(1);

    cleanup();
    const rendered = renderDialog();
    await fireEvent.keyDown(document, { key: "Escape" });
    expect(rendered.onClose).toHaveBeenCalledTimes(1);
  });

  it("clears query and results after closing and reopening", async () => {
    vi.useFakeTimers();
    const issues = [fakeIssue({ id: 100, uid: "u100", qualified_id: "Kata#100", title: "First" })];
    const { kata } = makeKata(async () => searchResponse(issues));
    const onPick = vi.fn();
    const onClose = vi.fn();
    const baseProps = { open: true, kata, onPick, onClose };
    const { rerender } = render(IssuePickerDialog, { props: baseProps });

    await fireEvent.input(getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);
    await waitFor(() => screen.getByRole("option"));

    await rerender({ ...baseProps, open: false });
    expect(screen.queryByRole("dialog")).toBeNull();

    await rerender({ ...baseProps, open: true });
    expect(getSearchInput().value).toBe("");
    expect(screen.getByText(/Type to search open tasks/i)).toBeTruthy();
    expect(screen.queryByRole("option")).toBeNull();
  });
});
