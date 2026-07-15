import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { MiddlemanClient } from "@middleman/ui";
import RepoBrowserFeature from "./RepoBrowserFeature.svelte";

const route = {
  mode: "source" as const,
  name: "widgets",
  owner: "acme",
  page: "repo-browser" as const,
  platformHost: "github.com",
  provider: "github",
  repoPath: "acme/widgets",
};

function testClient(): MiddlemanClient {
  const repo = {
    name: "widgets",
    owner: "acme",
    platform: "github",
    platform_host: "github.com",
    repo_path: "acme/widgets",
  };
  const ref = { type: "branch", name: "main", sha: "main-sha", stale: false };
  return {
    GET: vi.fn(async (path: string) => {
      if (path.endsWith("/browser/refs")) {
        return { data: { repo, refs: [ref], default_ref: ref }, response: new Response(null, { status: 200 }) };
      }
      if (path.endsWith("/browser/tree")) {
        return { data: { repo, ref, entries: [], truncated: false }, response: new Response(null, { status: 200 }) };
      }
      if (path.endsWith("/browser/last-changed")) {
        return { data: { repo, ref, commits: {} }, response: new Response(null, { status: 200 }) };
      }
      throw new Error(`unexpected GET ${path}`);
    }),
  } as unknown as MiddlemanClient;
}

describe("repository browser responsive rails", () => {
  it("reserves the visible history rail above the 900px breakpoint", async () => {
    await page.viewport(940, 700);
    render(RepoBrowserFeature, {
      props: {
        client: testClient(),
        route,
        onRouteChange: vi.fn(),
      },
    });

    const content = document.querySelector<HTMLElement>(".repo-browser__content");
    expect(content).not.toBeNull();
    Object.defineProperty(content!, "clientWidth", { configurable: true, value: 940 });
    window.dispatchEvent(new Event("resize"));

    await vi.waitFor(() => {
      const sidebar = document.querySelector<HTMLElement>(".repo-browser__sidebar");
      expect(sidebar?.style.width).toBe("260px");
    });
    await expect.element(page.getByRole("separator", { name: "Resize file history" })).toBeVisible();
  });
});
