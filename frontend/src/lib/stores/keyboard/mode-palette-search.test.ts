import { describe, expect, it, vi } from "vite-plus/test";

import { MODE_SEARCH_DISPLAY_LIMIT, searchModePalette } from "./mode-palette-search.js";
import type { DocsAPI } from "../../api/docs/api.js";
import type { KataTaskSummary } from "../../api/kata/taskTypes.js";

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

function kata(issues: readonly KataTaskSummary[] = [], daemonID = "daemon-work") {
  return { issues, daemonID };
}

function docs(overrides: Partial<Pick<DocsAPI, "searchAll">> = {}): Pick<DocsAPI, "searchAll"> {
  return {
    searchAll: async () => ({ query: "", hits: [], truncated: false }),
    ...overrides,
  };
}

describe("searchModePalette", () => {
  it("filters open and closed task rows from the accepted global-all snapshot", async () => {
    const result = await searchModePalette("budget", {
      kata: kata([
        task(),
        task({
          id: 2,
          uid: "issue-budget-closed",
          short_id: "budget-closed",
          qualified_id: "Finances#budget-closed",
          title: "Archive annual budget",
          status: "closed",
        }),
        task({ id: 3, uid: "issue-other", short_id: "other", qualified_id: "Finances#other", title: "Unrelated" }),
      ]),
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

    expect(
      result.tasks.ok && result.tasks.rows.map((row) => ({ uid: row.uid, status: row.status, daemon: row.daemon_id })),
    ).toEqual([
      { uid: "issue-budget", status: "open", daemon: "daemon-work" },
      { uid: "issue-budget-closed", status: "closed", daemon: "daemon-work" },
    ]);
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

  it("returns docs errors without losing snapshot task matches", async () => {
    const result = await searchModePalette("budget", {
      kata: kata([task()]),
      docs: docs({
        searchAll: async () => {
          throw new Error("docs search failed");
        },
      }),
    });

    expect(result.tasks).toMatchObject({ ok: true, truncated: false });
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
