import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataTaskReference, KataTaskReferenceResponse, KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
import IssuePickerDialog from "./IssuePickerDialog.svelte";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function fakeReference(overrides: Partial<KataTaskReference> = {}): KataTaskReference {
  return {
    uid: overrides.uid ?? "uid-100",
    short_id: overrides.short_id ?? "100",
    qualified_id: overrides.qualified_id ?? "Kata#100",
    reference: overrides.reference ?? "100",
    title: overrides.title ?? "Issue one hundred",
    project_id: overrides.project_id ?? 10,
    project_uid: overrides.project_uid ?? "project-kata",
    project_name: overrides.project_name ?? "Kata",
    status: overrides.status ?? "open",
  };
}

function searchResponse(references: KataTaskReference[]): KataTaskReferenceResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    generation: 7,
    invalidation_epoch: 2,
    fetched_at: "2026-05-18T00:00:00Z",
    references,
  };
}

function makeSearch(search?: KataTaskReferenceSearch): {
  searchReferences: KataTaskReferenceSearch;
  spy: ReturnType<typeof vi.fn>;
} {
  const fallback: KataTaskReferenceSearch = async () => searchResponse([]);
  const spy = vi.fn(search ?? fallback);
  return { searchReferences: spy, spy };
}

interface RenderOpts {
  open?: boolean;
  searchReferences?: KataTaskReferenceSearch;
  excludeUIDs?: ReadonlySet<string>;
  onPick?: (issue: KataTaskReference) => void;
  onClose?: () => void;
}

function renderDialog(opts: RenderOpts = {}) {
  const onPick = opts.onPick ?? vi.fn();
  const onClose = opts.onClose ?? vi.fn();
  const searchReferences = opts.searchReferences ?? makeSearch().searchReferences;
  const result = render(IssuePickerDialog, {
    props: {
      open: opts.open ?? true,
      searchReferences,
      onPick,
      onClose,
      ...(opts.excludeUIDs !== undefined ? { excludeUIDs: opts.excludeUIDs } : {}),
    },
  });
  return { ...result, onPick, onClose, searchReferences };
}

async function getSearchInput(): Promise<HTMLInputElement> {
  const existing = screen.queryByPlaceholderText(/Title or qualified ID/i);
  if (existing) return existing as HTMLInputElement;
  await fireEvent.click(screen.getByRole("button", { name: /Title or qualified ID/i }));
  return screen.getByPlaceholderText(/Title or qualified ID/i) as HTMLInputElement;
}

describe("IssuePickerDialog structure", () => {
  it("renders nothing when open=false", () => {
    renderDialog({ open: false });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByPlaceholderText(/Title or qualified ID/i)).toBeNull();
  });

  it("renders the task picker and empty-state hint when open", async () => {
    renderDialog();
    expect(screen.getByRole("dialog", { name: /Link to task/i })).toBeTruthy();
    expect(screen.getByText("Search tasks")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: /Title or qualified ID/i }));
    expect(await getSearchInput()).toBeTruthy();
    expect(screen.getByText(/Type to search open tasks/i)).toBeTruthy();
  });
});

describe("IssuePickerDialog debounce", () => {
  it("coalesces rapid keystrokes into one search with the latest query", async () => {
    vi.useFakeTimers();
    const { searchReferences, spy } = makeSearch(async () => searchResponse([fakeReference()]));
    renderDialog({ searchReferences });

    const input = await getSearchInput();
    await fireEvent.input(input, { target: { value: "a" } });
    await fireEvent.input(input, { target: { value: "ab" } });
    await fireEvent.input(input, { target: { value: "abc" } });

    expect(spy).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(250);
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy).toHaveBeenCalledWith("abc", { limit: 50 });
  });
});

describe("IssuePickerDialog results", () => {
  it("renders search results", async () => {
    vi.useFakeTimers();
    const issues = [
      fakeReference({ uid: "u100", reference: "100", qualified_id: "Kata#100", title: "First" }),
      fakeReference({ uid: "u101", short_id: "101", reference: "Kata#101", qualified_id: "Kata#101", title: "Second" }),
      fakeReference({ uid: "u102", reference: "102", qualified_id: "Kata#102", title: "Third" }),
    ];
    const { searchReferences } = makeSearch(async (_query, options) => searchResponse(issues.slice(0, options?.limit)));
    renderDialog({ searchReferences });

    await fireEvent.input(await getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => {
      expect(screen.getAllByRole("option")).toHaveLength(3);
    });
    expect(screen.getByRole("listbox", { name: /Title or qualified ID/i })).toBeTruthy();
    expect(screen.getByRole("option", { name: "100 First" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "Kata#101 Second" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "102 Third" })).toBeTruthy();
  });

  it("hides excluded results", async () => {
    vi.useFakeTimers();
    const issues = [
      fakeReference({ uid: "u100", reference: "100", title: "Keep me" }),
      fakeReference({ uid: "u101", reference: "101", title: "Hide me" }),
    ];
    const { searchReferences } = makeSearch(async () => searchResponse(issues));
    renderDialog({ searchReferences, excludeUIDs: new Set(["u101"]) });

    await fireEvent.input(await getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => {
      expect(screen.getAllByRole("option")).toHaveLength(1);
    });
    expect(screen.getByRole("option", { name: "100 Keep me" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: /101 Hide me/ })).toBeNull();
  });

  it("shows no matches when every result is excluded", async () => {
    vi.useFakeTimers();
    const issues = [fakeReference({ uid: "u100", title: "First" }), fakeReference({ uid: "u101", title: "Second" })];
    const { searchReferences } = makeSearch(async () => searchResponse(issues));
    renderDialog({ searchReferences, excludeUIDs: new Set(["u100", "u101"]) });

    await fireEvent.input(await getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => {
      expect(screen.getByText(/No matches/i)).toBeTruthy();
    });
    expect(screen.queryByRole("option")).toBeNull();
    expect(screen.getByRole("listbox", { name: /Title or qualified ID/i })).toBeTruthy();
  });

  it("discards stale slow results after a faster search wins", async () => {
    vi.useFakeTimers();
    let resolveFirst!: (value: KataTaskReferenceResponse) => void;
    const firstPending = new Promise<KataTaskReferenceResponse>((resolve) => {
      resolveFirst = resolve;
    });
    const firstIssues = [fakeReference({ uid: "u200", reference: "200", title: "Stale" })];
    const secondIssues = [fakeReference({ uid: "u300", reference: "300", title: "Fresh" })];
    let call = 0;
    const spy = vi.fn(async (): Promise<KataTaskReferenceResponse> => {
      call++;
      if (call === 1) return firstPending;
      return searchResponse(secondIssues);
    });
    renderDialog({ searchReferences: spy });

    await fireEvent.input(await getSearchInput(), { target: { value: "stale" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(spy).toHaveBeenCalledTimes(1);

    await fireEvent.input(await getSearchInput(), { target: { value: "fresh" } });
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
    renderDialog({ searchReferences: spy });

    const input = await getSearchInput();
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
      fakeReference({ uid: "u100", reference: "100", qualified_id: "Kata#100", title: "First" }),
      fakeReference({ uid: "u101", short_id: "101", reference: "Kata#101", qualified_id: "Kata#101", title: "Second" }),
    ];
    const { searchReferences } = makeSearch(async () => searchResponse(issues));
    const onPick = vi.fn();
    renderDialog({ searchReferences, onPick });

    await fireEvent.input(await getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);
    const row = await waitFor(() => screen.getByRole("option", { name: /Kata#101.*Second/i }));
    await fireEvent.mouseDown(row);

    expect(screen.queryByRole("combobox", { name: "Title or qualified ID..." })).toBeNull();
    const linkBtn = screen.getByRole("button", { name: /^Link$/ }) as HTMLButtonElement;
    expect(linkBtn.disabled).toBe(false);
    await fireEvent.click(linkBtn);

    expect(onPick).toHaveBeenCalledWith({
      uid: "u101",
      short_id: "101",
      qualified_id: "Kata#101",
      reference: "Kata#101",
      title: "Second",
      project_id: 10,
      project_uid: "project-kata",
      project_name: "Kata",
      status: "open",
    });
  });

  it("keeps Link disabled until a row is selected", async () => {
    vi.useFakeTimers();
    const { searchReferences } = makeSearch(async () => searchResponse([fakeReference()]));
    renderDialog({ searchReferences });

    await fireEvent.input(await getSearchInput(), { target: { value: "kata" } });
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
        ? searchResponse([fakeReference({ uid: "u100", reference: "100", title: "First" })])
        : searchResponse([fakeReference({ uid: "u200", reference: "200", title: "Different" })]);
    });
    renderDialog({ searchReferences: spy });

    await fireEvent.input(await getSearchInput(), { target: { value: "first" } });
    await vi.advanceTimersByTimeAsync(250);
    await fireEvent.mouseDown(await waitFor(() => screen.getByRole("option", { name: /100.*First/i })));

    const linkBtn = screen.getByRole("button", { name: /^Link$/ }) as HTMLButtonElement;
    expect(linkBtn.disabled).toBe(false);

    await fireEvent.input(await getSearchInput(), { target: { value: "diff" } });
    expect(linkBtn.disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(250);
    await waitFor(() => screen.getByText("200"));
    expect(linkBtn.disabled).toBe(true);
  });

  it("discards in-flight results when the query is cleared", async () => {
    vi.useFakeTimers();
    let resolvePending!: (value: KataTaskReferenceResponse) => void;
    const pending = new Promise<KataTaskReferenceResponse>((resolve) => {
      resolvePending = resolve;
    });
    const spy = vi.fn(async () => pending);
    renderDialog({ searchReferences: spy });

    const input = await getSearchInput();
    await fireEvent.input(input, { target: { value: "slow" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(spy).toHaveBeenCalledTimes(1);

    await fireEvent.input(input, { target: { value: "" } });
    expect(screen.getByText(/Type to search open tasks/i)).toBeTruthy();

    resolvePending(searchResponse([fakeReference({ uid: "u999", reference: "999", title: "Stale" })]));
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(screen.queryByText("Stale")).toBeNull();
    expect(screen.queryByText("Kata#999")).toBeNull();
  });

  it("filters excluded issues before applying the result cap", async () => {
    vi.useFakeTimers();
    const issues: KataTaskReference[] = [];
    for (let i = 1; i <= 25; i++) {
      issues.push(fakeReference({ uid: `u${i}`, reference: `${i}`, title: `Excluded ${i}` }));
    }
    issues.push(fakeReference({ uid: "u999", reference: "999", title: "Visible" }));
    const excludeUIDs = new Set<string>();
    for (let i = 1; i <= 25; i++) excludeUIDs.add(`u${i}`);

    const { searchReferences } = makeSearch(async (_query, options) => searchResponse(issues.slice(0, options?.limit)));
    renderDialog({ searchReferences, excludeUIDs });

    await fireEvent.input(await getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);

    await waitFor(() => expect(screen.getByRole("option", { name: "999 Visible" })).toBeTruthy());
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
    const issues = [fakeReference({ uid: "u100", reference: "100", title: "First" })];
    const { searchReferences } = makeSearch(async () => searchResponse(issues));
    const onPick = vi.fn();
    const onClose = vi.fn();
    const baseProps = { open: true, searchReferences, onPick, onClose };
    const { rerender } = render(IssuePickerDialog, { props: baseProps });

    await fireEvent.input(await getSearchInput(), { target: { value: "kata" } });
    await vi.advanceTimersByTimeAsync(250);
    await waitFor(() => screen.getByRole("option"));

    await rerender({ ...baseProps, open: false });
    expect(screen.queryByRole("dialog")).toBeNull();

    await rerender({ ...baseProps, open: true });
    expect((await getSearchInput()).value).toBe("");
    expect(screen.getByText(/Type to search open tasks/i)).toBeTruthy();
    expect(screen.queryByRole("option")).toBeNull();
  });
});
