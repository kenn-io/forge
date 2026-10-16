import type { Component } from "svelte";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { cleanup, render } from "vitest-browser-svelte";
import "../../../app.css";
import AppRuntimeHarness from "../../../test/AppRuntimeHarness.svelte";
import ExternalContextCards from "./ExternalContextCards.svelte";
const cards = {
  ready: {
    recommendation: { verdict: "Merge", reason: "Tests pass; the configuration fix is ready." },
    status: "neutral",
    summary: "Importance 8/10 \u00b7 Complexity 2.1 \u00b7 Alignment 9.2",
    markdown:
      "Rejects empty paths before loading configuration.\n\n| | |\n| --- | --- |\n| Design | **Uses existing validation**<br>Keeps path checks in the configuration loader. |\n| Size | \u2022 Change: 48 lines (18 code, 30 tests, 0 docs) |\n| State | CI passed. Review approved. |\n\n[View in Kennel](https://kennel.example/releases/pr-grading)",
    result_head_sha: "head",
    actions: [{ id: "reassess:3", label: "Reassess" }],
    refresh_after_seconds: 0,
  },
  hold: {
    recommendation: { verdict: "Hold", reason: "CI checks are failing." },
    status: "neutral",
    summary: "Importance 8/10 \u00b7 Complexity 2.1 \u00b7 Alignment 9.2",
    markdown:
      "Rejects empty paths before loading configuration.\n\n| | |\n| --- | --- |\n| Design | **Uses existing validation**<br>Keeps path checks in the configuration loader. |\n| Size | \u2022 Change: 48 lines (18 code, 30 tests, 0 docs) |\n| State | Windows path test failed. Review approved. |\n\n[View in Kennel](https://kennel.example/releases/pr-grading)",
    result_head_sha: "head",
    actions: [{ id: "reassess:3", label: "Reassess" }],
    refresh_after_seconds: 0,
  },
  stale: {
    recommendation: { verdict: "Hold", reason: "The assessment does not cover the current PR evidence." },
    status: "neutral",
    summary: "Importance 8/10 \u00b7 Complexity 2.1 \u00b7 Alignment 9.2",
    markdown:
      "Previous assessment\n\nRejects empty paths before loading configuration.\n\n| | |\n| --- | --- |\n| Design | **Uses existing validation**<br>Keeps path checks in the configuration loader. |\n| Size | \u2022 Change: 48 lines (18 code, 30 tests, 0 docs) |\n| State | Windows path test failed. Review approved. |\n\n[View in Kennel](https://kennel.example/releases/pr-grading)",
    result_head_sha: "old",
    actions: [{ id: "reassess:3", label: "Reassess" }],
    refresh_after_seconds: 0,
  },
  empty: {
    recommendation: { verdict: "Hold", reason: "No assessment is available." },
    status: "neutral",
    summary: "No PR assessment yet",
    markdown: "\n\n[View in Kennel](https://kennel.example/releases/pr-grading)",
    actions: [{ id: "reassess:3", label: "Reassess" }],
    refresh_after_seconds: 0,
  },
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  document.querySelector("#capture-host")?.remove();
});
for (const width of [960, 390]) {
  for (const [name, card] of Object.entries(cards)) {
    it(`keeps ${name} advice visible at ${width}px`, async () => {
      await page.viewport(width, 460);
      vi.stubGlobal("fetch", async (request: Request) =>
        request.url.endsWith("/external-context/sources")
          ? Response.json({ sources: [{ id: "grading", name: "PR grading" }] })
          : Response.json({ card }),
      );
      const target = document.createElement("div");
      target.id = "capture-host";
      target.style.cssText = "padding:24px;max-width:900px;margin:0 auto;box-sizing:border-box;";
      document.body.append(target);
      await render(AppRuntimeHarness, {
        target,
        props: {
          component: ExternalContextCards as Component,
          ref: { provider: "github", owner: "example", name: "project", repoPath: "example/project" },
          repositoryKey: { kind: "id", id: 123 },
          number: 42,
          headSha: "head",
        },
      });
      await expect.element(page.getByText(card.recommendation.reason, { exact: true })).toBeVisible();
      await document.fonts.ready;
      const details = target.querySelector("details")!;
      expect(details.open).toBe(false);
      expect(target.querySelector(".context-recommendation")!.closest("details")).toBeNull();
      expect(target.scrollWidth).toBeLessThanOrEqual(width);
      expect([...target.querySelectorAll("button")].map((b) => b.textContent?.trim())).toEqual(["Refresh", "Reassess"]);
      await page.getByText("Details:", { exact: false }).click();
      expect(details.open).toBe(true);
      await expect.element(page.getByText(card.recommendation.reason, { exact: true })).toBeVisible();
      expect(target.querySelectorAll(".context-recommendation")).toHaveLength(1);
      if (name === "stale") await expect.element(page.getByText("Results are for an older commit")).toBeVisible();
    });
  }
}
