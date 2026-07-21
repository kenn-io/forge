import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Issue } from "../api/types.js";
import type { MiddlemanClient } from "../types.js";
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";
import { createIssuesStore } from "./issues.svelte.js";

afterEach(() => {
  for (const item of getFlashes()) dismissFlash(item.id);
});

function issue(id: number, author: string): Issue {
  return {
    ID: id,
    Number: id,
    Title: `Issue ${id}`,
    Author: author,
    State: "open",
    repo_owner: "acme",
    repo_name: "widgets",
    platform_host: "github.com",
    repo: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      repo_path: "acme/widgets",
    },
  } as Issue;
}

describe("issues store bot visibility", () => {
  it("hydrates the persisted preference and hides bot-authored issues", async () => {
    const client = {
      GET: vi.fn(async () => ({
        data: [issue(1, "alice"), issue(2, "renovate[bot]"), issue(3, "release-bot")],
        error: undefined,
      })),
    } as unknown as MiddlemanClient;
    const store = createIssuesStore({ client });

    await store.loadIssues();
    store.hydrateDefaults({ hide_bots: true });

    expect(store.getHideBots()).toBe(true);
    expect(store.getIssues().map((item) => item.Author)).toEqual(["alice"]);
  });

  it("persists visibility changes and adopts the saved response", async () => {
    const put = vi.fn(async () => ({
      data: { issues: { hide_bots: true } },
      error: undefined,
    }));
    const store = createIssuesStore({
      client: { PUT: put } as unknown as MiddlemanClient,
    });

    await store.setHideBots(true);

    expect(put).toHaveBeenCalledWith("/settings", {
      body: { issues: { hide_bots: true } },
    });
    expect(store.getHideBots()).toBe(true);
  });

  it("restores the previous preference when persistence fails", async () => {
    const store = createIssuesStore({
      client: {
        PUT: vi.fn(async () => ({
          data: undefined,
          error: { detail: "settings unavailable" },
        })),
      } as unknown as MiddlemanClient,
    });

    await store.setHideBots(true);

    expect(store.getHideBots()).toBe(false);
    expect(getFlash()).toMatchObject({ message: "settings unavailable", tone: "danger" });
  });

  it("restores the previous preference when the settings request throws", async () => {
    const store = createIssuesStore({
      client: {
        PUT: vi.fn(async () => {
          throw new Error("network unavailable");
        }),
      } as unknown as MiddlemanClient,
    });

    await store.setHideBots(true);

    expect(store.getHideBots()).toBe(false);
    expect(getFlash()).toMatchObject({ message: "network unavailable", tone: "danger" });
  });
});
