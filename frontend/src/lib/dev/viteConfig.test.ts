import { describe, expect, it } from "vite-plus/test";
import config, {
  resolveBrowserTestWorkers,
  resolveUnitTestWorkers,
  resolveViteAllowedHosts,
  resolveViteHmr,
  resolveViteServerPort,
  webSocketDebugEnabled,
} from "../../../vite.config";

describe("vite config", () => {
  it("bounds browser test concurrency in CI", () => {
    expect(resolveBrowserTestWorkers({})).toBeUndefined();
    expect(resolveBrowserTestWorkers({ CI: "1" })).toBe(2);
  });

  it("uses the guaranteed CI cores for unit tests", () => {
    expect(resolveUnitTestWorkers({})).toBeUndefined();
    expect(resolveUnitTestWorkers({ CI: "1" })).toBe(14);
    expect(resolveUnitTestWorkers({ CI: "1", KENN_FORGE_CI_WORKERS: "2" })).toBe(2);
  });

  it("runs unit tests in worker threads", () => {
    expect(config.test.projects?.[0]).toMatchObject({
      test: {
        pool: "threads",
      },
    });
  });

  it("prebundles frontend browser-test dependencies", () => {
    const includes = config.optimizeDeps?.include ?? [];

    expect(includes).toContain("shiki");
    expect(includes).toContain("@lucide/svelte/icons/list-chevrons-down-up");
    expect(includes).toContain("@lucide/svelte/icons/list-chevrons-up-down");
  });

  it("pins the dev server listener to loopback while HMR follows the page origin", () => {
    expect(config.server?.host).toBe("127.0.0.1");
    expect(config.server?.port).toBe(5174);
    expect(config.server?.strictPort).toBe(true);
    expect(config.server?.hmr).toEqual({
      path: "/__vite_hmr",
    });
  });

  it("keeps API proxy connections open for SSE streams", () => {
    const proxy = config.server?.proxy;
    expect(proxy).toBeDefined();
    expect(typeof proxy).toBe("object");
    if (!proxy || typeof proxy !== "object" || Array.isArray(proxy)) {
      throw new Error("expected object proxy config");
    }

    const apiProxy = proxy["/api"];
    expect(apiProxy).toMatchObject({
      changeOrigin: true,
      timeout: 0,
      proxyTimeout: 0,
    });
    expect(apiProxy).not.toMatchObject({ ws: true });
  });

  it("proxies terminal websocket upgrades under /ws only", () => {
    const proxy = config.server?.proxy;
    expect(proxy).toBeDefined();
    expect(typeof proxy).toBe("object");
    if (!proxy || typeof proxy !== "object" || Array.isArray(proxy)) {
      throw new Error("expected object proxy config");
    }

    expect(proxy["/ws"]).toMatchObject({
      changeOrigin: true,
      ws: true,
    });
    expect(proxy["/ws"]).not.toHaveProperty("configure");
  });

  it("requires explicit opt-in for websocket proxy diagnostics", () => {
    expect(webSocketDebugEnabled({})).toBe(false);
    expect(webSocketDebugEnabled({ KENN_FORGE_WS_DEBUG: "0" })).toBe(false);
    expect(webSocketDebugEnabled({ KENN_FORGE_WS_DEBUG: "false" })).toBe(false);
    expect(webSocketDebugEnabled({ KENN_FORGE_WS_DEBUG: "1" })).toBe(true);
    expect(webSocketDebugEnabled({ KENN_FORGE_WS_DEBUG: "true" })).toBe(true);
    expect(webSocketDebugEnabled({ KENN_FORGE_WS_DEBUG: "yes" })).toBe(true);
  });

  it("uses the Vite CLI port for the dev server listener", () => {
    expect(resolveViteServerPort(["vite", "--port", "4173"])).toBe(4173);
    expect(resolveViteServerPort(["vite", "--port=4180"])).toBe(4180);
    expect(resolveViteServerPort(["vite"])).toBe(5174);
  });

  it("allows explicitly configured tailnet hosts", () => {
    expect(resolveViteAllowedHosts({})).toBeUndefined();
    expect(
      resolveViteAllowedHosts({
        KENN_FORGE_VITE_ALLOWED_HOSTS: "forge.example.test, another.example.test",
      }),
    ).toEqual(["forge.example.test", "another.example.test"]);
  });

  it("can advertise HMR through the tailnet HTTPS endpoint", () => {
    expect(
      resolveViteHmr({
        KENN_FORGE_VITE_HMR_HOST: "forge.example.test",
        KENN_FORGE_VITE_HMR_PROTOCOL: "wss",
        KENN_FORGE_VITE_HMR_CLIENT_PORT: "443",
      }),
    ).toEqual({
      protocol: "wss",
      host: "forge.example.test",
      clientPort: 443,
      path: "/__vite_hmr",
    });
  });
});
