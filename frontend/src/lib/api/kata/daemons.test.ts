import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { KATA_DAEMON_HEADER, fetchKataDaemons, kataProxyPath, withKataDaemon } from "./daemons.js";

function recordRequest(input: RequestInfo | URL, init?: RequestInit): Request {
  if (typeof Request !== "undefined" && input instanceof Request) {
    return new Request(input, init);
  }
  if (input instanceof URL) {
    return new Request(input, init);
  }
  return new Request(new URL(String(input), window.location.origin), init);
}

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

  it("adds the selected daemon header only for same-origin proxy requests", async () => {
    const seen: Array<Request> = [];
    const inner = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(recordRequest(input, init));
      return Response.json({});
    });
    const fetch = withKataDaemon(inner, () => "work");

    await fetch("/api/v1/kata/proxy/api/v1/projects");
    await fetch("https://daemon.example/api/v1/projects", {
      headers: { [KATA_DAEMON_HEADER]: "work" },
    });

    expect(seen[0]?.headers.get(KATA_DAEMON_HEADER)).toBe("work");
    expect(seen[1]?.headers.has(KATA_DAEMON_HEADER)).toBe(false);
  });

  it("omits the daemon header when no daemon is active", async () => {
    const seen: Array<Request> = [];
    const inner = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(recordRequest(input, init));
      return Response.json({});
    });
    const fetch = withKataDaemon(inner, () => undefined);

    await fetch("/api/v1/kata/proxy/api/v1/projects");

    expect(seen[0]?.headers.has(KATA_DAEMON_HEADER)).toBe(false);
  });

  it("honors an explicit daemon header on proxy requests", async () => {
    const seen: Array<Request> = [];
    const inner = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(recordRequest(input, init));
      return Response.json({});
    });
    const fetch = withKataDaemon(inner, () => "work");

    await fetch("/api/v1/kata/proxy/api/v1/projects", {
      headers: { [KATA_DAEMON_HEADER]: "home" },
    });

    expect(seen[0]?.headers.get(KATA_DAEMON_HEADER)).toBe("home");
  });
});
