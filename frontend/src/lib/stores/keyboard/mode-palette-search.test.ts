import { describe, expect, it, vi } from "vite-plus/test";

import { MODE_SEARCH_DISPLAY_LIMIT, searchModePalette } from "./mode-palette-search.js";
import type { DocsAPI } from "../../api/docs/api.js";
import type { KataTaskAPI, KataTaskSearchResponse, KataTaskSummary } from "../../api/kata/taskTypes.js";

function task(overrides: Partial<KataTaskSummary> = {}): KataTaskSummary {
  return {
    id: 1,
    uid: "issue-budget",
    short_id: "budget",
    qualified_id: "Finances#budget",
    project_id: 1,
    project_uid: "project-finances",
    project_name: "Finances",
    title: "Set monthly budget",
    status: "open",
    metadata: {},
    revision: 1,
    author: "fixture-user",
    created_at: "2026-05-17T00:00:00Z",
    updated_at: "2026-05-17T00:00:00Z",
    labels: [],
    ...overrides,
  };
}

function kata(overrides: Partial<Pick<KataTaskAPI, "search">> = {}): Pick<KataTaskAPI, "search"> {
  return {
    search: async (): Promise<KataTaskSearchResponse> => ({
      filters: { scope: { kind: "all" }, status: "all", owner: "", label: "", query: "" },
      issues: [],
      fetched_at: "2026-05-17T00:00:00Z",
    }),
    ...overrides,
  };
}

function docs(overrides: Partial<Pick<DocsAPI, "searchAll">> = {}): Pick<DocsAPI, "searchAll"> {
  return {
    searchAll: async () => ({ query: "", hits: [], truncated: false }),
    ...overrides,
  };
}

describe("searchModePalette", () => {
  it("fires task and docs search in parallel", async () => {
    const order: string[] = [];
    let resolveKata: (value: KataTaskSearchResponse) => void = () => {};
    let resolveDocs: (value: Awaited<ReturnType<DocsAPI["searchAll"]>>) => void = () => {};

    const promise = searchModePalette("budget", {
      kata: kata({
        search: () => {
          order.push("task-start");
          return new Promise((resolve) => {
            resolveKata = resolve;
          });
        },
      }),
      docs: docs({
        searchAll: () => {
          order.push("docs-start");
          return new Promise((resolve) => {
            resolveDocs = resolve;
          });
        },
      }),
    });

    await Promise.resolve();
    expect(order).toEqual(["task-start", "docs-start"]);
    resolveKata({
      filters: { scope: { kind: "all" }, status: "all", owner: "", label: "", query: "budget" },
      issues: [],
      fetched_at: "2026-05-17T00:00:00Z",
    });
    resolveDocs({ query: "budget", hits: [], truncated: false });
    await promise;
  });

  it("normalizes task and docs rows", async () => {
    const result = await searchModePalette("budget", {
      kata: kata({
        search: vi.fn(
          async (): Promise<KataTaskSearchResponse> => ({
            filters: { scope: { kind: "all" }, status: "all", owner: "", label: "", query: "budget" },
            issues: [task()],
            fetched_at: "2026-05-17T00:00:00Z",
          }),
        ),
      }),
      docs: docs({
        searchAll: vi.fn(
          async (): Promise<Awaited<ReturnType<DocsAPI["searchAll"]>>> => ({
            query: "budget",
            truncated: false,
            hits: [
              {
                folder: "notes",
                folder_name: "Notes",
                name: "budget.md",
                rel_path: "finance/budget.md",
                score: 10,
                hit_type: "body",
                line: 4,
                snippet: { text: "monthly budget", matches: [{ start: 8, end: 14 }] },
              },
            ],
          }),
        ),
      }),
    });

    expect(result.tasks).toEqual({
      ok: true,
      truncated: false,
      rows: [
        {
          kind: "kata-task",
          uid: "issue-budget",
          short_id: "budget",
          qualified_id: "Finances#budget",
          title: "Set monthly budget",
          project_name: "Finances",
          status: "open",
        },
      ],
    });
    expect(result.docs).toEqual({
      ok: true,
      truncated: false,
      rows: [
        {
          kind: "doc",
          folder: "notes",
          folder_name: "Notes",
          rel_path: "finance/budget.md",
          hit_type: "body",
          line: 4,
          snippet: { text: "monthly budget", matches: [{ start: 8, end: 14 }] },
        },
      ],
    });
  });

  it("returns per-section errors without throwing", async () => {
    const result = await searchModePalette("budget", {
      kata: kata({
        search: async () => {
          throw new Error("task search failed");
        },
      }),
      docs: docs({
        searchAll: async () => {
          throw new Error("docs search failed");
        },
      }),
    });

    expect(result.tasks).toEqual({ ok: false, error: "task search failed" });
    expect(result.docs).toEqual({ ok: false, error: "docs search failed" });
  });

  it("caps docs rows at the shared display limit", async () => {
    const hits = Array.from({ length: MODE_SEARCH_DISPLAY_LIMIT + 1 }, (_, index) => ({
      folder: "notes",
      folder_name: "Notes",
      name: `budget-${index}.md`,
      rel_path: `finance/budget-${index}.md`,
      score: 10 - index,
      hit_type: "filename" as const,
    }));
    const result = await searchModePalette("budget", {
      kata: kata(),
      docs: docs({
        searchAll: async () => ({ query: "budget", truncated: false, hits }),
      }),
    });

    expect(result.docs).toMatchObject({ ok: true, truncated: true });
    if (result.docs.ok) {
      expect(result.docs.rows).toHaveLength(MODE_SEARCH_DISPLAY_LIMIT);
      expect(result.docs.rows.at(-1)?.rel_path).toBe("finance/budget-9.md");
    }
  });
});
