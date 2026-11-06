import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { createMockApiFetch, jsonResponse } from "./test/mockApiFetch.js";
import type { Issue } from "./lib/api/types.js";

let mounted: MountedBrowserApp | null = null;

beforeEach(async () => {
  localStorage.clear();
  await page.viewport(1280, 900);
});

afterEach(async () => {
  mounted?.unmount();
  mounted = null;
  localStorage.clear();
  await resetKeyboardModuleState();
});

function rowTitles(): string[] {
  return [...document.querySelectorAll(".issue-list .issue-item")].map((row) => row.textContent ?? "");
}

describe("issue sidebar label filter", () => {
  it("keeps issues with every required label and none of the excluded ones", async () => {
    const api = createMockApiFetch();
    const [base]: Issue[] = await (await api.fetch("/api/v1/issues")).json();
    const labeled = (...names: string[]) => names.map((name) => ({ name, color: "d73a4a", is_default: false }));
    const issues = [
      { ...base!, ID: 801, Number: 801, Title: "Crash on save", labels: labeled("bug") },
      { ...base!, ID: 802, Number: 802, Title: "Known crash", labels: labeled("Bug", "wontfix") },
      { ...base!, ID: 803, Number: 803, Title: "Write the guide", labels: labeled("docs") },
    ];
    mounted = await mountBrowserApp("/issues", {
      overrides: [
        (req) => (req.method === "GET" && req.url.pathname === "/api/v1/issues" ? jsonResponse(issues) : null),
      ],
    });
    await vi.waitFor(() => expect(rowTitles()).toHaveLength(3));

    await page.elementLocator(document.querySelector(".issue-list .compact-filter-menu button")!).click();
    await page.getByRole("button", { name: /^Labels/ }).click();
    const flyout = page.getByRole("dialog", { name: "Labels" });
    // Labels differing only in case across repositories are one choice.
    await vi.waitFor(() => {
      const counts = [...document.querySelectorAll(".kit-filter-dropdown__submenu .label-filter__row")].map((row) => [
        row.querySelector(".label-filter__name")?.textContent,
        row.querySelector(".label-filter__count")?.textContent,
      ]);
      expect(counts).toEqual([
        ["bug", "2"],
        ["docs", "1"],
        ["wontfix", "1"],
      ]);
    });
    await flyout.getByRole("button", { name: /^bug/ }).click();
    await flyout.getByRole("button", { name: /^wontfix/ }).click();
    await flyout.getByRole("button", { name: /^wontfix/ }).click();

    await vi.waitFor(() => {
      const titles = rowTitles();
      expect(titles).toHaveLength(1);
      expect(titles[0]).toContain("Crash on save");
    });
    expect(document.querySelector(".issue-list .list-count-chip")?.textContent).toContain("1");
  });
});
