import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";

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
  runtime: null as OwnedAppRuntime | null,
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

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    if (state.runtime === null) throw new Error("test runtime was not initialized");
    return state.runtime;
  },
}));

describe("RepoTreePicker", () => {
  beforeEach(() => {
    state.repo = undefined;
    state.branch = undefined;
    state.runtime = makeAppRuntime();
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
            root_path: "/work/kenn-forge",
            name: "kenn-forge",
            count: 4,
          },
        ],
      },
    });
  });

  afterEach(async () => {
    cleanup();
    state.jobs = null;
    client.GET.mockReset();
    if (state.runtime !== null) await Effect.runPromise(state.runtime.disposeEffect);
    state.runtime = null;
  });

  it("closes when pressing outside the picker", async () => {
    render(RepoTreePicker);

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    expect(screen.getByPlaceholderText("Filter repos...")).toBeTruthy();

    await fireEvent.mouseDown(document.body);

    expect(screen.queryByPlaceholderText("Filter repos...")).toBeNull();
  });
});
