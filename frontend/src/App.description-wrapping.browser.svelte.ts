import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";

import { mountBrowserApp, resetKeyboardModuleState, type MountedBrowserApp } from "./test/browserAppHarness.js";
import { createMockApiHandler, jsonResponse } from "./test/mockApiFetch.js";

describe("description paragraph wrapping", () => {
  vi.setConfig({ testTimeout: 30_000 });
  let mounted: MountedBrowserApp | null = null;

  afterEach(async () => {
    mounted?.unmount();
    mounted = null;
    localStorage.clear();
    await resetKeyboardModuleState();
  });

  for (const [kind, number, field] of [
    ["pulls", 42, "merge_request"],
    ["issues", 7, "issue"],
  ] as const) {
    it(`reflows ${kind} prose in a narrow detail pane while preserving explicit breaks`, async () => {
      await page.viewport(540, 900);
      const detailPath = `/api/v1/${kind}/github/acme/widgets/${number}`;
      const detail = await createMockApiHandler()
        .handle({
          method: "GET",
          url: new URL(detailPath, "http://localhost"),
          bodyText: "",
        })
        .json();
      detail[field].Body = [
        "These **words**",
        "**belong** together.",
        "",
        "Prepare dedicated workers for approved releases. Each uses its own",
        "service account, app key and job token. The service checks the running",
        "source revision before starting another job.",
        "",
        "First hard line.  ",
        "Second hard line.\\",
        "Third hard line.",
        "",
        "```text",
        "first code line",
        "second code line",
        "```",
      ].join("\n");
      mounted = await mountBrowserApp(`/${kind}/github/acme/widgets/${number}`, {
        overrides: [(request) => (request.url.pathname === detailPath ? jsonResponse(detail) : null)],
      });

      await vi.waitFor(() => {
        const body = document.querySelector<HTMLElement>(".detail-description .markdown-body");
        expect(body).not.toBeNull();
        const paragraphs = body!.querySelectorAll("p");
        expect(paragraphs).toHaveLength(3);
        expect(paragraphs[0]!.querySelectorAll("br")).toHaveLength(0);
        expect(paragraphs[1]!.querySelectorAll("br")).toHaveLength(0);
        expect(paragraphs[2]!.querySelectorAll("br")).toHaveLength(2);
        const [words, belong] = paragraphs[0]!.querySelectorAll("strong");
        expect(words!.getBoundingClientRect().top).toBe(belong!.getBoundingClientRect().top);
        expect(body!.scrollWidth).toBeLessThanOrEqual(body!.clientWidth);
        expect(body!.querySelector("pre code")?.textContent?.trimEnd()).toBe("first code line\nsecond code line");
      }, 10_000);
    });
  }
});
