import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type {
  MessageDetailData,
  MessagesAggregateResponse,
  MessagesAPI,
  MessagesCapabilities,
  MessageSummary,
  MessagesSearchResult,
} from "../../api/messages/types";
import type { SavedSearchesAPI, SavedSearchesAPIError } from "../../api/messages/savedSearchesClient";
import type { SavedSearch } from "../../messages/savedSearches";
import { defaultMessagesRoute, type MessagesRoute } from "../../messages/route";
import type { MessageLinkInput } from "../../messages/messageLinks";
import type { IssueFilters, IssueSummary, KataAPI, SearchResponse } from "../../messages/types";
import MessagesWorkspace from "./MessagesWorkspace.svelte";

const LAYOUT_KEY = "middleman:messagesLayout/v1";
const SAVED_SEARCHES_KEY = "middleman:messagesSavedSearches/v1";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  localStorage.clear();
});

function makeCapabilities(overrides: Partial<MessagesCapabilities> = {}): MessagesCapabilities {
  return {
    configured: true,
    ok: true,
    status: "ok",
    modes: ["fts"],
    features: {
      threads_endpoint: true,
      mutations: false,
      attachments_download: false,
      sse_events: false,
    },
    ...overrides,
  };
}

function makeMessage(id: number, overrides: Partial<MessageSummary> = {}): MessageSummary {
  return {
    id,
    conversation_id: id,
    subject: `Subject ${id}`,
    from: `sender${id}@example.com`,
    to: ["bob@example.com"],
    cc: [],
    bcc: [],
    sent_at: "2026-05-15T09:00:00Z",
    snippet: `Snippet ${id}.`,
    labels: [],
    has_attachments: false,
    size_bytes: 1024,
    deleted_at: null,
    ...overrides,
  };
}

function makeDetail(id: number, overrides: Partial<MessageDetailData> = {}): MessageDetailData {
  return {
    ...makeMessage(id),
    body: `Body ${id}`,
    attachments: [],
    ...overrides,
  };
}

function makeSearchResult(messages: MessageSummary[]): MessagesSearchResult {
  return {
    query: "",
    mode: "fts",
    total: messages.length,
    page: 1,
    page_size: 25,
    paginatable: false,
    messages,
  };
}

function makeMessagesApi(overrides: Partial<MessagesAPI> = {}): MessagesAPI {
  return {
    capabilities: async () => makeCapabilities(),
    search: async () => makeSearchResult([]),
    message: async () => {
      throw new Error("message fixture not implemented");
    },
    inlineURL: () => "",
    remoteImageURL: () => "",
    aggregates: async (view) => ({ view_type: view, rows: [] }),
    thread: async () => [],
    configure: async () => makeCapabilities(),
    ...overrides,
  };
}

function makeSavedSearchesAPI(overrides: Partial<SavedSearchesAPI> = {}): SavedSearchesAPI {
  return {
    list: async () => ({ searches: [], etag: '"sha256:empty"' }),
    replace: async (searches: SavedSearch[]) => ({ searches, etag: '"sha256:after"' }),
    ...overrides,
  };
}

function makeStaleETagError(): SavedSearchesAPIError {
  const err = new Error("stale etag") as SavedSearchesAPIError;
  err.status = 412;
  err.code = "conflict";
  err.reason = "stale_etag";
  return err;
}

function renderWorkspace(
  options: {
    capabilities?: MessagesCapabilities;
    route?: MessagesRoute;
    onRouteChange?: (next: MessagesRoute) => void;
    messagesApi?: MessagesAPI;
    savedSearchesApi?: SavedSearchesAPI;
    messagesConfigVersion?: number;
    kata?: Pick<KataAPI, "search">;
    onLinkMessage?: (issueUid: string, input: MessageLinkInput) => Promise<{ qualified_id: string }>;
    onOpenIssue?: (uid: string) => void;
  } = {},
) {
  return render(MessagesWorkspace, {
    props: {
      messagesApi: options.messagesApi ?? makeMessagesApi(),
      savedSearchesApi: options.savedSearchesApi ?? makeSavedSearchesAPI(),
      capabilities: options.capabilities ?? makeCapabilities(),
      route: options.route ?? defaultMessagesRoute,
      onRouteChange: options.onRouteChange ?? (() => {}),
      messagesConfigVersion: options.messagesConfigVersion ?? 0,
      kata: options.kata,
      onLinkMessage: options.onLinkMessage,
      onOpenIssue: options.onOpenIssue,
    },
  });
}

function aggregateResponse(
  view: "senders" | "labels" | "domains",
  rows: { key: string; count: number }[],
): MessagesAggregateResponse {
  return {
    view_type: view,
    rows: rows.map((row) => ({
      key: row.key,
      count: row.count,
      total_size: row.count * 1024,
      attachment_count: 0,
      attachment_size: 0,
    })),
  };
}

function fakeAggregates(): MessagesAPI["aggregates"] {
  return vi.fn(async (view) => {
    if (view === "senders") {
      return aggregateResponse("senders", [
        { key: "alice@example.com", count: 4 },
        { key: "bob@example.com", count: 2 },
      ]);
    }
    if (view === "labels") return aggregateResponse("labels", [{ key: "Inbox", count: 6 }]);
    return aggregateResponse("domains", [{ key: "example.com", count: 6 }]);
  }) as unknown as MessagesAPI["aggregates"];
}

function focusedRouteFor(id: number): MessagesRoute {
  return { ...defaultMessagesRoute, message: String(id) };
}

describe("MessagesWorkspace status and routing", () => {
  it("status ok renders search, list, and detail empty state without a banner", () => {
    renderWorkspace();

    expect(screen.getByRole("search", { name: "Search messages" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Messages" })).toBeTruthy();
    expect(screen.getByText("Select a message to read it.")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("status misconfigured renders the banner only", () => {
    renderWorkspace({ capabilities: makeCapabilities({ status: "misconfigured", ok: false }) });

    expect(screen.getByRole("alert").textContent).toContain("Messages are misconfigured");
    expect(screen.queryByRole("search", { name: "Search messages" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Messages" })).toBeNull();
    expect(screen.queryByText("Select a message to read it.")).toBeNull();
  });

  it("status down renders the unavailable banner only", () => {
    renderWorkspace({ capabilities: makeCapabilities({ status: "down", ok: false }) });

    expect(screen.getByRole("alert").textContent).toContain("Messages unavailable");
    expect(screen.queryByRole("search", { name: "Search messages" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Messages" })).toBeNull();
  });

  it("status unauthorized renders the key rejection banner only", () => {
    renderWorkspace({ capabilities: makeCapabilities({ status: "unauthorized", ok: false }) });

    expect(screen.getByRole("alert").textContent).toContain("Messages key rejected");
    expect(screen.queryByRole("search", { name: "Search messages" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Messages" })).toBeNull();
  });

  it("status undefined hides both banner and workspace shell", () => {
    const capabilities = makeCapabilities({ configured: false, ok: false });
    delete capabilities.status;
    renderWorkspace({ capabilities });

    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("search", { name: "Search messages" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Messages" })).toBeNull();
  });

  it("misconfigured status detail is rendered in the banner copy", () => {
    renderWorkspace({
      capabilities: makeCapabilities({
        status: "misconfigured",
        ok: false,
        status_detail: "api_key_env is unset",
      }),
    });

    expect(screen.getByRole("alert").textContent).toContain("api_key_env is unset");
  });

  it("search submit calls onRouteChange with the trimmed query", async () => {
    const onRouteChange = vi.fn();
    renderWorkspace({ onRouteChange });

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "  hello  " } });
    await fireEvent.submit(screen.getByRole("search", { name: "Search messages" }));

    expect(onRouteChange).toHaveBeenCalledOnce();
    expect(onRouteChange).toHaveBeenCalledWith(expect.objectContaining({ mode: "messages", q: "hello" }));
  });

  it("search submit clears focused message and linked view", async () => {
    const onRouteChange = vi.fn();
    renderWorkspace({
      route: { mode: "messages", q: "old", message: "1001", view: "linked" },
      onRouteChange,
    });

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "new" } });
    await fireEvent.submit(screen.getByRole("search", { name: "Search messages" }));

    expect(onRouteChange).toHaveBeenCalledWith(expect.objectContaining({ q: "new", message: null, view: undefined }));
  });

  it("empty search submit with no prior query is a no-op", async () => {
    const onRouteChange = vi.fn();
    renderWorkspace({ onRouteChange });

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "   " } });
    await fireEvent.submit(screen.getByRole("search", { name: "Search messages" }));

    expect(onRouteChange).not.toHaveBeenCalled();
  });

  it("empty search submit clears a prior route query", async () => {
    const onRouteChange = vi.fn();
    renderWorkspace({ route: { mode: "messages", q: "previous", message: null }, onRouteChange });

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "" } });
    await fireEvent.submit(screen.getByRole("search", { name: "Search messages" }));

    expect(onRouteChange).toHaveBeenCalledWith(expect.objectContaining({ q: null, message: null }));
  });

  it("route query populates the search input on mount", () => {
    renderWorkspace({ route: { mode: "messages", q: "prepopulated query", message: null } });

    expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe("prepopulated query");
  });

  it("pane size persists to localStorage and is restored on remount", async () => {
    localStorage.setItem(LAYOUT_KEY, "480");

    const { container, unmount } = renderWorkspace();
    const listPane = container.querySelector(".messages-pane-list") as HTMLElement | null;
    expect(listPane?.style.flexBasis).toBe("480px");

    await fireEvent.keyDown(screen.getByRole("button", { name: "Resize messages message list" }), {
      key: "ArrowRight",
    });
    expect(listPane?.style.flexBasis).toBe("504px");
    expect(localStorage.getItem(LAYOUT_KEY)).toBe("504");

    unmount();

    const remounted = renderWorkspace();
    const remountedListPane = remounted.container.querySelector(".messages-pane-list") as HTMLElement | null;

    expect(remountedListPane?.style.flexBasis).toBe("504px");
  });
});

describe("MessagesWorkspace search and detail effects", () => {
  it("searches the empty query on mount", async () => {
    const search = vi.fn(async () => makeSearchResult([]));
    renderWorkspace({ messagesApi: makeMessagesApi({ search }) });

    await waitFor(() => expect(search).toHaveBeenCalledOnce());
    expect(search).toHaveBeenCalledWith("", expect.objectContaining({ mode: "fts" }));
  });

  it("searches with the route query on mount", async () => {
    const search = vi.fn(async () => makeSearchResult([]));
    renderWorkspace({
      route: { mode: "messages", q: "project", message: null },
      messagesApi: makeMessagesApi({ search }),
    });

    await waitFor(() => expect(search).toHaveBeenCalledOnce());
    expect(search).toHaveBeenCalledWith("project", expect.objectContaining({ mode: "fts" }));
  });

  it("uses the first advertised search mode", async () => {
    const search = vi.fn(async () => makeSearchResult([]));
    renderWorkspace({
      capabilities: makeCapabilities({ modes: ["vector"] }),
      messagesApi: makeMessagesApi({ search }),
    });

    await waitFor(() => expect(search).toHaveBeenCalledOnce());
    expect(search).toHaveBeenCalledWith("", expect.objectContaining({ mode: "vector" }));
  });

  it("renders messages returned by search", async () => {
    const search = vi.fn(async () => makeSearchResult([makeMessage(10), makeMessage(11)]));
    renderWorkspace({ messagesApi: makeMessagesApi({ search }) });

    await waitFor(() => expect(document.querySelectorAll("button.row")).toHaveLength(2));
    expect(document.body.textContent).toContain("sender10@example.com");
    expect(document.body.textContent).toContain("Subject 11");
  });

  it("renders the empty list state when search returns no messages", async () => {
    renderWorkspace({
      messagesApi: makeMessagesApi({ search: async () => makeSearchResult([]) }),
    });

    await waitFor(() => expect(screen.getByText("No messages match your search.")).toBeTruthy());
  });

  it("selecting a row writes the message id to the route", async () => {
    const search = vi.fn(async () => makeSearchResult([makeMessage(55), makeMessage(56)]));
    const onRouteChange = vi.fn();
    renderWorkspace({ messagesApi: makeMessagesApi({ search }), onRouteChange });

    await waitFor(() => expect(document.querySelectorAll("button.row")).toHaveLength(2));
    await fireEvent.click(document.querySelectorAll<HTMLButtonElement>("button.row")[1]!);

    expect(onRouteChange).toHaveBeenCalledWith(expect.objectContaining({ message: "56" }));
  });

  it("non-integer route message does not call message detail", async () => {
    const search = vi.fn(async () => makeSearchResult([makeMessage(77)]));
    const message = vi.fn();
    renderWorkspace({
      route: { mode: "messages", q: null, message: "not-a-number" },
      messagesApi: makeMessagesApi({ search, message }),
    });

    await waitFor(() => expect(search).toHaveBeenCalledOnce());
    expect(message).not.toHaveBeenCalled();
    expect(screen.getByText("Select a message to read it.")).toBeTruthy();
    const row = document.querySelector<HTMLButtonElement>("button.row");
    expect(row).not.toBeNull();
    expect(row?.getAttribute("aria-current")).toBeNull();
  });

  it("valid route message fetches and renders detail", async () => {
    const message = vi.fn(async () => makeDetail(99, { subject: "Hello world", body: "Body text here." }));
    renderWorkspace({
      route: { mode: "messages", q: null, message: "99" },
      messagesApi: makeMessagesApi({ message }),
    });

    await waitFor(() => expect(message).toHaveBeenCalledWith(99));
    await waitFor(() => expect(screen.getByText("Hello world")).toBeTruthy());
  });

  it("search failure renders an error alert in the list pane", async () => {
    renderWorkspace({
      messagesApi: makeMessagesApi({
        search: async () => {
          throw new Error("network timeout");
        },
      }),
    });

    await waitFor(() => {
      const alert = screen.queryByRole("alert");
      expect(alert).toBeTruthy();
      expect(alert!.textContent).toContain("network timeout");
    });
  });

  it("banner-only states do not search or fetch detail", async () => {
    const search = vi.fn(async () => makeSearchResult([]));
    const message = vi.fn(async () => makeDetail(42));
    const messagesApi = makeMessagesApi({ search, message });

    for (const status of ["misconfigured", "down", "unauthorized"] as const) {
      cleanup();
      renderWorkspace({
        capabilities: makeCapabilities({ status, ok: false }),
        route: { mode: "messages", q: "anything", message: "42" },
        messagesApi,
      });
      await Promise.resolve();
      await Promise.resolve();
    }

    expect(search).not.toHaveBeenCalled();
    expect(message).not.toHaveBeenCalled();
  });

  it("stale search response is discarded when a newer query resolves first", async () => {
    let resolveFirst!: (result: MessagesSearchResult) => void;
    let resolveSecond!: (result: MessagesSearchResult) => void;
    const first = new Promise<MessagesSearchResult>((resolve) => {
      resolveFirst = resolve;
    });
    const second = new Promise<MessagesSearchResult>((resolve) => {
      resolveSecond = resolve;
    });
    const search = vi.fn().mockReturnValueOnce(first).mockReturnValueOnce(second);
    const messagesApi = makeMessagesApi({ search });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: { mode: "messages", q: "alpha", message: null } satisfies MessagesRoute,
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await rerender({ ...props, route: { mode: "messages", q: "beta", message: null } });
    resolveSecond(makeSearchResult([makeMessage(2)]));
    await Promise.resolve();
    resolveFirst(makeSearchResult([makeMessage(1)]));
    await Promise.resolve();
    await Promise.resolve();

    await waitFor(() => {
      const rows = document.querySelectorAll("button.row");
      expect(rows).toHaveLength(1);
      expect(rows[0]!.textContent).toContain("sender2@example.com");
    });
    expect(document.body.textContent).not.toContain("sender1@example.com");
  });

  it("in-flight detail fetch is orphaned when route message clears", async () => {
    let resolveDetail!: (detail: MessageDetailData) => void;
    const detailPromise = new Promise<MessageDetailData>((resolve) => {
      resolveDetail = resolve;
    });
    const message = vi.fn().mockReturnValueOnce(detailPromise);
    const props = {
      messagesApi: makeMessagesApi({ message }),
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: { mode: "messages", q: null, message: "42" } satisfies MessagesRoute,
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(message).toHaveBeenCalledWith(42));
    await rerender({ ...props, route: { mode: "messages", q: null, message: null } });
    resolveDetail(makeDetail(42, { subject: "Stale message" }));
    await Promise.resolve();
    await Promise.resolve();

    await waitFor(() => expect(screen.getByText("Select a message to read it.")).toBeTruthy());
    expect(screen.queryByText("Stale message")).toBeNull();
  });
});

describe("MessagesWorkspace facets", () => {
  it("renders the facets sidebar in ok status", () => {
    renderWorkspace();

    expect(screen.getByRole("navigation", { name: "Messages facets" })).toBeTruthy();
  });

  it("does not call aggregates in banner-only states", async () => {
    vi.useFakeTimers();
    const aggregates = fakeAggregates();
    const messagesApi = makeMessagesApi({ aggregates });

    for (const status of ["misconfigured", "down", "unauthorized"] as const) {
      cleanup();
      renderWorkspace({
        capabilities: makeCapabilities({ status, ok: false }),
        route: { mode: "messages", q: "anything", message: null },
        messagesApi,
      });
      await vi.advanceTimersByTimeAsync(500);
    }

    expect(aggregates).not.toHaveBeenCalled();
  });

  it("facet click appends the token and clears focused message", async () => {
    const aggregates = fakeAggregates();
    const onRouteChange = vi.fn();
    renderWorkspace({
      route: { mode: "messages", q: "project", message: "1001" },
      onRouteChange,
      messagesApi: makeMessagesApi({ aggregates }),
    });

    await waitFor(() => expect(screen.getByRole("button", { name: /alice@example\.com/ })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: /alice@example\.com/ }));

    expect(onRouteChange).toHaveBeenCalledOnce();
    expect(onRouteChange).toHaveBeenCalledWith(
      expect.objectContaining({ q: "project from:alice@example.com", message: null }),
    );
  });

  it("duplicate facet click is a no-op unless leaving linked view", async () => {
    const aggregates = fakeAggregates();
    const onRouteChange = vi.fn();
    renderWorkspace({
      route: { mode: "messages", q: "from:alice@example.com", message: null },
      onRouteChange,
      messagesApi: makeMessagesApi({ aggregates }),
    });

    await waitFor(() => expect(screen.getByRole("button", { name: /alice@example\.com/ })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: /alice@example\.com/ }));

    expect(onRouteChange).not.toHaveBeenCalled();
  });

  it("duplicate facet click clears linked view and focused message", async () => {
    const aggregates = fakeAggregates();
    const onRouteChange = vi.fn();
    renderWorkspace({
      route: { mode: "messages", q: "from:alice@example.com", message: "1001", view: "linked" },
      onRouteChange,
      messagesApi: makeMessagesApi({ aggregates }),
    });

    await waitFor(() => expect(screen.getByRole("button", { name: /alice@example\.com/ })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: /alice@example\.com/ }));

    expect(onRouteChange).toHaveBeenCalledOnce();
    expect(onRouteChange).toHaveBeenCalledWith(
      expect.objectContaining({ q: "from:alice@example.com", message: null, view: undefined }),
    );
  });

  it("rapid route query updates coalesce into one aggregate batch", async () => {
    vi.useFakeTimers();
    const aggregates = fakeAggregates();
    const props = {
      messagesApi: makeMessagesApi({ aggregates }),
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: { mode: "messages", q: "a", message: null } satisfies MessagesRoute,
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await vi.advanceTimersByTimeAsync(50);
    await rerender({ ...props, route: { mode: "messages", q: "ab", message: null } });
    await vi.advanceTimersByTimeAsync(50);
    await rerender({ ...props, route: { mode: "messages", q: "abc", message: null } });
    await vi.advanceTimersByTimeAsync(50);
    await rerender({ ...props, route: { mode: "messages", q: "abcd", message: null } });

    expect(aggregates).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(300);

    expect(aggregates).toHaveBeenCalledTimes(3);
  });

  it("drops stale facets batches when a newer batch resolves first", async () => {
    let resolveFirst!: (value: MessagesAggregateResponse) => void;
    let resolveSecond!: (value: MessagesAggregateResponse) => void;
    const firstBatch = new Promise<MessagesAggregateResponse>((resolve) => {
      resolveFirst = resolve;
    });
    const secondBatch = new Promise<MessagesAggregateResponse>((resolve) => {
      resolveSecond = resolve;
    });

    let batchIndex = 0;
    const aggregates = vi.fn(async () => {
      if (batchIndex < 3) {
        batchIndex++;
        return firstBatch;
      }
      return secondBatch;
    });
    const messagesApi = makeMessagesApi({ aggregates: aggregates as unknown as MessagesAPI["aggregates"] });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: { mode: "messages", q: "alpha", message: null } satisfies MessagesRoute,
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(aggregates).toHaveBeenCalledTimes(3));
    await rerender({ ...props, route: { mode: "messages", q: "beta", message: null } });
    await waitFor(() => expect(aggregates).toHaveBeenCalledTimes(6));

    resolveSecond(aggregateResponse("senders", [{ key: "carol@example.com", count: 9 }]));
    await Promise.resolve();
    await Promise.resolve();
    resolveFirst(aggregateResponse("senders", [{ key: "alice@example.com", count: 4 }]));
    await Promise.resolve();
    await Promise.resolve();

    await waitFor(() => {
      expect(screen.queryAllByRole("button", { name: /carol@example\.com/ }).length).toBeGreaterThan(0);
    });
    expect(screen.queryAllByRole("button", { name: /alice@example\.com/ })).toHaveLength(0);
  });

  it("clears facet errors when a fresh fetch begins after a failure", async () => {
    let callCount = 0;
    let resolveSecond!: () => void;
    const secondPending = new Promise<MessagesAggregateResponse>((resolve) => {
      resolveSecond = () => resolve(aggregateResponse("senders", []));
    });
    const aggregates = vi.fn(async () => {
      callCount++;
      if (callCount <= 3) throw new Error("aggregates exploded");
      return secondPending;
    });
    const messagesApi = makeMessagesApi({ aggregates: aggregates as unknown as MessagesAPI["aggregates"] });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: { mode: "messages", q: "alpha", message: null } satisfies MessagesRoute,
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => {
      expect(screen.getAllByRole("alert").some((alert) => alert.textContent?.includes("aggregates exploded"))).toBe(
        true,
      );
    });

    await rerender({ ...props, route: { mode: "messages", q: "beta", message: null } });

    await waitFor(() => {
      expect(screen.queryAllByRole("alert").some((alert) => alert.textContent?.includes("aggregates exploded"))).toBe(
        false,
      );
    });

    resolveSecond();
  });
});

describe("MessagesWorkspace threading", () => {
  it("skips thread fetches when the threads endpoint capability is disabled", async () => {
    const messages = [makeMessage(1002)];
    const thread = vi.fn(async () => {
      throw new Error("thread must not be called when disabled");
    });
    const capabilities = makeCapabilities({
      features: {
        threads_endpoint: false,
        mutations: false,
        attachments_download: false,
        sse_events: false,
      },
    });
    renderWorkspace({
      capabilities,
      route: focusedRouteFor(1002),
      messagesApi: makeMessagesApi({
        search: async () => makeSearchResult(messages),
        message: async () => makeDetail(1002, { body: "hi" }),
        thread,
      }),
    });

    await waitFor(() => expect(screen.queryByRole("heading", { level: 1, name: /Subject 1002/i })).toBeTruthy());
    expect(thread).not.toHaveBeenCalled();
    expect(screen.queryByText(/\bmsgs?\b/i)).toBeNull();
  });

  it("fetches the thread when a selected list row supplies a conversation id", async () => {
    const m1001 = makeMessage(1001, { conversation_id: 1001 });
    const m1002 = makeMessage(1002, { conversation_id: 1001 });
    const m1003 = makeMessage(1003, { conversation_id: 1001 });
    const thread = vi.fn(async () => [m1001, m1002, m1003]);
    renderWorkspace({
      route: focusedRouteFor(1002),
      messagesApi: makeMessagesApi({
        search: async () => makeSearchResult([m1001, m1002, m1003]),
        message: async () => makeDetail(1002, { conversation_id: 1001, body: "hi" }),
        thread,
      }),
    });

    await waitFor(() => expect(thread).toHaveBeenCalledWith(1001));
    await waitFor(() => {
      expect(screen.getAllByRole("button", { name: /open message/i }).length).toBeGreaterThanOrEqual(2);
    });
  });

  it("falls back to the selected detail when the thread endpoint returns an empty conversation", async () => {
    const thread = vi.fn(async () => []);
    renderWorkspace({
      route: focusedRouteFor(1002),
      messagesApi: makeMessagesApi({
        search: async () => makeSearchResult([]),
        message: async () =>
          makeDetail(1002, {
            conversation_id: 1001,
            subject: "Detail-only message",
            body: "Detail body",
          }),
        thread,
      }),
    });

    await waitFor(() => expect(thread).toHaveBeenCalledWith(1001));
    await waitFor(() => expect(screen.queryByRole("heading", { level: 1, name: "Detail-only message" })).toBeTruthy());
    expect(screen.queryByText("Loading conversation...")).toBeNull();
    expect(screen.queryAllByRole("button", { name: /open message/i })).toHaveLength(0);
    expect(screen.queryByText(/\bmsgs?\b/i)).toBeNull();
  });

  it("collapsed peer selection preserves the active query and swaps only the message id", async () => {
    const onRouteChange = vi.fn();
    const m1001 = makeMessage(1001, { conversation_id: 1001 });
    const m1002 = makeMessage(1002, { conversation_id: 1001 });
    renderWorkspace({
      route: { mode: "messages", q: "project", message: "1002" },
      onRouteChange,
      messagesApi: makeMessagesApi({
        search: async () => makeSearchResult([m1001, m1002]),
        message: async () => makeDetail(1002, { conversation_id: 1001, body: "hi" }),
        thread: async () => [m1001, m1002],
      }),
    });

    await waitFor(() => expect(screen.getAllByRole("button", { name: /open message/i }).length).toBeGreaterThan(0));
    await fireEvent.click(screen.getAllByRole("button", { name: /open message/i })[0]!);

    expect(onRouteChange).toHaveBeenCalledOnce();
    const next = onRouteChange.mock.calls[0]![0] as MessagesRoute;
    expect(next.q).toBe("project");
    expect(next.message).toBe("1001");
    expect(next).not.toHaveProperty("view");
  });

  it("drops stale thread responses so they do not seed the cache", async () => {
    const m1002 = makeMessage(1002, { conversation_id: 1001 });
    const m2002 = makeMessage(2002, { conversation_id: 2001 });
    let resolveFirst!: (value: MessageSummary[]) => void;
    const firstThread = new Promise<MessageSummary[]>((resolve) => {
      resolveFirst = resolve;
    });
    let call = 0;
    const messagesApi = makeMessagesApi({
      search: async () => makeSearchResult([m1002, m2002]),
      message: async (id: number) =>
        makeDetail(id, {
          ...(id === 1002 ? m1002 : m2002),
          body: "hi",
        }),
      thread: async () => {
        call++;
        if (call === 1) return firstThread;
        if (call === 2) return [m2002];
        return [m1002];
      },
    });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: focusedRouteFor(1002),
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(call).toBe(1));
    await rerender({ ...props, route: focusedRouteFor(2002) });
    await waitFor(() => expect(call).toBe(2));
    resolveFirst([m1002]);
    await Promise.resolve();
    await Promise.resolve();
    await rerender({ ...props, route: focusedRouteFor(1002) });

    await waitFor(() => expect(call).toBe(3));
  });

  it("reselects a cached thread peer absent from current search results without refetching", async () => {
    const m1001 = makeMessage(1001, { conversation_id: 1001 });
    const m1002 = makeMessage(1002, { conversation_id: 1001 });
    const thread = vi.fn(async () => [m1001, m1002]);
    const messagesApi = makeMessagesApi({
      search: async () => makeSearchResult([m1002]),
      message: async (id: number) =>
        makeDetail(id, {
          ...(id === 1001 ? m1001 : m1002),
          body: "hi",
        }),
      thread,
    });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: focusedRouteFor(1002),
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(thread).toHaveBeenCalledTimes(1));

    await rerender({ ...props, route: focusedRouteFor(1001) });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(thread).toHaveBeenCalledTimes(1);
  });

  it("does not call thread while capabilities are not ok", async () => {
    const thread = vi.fn(async () => [makeMessage(1002, { conversation_id: 1001 })]);
    renderWorkspace({
      capabilities: makeCapabilities({ ok: false, status: "down" }),
      route: focusedRouteFor(1002),
      messagesApi: makeMessagesApi({ thread }),
    });

    await Promise.resolve();
    await Promise.resolve();
    expect(thread).not.toHaveBeenCalled();
  });

  it("clears cached threads when capabilities transition away from ok", async () => {
    const m1001 = makeMessage(1001, { conversation_id: 1001 });
    const m1002 = makeMessage(1002, { conversation_id: 1001 });
    const thread = vi.fn(async () => [m1001, m1002]);
    const messagesApi = makeMessagesApi({
      search: async () => makeSearchResult([m1001, m1002]),
      message: async (id: number) =>
        makeDetail(id, {
          ...(id === 1001 ? m1001 : m1002),
          body: "hi",
        }),
      thread,
    });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities: makeCapabilities(),
      route: focusedRouteFor(1002),
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(thread).toHaveBeenCalledTimes(1));
    await rerender({ ...props, capabilities: makeCapabilities({ ok: false, status: "down" }) });
    await rerender({ ...props, capabilities: makeCapabilities() });

    await waitFor(() => expect(thread).toHaveBeenCalledTimes(2));
  });
});

describe("MessagesWorkspace linked messages", () => {
  function makeLinkedIssue(overrides: Partial<IssueSummary> = {}): IssueSummary {
    return {
      id: 1,
      uid: "issue-uid-1",
      short_id: "1",
      qualified_id: "PROJECT-1",
      title: "Issue with message link",
      status: "all",
      metadata: {
        mail_links: [
          {
            message_id: 555,
            conversation_id: 555,
            subject: "Linked subject from alice",
            from: "alice@example.com",
            sent_at: "2026-05-15T09:00:00Z",
            added_at: "2026-05-15T10:00:00Z",
          },
        ],
      },
      ...overrides,
    };
  }

  function linkedSearchResponse(issues: IssueSummary[], query = ""): SearchResponse {
    return {
      filters: {
        scope: { kind: "all" },
        status: "all",
        owner: "",
        label: "",
        query,
      },
      issues,
      fetched_at: "2026-05-15T10:00:00Z",
    };
  }

  it("view=linked renders linked messages and wires row and issue actions", async () => {
    const kataSearch = vi.fn(async () => linkedSearchResponse([makeLinkedIssue()]));
    const onRouteChange = vi.fn();
    const onOpenIssue = vi.fn();

    renderWorkspace({
      route: { mode: "messages", q: null, message: null, view: "linked" },
      kata: { search: kataSearch },
      onRouteChange,
      onOpenIssue,
    });

    await waitFor(() => expect(kataSearch).toHaveBeenCalledOnce());
    const linkedRegion = await waitFor(() => screen.getByRole("region", { name: "Linked messages" }));

    await fireEvent.click(within(linkedRegion).getByRole("button", { name: "PROJECT-1" }));
    expect(onOpenIssue).toHaveBeenCalledWith("issue-uid-1");

    await fireEvent.click(within(linkedRegion).getByRole("button", { name: "Linked subject from alice" }));
    expect(onRouteChange).toHaveBeenCalledWith(expect.objectContaining({ message: "555", view: "linked" }));
  });

  it("ignores a stale linked-issue refresh after linking a message", async () => {
    const baseIssue: IssueSummary = {
      id: 42,
      uid: "issue-uid-42",
      short_id: "42",
      qualified_id: "Kata#42",
      title: "Picked issue",
      status: "open",
      metadata: {},
    };
    const issueWithLink = makeLinkedIssue({
      ...baseIssue,
      metadata: {
        mail_links: [
          {
            message_id: 1001,
            conversation_id: 1001,
            subject: "Subject 1001",
            from: "sender1001@example.com",
            sent_at: "2026-05-15T09:00:00Z",
            added_at: "2026-05-15T10:00:00Z",
          },
        ],
      },
    });

    let refreshCalls = 0;
    let resolveFirstRefresh!: (response: SearchResponse) => void;
    const firstRefreshPending = new Promise<SearchResponse>((resolve) => {
      resolveFirstRefresh = resolve;
    });
    const kataSearch = vi.fn(async (filters: IssueFilters) => {
      if (filters.query !== "") return linkedSearchResponse([baseIssue], filters.query);
      refreshCalls++;
      if (refreshCalls === 1) return firstRefreshPending;
      return linkedSearchResponse([issueWithLink]);
    });
    const onLinkMessage = vi.fn().mockResolvedValue({ qualified_id: "Kata#42" });

    renderWorkspace({
      capabilities: makeCapabilities({
        features: {
          threads_endpoint: false,
          mutations: false,
          attachments_download: false,
          sse_events: false,
        },
      }),
      route: { mode: "messages", q: "anything", message: "1001" },
      messagesApi: makeMessagesApi({
        search: async () => makeSearchResult([makeMessage(1001)]),
        message: async () => makeDetail(1001, { body: "hello" }),
      }),
      kata: { search: kataSearch },
      onLinkMessage,
      onOpenIssue: vi.fn(),
    });

    await waitFor(() => expect(refreshCalls).toBe(1));

    await fireEvent.click(await waitFor(() => screen.getByRole("button", { name: /Link to task/i })));
    await fireEvent.input(await waitFor(() => screen.getByPlaceholderText(/Title or qualified ID/i)), {
      target: { value: "test" },
    });
    await new Promise((resolve) => setTimeout(resolve, 250));

    await fireEvent.click(await waitFor(() => screen.getByRole("button", { name: /Kata#42.*Picked issue/i })));
    await fireEvent.click(screen.getByRole("button", { name: /^Link$/ }));

    await waitFor(() => expect(onLinkMessage).toHaveBeenCalledOnce());
    await Promise.resolve();
    await waitFor(() => expect(refreshCalls).toBe(2));

    const linkedRegion = await waitFor(() => screen.getByRole("region", { name: "Linked tasks" }));
    expect(within(linkedRegion).getByRole("button", { name: /Kata#42/ })).toBeTruthy();

    resolveFirstRefresh(linkedSearchResponse([baseIssue]));
    await firstRefreshPending;
    await Promise.resolve();
    await tick();

    expect(
      within(screen.getByRole("region", { name: "Linked tasks" })).getByRole("button", { name: /Kata#42/ }),
    ).toBeTruthy();
  });
});

describe("MessagesWorkspace image state", () => {
  function htmlDetail(id: number, token: string): MessageDetailData {
    return makeDetail(id, {
      body_html: '<img data-remote-image-idx="0" alt="banner">',
      remote_image_count: 1,
      remote_image_token: token,
    });
  }

  it("clears loaded images and view mode when the messages config version changes", async () => {
    const token = "deadbeef".repeat(4);
    const capabilities = makeCapabilities({
      url: "http://messages.example.test/",
      api_key_env: "MSGVAULT_API_KEY",
      features: {
        threads_endpoint: false,
        mutations: false,
        attachments_download: false,
        sse_events: false,
      },
    });
    const messagesApi = makeMessagesApi({
      search: async () => makeSearchResult([makeMessage(1001)]),
      message: async () => htmlDetail(1001, token),
      remoteImageURL: (id: number, imageToken: string, index: string) =>
        `/api/v1/msgvault/messages/${id}/remote-image/${imageToken}/${index}`,
    });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities,
      route: focusedRouteFor(1001),
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { container, rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(screen.getByRole("button", { name: /Load images/i })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: /Load images/i }));

    await waitFor(() => {
      const srcdoc = container.querySelector("iframe.html-iframe")?.getAttribute("srcdoc") ?? "";
      expect(srcdoc).toContain(`/api/v1/msgvault/messages/1001/remote-image/${token}/0`);
    });

    await fireEvent.click(screen.getByRole("button", { name: "Text" }));
    await waitFor(() => expect(container.querySelector("pre.msg-body")).toBeTruthy());

    await rerender({ ...props, messagesConfigVersion: 1 });

    await waitFor(() => expect(screen.getByRole("button", { name: /Load images/i })).toBeTruthy());
    const srcdoc = container.querySelector("iframe.html-iframe")?.getAttribute("srcdoc") ?? "";
    expect(srcdoc).toContain('data-remote-image-idx="0"');
    expect(srcdoc).not.toContain(`/api/v1/msgvault/messages/1001/remote-image/${token}/0`);
    expect(screen.getByRole("button", { name: "HTML" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("re-prompts image consent after token rotation while preserving the message view mode", async () => {
    const tokenA = "aabbccdd00112233aabbccdd00112233";
    const tokenB = "99887766554433229988776655443322";
    const capabilities = makeCapabilities({
      url: "http://messages.example.test/",
      api_key_env: "MSGVAULT_API_KEY",
      features: {
        threads_endpoint: false,
        mutations: false,
        attachments_download: false,
        sse_events: false,
      },
    });
    let currentToken = tokenA;
    const messagesApi = makeMessagesApi({
      search: async () => makeSearchResult([makeMessage(1001)]),
      message: async () => htmlDetail(1001, currentToken),
      remoteImageURL: (id: number, imageToken: string, index: string) =>
        `/api/v1/msgvault/messages/${id}/remote-image/${imageToken}/${index}`,
    });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities,
      route: focusedRouteFor(1001),
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { container, rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(screen.getByRole("button", { name: /Load images/i })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: /Load images/i }));
    await waitFor(() => {
      const srcdoc = container.querySelector("iframe.html-iframe")?.getAttribute("srcdoc") ?? "";
      expect(srcdoc).toContain(`/api/v1/msgvault/messages/1001/remote-image/${tokenA}/0`);
    });

    await fireEvent.click(screen.getByRole("button", { name: "Text" }));
    await waitFor(() => expect(container.querySelector("pre.msg-body")).toBeTruthy());

    currentToken = tokenB;
    await rerender({ ...props, route: { ...defaultMessagesRoute, message: null } });
    await rerender({ ...props, route: focusedRouteFor(1001) });

    await waitFor(() => expect(container.querySelector("pre.msg-body")).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "HTML" }));

    await waitFor(() => expect(screen.getByRole("button", { name: /Load images/i })).toBeTruthy());
    const srcdoc = container.querySelector("iframe.html-iframe")?.getAttribute("srcdoc") ?? "";
    expect(srcdoc).toContain('data-remote-image-idx="0"');
    expect(srcdoc).not.toContain(`/api/v1/msgvault/messages/1001/remote-image/${tokenA}/0`);
    expect(srcdoc).not.toContain(`/api/v1/msgvault/messages/1001/remote-image/${tokenB}/0`);
  });

  it("preserves image consent and view mode across a transient capability flap", async () => {
    const token = "feedface".repeat(4);
    const capabilities = makeCapabilities({
      url: "http://messages.example.test/",
      api_key_env: "MSGVAULT_API_KEY",
      features: {
        threads_endpoint: false,
        mutations: false,
        attachments_download: false,
        sse_events: false,
      },
    });
    const downCapabilities = makeCapabilities({
      url: "http://messages.example.test/",
      api_key_env: "MSGVAULT_API_KEY",
      ok: false,
      status: "down",
      features: capabilities.features,
    });
    const messagesApi = makeMessagesApi({
      search: async () => makeSearchResult([makeMessage(1001)]),
      message: async () => htmlDetail(1001, token),
      remoteImageURL: (id: number, imageToken: string, index: string) =>
        `/api/v1/msgvault/messages/${id}/remote-image/${imageToken}/${index}`,
    });
    const props = {
      messagesApi,
      savedSearchesApi: makeSavedSearchesAPI(),
      capabilities,
      route: focusedRouteFor(1001),
      onRouteChange: () => {},
      messagesConfigVersion: 0,
    };
    const { container, rerender } = render(MessagesWorkspace, { props });

    await waitFor(() => expect(screen.getByRole("button", { name: /Load images/i })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: /Load images/i }));
    await waitFor(() => {
      const srcdoc = container.querySelector("iframe.html-iframe")?.getAttribute("srcdoc") ?? "";
      expect(srcdoc).toContain(`/api/v1/msgvault/messages/1001/remote-image/${token}/0`);
    });
    await fireEvent.click(screen.getByRole("button", { name: "Text" }));
    await waitFor(() => expect(container.querySelector("pre.msg-body")).toBeTruthy());

    await rerender({ ...props, capabilities: downCapabilities });
    expect(screen.queryByRole("search", { name: "Search messages" })).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("Messages unavailable");

    await rerender({ ...props, capabilities });

    await waitFor(() => expect(container.querySelector("pre.msg-body")).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "HTML" }));
    await waitFor(() => {
      const srcdoc = container.querySelector("iframe.html-iframe")?.getAttribute("srcdoc") ?? "";
      expect(srcdoc).toContain(`/api/v1/msgvault/messages/1001/remote-image/${token}/0`);
    });
  });
});

describe("MessagesWorkspace saved searches", () => {
  it("hydrates saved searches from the API on mount", async () => {
    const list = vi.fn(async () => ({
      searches: [{ name: "Recent", query: "newer:7d" }],
      etag: '"sha256:abc"',
    }));
    renderWorkspace({ savedSearchesApi: makeSavedSearchesAPI({ list }) });

    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.getByRole("button", { name: "Delete saved search Recent" })).toBeTruthy());
  });

  it("keeps saved searches local and skips replace when hydration fails", async () => {
    const list = vi.fn().mockRejectedValue(new Error("network down"));
    const replace = vi.fn();
    renderWorkspace({
      route: { mode: "messages", q: "from:alice@example.com", message: null },
      savedSearchesApi: makeSavedSearchesAPI({ list, replace }),
    });

    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    await waitFor(() => {
      expect(screen.getByRole("status").textContent).toContain("Saved searches couldn't be loaded");
    });

    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    await fireEvent.input(screen.getByRole("textbox", { name: "Saved search name" }), {
      target: { value: "Alice" },
    });
    await fireEvent.keyDown(screen.getByRole("textbox", { name: "Saved search name" }), { key: "Enter" });

    await waitFor(() => expect(screen.getByRole("button", { name: "Delete saved search Alice" })).toBeTruthy());
    expect(replace).not.toHaveBeenCalled();
    expect(screen.getByRole("status").textContent).toContain("Saved searches couldn't be loaded");
  });

  it("saving a search replaces with the hydrated ETag", async () => {
    const list = vi.fn(async () => ({ searches: [], etag: '"sha256:e0"' }));
    const replace = vi.fn(async (_searches: SavedSearch[], _ifMatch?: string) => ({
      searches: [{ name: "n", query: "q" }],
      etag: '"sha256:e1"',
    }));
    renderWorkspace({
      route: { mode: "messages", q: "q", message: null },
      savedSearchesApi: makeSavedSearchesAPI({ list, replace }),
    });

    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    await fireEvent.input(screen.getByRole("textbox", { name: "Saved search name" }), { target: { value: "n" } });
    await fireEvent.keyDown(screen.getByRole("textbox", { name: "Saved search name" }), { key: "Enter" });

    await waitFor(() => expect(replace).toHaveBeenCalledOnce());
    const [searches, etag] = replace.mock.calls[0] as [SavedSearch[], string];
    expect(searches).toEqual([{ name: "n", query: "q" }]);
    expect(etag).toBe('"sha256:e0"');
  });

  it("rebases and retries once when save hits a stale ETag", async () => {
    const list = vi
      .fn()
      .mockResolvedValueOnce({ searches: [], etag: '"sha256:e0"' })
      .mockResolvedValueOnce({ searches: [{ name: "concurrent", query: "x" }], etag: '"sha256:e1"' });
    const replace = vi
      .fn()
      .mockRejectedValueOnce(makeStaleETagError())
      .mockResolvedValueOnce({
        searches: [
          { name: "concurrent", query: "x" },
          { name: "n", query: "q" },
        ],
        etag: '"sha256:e2"',
      });
    renderWorkspace({
      route: { mode: "messages", q: "q", message: null },
      savedSearchesApi: makeSavedSearchesAPI({ list, replace }),
    });

    await waitFor(() => expect(list).toHaveBeenCalledTimes(1));
    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    await fireEvent.input(screen.getByRole("textbox", { name: "Saved search name" }), { target: { value: "n" } });
    await fireEvent.keyDown(screen.getByRole("textbox", { name: "Saved search name" }), { key: "Enter" });

    await waitFor(() => {
      expect(list).toHaveBeenCalledTimes(2);
      expect(replace).toHaveBeenCalledTimes(2);
    });
    const [secondSearches, secondETag] = replace.mock.calls[1] as [SavedSearch[], string];
    expect(secondETag).toBe('"sha256:e1"');
    expect(secondSearches).toEqual(
      expect.arrayContaining([
        { name: "concurrent", query: "x" },
        { name: "n", query: "q" },
      ]),
    );
  });

  it("waits for pending hydration before saving so replace carries an ETag", async () => {
    let resolveList!: (value: { searches: SavedSearch[]; etag: string }) => void;
    const listPending = new Promise<{ searches: SavedSearch[]; etag: string }>((resolve) => {
      resolveList = resolve;
    });
    const list = vi.fn(() => listPending);
    const replace = vi.fn(async (_searches: SavedSearch[], _ifMatch?: string) => ({
      searches: [{ name: "n", query: "q" }],
      etag: '"sha256:after"',
    }));
    renderWorkspace({
      route: { mode: "messages", q: "q", message: null },
      savedSearchesApi: makeSavedSearchesAPI({ list, replace }),
    });

    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    await fireEvent.input(screen.getByRole("textbox", { name: "Saved search name" }), { target: { value: "n" } });
    await fireEvent.keyDown(screen.getByRole("textbox", { name: "Saved search name" }), { key: "Enter" });
    await Promise.resolve();
    await Promise.resolve();
    expect(replace).not.toHaveBeenCalled();

    resolveList({ searches: [], etag: '"sha256:e0"' });

    await waitFor(() => expect(replace).toHaveBeenCalledOnce());
    const [, ifMatch] = replace.mock.calls[0] as [SavedSearch[], string];
    expect(ifMatch).toBe('"sha256:e0"');
  });

  it("replays a save against hydrated entries instead of replacing them", async () => {
    let resolveList!: (value: { searches: SavedSearch[]; etag: string }) => void;
    const listPending = new Promise<{ searches: SavedSearch[]; etag: string }>((resolve) => {
      resolveList = resolve;
    });
    const list = vi.fn(() => listPending);
    const replace = vi.fn(async (_searches: SavedSearch[], _ifMatch?: string) => ({
      searches: [
        { name: "Existing", query: "old" },
        { name: "new", query: "q" },
      ],
      etag: '"sha256:after"',
    }));
    renderWorkspace({
      route: { mode: "messages", q: "q", message: null },
      savedSearchesApi: makeSavedSearchesAPI({ list, replace }),
    });

    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    await fireEvent.input(screen.getByRole("textbox", { name: "Saved search name" }), { target: { value: "new" } });
    await fireEvent.keyDown(screen.getByRole("textbox", { name: "Saved search name" }), { key: "Enter" });

    resolveList({
      searches: [{ name: "Existing", query: "old" }],
      etag: '"sha256:e0"',
    });

    await waitFor(() => expect(replace).toHaveBeenCalledOnce());
    const [searches, ifMatch] = replace.mock.calls[0] as [SavedSearch[], string];
    expect(ifMatch).toBe('"sha256:e0"');
    expect(searches).toEqual([
      { name: "Existing", query: "old" },
      { name: "new", query: "q" },
    ]);
  });

  it("ignores browser-stored searches because the API is authoritative", async () => {
    localStorage.setItem(SAVED_SEARCHES_KEY, JSON.stringify([{ name: "Stored", query: "from:alice@example.com" }]));
    const list = vi.fn(async () => ({ searches: [], etag: '"sha256:empty"' }));
    const replace = vi.fn();

    renderWorkspace({ savedSearchesApi: makeSavedSearchesAPI({ list, replace }) });

    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    expect(replace).not.toHaveBeenCalled();
    expect(localStorage.getItem(SAVED_SEARCHES_KEY)).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Delete saved search Stored" })).toBeNull();
  });

  it("does not seed saved searches from browser storage after list failure", async () => {
    localStorage.setItem(SAVED_SEARCHES_KEY, JSON.stringify([{ name: "Stored", query: "q1" }]));
    const list = vi.fn().mockRejectedValue(new Error("network down"));
    const replace = vi.fn();

    renderWorkspace({
      route: { mode: "messages", q: "newq", message: null },
      savedSearchesApi: makeSavedSearchesAPI({ list, replace }),
    });

    await waitFor(() => expect(list).toHaveBeenCalledOnce());
    expect(screen.queryByRole("button", { name: "Delete saved search Stored" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Save current search" }));
    await fireEvent.input(screen.getByRole("textbox", { name: "Saved search name" }), {
      target: { value: "new" },
    });
    await fireEvent.keyDown(screen.getByRole("textbox", { name: "Saved search name" }), { key: "Enter" });

    await Promise.resolve();
    await Promise.resolve();
    expect(replace).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("button", { name: "Delete saved search new" })).toBeTruthy());
    expect(localStorage.getItem(SAVED_SEARCHES_KEY)).not.toBeNull();
  });
});
