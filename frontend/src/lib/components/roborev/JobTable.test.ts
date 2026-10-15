import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { STORES_KEY } from "../../context.js";
import JobTable from "./JobTable.svelte";

function renderTable(sortColumn: string, sortDirection: "asc" | "desc") {
  const setSortColumn = vi.fn();
  render(JobTable, {
    context: new Map<symbol, unknown>([
      [
        STORES_KEY,
        {
          roborevJobs: {
            getJobs: () => [],
            getSortColumn: () => sortColumn,
            getSortDirection: () => sortDirection,
            setSortColumn,
            isLoading: () => false,
            getError: () => null,
            getHasMore: () => false,
          },
        },
      ],
    ]),
  });
  return setSortColumn;
}

describe("JobTable", () => {
  afterEach(() => {
    cleanup();
  });

  it("sorts the jobs by repository from the Repo / Branch / Ref header", async () => {
    const setSortColumn = renderTable("repo", "desc");
    const repo = screen.getByRole("button", { name: "Repo / Branch / Ref" });
    expect(repo.closest("th")?.getAttribute("aria-sort")).toBe("descending");
    expect(screen.getByRole("button", { name: "ID" }).closest("th")?.hasAttribute("aria-sort")).toBe(false);

    await fireEvent.click(repo);
    expect(setSortColumn).toHaveBeenCalledExactlyOnceWith("repo");
  });
});
