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
    expect(request?.headers.get("Content-Type")).toBe("application/json");
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

  it("does not overwrite generated multipart content types", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const body = new FormData();
    body.append("upload", new Blob(["avatar"]), "avatar.txt");

    const fetch = csrfFetch(inner);
    await fetch("https://middleman.test/api/v1/uploads", { method: "POST", body });

    expect(request?.headers.get("Content-Type")).not.toBe("application/json");
  });

  it("does not overwrite generated form content types", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = csrfFetch(inner);
    await fetch("https://middleman.test/api/v1/search", {
      method: "POST",
      body: new URLSearchParams({ q: "notifications" }),
    });

    expect(request?.headers.get("Content-Type")).not.toBe("application/json");
    await expect(request?.text()).resolves.toBe("q=notifications");
  });

  it("accepts URLSearchParams from another browser realm", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });
    const frame = document.createElement("iframe");
    document.body.append(frame);
    const OtherURLSearchParams = frame.contentWindow?.URLSearchParams;
    if (!OtherURLSearchParams) throw new Error("missing iframe URLSearchParams");

    const fetch = csrfFetch(inner);
    await fetch("https://middleman.test/api/v1/search", {
      method: "POST",
      body: new OtherURLSearchParams({ q: "notifications" }) as BodyInit,
    });

    frame.remove();
    expect(request?.headers.get("Content-Type")).not.toBe("application/json");
    await expect(request?.text()).resolves.toBe("q=notifications");
  });

  it("replaces Request text/plain content types on generated JSON mutations", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = csrfFetch(inner);
    await fetch(
      new Request("https://middleman.test/api/v1/ready", {
        method: "POST",
        body: JSON.stringify({ ready: true }),
      }),
    );

    expect(request?.headers.get("Content-Type")).toBe("application/json");
    await expect(request?.json()).resolves.toEqual({ ready: true });
  });

  it("adds JSON content type to zero-body mutation requests", async () => {
    let request: Request | null = null;
    const inner = vi.fn(async (input: RequestInfo | URL) => {
      request = input instanceof Request ? input : new Request(input);
      return Response.json({});
    });

    const fetch = csrfFetch(inner);
    await fetch("https://middleman.test/api/v1/notifications/sync", { method: "POST" });

    expect(request?.method).toBe("POST");
    expect(request?.headers.get("Content-Type")).toBe("application/json");
    await expect(request?.text()).resolves.toBe("");
  });
});
