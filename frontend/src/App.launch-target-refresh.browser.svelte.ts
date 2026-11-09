import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, mockLaunchTargets, mockSettings } from "./test/mockApiFetch.js";

const settings = (available: boolean) => ({
  ...mockSettings,
  launch_targets: mockLaunchTargets(available),
});

let mounted: MountedBrowserApp | undefined;
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
  vi.restoreAllMocks();
});

it.each([
  ["pull request", "/pulls/github/acme/widgets/42"],
  ["issue", "/issues/github/acme/widgets/7"],
])("refreshes %s launch choices after install and removal", async (_kind, route) => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  let available = false;
  let reads = 0;
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
  await vi.advanceTimersByTimeAsync(0);
  expect(options.element().hasAttribute("disabled")).toBe(true);
  available = true;
  await vi.advanceTimersByTimeAsync(5_000);
  await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(false));
  available = false;
  await vi.advanceTimersByTimeAsync(5_000);
  await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(true));
});
