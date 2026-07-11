// Settings sidebar search on kit SettingsLayout's sidebarHeader snippet: the
// host owns filtering (kit just renders the category list it is given), so
// this covers the app-side contract — keyword matches, the empty-state
// notice, and that clearing the query restores the full grouped nav and the
// prior selection.

import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, type MountedBrowserApp } from "./test/browserAppHarness.js";

const WAIT = 10_000;

function navLabels(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>(".kit-settings__nav-label")).map(
    (el) => el.textContent?.trim() ?? "",
  );
}

describe("settings sidebar search", () => {
  let mounted: MountedBrowserApp | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
  });

  it("filters categories by keywords, shows an empty notice, and restores on clear", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/settings");
    await vi.waitFor(() => expect(navLabels()).toHaveLength(8), WAIT);

    const search = document.querySelector<HTMLInputElement>(".kit-settings__sidebar-header input[type='search']");
    expect(search).not.toBeNull();

    const setQuery = async (value: string) => {
      search!.value = value;
      search!.dispatchEvent(new Event("input", { bubbles: true }));
      await Promise.resolve();
    };

    // "ligatures" is a search-only keyword: it appears in no label, group,
    // or summary, so a hit proves keyword matching.
    await setQuery("ligatures");
    await vi.waitFor(() => expect(navLabels()).toEqual(["Terminal"]), WAIT);
    // The Workspace group heading survives for its remaining item.
    expect(
      Array.from(document.querySelectorAll(".kit-settings__group-title")).map((el) => el.textContent?.trim()),
    ).toEqual(["Workspace"]);

    await setQuery("no such setting anywhere");
    await vi.waitFor(() => expect(navLabels()).toEqual([]), WAIT);
    expect(document.querySelector(".settings-page")?.textContent).toContain("No matching settings");

    await setQuery("");
    await vi.waitFor(() => expect(navLabels()).toHaveLength(8), WAIT);
    expect(document.querySelector(".settings-page")?.textContent).not.toContain("No matching settings");
  });

  it("keeps the selected category while it is filtered out and restores it on clear", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/settings");
    await vi.waitFor(() => expect(navLabels()).toHaveLength(8), WAIT);

    const terminalButton = Array.from(document.querySelectorAll<HTMLButtonElement>(".kit-settings__nav-item")).find(
      (btn) => btn.textContent?.includes("Terminal"),
    );
    terminalButton!.click();
    await vi.waitFor(() => {
      expect(document.querySelector(".kit-settings__nav-item--active")?.textContent).toContain("Terminal");
    }, WAIT);

    const search = document.querySelector<HTMLInputElement>(".kit-settings__sidebar-header input[type='search']")!;
    search.value = "fleet";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    // Terminal is filtered out; kit's display falls back to the first
    // visible category without committing it to the bound selection.
    await vi.waitFor(() => expect(navLabels()).toEqual(["Fleet federation"]), WAIT);

    search.value = "";
    search.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.waitFor(() => {
      expect(document.querySelector(".kit-settings__nav-item--active")?.textContent).toContain("Terminal");
    }, WAIT);
  });

  it("'Back to app' routes to an in-app view rather than browser history", async () => {
    await page.viewport(1280, 900);
    mounted = await mountBrowserApp("/settings");
    await vi.waitFor(() => expect(navLabels()).toHaveLength(8), WAIT);

    // The fix's contract is that this control must not fall back to
    // window.history.back(): on a direct or bookmarked /settings entry the
    // previous history entry can be an unrelated site, so history.back() would
    // navigate out of middleman. Asserting the resulting route alone is not a
    // reliable guard here — the harness's leftover history can make back() land
    // on "/" by coincidence — so stub back() (also keeps a regressed impl from
    // navigating the test page away) and assert it is never reached.
    const backSpy = vi.spyOn(window.history, "back").mockImplementation(() => {});
    const back = document.querySelector<HTMLButtonElement>(".settings-page .back-button");
    expect(back).not.toBeNull();
    back!.click();
    // backToApp runs synchronously on click, so this fails fast on a regression.
    expect(backSpy).not.toHaveBeenCalled();

    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);
    expect(document.querySelector(".settings-page")).toBeNull();
    expect(new URL(window.location.href).pathname).toBe("/");
    backSpy.mockRestore();
  });
});
