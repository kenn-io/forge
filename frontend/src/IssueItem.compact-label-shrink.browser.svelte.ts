import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

import type { Issue } from "./lib/api/types.js";
import IssueItem from "./lib/components/sidebar/IssueItem.svelte";
import { STORES_KEY } from "./lib/context.js";
import "./app.css";

describe("IssueItem compact label row", () => {
  afterEach(() => {
    cleanup();
    document.querySelector("[data-issue-item-test]")?.remove();
  });

  async function mountIssue(
    widthPx: number,
    labels: { name: string; color: string }[],
    mobileMain = false,
  ): Promise<void> {
    const wrapper = document.createElement("div");
    wrapper.dataset.issueItemTest = "";
    wrapper.style.width = `${widthPx}px`;
    if (mobileMain) wrapper.classList.add("mobile-main");
    document.body.appendChild(wrapper);

    const issue = {
      Number: 1351,
      Title: "Investigate tool calls that do not identify long file references correctly",
      Author: "alice",
      State: "open",
      LastActivityAt: new Date().toISOString(),
      repo_owner: "acme",
      repo_name: "widgets",
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
      },
      Starred: false,
      labels,
    } as unknown as Issue;

    await render(IssueItem, {
      target: wrapper,
      props: {
        issue,
        selected: false,
        showRepo: true,
        repoLabel: "acme/widgets",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([
        [
          STORES_KEY,
          {
            settings: { getWorkspaceSettings: () => ({ show_agent_status_in_lists: false }) },
            activity: { getUseWorkspaceActivityForRecency: () => false },
            issues: { toggleIssueStar: vi.fn() },
          },
        ],
      ]),
    });
  }

  function assertChipsShownWhole(names: string[]): void {
    const clusterRect = document.querySelector<HTMLElement>(".issue-item .label-row")!.getBoundingClientRect();
    const chips = [...document.querySelectorAll<HTMLElement>(".issue-item .kit-color-label")].filter(
      (chip) => getComputedStyle(chip).visibility === "visible",
    );
    expect(chips.map((chip) => chip.textContent)).toEqual(names);
    for (const chip of chips) {
      expect(chip.scrollWidth).toBeLessThanOrEqual(chip.clientWidth);
      expect(chip.getBoundingClientRect().right).toBeLessThanOrEqual(clusterRect.right + 0.5);
    }
  }

  function assertNumberAndTitleVisible(): void {
    const rowRight = document.querySelector<HTMLElement>(".issue-item")!.getBoundingClientRect().right;
    const number = document.querySelector<HTMLElement>(".issue-item .item-number")!.getBoundingClientRect();
    expect(number.width).toBeGreaterThan(0);
    expect(number.right).toBeLessThanOrEqual(rowRight + 0.5);
    expect(
      document.querySelector<HTMLElement>(".issue-item .title-text")!.getBoundingClientRect().width,
    ).toBeGreaterThan(0);
  }

  it("keeps a short label chip intact beside a long title", async () => {
    await mountIssue(420, [{ name: "bug", color: "d73a4a" }]);
    assertChipsShownWhole(["bug"]);
  });

  it.each([
    ["a normal sidebar width", 340, false],
    ["the 200px minimum sidebar width", 200, false],
    ["a narrow phone card", 360, true],
  ])("shows two short labels whole beside a long title at %s", async (_name, widthPx, mobileMain) => {
    await mountIssue(
      widthPx,
      [
        { name: "bug", color: "d73a4a" },
        { name: "ux", color: "0075ca" },
      ],
      mobileMain,
    );
    assertChipsShownWhole(["bug", "ux"]);
    assertNumberAndTitleVisible();
  });

  it("shows a long label in full at a normal sidebar width", async () => {
    await mountIssue(340, [{ name: "component-integration-testing-suite", color: "d73a4a" }]);
    await vi.waitFor(() => assertChipsShownWhole(["component-integration-testing-suite"]));
    assertNumberAndTitleVisible();
  });

  it("moves long labels whole into the overflow badge at the 200px minimum sidebar width", async () => {
    await mountIssue(200, [
      { name: "component-integration-testing-suite", color: "d73a4a" },
      { name: "documentation-and-release-notes-update", color: "0075ca" },
    ]);
    await vi.waitFor(() => expect(document.querySelector(".issue-item .label-more")?.textContent).toBe("+2"));
    assertChipsShownWhole([]);
    assertNumberAndTitleVisible();
  });
});
