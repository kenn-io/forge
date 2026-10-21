import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { createMockApiFetch, jsonResponse, type MockRequest } from "../../test/mockApiFetch.js";
import { worklistRoutes } from "../../test/worklistFixtures.js";
import type { WorklistImportPreview } from "../api/worklists.js";
import { STORES_KEY } from "../context.js";
import { navigate } from "../stores/router.svelte.js";
import { createWorklistsStore } from "../stores/worklists.svelte.js";
import WorklistImportView from "./WorklistImportViewTestHarness.svelte";

const originalFetch = globalThis.fetch;

const preview: WorklistImportPreview = {
  title: "Merge today",
  recipe: "open PRs with green CI",
  created_by: "triage-bot",
  importable_count: 1,
  items: [
    {
      position: 0,
      provider: "github",
      platform_host: "",
      owner: "acme",
      name: "widgets",
      item_type: "pr",
      number: 42,
      score: 9,
      section: "Ready",
      note: "small fix",
      caveat: "check the migration",
      status: "importable",
      pull: null,
      issue: null,
    },
    {
      position: 1,
      provider: "github",
      platform_host: "",
      owner: "other",
      name: "repo",
      item_type: "issue",
      number: 7,
      score: null,
      section: "",
      note: "",
      caveat: "",
      status: "repo_not_tracked",
      pull: null,
      issue: null,
    },
  ],
};

interface ImportServer {
  previews: string[];
  imports: string[];
  importResponse: () => Response | Promise<Response>;
}

function renderImport(path: string, server: ImportServer): void {
  const importRoutes = (request: MockRequest): Response | null => {
    if (request.method !== "POST") return null;
    const { fragment } = JSON.parse(request.bodyText) as { fragment: string };
    if (request.url.pathname === "/api/v1/worklists/import/preview") {
      server.previews.push(fragment);
      return jsonResponse(preview);
    }
    if (request.url.pathname === "/api/v1/worklists/import") {
      server.imports.push(fragment);
      return null;
    }
    return null;
  };
  const handle = createMockApiFetch([importRoutes, worklistRoutes({ lists: [], requests: [] })]);
  globalThis.fetch = async (input, init) => {
    const request = input instanceof Request ? input : new Request(new URL(String(input), window.location.href), init);
    if (request.method === "POST" && new URL(request.url).pathname === "/api/v1/worklists/import") {
      server.imports.push(((await request.clone().json()) as { fragment: string }).fragment);
      return server.importResponse();
    }
    return handle.fetch(input, init);
  };
  navigate(path);
  render(WorklistImportView, {
    context: new Map<symbol, unknown>([[STORES_KEY, { worklists: createWorklistsStore() }]]),
  });
}

describe("WorklistImportView", () => {
  let server: ImportServer;

  beforeEach(() => {
    server = {
      previews: [],
      imports: [],
      importResponse: () => jsonResponse({ worklist_id: 7, item_count: 1, skipped_count: 1 }),
    };
  });

  afterEach(() => {
    cleanup();
    globalThis.fetch = originalFetch;
    navigate("/");
  });

  it("previews the link's list without creating it", async () => {
    renderImport("/worklists/import#v1.abc", server);

    expect(await screen.findByRole("heading", { name: "Merge today" })).toBeTruthy();
    expect(server.previews).toEqual(["v1.abc"]);
    expect(screen.getByText("acme/widgets#42")).toBeTruthy();
    expect(screen.getByText("Not tracked")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Create with 1 of 2" })).toBeTruthy();
    expect(server.imports).toEqual([]);
  });

  it("creates the list on confirm and replaces the link with the new list", async () => {
    renderImport("/worklists/import#v1.abc", server);
    const historyLength = window.history.length;

    await fireEvent.click(await screen.findByRole("button", { name: "Create with 1 of 2" }));

    await waitFor(() => expect(window.location.pathname).toBe("/worklists/7"));
    expect(server.imports).toEqual(["v1.abc"]);
    expect(window.location.hash).toBe("");
    expect(window.history.length).toBe(historyLength);
  });

  it("stays where the maintainer went when the import finishes after they left", async () => {
    let respond: (response: Response) => void = () => {};
    server.importResponse = () => new Promise<Response>((resolve) => (respond = resolve));
    renderImport("/worklists/import#v1.abc", server);
    await fireEvent.click(await screen.findByRole("button", { name: "Create with 1 of 2" }));
    await waitFor(() => expect(server.imports).toEqual(["v1.abc"]));

    navigate("/pulls");
    respond(jsonResponse({ worklist_id: 7, item_count: 1, skipped_count: 1 }));

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(window.location.pathname).toBe("/pulls");
  });

  it("shows why an unreadable link cannot be imported", async () => {
    globalThis.fetch = createMockApiFetch([
      (request) =>
        request.url.pathname === "/api/v1/worklists/import/preview"
          ? jsonResponse(
              {
                title: "Bad Request",
                status: 400,
                detail: "not a worklist link: unknown version",
                code: "validationError",
              },
              400,
            )
          : null,
    ]).fetch;
    navigate("/worklists/import#v9.abc");
    render(WorklistImportView, {
      context: new Map<symbol, unknown>([[STORES_KEY, { worklists: createWorklistsStore() }]]),
    });

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByText(/not a worklist link/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Create/ })).toBeNull();
  });
});
