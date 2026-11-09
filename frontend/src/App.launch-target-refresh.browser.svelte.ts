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

it("refreshes agent availability on a visibility return and skips hidden tabs", async () => {
  await page.viewport(1400, 900);
  let reads = 0;
  let available = true;
  mounted = await mountBrowserApp("/pulls/github/acme/widgets/42", {
    overrides: [
      (request) => {
        if (request.url.pathname === "/api/v1/settings") return jsonResponse(settings(false));
        if (request.url.pathname !== "/api/v1/settings/local") return null;
        reads += 1;
        return jsonResponse(settings(available));
      },
    ],
  });
  // The detail mounts only after the startup settings snapshot has hydrated.
  await expect.element(page.getByText("Add browser regression coverage", { exact: true }).first()).toBeVisible();
  const options = page.getByRole("button", { name: "Create Workspace options" });
  expect(options.element().hasAttribute("disabled")).toBe(true);

  document.dispatchEvent(new Event("visibilitychange"));
  await vi.waitFor(() => expect(reads).toBe(1));
  await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(false));

  available = false;
  window.dispatchEvent(new Event("focus"));
  await vi.waitFor(() => expect(reads).toBe(2));
  await vi.waitFor(() => expect(options.element().hasAttribute("disabled")).toBe(true));

  const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  const commands = vi.spyOn(mounted.runtime, "runCommand");
  window.dispatchEvent(new Event("focus"));
  document.dispatchEvent(new Event("visibilitychange"));
  await Promise.all(commands.mock.results.map((result) => result.value.exit));
  expect(reads).toBe(2);

  visibility.mockReturnValue("visible");
  document.dispatchEvent(new Event("visibilitychange"));
  await vi.waitFor(() => expect(reads).toBe(3));
});
