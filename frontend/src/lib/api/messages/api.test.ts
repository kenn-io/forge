import { describe, expect, test } from "vite-plus/test";

import { createMessagesAPI } from "./api.js";
import type { MessagesCapabilities } from "./types.js";

function problemResponse(status: number, code: string, detail: string, details?: Record<string, unknown>): Response {
  return new Response(JSON.stringify({ title: code, status, detail, code, details }), {
    status,
    headers: { "content-type": "application/problem+json" },
  });
}

function stubFetch(routes: Record<string, (req: Request) => Response | Promise<Response>>): typeof fetch {
  return async (input, init) => {
    const request = input instanceof Request ? input : new Request(String(input instanceof URL ? input : input), init);
    const absolute = new URL(request.url, "http://test");
    const path = absolute.pathname + absolute.search;
    for (const [pattern, handler] of Object.entries(routes)) {
      if (path.startsWith(pattern)) {
        return handler(request);
      }
    }
    throw new Error(`unhandled fetch: ${path}`);
  };
}

describe("MessagesAPI real client", () => {
  test("capabilities() GETs msgvault health and normalizes modes/features", async () => {
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/health": () =>
          new Response(
            JSON.stringify({
              configured: true,
              ok: true,
              status: "ok",
              modes: ["fts"],
              features: { threads_endpoint: true, mutations: false, attachments_download: false, sse_events: false },
            } satisfies MessagesCapabilities),
          ),
      }),
    });

    const caps = await api.capabilities();

    expect(caps.configured).toBe(true);
    expect(caps.modes).toEqual(["fts"]);
    expect(caps.features.threads_endpoint).toBe(true);
  });

  test("search() URL-encodes q and forwards mode/page", async () => {
    let seenURL = "";
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/search": (req) => {
          seenURL = req.url;
          return new Response(
            JSON.stringify({
              query: "x",
              mode: "fts",
              total: 0,
              page: 1,
              page_size: 20,
              paginatable: true,
              messages: null,
            }),
          );
        },
      }),
    });

    await api.search("label:Inbox is:unread", { mode: "fts", page: 2, pageSize: 10 });

    const u = new URL(seenURL);
    expect(u.searchParams.get("q")).toBe("label:Inbox is:unread");
    expect(u.searchParams.get("mode")).toBe("fts");
    expect(u.searchParams.get("page")).toBe("2");
    expect(u.searchParams.get("page_size")).toBe("10");
  });

  test("message() normalizes nullable arrays to arrays", async () => {
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/messages/42": () =>
          new Response(
            JSON.stringify({
              id: 42,
              conversation_id: 7,
              subject: "Hi",
              from: "alice@example.com",
              to: null,
              cc: null,
              bcc: null,
              sent_at: "2026-05-15T09:00:00Z",
              snippet: "",
              labels: null,
              has_attachments: false,
              size_bytes: 0,
              deleted_at: null,
              body: "plain",
              attachments: null,
            }),
          ),
      }),
    });

    const message = await api.message(42);

    expect(message.to).toEqual([]);
    expect(message.cc).toEqual([]);
    expect(message.bcc).toEqual([]);
    expect(message.labels).toEqual([]);
    expect(message.attachments).toEqual([]);
  });

  test("problem+json error is rethrown with code and detail", async () => {
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/messages": () => problemResponse(503, "serviceUnavailable", "bad key"),
      }),
    });

    await expect(api.message(42)).rejects.toMatchObject({
      name: "MessagesAPIError",
      status: 503,
      code: "serviceUnavailable",
      message: "bad key",
    });
  });

  test("inlineURL builds the msgvault image path", () => {
    const api = createMessagesAPI();

    expect(api.inlineURL(42, "logo")).toBe("/api/v1/msgvault/messages/42/inline?cid=logo");
  });

  test("image URL helpers accept a relative API base", () => {
    const api = createMessagesAPI({ baseURL: "/api/v1" });

    expect(api.inlineURL(42, "logo")).toBe("/api/v1/msgvault/messages/42/inline?cid=logo");
    expect(api.remoteImageURL(42, "tok", "0")).toBe("/api/v1/msgvault/messages/42/remote-image/tok/0");
  });

  test("thread() returns normalized message summaries", async () => {
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/threads/1001": () =>
          new Response(
            JSON.stringify({
              conversation_id: 1001,
              messages: [
                {
                  id: 1,
                  conversation_id: 1001,
                  subject: "First",
                  from: "alice@example.com",
                  to: null,
                  cc: null,
                  bcc: null,
                  sent_at: "2026-05-15T09:00:00Z",
                  snippet: "",
                  labels: null,
                  has_attachments: false,
                  size_bytes: 0,
                  deleted_at: null,
                },
              ],
            }),
          ),
      }),
    });

    const out = await api.thread(1001);

    expect(out).toHaveLength(1);
    expect(out[0]!.subject).toBe("First");
    expect(out[0]!.to).toEqual([]);
  });

  test("configure() POSTs JSON body and returns capabilities", async () => {
    let seenMethod = "";
    let seenContentType: string | null = null;
    let seenBody = "";
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/configure": async (req) => {
          seenMethod = req.method;
          seenContentType = req.headers.get("content-type");
          seenBody = await req.text();
          return new Response(
            JSON.stringify({
              configured: true,
              ok: true,
              status: "ok",
              modes: ["fts"],
              features: { threads_endpoint: false, mutations: false, attachments_download: false, sse_events: false },
              url: "https://msgvault.example.com",
              api_key_env: "MSGVAULT_API_KEY",
            } satisfies MessagesCapabilities),
          );
        },
      }),
    });

    const caps = await api.configure({
      url: "https://msgvault.example.com",
      api_key_env: "MSGVAULT_API_KEY",
    });

    expect(seenMethod).toBe("POST");
    expect(seenContentType).toBe("application/json");
    expect(JSON.parse(seenBody)).toEqual({
      url: "https://msgvault.example.com",
      api_key_env: "MSGVAULT_API_KEY",
    });
    expect(caps).toMatchObject({
      configured: true,
      ok: true,
      status: "ok",
      url: "https://msgvault.example.com",
      api_key_env: "MSGVAULT_API_KEY",
    });
  });

  test("configure() surfaces invalid URL reason with the server code preserved", async () => {
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/configure": () =>
          problemResponse(400, "badRequest", "url scheme must be http or https", { reason: "invalidURL" }),
      }),
    });

    await expect(
      api.configure({ url: "ftp://msgvault.example.com", api_key_env: "MSGVAULT_API_KEY" }),
    ).rejects.toMatchObject({
      name: "MessagesAPIError",
      status: 400,
      code: "badRequest",
      reason: "invalidURL",
      message: "url scheme must be http or https",
    });
  });

  test("configure() preserves API key unsupported reason from the server problem details", async () => {
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/configure": () =>
          problemResponse(400, "badRequest", "api_key is not stored; set the env var named by api_key_env instead", {
            reason: "apiKeyUnsupported",
          }),
      }),
    });

    await expect(
      api.configure({ url: "https://msgvault.example.com", api_key_env: "MSGVAULT_API_KEY" }),
    ).rejects.toMatchObject({
      name: "MessagesAPIError",
      status: 400,
      code: "badRequest",
      reason: "apiKeyUnsupported",
    });
  });

  test("aggregates() forwards view_type, q, hide_deleted=false, sort, direction, limit", async () => {
    let seenURL = "";
    const api = createMessagesAPI({
      baseURL: "http://test/api/v1",
      fetch: stubFetch({
        "/api/v1/msgvault/aggregates": (req) => {
          seenURL = req.url;
          return new Response(JSON.stringify({ view_type: "senders", rows: null }));
        },
      }),
    });

    await api.aggregates("senders", {
      q: "label:Inbox",
      hideDeleted: false,
      sort: "count",
      direction: "desc",
      limit: 25,
    });

    const u = new URL(seenURL);
    expect(u.searchParams.get("view_type")).toBe("senders");
    expect(u.searchParams.get("q")).toBe("label:Inbox");
    expect(u.searchParams.get("hide_deleted")).toBe("false");
    expect(u.searchParams.get("sort")).toBe("count");
    expect(u.searchParams.get("direction")).toBe("desc");
    expect(u.searchParams.get("limit")).toBe("25");
  });
});
