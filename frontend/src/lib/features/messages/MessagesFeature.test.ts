import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { MessagesCapabilities, MessageDetailData, MessagesSearchResult } from "../../api/messages/types";
import type { SearchResponse } from "../../messages/types";
import MessagesFeature from "./MessagesFeature.svelte";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    headers: { "content-type": "application/json" },
  });
}

const baseFeatures: MessagesCapabilities["features"] = {
  threads_endpoint: false,
  mutations: false,
  attachments_download: false,
  sse_events: false,
};

function makeCapabilities(overrides: Partial<MessagesCapabilities> = {}): MessagesCapabilities {
  return {
    configured: true,
    ok: true,
    status: "ok",
    modes: ["fts"],
    features: {
      ...baseFeatures,
      ...overrides.features,
    },
    url: "http://msgvault.test",
    api_key_env: "MSGVAULT_API_KEY",
    ...overrides,
  };
}

function unconfiguredCapabilities(): MessagesCapabilities {
  const capabilities = makeCapabilities({
    configured: false,
    ok: false,
    modes: [],
  });
  delete capabilities.status;
  delete capabilities.url;
  delete capabilities.api_key_env;
  return capabilities;
}

function messageDetail(): MessageDetailData {
  return {
    id: 1001,
    conversation_id: 1001,
    subject: "Project kickoff",
    from: "alice@example.com",
    to: ["bob@example.com"],
    cc: [],
    bcc: [],
    sent_at: "2026-05-15T09:00:00Z",
    snippet: "Let's get this started.",
    labels: ["Inbox"],
    has_attachments: false,
    size_bytes: 2048,
    deleted_at: null,
    body: "Hi team,\n\nLet us begin.",
    attachments: [],
  };
}

function searchResult(): MessagesSearchResult {
  const detail = messageDetail();
  return {
    query: "",
    mode: "fts",
    total: 1,
    page: 1,
    page_size: 20,
    paginatable: false,
    messages: [
      {
        id: detail.id,
        conversation_id: detail.conversation_id,
        subject: detail.subject,
        from: detail.from,
        to: detail.to,
        cc: detail.cc,
        bcc: detail.bcc,
        sent_at: detail.sent_at,
        snippet: detail.snippet,
        labels: detail.labels,
        has_attachments: detail.has_attachments,
        size_bytes: detail.size_bytes,
        deleted_at: detail.deleted_at,
      },
    ],
  };
}

interface FetchCall {
  method: string;
  path: string;
  body?: unknown;
}

function installFetch(
  options: {
    capabilities?: MessagesCapabilities;
    health?: Array<MessagesCapabilities | Error>;
    configure?: MessagesCapabilities | ((input: { url: string; api_key_env: string }) => MessagesCapabilities);
  } = {},
): FetchCall[] {
  let capabilities = options.capabilities ?? makeCapabilities();
  const health = [...(options.health ?? [])];
  const calls: FetchCall[] = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const request = input instanceof Request ? input : new Request(input, init);
    const url = new URL(request.url);
    const call: FetchCall = { method: request.method, path: url.pathname };
    if (request.method !== "GET" && request.body !== null) {
      call.body = await request.clone().json();
    }
    calls.push(call);
    switch (url.pathname) {
      case "/api/v1/msgvault/health": {
        const next = health.shift();
        if (next instanceof Error) throw next;
        if (next !== undefined) capabilities = next;
        return jsonResponse(capabilities);
      }
      case "/api/v1/msgvault/configure": {
        const inputBody = call.body as { url: string; api_key_env: string };
        capabilities =
          typeof options.configure === "function"
            ? options.configure(inputBody)
            : (options.configure ?? makeCapabilities(inputBody));
        return jsonResponse(capabilities);
      }
      case "/api/v1/msgvault/search":
        return jsonResponse(searchResult());
      case "/api/v1/msgvault/messages/1001":
        return jsonResponse(messageDetail());
      case "/api/v1/msgvault/aggregates":
        return jsonResponse({ view_type: url.searchParams.get("view") ?? "senders", rows: [] });
      case "/api/v1/messages/saved-searches":
        return jsonResponse({ searches: [], etag: '"empty"' });
      default:
        throw new Error(`unhandled fetch: ${url.pathname}${url.search}`);
    }
  });
  return calls;
}

describe("MessagesFeature", () => {
  it("renders first-use setup when Messages are not configured", async () => {
    const onCapabilitiesChange = vi.fn();
    const calls = installFetch({ capabilities: unconfiguredCapabilities() });

    render(MessagesFeature, {
      props: {
        route: { mode: "messages", q: null, message: null },
        onRouteChange: vi.fn(),
        onCapabilitiesChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByText("Messages are not set up.")).toBeTruthy();
    });

    expect(screen.getByRole("button", { name: "Set up Messages" })).toBeTruthy();
    expect(screen.queryByRole("search", { name: "Search messages" })).toBeNull();
    expect(onCapabilitiesChange).toHaveBeenCalledWith(expect.objectContaining({ configured: false, ok: false }));
    expect(calls.map((call) => call.path)).toEqual(["/api/v1/msgvault/health"]);
  });

  it("renders configured-but-unavailable Messages without activating workspace controls", async () => {
    const onCapabilitiesChange = vi.fn();
    const calls = installFetch({
      capabilities: makeCapabilities({
        ok: false,
        status: "down",
        status_detail: "connection refused",
      }),
    });

    render(MessagesFeature, {
      props: {
        route: { mode: "messages", q: null, message: "1001" },
        onRouteChange: vi.fn(),
        onCapabilitiesChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("Messages unavailable");
    });

    expect(screen.getByRole("button", { name: "Configure" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Configure messages" })).toBeTruthy();
    expect(screen.queryByRole("search", { name: "Search messages" })).toBeNull();
    expect(screen.queryByText("Select a message to read it.")).toBeNull();
    expect(onCapabilitiesChange).toHaveBeenCalledWith(
      expect.objectContaining({ configured: true, ok: false, status: "down" }),
    );
    expect(calls.some((call) => call.path === "/api/v1/msgvault/search")).toBe(false);
    expect(calls.some((call) => call.path === "/api/v1/msgvault/messages/1001")).toBe(false);
  });

  it("reports capability load failures and retries into the workspace", async () => {
    const onCapabilitiesChange = vi.fn();
    const calls = installFetch({
      health: [new Error("health unavailable"), makeCapabilities()],
    });

    render(MessagesFeature, {
      props: {
        route: { mode: "messages", q: null, message: null },
        onRouteChange: vi.fn(),
        onCapabilitiesChange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("health unavailable");
    });
    expect(onCapabilitiesChange).toHaveBeenCalledWith(null);

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(screen.getByRole("search", { name: "Search messages" })).toBeTruthy();
    });
    expect(onCapabilitiesChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ configured: true, ok: true, status: "ok" }),
    );
    expect(calls.filter((call) => call.path === "/api/v1/msgvault/health")).toHaveLength(2);
  });

  it("opens setup from the unconfigured state and refreshes capabilities after save", async () => {
    const onCapabilitiesChange = vi.fn();
    const calls = installFetch({
      capabilities: unconfiguredCapabilities(),
      configure: makeCapabilities(),
    });

    render(MessagesFeature, {
      props: {
        route: { mode: "messages", q: null, message: null },
        onRouteChange: vi.fn(),
        onCapabilitiesChange,
      },
    });

    await fireEvent.click(await screen.findByRole("button", { name: "Set up Messages" }));
    await fireEvent.input(screen.getByLabelText(/Message source URL/i), {
      target: { value: "https://messages.example.com" },
    });
    await fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);

    await waitFor(() => {
      expect(screen.getByRole("search", { name: "Search messages" })).toBeTruthy();
    });

    expect(calls.find((call) => call.path === "/api/v1/msgvault/configure")).toMatchObject({
      method: "POST",
      body: {
        url: "https://messages.example.com",
        api_key_env: "MSGVAULT_API_KEY",
      },
    });
    expect(onCapabilitiesChange).toHaveBeenNthCalledWith(1, expect.objectContaining({ configured: false, ok: false }));
    expect(onCapabilitiesChange).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ configured: true, ok: true, status: "ok" }),
    );
  });

  it("restores focus to message search after first-use setup succeeds", async () => {
    installFetch({
      capabilities: unconfiguredCapabilities(),
      configure: makeCapabilities(),
    });

    render(MessagesFeature, {
      props: {
        route: { mode: "messages", q: null, message: null },
        onRouteChange: vi.fn(),
      },
    });

    await fireEvent.click(await screen.findByRole("button", { name: "Set up Messages" }));
    await fireEvent.input(screen.getByLabelText(/Message source URL/i), {
      target: { value: "https://messages.example.com" },
    });
    await fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);

    await waitFor(() => {
      const search = screen.getByRole("search", { name: "Search messages" });
      const input = search.querySelector<HTMLInputElement>('input[type="search"]');
      expect(input).toBeTruthy();
      expect(document.activeElement).toBe(input);
    });
  });

  it("opens setup from a degraded state with the existing source settings", async () => {
    installFetch({
      capabilities: makeCapabilities({
        ok: false,
        status: "misconfigured",
        status_detail: "api_key_env is unset",
        url: "https://messages.example.com",
        api_key_env: "ALT_MSGVAULT_KEY",
      }),
    });

    render(MessagesFeature, {
      props: {
        route: { mode: "messages", q: null, message: null },
        onRouteChange: vi.fn(),
      },
    });

    await fireEvent.click(await screen.findByRole("button", { name: "Configure" }));

    expect((screen.getByLabelText(/Message source URL/i) as HTMLInputElement).value).toBe(
      "https://messages.example.com",
    );
    expect((screen.getByLabelText(/API key env var name/i) as HTMLInputElement).value).toBe("ALT_MSGVAULT_KEY");
  });

  it("threads Kata search and link callbacks into the workspace", async () => {
    installFetch();
    const kata = {
      search: vi.fn(async (): Promise<SearchResponse> => ({ issues: [], fetched_at: "2026-05-18T00:00:00Z" })),
    };
    const onLinkMessage = vi.fn(async () => ({ qualified_id: "Kata#100" }));

    render(MessagesFeature, {
      props: {
        route: { mode: "messages", q: null, message: "1001" },
        onRouteChange: vi.fn(),
        kata,
        onLinkMessage,
        onOpenIssue: vi.fn(),
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Project kickoff" })).toBeTruthy();
    });

    expect(screen.getByRole("button", { name: /Link to task/i })).toBeTruthy();
    expect(kata.search).toHaveBeenCalledWith({
      scope: { kind: "all" },
      status: "all",
      owner: "",
      label: "",
      query: "",
    });
  });
});
