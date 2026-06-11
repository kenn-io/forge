import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import MessagesFacets from "./MessagesFacets.svelte";
import type { MessagesAggregateRow } from "../../api/messages/types";

afterEach(cleanup);

// Synthetic fixtures only - no real message source data.
function row(key: string, count: number): MessagesAggregateRow {
  return {
    key,
    count,
    total_size: count * 1024,
    attachment_count: 0,
    attachment_size: 0,
  };
}

const SENDERS: MessagesAggregateRow[] = [
  row("alice@example.com", 12),
  row("bob@example.com", 8),
  row("carol@example.com", 3),
];

const LABELS: MessagesAggregateRow[] = [row("Inbox", 25), row("Work", 10), row("Personal", 4)];

const DOMAINS: MessagesAggregateRow[] = [row("example.com", 39)];

function loaded(rows: readonly MessagesAggregateRow[]) {
  return { rows };
}

function renderFacets(
  opts: {
    senders?: { rows: readonly MessagesAggregateRow[] | null };
    labels?: { rows: readonly MessagesAggregateRow[] | null };
    domains?: { rows: readonly MessagesAggregateRow[] | null };
    error?: string | null;
    onSelectFacet?: (token: string) => void;
    showLinkedView?: boolean;
    activeView?: "linked" | null;
    onSelectView?: (view: "linked" | null) => void;
  } = {},
) {
  const onSelectFacet = opts.onSelectFacet ?? vi.fn();
  const onSelectView = opts.onSelectView ?? vi.fn();
  const result = render(MessagesFacets, {
    props: {
      senders: opts.senders ?? loaded(SENDERS),
      labels: opts.labels ?? loaded(LABELS),
      domains: opts.domains ?? loaded(DOMAINS),
      error: opts.error ?? null,
      onSelectFacet,
      showLinkedView: opts.showLinkedView,
      activeView: opts.activeView,
      onSelectView,
    },
  });
  return { ...result, onSelectFacet, onSelectView };
}

// ---------------------------------------------------------------- structure

describe("MessagesFacets - structure", () => {
  test("renders a single nav with the 'Messages facets' aria-label", () => {
    renderFacets();
    expect(screen.getByRole("navigation", { name: "Messages facets" })).toBeTruthy();
  });

  test("renders Senders, Labels, and Domains section headers", () => {
    renderFacets();
    expect(screen.getByRole("heading", { name: "Senders" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Labels" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Domains" })).toBeTruthy();
  });
});

// ---------------------------------------------------------------- token format per category

describe("MessagesFacets - token format per category", () => {
  test("clicking a sender row fires onSelectFacet with from:<key>", async () => {
    const onSelectFacet = vi.fn();
    renderFacets({ onSelectFacet });
    const btn = screen.getByRole("button", { name: /alice@example\.com/ });
    await fireEvent.click(btn);
    expect(onSelectFacet).toHaveBeenCalledOnce();
    expect(onSelectFacet).toHaveBeenCalledWith("from:alice@example.com");
  });

  test("clicking a label row fires onSelectFacet with label:<key>", async () => {
    const onSelectFacet = vi.fn();
    renderFacets({ onSelectFacet });
    const btn = screen.getByRole("button", { name: /Inbox/ });
    await fireEvent.click(btn);
    expect(onSelectFacet).toHaveBeenCalledOnce();
    expect(onSelectFacet).toHaveBeenCalledWith("label:Inbox");
  });

  test("clicking a domain row fires onSelectFacet with domain:<key>", async () => {
    const onSelectFacet = vi.fn();
    // Use a domain-only fixture so the regex doesn't also match the
    // alice@example.com sender row.
    renderFacets({
      onSelectFacet,
      senders: loaded([]),
      labels: loaded([]),
    });
    const btn = screen.getByRole("button", { name: /example\.com/ });
    await fireEvent.click(btn);
    expect(onSelectFacet).toHaveBeenCalledOnce();
    expect(onSelectFacet).toHaveBeenCalledWith("domain:example.com");
  });

  test("each row shows the key and count", () => {
    renderFacets({ senders: loaded([row("alice@example.com", 12)]) });
    const btn = screen.getByRole("button", { name: /alice@example\.com/ });
    expect(btn.textContent).toContain("alice@example.com");
    expect(btn.textContent).toContain("12");
  });
});

// ---------------------------------------------------------------- top-20 cap

describe("MessagesFacets - top-20 cap", () => {
  test("renders at most 20 sender rows even if 50 are passed", () => {
    const fifty = Array.from({ length: 50 }, (_, i) => row(`sender${i}@example.com`, 50 - i));
    renderFacets({
      senders: loaded(fifty),
      labels: loaded([]),
      domains: loaded([]),
    });
    const senderSection = screen.getByRole("heading", { name: "Senders" }).closest("section");
    expect(senderSection).not.toBeNull();
    const buttons = senderSection!.querySelectorAll("button");
    expect(buttons).toHaveLength(20);
    // First 20 by passed order should be rendered (component doesn't re-sort).
    expect(buttons[0]!.textContent).toContain("sender0@example.com");
    expect(buttons[19]!.textContent).toContain("sender19@example.com");
    // Row 20 (the 21st) should NOT appear.
    expect(senderSection!.textContent ?? "").not.toContain("sender20@example.com");
  });

  test("cap applies independently to labels and domains", () => {
    const fiftyLabels = Array.from({ length: 50 }, (_, i) => row(`label${i}`, 1));
    const fiftyDomains = Array.from({ length: 50 }, (_, i) => row(`d${i}.example.com`, 1));
    renderFacets({
      senders: loaded([]),
      labels: loaded(fiftyLabels),
      domains: loaded(fiftyDomains),
    });
    const labelSection = screen.getByRole("heading", { name: "Labels" }).closest("section");
    const domainSection = screen.getByRole("heading", { name: "Domains" }).closest("section");
    expect(labelSection!.querySelectorAll("button")).toHaveLength(20);
    expect(domainSection!.querySelectorAll("button")).toHaveLength(20);
  });
});

// ---------------------------------------------------------------- loading state

describe("MessagesFacets - loading state", () => {
  test("rows: null renders skeleton rows with aria-busy", () => {
    renderFacets({
      senders: { rows: null },
      labels: { rows: null },
      domains: { rows: null },
    });
    const busyLists = document.querySelectorAll("ul[aria-busy='true']");
    expect(busyLists).toHaveLength(3);
    // No interactive buttons in pure loading state.
    expect(document.querySelectorAll("button")).toHaveLength(0);
    // Skeleton placeholder <li>s are present.
    expect(document.querySelectorAll("li.skel").length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------- empty state

describe("MessagesFacets - empty state", () => {
  test("rows: [] for senders shows 'No senders.'", () => {
    renderFacets({ senders: loaded([]) });
    expect(screen.getByText("No senders.")).toBeTruthy();
  });

  test("rows: [] for labels shows 'No labels.'", () => {
    renderFacets({ labels: loaded([]) });
    expect(screen.getByText("No labels.")).toBeTruthy();
  });

  test("rows: [] for domains shows 'No domains.'", () => {
    renderFacets({ domains: loaded([]) });
    expect(screen.getByText("No domains.")).toBeTruthy();
  });
});

// ---------------------------------------------------------------- error state

describe("MessagesFacets - error state", () => {
  test("non-null error renders an alert with the message", () => {
    renderFacets({ error: "Failed to load facets." });
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("Failed to load facets.");
  });

  test("null error renders no alert", () => {
    renderFacets({ error: null });
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

// ---------------------------------------------------------------- views toggle

describe("MessagesFacets - Views toggle", () => {
  test("showLinkedView=true renders 'Search results' and 'Linked messages' items", () => {
    renderFacets({ showLinkedView: true });
    expect(screen.getByRole("button", { name: "Search results" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Linked messages" })).toBeTruthy();
  });

  test("showLinkedView=false renders no Views section", () => {
    renderFacets({ showLinkedView: false });
    expect(screen.queryByRole("button", { name: "Search results" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Linked messages" })).toBeNull();
  });

  test("showLinkedView omitted renders no Views section", () => {
    renderFacets();
    expect(screen.queryByRole("button", { name: "Search results" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Linked messages" })).toBeNull();
  });

  test("clicking 'Linked messages' fires onSelectView('linked')", async () => {
    const onSelectView = vi.fn();
    renderFacets({ showLinkedView: true, onSelectView });
    await fireEvent.click(screen.getByRole("button", { name: "Linked messages" }));
    expect(onSelectView).toHaveBeenCalledOnce();
    expect(onSelectView).toHaveBeenCalledWith("linked");
  });

  test("clicking 'Search results' fires onSelectView(null)", async () => {
    const onSelectView = vi.fn();
    renderFacets({ showLinkedView: true, activeView: "linked", onSelectView });
    await fireEvent.click(screen.getByRole("button", { name: "Search results" }));
    expect(onSelectView).toHaveBeenCalledOnce();
    expect(onSelectView).toHaveBeenCalledWith(null);
  });

  test("activeView=null: 'Search results' button has active class", () => {
    renderFacets({ showLinkedView: true, activeView: null });
    const btn = screen.getByRole("button", { name: "Search results" });
    expect(btn.classList.contains("active")).toBe(true);
    expect(screen.getByRole("button", { name: "Linked messages" }).classList.contains("active")).toBe(false);
  });

  test("activeView='linked': 'Linked messages' button has active class", () => {
    renderFacets({ showLinkedView: true, activeView: "linked" });
    const btn = screen.getByRole("button", { name: "Linked messages" });
    expect(btn.classList.contains("active")).toBe(true);
    expect(screen.getByRole("button", { name: "Search results" }).classList.contains("active")).toBe(false);
  });

  test("activeView omitted (undefined): 'Search results' button has active class", () => {
    renderFacets({ showLinkedView: true });
    const btn = screen.getByRole("button", { name: "Search results" });
    expect(btn.classList.contains("active")).toBe(true);
  });
});

// ---------------------------------------------------------------- mixed state

describe("MessagesFacets - mixed state", () => {
  test("renders each category's state independently", () => {
    renderFacets({
      senders: loaded(SENDERS), // loaded with rows
      labels: { rows: null }, // still loading
      domains: loaded([]), // empty
    });
    // Senders: real button visible.
    expect(screen.getByRole("button", { name: /alice@example\.com/ })).toBeTruthy();
    // Labels: skeleton list present, no Inbox button.
    expect(document.querySelectorAll("ul[aria-busy='true']")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: /Inbox/ })).toBeNull();
    // Domains: empty-state copy.
    expect(screen.getByText("No domains.")).toBeTruthy();
  });
});
