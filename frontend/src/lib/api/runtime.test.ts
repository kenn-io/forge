import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { createRuntimeClient, detectUnauthorized } from "./runtime.js";
import { isAuthenticated, setAuthenticated } from "../stores/auth.svelte.js";

describe("runtime", () => {
  it("serializes activity type filters as comma-separated query params", async () => {
    let requestURL = "";
    const fetchMock = vi.fn(async (input: URL | RequestInfo) => {
      requestURL = input instanceof Request ? input.url : String(input);
      return new Response(JSON.stringify({ items: [], capped: false }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });

    const client = createRuntimeClient(fetchMock, "https://middleman.test/api/v1");
    await client.GET("/activity", {
      params: { query: { types: ["comment", "review"] } },
    });

    expect(requestURL).toContain("types=comment,review");
    expect(requestURL).not.toContain("types=comment&types=review");
  });
});

afterEach(() => setAuthenticated());

describe("detectUnauthorized", () => {
  it("flips auth state to false on a 401 response", async () => {
    const wrapped = detectUnauthorized(async () => new Response(null, { status: 401 }));
    await wrapped("http://x/api/v1/snapshot");
    expect(isAuthenticated()).toBe(false);
  });

  it("leaves auth state intact on a 200 response", async () => {
    const wrapped = detectUnauthorized(async () => new Response("{}", { status: 200 }));
    await wrapped("http://x/api/v1/snapshot");
    expect(isAuthenticated()).toBe(true);
  });
});
