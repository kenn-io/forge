import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { writeTerminalClipboardThroughServer } from "./terminalClipboardFallback";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("terminal clipboard server fallback", () => {
  it("posts JSON text to the same-origin endpoint", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(null, { status: 204 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await writeTerminalClipboardThroughServer("copied in Firefox");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [input, init] = fetchMock.mock.calls[0]!;
    const request = new Request(input, init);
    expect(request.url).toBe(`${window.location.origin}/api/v1/terminal/clipboard`);
    expect(request.method).toBe("POST");
    expect(request.headers.get("Content-Type")).toBe("application/json");
    await expect(request.json()).resolves.toEqual({
      text: "copied in Firefox",
    });
  });

  it("rejects when Kenn Forge cannot write the local clipboard", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 503 })),
    );

    await expect(writeTerminalClipboardThroughServer("blocked")).rejects.toThrow(
      "terminal clipboard fallback failed (503)",
    );
  });
});
