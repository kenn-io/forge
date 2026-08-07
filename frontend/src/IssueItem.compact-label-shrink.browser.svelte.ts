import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

import type { Issue } from "../../packages/ui/src/api/types.js";
import IssueItem from "../../packages/ui/src/components/sidebar/IssueItem.svelte";
import { STORES_KEY } from "../../packages/ui/src/context.js";
import "./app.css";

describe("IssueItem compact label row", () => {
  afterEach(() => {
    cleanup();
    document.querySelector("[data-issue-item-test]")?.remove();
  });

  it("keeps a short label chip intact beside a long title", () => {
    const wrapper = document.createElement("div");
    wrapper.dataset.issueItemTest = "";
    wrapper.style.width = "420px";
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
      labels: [{ name: "bug", color: "d73a4a" }],
    } as unknown as Issue;

    render(IssueItem, {
      target: wrapper,
      props: {
        issue,
        selected: false,
        showRepo: true,
        repoLabel: "acme/widgets",
        onclick: () => {},
      },
      context: new Map<symbol, unknown>([[STORES_KEY, { issues: { toggleIssueStar: vi.fn() } }]]),
    });

    const pill = document.querySelector<HTMLElement>(".issue-item .kit-color-label")!;

    expect(pill.textContent).toBe("bug");
    expect(pill.scrollWidth).toBeLessThanOrEqual(pill.clientWidth);
  });
});
