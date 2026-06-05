import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import DiffStats from "./DiffStats.svelte";

describe("DiffStats", () => {
  afterEach(() => {
    cleanup();
  });

  it("formats additions and deletions consistently", () => {
    render(DiffStats, {
      props: {
        additions: 2781,
        deletions: 216,
      },
    });

    const stats = screen.getByLabelText("2781 additions, 216 deletions");
    expect(stats.textContent).toBe("+2.78k −216");
    expect(stats.querySelector(".diff-stats__add")?.textContent?.trim()).toBe("+2.78k");
    expect(stats.querySelector(".diff-stats__del")?.textContent?.trim()).toBe("−216");
  });

  it("dims zero values when requested", () => {
    render(DiffStats, {
      props: {
        additions: 0,
        deletions: 1,
        dimZeros: true,
      },
    });

    const stats = screen.getByLabelText("0 additions, 1 deletion");
    expect(stats.textContent).toBe("+0 −1");
    expect(stats.querySelector(".diff-stats__add")?.classList.contains("diff-stats__value--dim")).toBe(true);
    expect(stats.querySelector(".diff-stats__del")?.classList.contains("diff-stats__value--dim")).toBe(false);
  });
});
