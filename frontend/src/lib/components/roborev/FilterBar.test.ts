import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vite-plus/test";

import FilterBar from "./FilterBar.svelte";

type JobsStoreStub = {
  getFilterSearch: () => string | undefined;
  getFilterStatus: () => string | undefined;
  getFilterHideClosed: () => boolean;
  getFilterShowAutoDesign: () => boolean;
  getFilterRepo: () => string | undefined;
  getFilterBranch: () => string | undefined;
  setFilter: Mock<(key: string, value: string | boolean | undefined) => void>;
};

const state = {
  showAutoDesign: false,
  jobs: null as JobsStoreStub | null,
};

const client = {
  GET: vi.fn(),
};

vi.mock("../../context.js", () => ({
  getStores: () => ({
    roborevJobs: state.jobs,
  }),
  getRoborevClient: () => client,
}));

describe("FilterBar", () => {
  beforeEach(() => {
    state.showAutoDesign = false;
    state.jobs = {
      getFilterSearch: () => undefined,
      getFilterStatus: () => undefined,
      getFilterHideClosed: () => false,
      getFilterShowAutoDesign: () => state.showAutoDesign,
      getFilterRepo: () => undefined,
      getFilterBranch: () => undefined,
      setFilter: vi.fn((key: string, value: string | boolean | undefined) => {
        if (key === "showAutoDesign") state.showAutoDesign = value === true;
      }),
    };
    client.GET.mockResolvedValue({ data: { repos: [] }, error: undefined });
  });

  afterEach(() => {
    cleanup();
    state.jobs = null;
    client.GET.mockReset();
  });

  it("shows an unchecked auto-design toggle that enables the filter", async () => {
    render(FilterBar);

    const checkbox = screen.getByLabelText("Show auto-design") as HTMLInputElement;
    expect(checkbox.checked).toBe(false);

    await fireEvent.click(checkbox);

    expect(state.jobs?.setFilter).toHaveBeenCalledWith("showAutoDesign", true);
  });
});
