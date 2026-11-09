import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { jsonResponse, mockSettings } from "./test/mockApiFetch.js";

const settings = (available: boolean) => ({
  ...mockSettings,
  quick_actions: [{ label: "Review", agent: "claude", prompt: "Review this change" }],
  launch_targets: [
    {
      key: "claude",
      label: "Claude",
      kind: "agent",
      source: "builtin",
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
])("refreshes %s launch choices after install and removal", async (_kind, route) => {
  await page.viewport(1400, 900);
  let visible = true;
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => (visible ? "visible" : "hidden"));
  vi.spyOn(document, "hidden", "get").mockImplementation(() => !visible);
  const setVisible = (value: boolean) => {
    visible = value;
    document.dispatchEvent(new Event("visibilitychange"));
  };
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
  expect(options.element().hasAttribute("disabled")).toBe(true);
  setVisible(false);
  available = true;
  setVisible(true);
  await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(false));
  await options.click();
  await expect.element(page.getByRole("menuitem", { name: "Claude" })).toBeVisible();
  await options.click();
  const quickActions = page.getByRole("button", { name: "Quick actions" });
  await quickActions.click();
  await expect.element(page.getByRole("menuitem", { name: "Review Claude" })).toBeEnabled();
  await quickActions.click();
  setVisible(false);
  available = false;
  setVisible(true);
  await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(true));
  await quickActions.click();
  await expect
    .element(page.getByRole("menuitem", { name: "Review claude not found on PATH" }))
    .toHaveAttribute("title", "claude not found on PATH");
});
