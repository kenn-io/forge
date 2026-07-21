import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { fetchKataDaemons, kataProxyPath } from "./daemons.js";

function requestURL(input: RequestInfo | URL): URL {
  if (typeof Request !== "undefined" && input instanceof Request) {
    return new URL(input.url);
  }
  if (input instanceof URL) {
    return input;
  }
  return new URL(String(input), window.location.origin);
}

describe("kata api helpers", () => {
  afterEach(() => {
    delete window.__BASE_PATH__;
    vi.restoreAllMocks();
  });

  it("loads daemon roster from the middleman API", async () => {
    let seenURL: URL | undefined;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenURL = requestURL(input);
      return new Response(
        JSON.stringify({
          daemons: [
            {
              id: "home",
              url: "http://127.0.0.1:7777",
              default: true,
              auth: "none",
              health: "connected",
            },
            {
              id: "work",
              url: "https://work.example",
              default: false,
              auth: "token",
              health: "auth_required",
            },
          ],
        }),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    });

    const daemons = await fetchKataDaemons(fetchMock);

    expect(seenURL?.pathname).toBe("/api/v1/kata/daemons");
    expect(daemons.map((d) => d.id)).toEqual(["home", "work"]);
    expect(daemons[1]?.health).toBe("auth_required");
  });

  it("preserves the operator-facing hint", async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            daemons: [
              {
                id: "local",
                url: "",
                default: true,
                auth: "none",
                health: "down",
                hint: "local daemon not running; run `kata daemon start`",
              },
            ],
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
    );

    const daemons = await fetchKataDaemons(fetchMock);

    expect(daemons[0]?.hint).toBe("local daemon not running; run `kata daemon start`");
  });

  it("warns and normalizes malformed daemon arrays to an empty roster", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    let seenURL: URL | undefined;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenURL = requestURL(input);
      return new Response(JSON.stringify({ daemons: null }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });

    const daemons = await fetchKataDaemons(fetchMock);

    expect(seenURL?.pathname).toBe("/api/v1/kata/daemons");
    expect(daemons).toEqual([]);
    expect(warn).toHaveBeenCalledWith("fetchKataDaemons: malformed daemon roster response");
  });

  it("uses the configured base path for daemon roster and proxy URLs", async () => {
    window.__BASE_PATH__ = "/middleman/";
    let seenURL: URL | undefined;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      seenURL = requestURL(input);
      return new Response(
        JSON.stringify({
          daemons: [
            {
              id: "home",
              url: "http://127.0.0.1:7777",
              default: true,
              auth: "none",
              health: "connected",
            },
          ],
        }),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    });

    const daemons = await fetchKataDaemons(fetchMock);

    expect(seenURL?.pathname).toBe("/middleman/api/v1/kata/daemons");
    expect(daemons.map((d) => d.id)).toEqual(["home"]);
    expect(kataProxyPath("/api/v1/projects?include=stats")).toBe(
      "/middleman/api/v1/kata/proxy/api/v1/projects?include=stats",
    );
  });

  it("returns an empty roster when the control endpoint is absent", async () => {
    const fetchMock = vi.fn(async () => new Response("not found", { status: 404 }));

    await expect(fetchKataDaemons(fetchMock)).resolves.toEqual([]);
  });
});
