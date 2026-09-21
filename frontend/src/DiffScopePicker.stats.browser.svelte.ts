import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { cleanup, render } from "vitest-browser-svelte";

import "./app.css";
import DiffScopePicker from "./lib/components/diff/DiffScopePicker.svelte";
import { STORES_KEY } from "./lib/context.js";

const commits = [
  {
    sha: "a71d5f0abcdef0123456789",
    message: "refactor(search): share query terms across filters and result views",
    authored_at: new Date().toISOString(),
    stats: { additions: 12345, deletions: 42 },
  },
  {
    sha: "c902f6cabcdef0123456789",
    message: "feat(api): add bounded search results",
    authored_at: new Date().toISOString(),
    stats: { additions: 0, deletions: 0 },
  },
] as const;

async function mountPicker(width: number) {
  await page.viewport(width, 500);
  const wrapper = document.createElement("div");
  wrapper.dataset.commitStatsTest = "";
  wrapper.style.cssText = `width: ${width - 40}px; margin: 20px; container-type: inline-size; display: flex; justify-content: ${width > 760 ? "flex-end" : "flex-start"}`;
  document.body.appendChild(wrapper);
  const selectCommit = vi.fn();
  const selectRange = vi.fn();
  const scope = { kind: "commit", sha: commits[0].sha };
  await render(DiffScopePicker, {
    target: wrapper,
    context: new Map([
      [
        STORES_KEY,
        {
          diff: {
            getCommits: () => commits,
            isCommitsLoading: () => false,
            getCommitsError: () => null,
            getScope: () => scope,
            loadCommits: vi.fn(),
            selectCommit,
            selectRange,
            resetToHead: vi.fn(),
          },
        },
      ],
    ]),
  });
  await page.getByRole("button", { name: /Select commit range/ }).click();
  return { wrapper, selectCommit, selectRange };
}

describe("Commit range line counts", () => {
  afterEach(() => {
    cleanup();
    document.querySelector("[data-commit-stats-test]")?.remove();
  });

  it("fits shared counts beside long messages and preserves single/range selection", async () => {
    const { wrapper, selectCommit, selectRange } = await mountPicker(1000);
    const menu = wrapper.querySelector<HTMLElement>(".diff-scope-picker__menu")!;
    expect(menu.getBoundingClientRect().width).toBe(520);
    await expect.element(page.getByLabelText("12345 additions, 42 deletions")).toBeVisible();
    await expect.element(page.getByLabelText("0 additions, 0 deletions")).toBeVisible();
    const row = wrapper.querySelector<HTMLElement>(".commit-item")!;
    const message = row.querySelector<HTMLElement>(".commit-item__msg")!;
    const stats = row.querySelector<HTMLElement>(".commit-item__stats")!;
    const date = row.querySelector<HTMLElement>(".commit-item__date")!;
    expect(message.scrollWidth).toBeGreaterThan(message.clientWidth);
    expect(message.getBoundingClientRect().right).toBeLessThan(stats.getBoundingClientRect().left);
    expect(stats.getBoundingClientRect().right).toBeLessThan(date.getBoundingClientRect().left);
    expect(row.scrollWidth).toBe(row.clientWidth);
    await page.getByRole("button", { name: /refactor\(search\)/ }).click();
    expect(selectCommit).toHaveBeenCalledWith(commits[0].sha);
    await page.getByRole("button", { name: /feat\(api\)/ }).click({ modifiers: ["Shift"] });
    expect(selectRange).toHaveBeenCalledWith(commits[0].sha, commits[1].sha);
  });

  it("hides counts and keeps the menu within a phone-width viewport", async () => {
    const { wrapper } = await mountPicker(390);
    const stats = wrapper.querySelector<HTMLElement>(".commit-item__stats")!;
    expect(getComputedStyle(stats).display).toBe("none");
    const menu = wrapper.querySelector<HTMLElement>(".diff-scope-picker__menu")!;
    const bounds = menu.getBoundingClientRect();
    expect(bounds.left).toBeGreaterThanOrEqual(0);
    expect(bounds.right).toBeLessThanOrEqual(390);
    expect(menu.scrollWidth).toBe(menu.clientWidth);
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390);
  });
});
