import { makeRouteMockFetch } from "../../testing/test/route-mock-client.js";
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
  getRoborevClient: () => makeRouteMockFetch(client),
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

  it.each(["rejected", "interrupted", "replaced"])("offers a retry when a branch read is %s", async (failure) => {
    client.GET.mockResolvedValueOnce({ data: { repos: [{ root_path: "/work/example", name: "example", count: 4 }] } });
    const first = Promise.withResolvers<unknown>();
    const second = Promise.withResolvers<unknown>();
    if (failure === "replaced") {
      client.GET.mockImplementationOnce(() => first.promise);
      client.GET.mockImplementationOnce(() => second.promise);
    } else if (failure === "interrupted") {
      client.GET.mockImplementationOnce(() => new Promise(() => {}));
      client.GET.mockResolvedValueOnce({
        data: { repos: [{ root_path: "/work/example", name: "example", count: 4 }] },
      });
    } else {
      client.GET.mockRejectedValueOnce(new Error("offline"));
    }
    client.GET.mockResolvedValueOnce({ data: { branches: [{ name: "main", count: 2 }] } });
    render(RepoTreePicker);
    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    await screen.findByText("example");
    await fireEvent.click(screen.getByTitle("Show branches"));
    if (failure === "interrupted") {
      await screen.findByText("Loading...");
      await fireEvent.click(screen.getByTitle("Filter by repository"));
      await fireEvent.click(screen.getByTitle("Filter by repository"));
    }
    if (failure === "replaced") {
      await screen.findByText("Loading...");
      await fireEvent.click(screen.getByTitle("Show branches"));
      await fireEvent.click(screen.getByTitle("Show branches"));
      first.resolve({ data: { branches: [{ name: "stale", count: 1 }] } });
      await vi.waitFor(() => expect(client.GET).toHaveBeenCalledTimes(3));
      expect(screen.getByText("Loading...")).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
      second.reject(new Error("offline"));
    }
    const retry = await screen.findByRole("button", { name: "Retry" });
    expect(client.GET).toHaveBeenCalledTimes(failure === "rejected" ? 2 : 3);
    expect(screen.queryByText("No branches")).toBeNull();
    expect(screen.queryByText("stale")).toBeNull();
    await fireEvent.click(retry);
    expect(await screen.findByText("main")).toBeTruthy();
  });
});
