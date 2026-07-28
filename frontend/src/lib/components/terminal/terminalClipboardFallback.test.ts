import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { writeTerminalClipboardThroughServer } from "./terminalClipboardFallback";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("terminal clipboard server fallback", () => {
  it("posts text through the same-origin CSRF-protected endpoint", async () => {
    const fetchMock = vi.fn(async (_request: Request) => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await writeTerminalClipboardThroughServer("copied in Firefox");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const request = fetchMock.mock.calls[0]![0] as Request;
    expect(request.url).toBe(`${window.location.origin}/api/v1/terminal/clipboard`);
    expect(request.method).toBe("POST");
    expect(request.headers.get("Content-Type")).toBe("application/json");
    expect(request.headers.get("X-Middleman-Csrf")).toBe("1");
    await expect(request.json()).resolves.toEqual({
      text: "copied in Firefox",
    });
  });

  it("rejects when Middleman cannot write the local clipboard", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 503 })),
    );

    await expect(writeTerminalClipboardThroughServer("blocked")).rejects.toThrow(
      "terminal clipboard fallback failed (503)",
    );
  });
});
