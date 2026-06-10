import { afterEach, describe, expect, test, vi } from "vite-plus/test";

import {
  createMockSavedSearchesBackend,
  createSavedSearchesAPI,
  type SavedSearchesAPIError,
} from "./savedSearchesClient.js";

afterEach(() => vi.restoreAllMocks());

describe("createSavedSearchesAPI live HTTP", () => {
  function stubFetch(responses: Array<{ status: number; body: unknown; headers?: Record<string, string> }>) {
    let call = 0;
    return vi.fn(async (input: RequestInfo | URL) => {
      const r = responses[call++];
      if (!r) throw new Error(`unexpected fetch call ${call} to ${String(input)}`);
      const headers = new Headers(r.headers ?? {});
      if (!headers.has("content-type")) headers.set("content-type", "application/json");
      return new Response(JSON.stringify(r.body), { status: r.status, headers });
    });
  }

  function requestOf(fetchFn: ReturnType<typeof stubFetch>, idx = 0): Request {
    const [input] = fetchFn.mock.calls[idx]!;
    if (!(input instanceof Request)) throw new Error("expected a Request argument");
    return input;
  }

  test("list() returns searches and etag from response body", async () => {
    const fetchFn = stubFetch([{ status: 200, body: { searches: [{ name: "x", query: "y" }], etag: '"sha256:abc"' } }]);
    const api = createSavedSearchesAPI({ baseURL: "http://test/api/v1", fetch: fetchFn });

    const got = await api.list();

    expect(got.searches).toEqual([{ name: "x", query: "y" }]);
    expect(got.etag).toBe('"sha256:abc"');
    expect(fetchFn).toHaveBeenCalledOnce();
    expect(requestOf(fetchFn).method).toBe("GET");
    expect(new URL(requestOf(fetchFn).url).pathname).toBe("/api/v1/messages/saved-searches");
  });

  test("replace() sends If-Match when provided and PUT body", async () => {
    const fetchFn = stubFetch([{ status: 200, body: { searches: [{ name: "x", query: "y" }], etag: '"sha256:new"' } }]);
    const api = createSavedSearchesAPI({ baseURL: "http://test/api/v1", fetch: fetchFn });

    const got = await api.replace([{ name: "x", query: "y" }], '"sha256:old"');

    expect(got.etag).toBe('"sha256:new"');
    const req = requestOf(fetchFn);
    expect(req.method).toBe("PUT");
    expect(req.headers.get("If-Match")).toBe('"sha256:old"');
    expect(req.headers.get("Content-Type")).toBe("application/json");
    expect(JSON.parse(await req.clone().text())).toEqual({ searches: [{ name: "x", query: "y" }] });
  });

  test("replace() omits If-Match when ifMatch is undefined", async () => {
    const fetchFn = stubFetch([{ status: 200, body: { searches: [], etag: '"sha256:x"' } }]);
    const api = createSavedSearchesAPI({ baseURL: "http://test/api/v1", fetch: fetchFn });

    await api.replace([]);

    expect(requestOf(fetchFn).headers.has("If-Match")).toBe(false);
  });

  test("non-2xx throws SavedSearchesAPIError with code and reason", async () => {
    const fetchFn = stubFetch([
      {
        status: 412,
        headers: { "content-type": "application/problem+json" },
        body: {
          title: "Precondition Failed",
          status: 412,
          code: "conflict",
          detail: "saved searches have changed since last load; refetch and retry",
          details: { reason: "stale_etag" },
        },
      },
    ]);
    const api = createSavedSearchesAPI({ baseURL: "http://test/api/v1", fetch: fetchFn });

    let err: SavedSearchesAPIError | undefined;
    try {
      await api.replace([], '"sha256:old"');
    } catch (e) {
      err = e as SavedSearchesAPIError;
    }

    expect(err?.status).toBe(412);
    expect(err?.code).toBe("conflict");
    expect(err?.reason).toBe("stale_etag");
    expect(err?.message).toBe("saved searches have changed since last load; refetch and retry");
  });
});

describe("createMockSavedSearchesBackend", () => {
  test("starts empty and round-trips replace then list", async () => {
    const mock = createMockSavedSearchesBackend();
    const empty = await mock.list();
    const after = await mock.replace([{ name: "x", query: "y" }]);
    const reread = await mock.list();

    expect(empty.searches).toEqual([]);
    expect(after.searches).toEqual([{ name: "x", query: "y" }]);
    expect(after.etag).not.toBe(empty.etag);
    expect(reread.searches).toEqual(after.searches);
    expect(reread.etag).toBe(after.etag);
  });

  test("replace() canonicalizes input and enforces If-Match", async () => {
    const mock = createMockSavedSearchesBackend();
    const after = await mock.replace([
      { name: "", query: "  " },
      { name: "A", query: "q1" },
      { name: "a", query: "q2" },
    ]);

    expect(after.searches).toEqual([{ name: "a", query: "q2" }]);
    await expect(mock.replace([{ name: "x", query: "z" }], '"sha256:bogus"')).rejects.toMatchObject({
      status: 412,
      code: "conflict",
      reason: "stale_etag",
    });
  });

  test("returned searches are isolated from internal state", async () => {
    const mock = createMockSavedSearchesBackend();
    const first = await mock.replace([{ name: "x", query: "y" }]);
    first.searches[0]!.query = "MUTATED";

    const reread = await mock.list();

    expect(reread.searches).toEqual([{ name: "x", query: "y" }]);
  });
});
