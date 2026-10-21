import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { createMockApiFetch, jsonResponse } from "../../test/mockApiFetch.js";
import { worklistRoutes } from "../../test/worklistFixtures.js";
import { STORES_KEY } from "../context.js";
import { dismissFlash, getFlashes } from "../stores/flash.svelte.js";
import { navigate } from "../stores/router.svelte.js";
import { createWorklistsStore } from "../stores/worklists.svelte.js";
import WorklistImportView from "./WorklistImportViewTestHarness.svelte";

const originalFetch = globalThis.fetch;

describe("WorklistImportView", () => {
  let imports: string[];
  let importResponse: () => Response | Promise<Response>;

  function openLink(path: string): void {
    const handle = createMockApiFetch([worklistRoutes({ lists: [], requests: [] })]);
    globalThis.fetch = async (input, init) => {
      const request =
        input instanceof Request ? input : new Request(new URL(String(input), window.location.href), init);
      if (request.method === "POST" && new URL(request.url).pathname === "/api/v1/worklists/import") {
        imports.push(((await request.clone().json()) as { fragment: string }).fragment);
        return importResponse();
      }
      return handle.fetch(input, init);
    };
    navigate(path);
    render(WorklistImportView, {
      context: new Map<symbol, unknown>([[STORES_KEY, { worklists: createWorklistsStore() }]]),
    });
  }

  beforeEach(() => {
    imports = [];
    importResponse = () => jsonResponse({ worklist_id: 7, item_count: 2, skipped_count: 1 });
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
    for (const flash of getFlashes()) dismissFlash(flash.id);
    navigate("/");
  });

  it("creates the link's list on open and replaces the link with it", async () => {
    navigate("/pulls");
    const historyLength = window.history.length;

    openLink("/worklists/import#v1.abc");

    await waitFor(() => expect(window.location.pathname).toBe("/worklists/7"));
    expect(imports).toEqual(["v1.abc"]);
    expect(window.location.hash).toBe("");
    expect(window.history.length).toBe(historyLength + 1);
    expect(getFlashes().map((flash) => [flash.message, flash.tone])).toEqual([
      ["Left out 1 of 3 items: not tracked, not synced, or listed twice.", "warning"],
    ]);
  });

  it("stays where the maintainer went when the import finishes after they left", async () => {
    let respond: (response: Response) => void = () => {};
    importResponse = () => new Promise<Response>((resolve) => (respond = resolve));
    openLink("/worklists/import#v1.abc");
    await waitFor(() => expect(imports).toEqual(["v1.abc"]));

    navigate("/pulls");
    respond(jsonResponse({ worklist_id: 7, item_count: 2, skipped_count: 0 }));

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(window.location.pathname).toBe("/pulls");
  });

  it("shows why Forge refused the link", async () => {
    importResponse = () =>
      jsonResponse(
        { title: "Bad Request", status: 400, detail: "not a worklist link: unknown version", code: "validationError" },
        400,
      );

    openLink("/worklists/import#v9.abc");

    expect(await screen.findByText(/not a worklist link/)).toBeTruthy();
    expect(window.location.pathname).toBe("/worklists/import");
    expect(screen.getByRole("button", { name: "Open worklists" })).toBeTruthy();
  });
});
