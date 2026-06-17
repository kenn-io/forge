import { createRequire } from "node:module";
import path from "node:path";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { svelteTesting } from "@testing-library/svelte/vite";
import { defaultClientConditions, searchForWorkspaceRoot, type Plugin, type ProxyOptions, type UserConfig } from "vite";
import { defineProject, type TestProjectInlineConfiguration } from "vite-plus/test/config";
import type { InlineConfig } from "vite-plus/test/node";
import { resolveDevApiUrl } from "./src/lib/dev/apiProxyTarget.ts";
import { healthcheckPlugin } from "./src/lib/dev/healthcheckPlugin.ts";

const require = createRequire(import.meta.url);
const testingLibrarySvelteEntry = require.resolve("@testing-library/svelte");

// resolveDevApiUrl() prefers MIDDLEMAN_API_URL, which dev-ephemeral sets
// to the generated backend URL before starting Vite.
const apiUrl = resolveDevApiUrl();
const devServerPort = resolveViteServerPort();
const devServerAllowedHosts = resolveViteAllowedHosts();
const devServerHmr = resolveViteHmr(devServerPort);
const workspaceRoot = searchForWorkspaceRoot(process.cwd());
const uiPkg = path.resolve(process.cwd(), "../packages/ui");
const uiIndex = path.resolve(process.cwd(), "../packages/ui/src/index.ts");
const uiGeneratedClient = path.resolve(process.cwd(), "../packages/ui/src/api/generated/client.ts");
const uiGeneratedSchema = path.resolve(process.cwd(), "../packages/ui/src/api/generated/schema.ts");
const uiApiTypes = path.resolve(process.cwd(), "../packages/ui/src/api/types.ts");
const uiApiCsrf = path.resolve(process.cwd(), "../packages/ui/src/api/csrf.ts");
const uiRoutes = path.resolve(process.cwd(), "../packages/ui/src/routes.ts");
const uiStoreDetail = path.resolve(process.cwd(), "../packages/ui/src/stores/detail.svelte.ts");
const uiStoreEvents = path.resolve(process.cwd(), "../packages/ui/src/stores/events.svelte.ts");
const uiStorePulls = path.resolve(process.cwd(), "../packages/ui/src/stores/pulls.svelte.ts");
const uiStoreIssues = path.resolve(process.cwd(), "../packages/ui/src/stores/issues.svelte.ts");
const uiStoreActivity = path.resolve(process.cwd(), "../packages/ui/src/stores/activity.svelte.ts");
const uiStoreSync = path.resolve(process.cwd(), "../packages/ui/src/stores/sync.svelte.ts");
const uiStoreDiff = path.resolve(process.cwd(), "../packages/ui/src/stores/diff.svelte.ts");
const uiStoreGrouping = path.resolve(process.cwd(), "../packages/ui/src/stores/grouping.svelte.ts");
const uiStoreDetailActivityView = path.resolve(
  process.cwd(),
  "../packages/ui/src/stores/detail-activity-view.svelte.ts",
);
const uiStoreSettings = path.resolve(process.cwd(), "../packages/ui/src/stores/settings.svelte.ts");

function devApiUrlPlugin(url: string): Plugin {
  return {
    name: "middleman-dev-api-url",
    apply: "serve",
    transformIndexHtml() {
      return [
        {
          tag: "script",
          children: `window.__MIDDLEMAN_DEV_API_URL__ = ${JSON.stringify(url)};`,
          injectTo: "head-prepend",
        },
      ];
    },
  };
}

export function resolveViteServerPort(argv: readonly string[] = process.argv): number {
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg) continue;
    if (arg === "--port" && i + 1 < argv.length) {
      const next = argv[i + 1];
      const parsed = parsePort(next);
      if (parsed !== null) return parsed;
    }
    if (arg.startsWith("--port=")) {
      const parsed = parsePort(arg.slice("--port=".length));
      if (parsed !== null) return parsed;
    }
  }
  return 5174;
}

function parsePort(value: string | undefined): number | null {
  if (!value) return null;
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    return null;
  }
  return parsed;
}

function parseHostList(value: string | undefined): string[] {
  return (value ?? "")
    .split(",")
    .map((host) => host.trim())
    .filter(Boolean);
}

export function resolveViteAllowedHosts(env: Record<string, string | undefined> = process.env): string[] | undefined {
  const hosts = parseHostList(env.MIDDLEMAN_VITE_ALLOWED_HOSTS);
  return hosts.length > 0 ? hosts : undefined;
}

export function resolveViteHmr(port = resolveViteServerPort(), env: Record<string, string | undefined> = process.env) {
  const protocol = env.MIDDLEMAN_VITE_HMR_PROTOCOL === "wss" ? "wss" : "ws";
  const host = env.MIDDLEMAN_VITE_HMR_HOST?.trim() || "127.0.0.1";
  const clientPort = parsePort(env.MIDDLEMAN_VITE_HMR_CLIENT_PORT) ?? port;

  return {
    protocol,
    host,
    clientPort,
    path: "/__vite_hmr",
  };
}

function logWebSocketProxyRequests(): NonNullable<ProxyOptions["configure"]> {
  return (proxy) => {
    proxy.on("proxyReqWs", (_proxyReq, req, socket) => {
      const url = req.url ?? "<unknown>";
      console.info(`[vite:ws-proxy] open ${url}`);
      socket.on("error", (err) => {
        console.error(`[vite:ws-proxy] socket error ${url}: ${err.message}`);
      });
    });
    proxy.on("error", (err, req) => {
      const url = req?.url ?? "<unknown>";
      console.error(`[vite:ws-proxy] error ${url}: ${err.message}`);
    });
    proxy.on("close", (_proxyRes, _proxySocket, proxyHead) => {
      const headLength = proxyHead instanceof Buffer ? proxyHead.length : 0;
      console.info(`[vite:ws-proxy] close proxyHeadBytes=${headLength}`);
    });
  };
}

export function webSocketDebugEnabled(env: Record<string, string | undefined> = process.env): boolean {
  switch (env.MIDDLEMAN_WS_DEBUG?.trim().toLowerCase()) {
    case "1":
    case "true":
    case "yes":
    case "on":
      return true;
    default:
      return false;
  }
}

function terminalWebSocketProxy(url: string): ProxyOptions {
  const proxy: ProxyOptions = {
    target: url,
    changeOrigin: true,
    ws: true,
  };
  if (webSocketDebugEnabled()) {
    proxy.configure = logWebSocketProxyRequests();
  }
  return proxy;
}

// The "unit" project preserves the prior flat test config: jsdom plus the
// localStorage/elementFromPoint shims in setup.ts. The browser glob exclude
// keeps *.browser.svelte.ts files off this project so they never double-run.
const unitTestProject = {
  extends: true,
  test: {
    name: "unit",
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.{test,spec}.?(c|m)[jt]s?(x)", "../packages/ui/src/**/*.{test,spec}.?(c|m)[jt]s?(x)"],
    exclude: ["tests/e2e/**", "tests/e2e-full/**", "node_modules/**", "src/**/*.browser.svelte.ts"],
  },
} satisfies TestProjectInlineConfiguration;

// The "browser" project runs *.browser.svelte.ts specs in a real headless
// chromium page via the Playwright provider. It intentionally omits setup.ts:
// a real page has native localStorage and elementFromPoint, so the jsdom shims
// would be wrong here.
//
// The Playwright provider is loaded with a dynamic import inside an async
// project factory rather than a top-level import on purpose: "vite-plus/test/
// browser-playwright" transitively pulls in the browser runtime (ws's
// WebSocketServer, the @vitest/browser client), which fails to evaluate under
// the jsdom unit runner. src/lib/dev/viteConfig.test.ts and
// healthcheckPlugin.test.ts import this config module directly, so a static
// browser import would crash those unit tests. The factory body only runs when
// the browser project itself is initialized, keeping the browser runtime out of
// the unit project's module graph.
//
// resolve.conditions forces the browser/client export conditions. vite-plugin-svelte
// only picks Svelte's "browser" export ("./src/index-client.js", which exposes
// mount()) when the environment is named/consumed as a client; under the browser
// test runtime it otherwise falls through to Svelte's server entry and mount()
// throws lifecycle_function_unavailable. Spreading Vite's defaultClientConditions
// keeps the standard "module"/"development|production" placeholders intact.
const browserTestProject = defineProject(async () => {
  const { playwright } = await import("vite-plus/test/browser-playwright");
  return {
    extends: true,
    resolve: {
      conditions: [...defaultClientConditions],
    },
    test: {
      name: "browser",
      include: ["src/**/*.browser.svelte.ts"],
      browser: {
        enabled: true,
        provider: playwright() as never,
        instances: [{ browser: "chromium" }],
        headless: true,
      },
    },
  };
});

const config = {
  base: "/",
  // The Go server serves this build under a configurable base_path (default
  // "/", e.g. "/middleman/" behind a reverse proxy) by rewriting index.html's
  // <script src>/<link href> at request time. That rewrite only reaches HTML,
  // not URLs baked inside JS bundles. An asset URL emitted as an absolute root
  // path -- new URL("/assets/x.js", import.meta.url) -- resolves against the
  // origin and drops the base path prefix, so it 404s behind a subpath proxy
  // (notably the Pierre diff web worker). Emitting JS-referenced asset URLs as
  // relative makes them resolve against the entry chunk's own already-prefixed
  // location instead. HTML keeps default absolute URLs so the server-side
  // index.html rewrite still applies. Guarded by scripts/check-asset-base-paths.mjs.
  experimental: {
    renderBuiltUrl(_filename, { hostType }) {
      return hostType === "js" ? { relative: true } : undefined;
    },
  },
  plugins: [healthcheckPlugin(), devApiUrlPlugin(apiUrl), svelte(), svelteTesting()],
  resolve: {
    alias: [
      {
        find: /^@testing-library\/svelte$/,
        replacement: testingLibrarySvelteEntry,
      },
      {
        find: /^@middleman\/ui$/,
        replacement: uiIndex,
      },
      {
        find: /^@middleman\/ui\/api\/client$/,
        replacement: uiGeneratedClient,
      },
      {
        find: /^@middleman\/ui\/api\/schema$/,
        replacement: uiGeneratedSchema,
      },
      {
        find: /^@middleman\/ui\/api\/types$/,
        replacement: uiApiTypes,
      },
      {
        find: /^@middleman\/ui\/api\/csrf$/,
        replacement: uiApiCsrf,
      },
      {
        find: /^@middleman\/ui\/routes$/,
        replacement: uiRoutes,
      },
      {
        find: /^@middleman\/ui\/stores\/detail$/,
        replacement: uiStoreDetail,
      },
      {
        find: /^@middleman\/ui\/stores\/events$/,
        replacement: uiStoreEvents,
      },
      {
        find: /^@middleman\/ui\/stores\/pulls$/,
        replacement: uiStorePulls,
      },
      {
        find: /^@middleman\/ui\/stores\/issues$/,
        replacement: uiStoreIssues,
      },
      {
        find: /^@middleman\/ui\/stores\/activity$/,
        replacement: uiStoreActivity,
      },
      {
        find: /^@middleman\/ui\/stores\/sync$/,
        replacement: uiStoreSync,
      },
      {
        find: /^@middleman\/ui\/stores\/diff$/,
        replacement: uiStoreDiff,
      },
      {
        find: /^@middleman\/ui\/stores\/grouping$/,
        replacement: uiStoreGrouping,
      },
      {
        find: /^@middleman\/ui\/stores\/detail-activity-view$/,
        replacement: uiStoreDetailActivityView,
      },
      {
        find: /^@middleman\/ui\/stores\/settings$/,
        replacement: uiStoreSettings,
      },
    ],
  },
  optimizeDeps: {
    exclude: ["@middleman/ui"],
  },
  server: {
    host: "127.0.0.1",
    port: devServerPort,
    strictPort: true,
    ...(devServerAllowedHosts ? { allowedHosts: devServerAllowedHosts } : {}),
    hmr: devServerHmr,
    fs: { allow: [workspaceRoot, uiPkg] },
    proxy: {
      "/api": {
        target: apiUrl,
        changeOrigin: true,
        timeout: 0,
        proxyTimeout: 0,
      },
      "/ws": terminalWebSocketProxy(apiUrl),
    },
  },
  test: {
    projects: [defineProject(unitTestProject), browserTestProject],
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
} satisfies UserConfig & { test: InlineConfig };

export default config;
