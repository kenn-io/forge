import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, mockSettings } from "./test/mockApiFetch.js";

const settings = (available: boolean) => ({
  ...mockSettings,
  launch_targets: [
    {
      key: "claude",
      label: "Claude",
      kind: "agent",
      source: "builtin",
      command: ["claude"],
      available,
      disabled_reason: available ? undefined : "claude not found on PATH",
    },
  ],
});

let mounted: MountedBrowserApp | undefined;
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

it.each([
  ["pull request", "/pulls/github/acme/widgets/42"],
  ["issue", "/issues/github/acme/widgets/7"],
])(
  "refreshes %s launch choices while visible and on return",
  async (_kind, route) => {
    await page.viewport(1400, 900);
    let reads = 0;
    let available = false;
    mounted = await mountBrowserApp(route, {
      overrides: [
        (request) => {
          if (request.url.pathname === "/api/v1/settings") return jsonResponse(settings(false));
          if (request.url.pathname !== "/api/v1/settings/local") return null;
          reads += 1;
          return jsonResponse(settings(available));
        },
      ],
    });
    const options = page.getByRole("button", { name: "Create Workspace options" });
    await expect.element(options).toBeVisible();
    await vi.waitFor(() => expect(reads).toBeGreaterThan(0));
    expect(options.element().hasAttribute("disabled")).toBe(true);
    available = true;
    await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(false), { timeout: 8000 });
    available = false;
    await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(true), { timeout: 8000 });

    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await new Promise((resolve) => setTimeout(resolve, 200));
    const hiddenReads = reads;
    available = true;
    await new Promise((resolve) => setTimeout(resolve, 5500));
    expect(reads).toBe(hiddenReads);
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(false), { timeout: 2000 });
  },
  25000,
);

it("keeps newer saved targets when a local settings read finishes later", async () => {
  const { Effect } = await import("effect");
  const { makeAppRuntime } = await import("./lib/app/runtime.js");
  const runtime = makeAppRuntime();
  const originalFetch = globalThis.fetch;
  let release!: (response: Response) => void;
  let reads = 0;
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.endsWith("/api/v1/settings/local")) {
      reads += 1;
      return new Promise<Response>((resolve) => {
        release = resolve;
      });
    }
    return originalFetch(input, init);
  }) as typeof fetch;
  const { createAppStores } = await import("./lib/app-stores.svelte.js");
  const composition = createAppStores({ runtime });
  const execution = runtime.runCommand(composition.listAgentStatusPolling, {
    operation: "test launch refresh",
    safeContext: {},
    onFailure: () => {},
  });
  try {
    await vi.waitFor(() => expect(reads).toBe(1));
    composition.stores.settings.setLaunchTargets([
      { key: "saved", label: "Saved", kind: "agent", source: "builtin", available: true },
    ]);
    release(jsonResponse(settings(false)));
    await vi.waitFor(() => expect(reads).toBe(2), { timeout: 8000 });
    expect(composition.stores.settings.getLaunchTargets().map((target) => target.key)).toEqual(["saved"]);
  } finally {
    execution.interrupt();
    await execution.exit;
    await Effect.runPromise(runtime.disposeEffect);
    globalThis.fetch = originalFetch;
  }
}, 12000);
