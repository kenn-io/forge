import { describe, expect, it, vi } from "vite-plus/test";

import { csrfFetch } from "./csrf.js";

describe("csrfFetch", () => {
  it("forwards fetch init options when called with a URL", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = csrfFetch(inner);
    await fetch("https://middleman.test/api/v1/settings", {
      method: "POST",
      body: JSON.stringify({ theme: "dark" }),
      headers: { "X-Test": "present" },
    });

    expect(request?.url).toBe("https://middleman.test/api/v1/settings");
    expect(request?.method).toBe("POST");
    expect(request?.headers.get("X-Test")).toBe("present");
    await expect(request?.text()).resolves.toBe('{"theme":"dark"}');
  });

  it("attaches the middleman csrf proof header to mutation requests", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = csrfFetch(inner);
    await fetch("https://middleman.test/api/v1/messages/saved-searches", {
      method: "PUT",
      body: JSON.stringify({ searches: [] }),
    });

    expect(request?.headers.get("X-Middleman-Csrf")).toBe("1");
  });

  it("does not attach the middleman csrf proof header to reads", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = csrfFetch(inner);
    await fetch("https://middleman.test/api/v1/messages/saved-searches");

    expect(request?.headers.has("X-Middleman-Csrf")).toBe(false);
  });
});
