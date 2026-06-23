import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vite-plus/test";

import RepoTreePicker from "./RepoTreePicker.svelte";

type JobsStoreStub = {
  getFilterRepo: () => string | undefined;
  getFilterBranch: () => string | undefined;
  setFilter: Mock<(key: string, value: string | undefined) => void>;
};

const state = {
  repo: undefined as string | undefined,
  branch: undefined as string | undefined,
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

describe("RepoTreePicker", () => {
  beforeEach(() => {
    state.repo = undefined;
    state.branch = undefined;
    state.jobs = {
      getFilterRepo: () => state.repo,
      getFilterBranch: () => state.branch,
      setFilter: vi.fn((key: string, value: string | undefined) => {
        if (key === "repo") state.repo = value;
        if (key === "branch") state.branch = value;
      }),
    };
    client.GET.mockResolvedValue({
      data: {
        repos: [
          {
            root_path: "/work/middleman",
            name: "middleman",
            count: 4,
          },
        ],
      },
    });
  });

  afterEach(() => {
    cleanup();
    state.jobs = null;
    client.GET.mockReset();
  });

  it("closes when pressing outside the picker", async () => {
    render(RepoTreePicker);

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    expect(screen.getByPlaceholderText("Filter repos...")).toBeTruthy();

    await fireEvent.mouseDown(document.body);

    expect(screen.queryByPlaceholderText("Filter repos...")).toBeNull();
  });
});
