import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { worklistResponse, worklistRoutes, type WorklistServer } from "../../test/worklistFixtures.js";
import { readWorklistStateFilter } from "../components/worklists/worklist-items.js";
import { NAVIGATE_KEY, STORES_KEY } from "../context.js";
import { resetModalStack } from "../stores/keyboard/modal-stack.svelte.js";
import { readMobileListOrigin } from "../stores/mobile-list-return.js";
import { navigate, replaceUrl } from "../stores/router.svelte.js";
import { createSettingsStore } from "../stores/settings.svelte.js";
import { createWorklistsStore } from "../stores/worklists.svelte.js";
import type { NavigateCallback } from "../types.js";
import MobileWorklistView from "./MobileWorklistViewTestHarness.svelte";

const originalFetch = globalThis.fetch;

const appNavigate: NavigateCallback = (event, options) => {
  const path = typeof event === "string" ? event : event.path;
  if (options?.replace) replaceUrl(path, options.state);
  else navigate(path, options?.state);
};

function currentPath(): string {
  return window.location.pathname + window.location.search;
}

function rowTitles(): string[] {
  const region = screen.queryByRole("region", { name: /items$/ });
  if (!region) return [];
  return within(region)
    .queryAllByRole("listitem")
    .map((item) => item.querySelector(".worklist-row")?.getAttribute("aria-labelledby") ?? "")
    .map((id) => document.getElementById(id)?.textContent?.trim() ?? "");
}

function renderView(path: string, server: WorklistServer) {
  globalThis.fetch = createMockApiFetch([worklistRoutes(server)]).fetch;
  navigate(path);
  render(MobileWorklistView, {
    context: new Map<symbol, unknown>([
      [STORES_KEY, { worklists: createWorklistsStore(), settings: createSettingsStore() }],
      [NAVIGATE_KEY, appNavigate],
    ]),
  });
}

describe("MobileWorklistView", () => {
  let server: WorklistServer;

  beforeEach(() => {
    localStorage.clear();
    resetModalStack();
    server = {
      lists: [worklistResponse(5, "Review queue"), worklistResponse(4, "Stale issues", [])],
      requests: [],
    };
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
    navigate("/");
    resetModalStack();
    localStorage.clear();
  });

  it("opens the newest list and shows its open items by section", async () => {
    renderView("/m/worklists", server);

    await waitFor(() => expect(currentPath()).toBe("/m/worklists/5"));
    await waitFor(() =>
      expect(rowTitles()).toEqual([
        "Add browser regression coverage",
        "Mirror host stub PR",
        "Theme toggle does not stick",
      ]),
    );
    expect(screen.getByRole("list", { name: "Ready to merge" })).toBeTruthy();
    expect(screen.getByRole("list", { name: "Needs a decision" })).toBeTruthy();
    expect(screen.getByText("1 of 4 done")).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Worklist: Review queue" })).toBeTruthy();
  });

  it("shows closed items under All and keeps that choice for the desktop rail", async () => {
    renderView("/m/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(3));

    await fireEvent.click(screen.getByRole("combobox", { name: "Show: Open" }));
    await fireEvent.click(screen.getByRole("option", { name: "All" }));

    await waitFor(() => expect(rowTitles()).toContain("Retire the legacy exporter"));
    expect(readWorklistStateFilter()).toBe("all");
  });

  it("opens an item in phone detail that returns to this list", async () => {
    renderView("/m/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(3));

    await fireEvent.click(screen.getByRole("button", { name: "Theme toggle does not stick" }));

    expect(currentPath()).toBe("/focus/issues/github/acme/widgets/7?platform_repo_id=1001");
    expect(readMobileListOrigin(history.state)).toBe("worklists");
  });

  it("collapses a section and keeps it collapsed after Back remounts the list", async () => {
    renderView("/m/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(3));

    const header = screen.getByRole("button", { name: /^Ready to merge/ });
    await fireEvent.click(header);

    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(rowTitles()).toEqual(["Theme toggle does not stick"]);

    cleanup();
    renderView("/m/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toEqual(["Theme toggle does not stick"]));

    // The collapsed set lives for the page; reopen the section for later tests.
    await fireEvent.click(screen.getByRole("button", { name: /^Ready to merge/ }));
    expect(rowTitles()).toHaveLength(3);
  });

  it("switches lists from the picker", async () => {
    renderView("/m/worklists/5", server);
    await waitFor(() => expect(rowTitles()).toHaveLength(3));

    await fireEvent.click(screen.getByRole("combobox", { name: "Worklist: Review queue" }));
    await fireEvent.click(screen.getByRole("option", { name: /^Stale issues/ }));

    await waitFor(() => expect(currentPath()).toBe("/m/worklists/4"));
    expect(await screen.findByText("No items remain on this list.")).toBeTruthy();
  });

  it("says when no worklists exist", async () => {
    server.lists = [];
    renderView("/m/worklists", server);

    expect(await screen.findByText("No worklists")).toBeTruthy();
    expect(currentPath()).toBe("/m/worklists");
  });

  it("treats a list dismissed elsewhere as missing", async () => {
    server.lists[0]!.dismissed_at = "2026-10-20T09:00:00Z";
    renderView("/m/worklists/5", server);

    await waitFor(() => expect(currentPath()).toBe("/m/worklists/4"));
  });
});
