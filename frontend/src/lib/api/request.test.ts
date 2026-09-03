import { describe, expect, it, vi } from "vite-plus/test";

import { normalizedFetch } from "./request.js";

describe("normalizedFetch", () => {
  it("forwards fetch init options as a Request", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = normalizedFetch(inner);
    await fetch("https://forge.test/api/v1/settings", {
      method: "POST",
      body: JSON.stringify({ theme: "dark" }),
      headers: { "X-Test": "present" },
    });

    expect(request?.url).toBe("https://forge.test/api/v1/settings");
    expect(request?.method).toBe("POST");
    expect(request?.headers.get("X-Test")).toBe("present");
    await expect(request?.text()).resolves.toBe('{"theme":"dark"}');
  });

  it("serializes URLSearchParams for custom transports", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = normalizedFetch(inner);
    await fetch("https://forge.test/api/v1/search", {
      method: "POST",
      body: new URLSearchParams({ q: "notifications" }),
    });

    expect(request?.headers.get("Content-Type")).toBe("application/x-www-form-urlencoded;charset=UTF-8");
    await expect(request?.text()).resolves.toBe("q=notifications");
  });
});
