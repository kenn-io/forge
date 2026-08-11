import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vite-plus/test";

import type { Repo } from "../api/types.js";
import { createSettingsStore } from "../stores/settings.svelte.js";
import { client } from "../api/runtime.js";
import RepoTypeahead from "./RepoTypeaheadRuntimeHarness.svelte";

let settingsStore: ReturnType<typeof createSettingsStore>;

vi.mock("../context.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../context.js")>();
  return {
    ...actual,
    getStores: () => ({
      settings: settingsStore,
    }),
  };
});

vi.mock("../api/runtime.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/runtime.js")>()),
  client: {
    GET: vi.fn(() => Promise.resolve({ data: [], error: undefined })),
  },
}));

const getRepos = client.GET as unknown as Mock<
  (path: string, options?: { signal?: AbortSignal }) => Promise<{ data: Repo[]; error: undefined }>
>;

describe("RepoTypeahead", () => {
  beforeEach(() => {
    // The expansion store persists collapsed nodes to localStorage, so clear
    // it between tests to keep each case from inheriting another's tree state.
    localStorage.clear();
    settingsStore = createSettingsStore();
    settingsStore.setConfiguredRepos([]);
    getRepos.mockReset();
    getRepos.mockResolvedValue({ data: [], error: undefined });
  });

  afterEach(() => {
    cleanup();
  });

  it("updates dropdown options when configured repos change", async () => {
    render(RepoTypeahead, {
      props: {
        selected: undefined,
        onchange: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    expect(screen.queryByRole("option", { name: /import-lab\/api/i })).toBeNull();

    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /import-lab\/api/i })).toBeTruthy();
    });
  });

  it("omits configured repos the server marked hidden from the UI", async () => {
    render(RepoTypeahead, {
      props: {
        selected: undefined,
        onchange: vi.fn(),
      },
    });

    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "api",
        repo_path: "acme/api",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: false,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "archive",
        repo_path: "acme/archive",
        is_glob: false,
        matched_repo_count: 1,
        hidden_from_ui: true,
      },
    ]);

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    await waitFor(() => {
      expect(screen.getByRole("option", { name: /acme\/api/i })).toBeTruthy();
    });
    expect(screen.queryByRole("option", { name: /acme\/archive/i })).toBeNull();
  });

  it("aborts the repository request when the component unmounts", async () => {
    let signal: AbortSignal | undefined;
    getRepos.mockImplementation((_path, options) => {
      signal = options?.signal;
      return new Promise(() => undefined);
    });
    const view = render(RepoTypeahead, {
      props: { selected: undefined, onchange: vi.fn() },
    });
    await waitFor(() => expect(getRepos).toHaveBeenCalled());

    view.unmount();

    expect(signal?.aborted).toBe(true);
  });

  it("replaces a repository request when configured identity changes at the same count", async () => {
    let resolveFirst!: (value: { data: Repo[]; error: undefined }) => void;
    const first = new Promise<{ data: Repo[]; error: undefined }>((resolve) => {
      resolveFirst = resolve;
    });
    const replacement = [
      { Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "two" },
    ] as unknown as Repo[];
    getRepos.mockImplementationOnce(() => first).mockResolvedValueOnce({ data: replacement, error: undefined });
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "one",
        repo_path: "acme/one",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);
    render(RepoTypeahead, { props: { selected: undefined, onchange: vi.fn() } });
    await waitFor(() => expect(getRepos).toHaveBeenCalledTimes(1));

    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "two",
        repo_path: "acme/two",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    await waitFor(() => expect(getRepos).toHaveBeenCalledTimes(2));
    resolveFirst({
      data: [{ Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "one" }] as unknown as Repo[],
      error: undefined,
    });
    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    await waitFor(() => expect(screen.getByRole("option", { name: /acme\/two/i })).toBeTruthy());
    expect(screen.queryByRole("option", { name: /acme\/one/i })).toBeNull();
  });

  it("keeps fetched repos for glob-backed settings entries", async () => {
    const fetchedRepos = [
      {
        Platform: "github",
        PlatformHost: "github.com",
        Owner: "roborev-dev",
        Name: "kenn-forge",
      },
      {
        Platform: "github",
        PlatformHost: "github.com",
        Owner: "roborev-dev",
        Name: "worker",
      },
    ] as unknown as Repo[];

    getRepos.mockResolvedValue({
      data: fetchedRepos,
      error: undefined,
    });

    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "roborev-dev",
        name: "*",
        repo_path: "roborev-dev/*",
        is_glob: true,
        matched_repo_count: 2,
      },
    ]);

    render(RepoTypeahead, {
      props: {
        selected: undefined,
        onchange: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /roborev-dev\/kenn-forge/i })).toBeTruthy();
      expect(screen.getByRole("option", { name: /roborev-dev\/worker/i })).toBeTruthy();
    });
  });

  it("allows selecting multiple repositories with checkboxes", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    const view = render(RepoTypeahead, {
      props: {
        selected: undefined,
        onchange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    await fireEvent.mouseDown(
      screen.getByRole("option", {
        name: /github.com\/import-lab\/api/i,
      }),
    );
    expect(onchange).toHaveBeenLastCalledWith("github|github.com/import-lab/api");

    await view.rerender({
      selected: "github|github.com/import-lab/api",
      onchange,
    });
    await fireEvent.mouseDown(
      screen.getByRole("option", {
        name: /github.com\/import-lab\/web/i,
      }),
    );
    expect(onchange).toHaveBeenLastCalledWith("github|github.com/import-lab/api,github|github.com/import-lab/web");
  });

  it("selecting an owner row selects all repos beneath it", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, { props: { selected: undefined, onchange } });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    const ownerCheckbox = screen
      .getByRole("option", { name: "github.com/import-lab" })
      .querySelector("input[type='checkbox']") as HTMLInputElement;
    await fireEvent.mouseDown(ownerCheckbox);

    expect(onchange).toHaveBeenLastCalledWith("github|github.com/import-lab/api,github|github.com/import-lab/web");
  });

  it("filters to matching leaves while keeping their owner visible", async () => {
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, {
      props: { selected: undefined, onchange: vi.fn() },
    });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    await fireEvent.input(screen.getByPlaceholderText("Filter repos..."), {
      target: { value: "web" },
    });

    await waitFor(() => {
      expect(
        screen.getByRole("option", {
          name: "github/github.com/import-lab/web",
        }),
      ).toBeTruthy();
      expect(
        screen.queryByRole("option", {
          name: "github/github.com/import-lab/api",
        }),
      ).toBeNull();
    });
  });

  it("clicking an owner row body expands/collapses without selecting", async () => {
    const onchange = vi.fn();
    // Two repos under import-lab so it renders a collapsible owner row; a
    // single-repo owner auto-flattens to one leaf and has no caret to toggle.
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);
    render(RepoTypeahead, { props: { selected: undefined, onchange } });
    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));

    // leaves visible initially
    expect(screen.getByRole("option", { name: "github/github.com/import-lab/api" })).toBeTruthy();
    // click the owner row body (its caret button has aria-label "Toggle import-lab";
    // click the row <li> itself, not the caret) -> collapses, hides leaves, selects nothing
    await fireEvent.mouseDown(screen.getByRole("option", { name: "github.com/import-lab" }));
    // NOTE: owner row body mousedown should toggle EXPAND, not select. After collapse the leaves are gone.
    await waitFor(() => {
      expect(
        screen.queryByRole("option", {
          name: "github/github.com/import-lab/api",
        }),
      ).toBeNull();
    });
    expect(onchange).not.toHaveBeenCalled();
  });

  it("clicking a leaf checkbox selects only that leaf", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);
    render(RepoTypeahead, { props: { selected: undefined, onchange } });
    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    const leaf = screen.getByRole("option", {
      name: "github/github.com/import-lab/api",
    });
    const checkbox = leaf.querySelector("input[type='checkbox']") as HTMLInputElement;
    await fireEvent.mouseDown(checkbox);
    expect(onchange).toHaveBeenLastCalledWith("github|github.com/import-lab/api");
  });

  it("drops removed repos after settings remove matching entries", async () => {
    const fetchedRepos = [
      {
        Platform: "github",
        PlatformHost: "github.com",
        Owner: "roborev-dev",
        Name: "kenn-forge",
      },
    ] as unknown as Repo[];
    const onchange = vi.fn();

    getRepos
      .mockResolvedValueOnce({
        data: fetchedRepos,
        error: undefined,
      })
      .mockResolvedValueOnce({
        data: [],
        error: undefined,
      });

    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "roborev-dev",
        name: "*",
        repo_path: "roborev-dev/*",
        is_glob: true,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, {
      props: {
        selected: "github.com/roborev-dev/kenn-forge",
        onchange,
      },
    });

    await fireEvent.click(
      screen.getByRole("button", {
        name: /github.com\/roborev-dev\/kenn-forge/i,
      }),
    );

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /roborev-dev\/kenn-forge/i })).toBeTruthy();
    });

    settingsStore.setConfiguredRepos([]);

    await waitFor(() => {
      expect(
        screen.queryByRole("option", {
          name: /roborev-dev\/kenn-forge/i,
        }),
      ).toBeNull();
      expect(onchange).toHaveBeenCalledWith(undefined);
    });
  });

  it("collapses and expands the focused owner with arrow keys", async () => {
    // Two repos under import-lab so it renders a collapsible owner row; a
    // single-repo owner auto-flattens to one leaf and has no caret to toggle.
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, {
      props: { selected: undefined, onchange: vi.fn() },
    });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    const input = screen.getByPlaceholderText("Filter repos...");

    // leaves visible by default
    expect(screen.getByRole("option", { name: "github/github.com/import-lab/api" })).toBeTruthy();

    // move highlight onto the owner row (index 1) and collapse it
    await fireEvent.keyDown(input, { key: "ArrowDown" });
    await fireEvent.keyDown(input, { key: "ArrowLeft" });

    await waitFor(() => {
      expect(
        screen.queryByRole("option", {
          name: "github/github.com/import-lab/api",
        }),
      ).toBeNull();
    });

    await fireEvent.keyDown(input, { key: "ArrowRight" });
    await waitFor(() => {
      expect(
        screen.getByRole("option", {
          name: "github/github.com/import-lab/api",
        }),
      ).toBeTruthy();
    });
  });

  it("moves focus from a leaf to its parent owner on ArrowLeft", async () => {
    // Single host auto-flattens, so rows are: [All repos], import-lab (owner,
    // depth 0), api (leaf, depth 1), web (leaf, depth 1). ArrowLeft on a leaf
    // should jump focus up to the owner row, per the keyboard contract.
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, {
      props: { selected: undefined, onchange: vi.fn() },
    });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    const input = screen.getByPlaceholderText("Filter repos...");

    // ArrowDown onto the owner row, then onto the first leaf (api).
    await fireEvent.keyDown(input, { key: "ArrowDown" });
    await fireEvent.keyDown(input, { key: "ArrowDown" });
    const leaf = screen.getByRole("option", {
      name: "github/github.com/import-lab/api",
    });
    await waitFor(() => expect(leaf.classList.contains("highlighted")).toBe(true));

    // ArrowLeft on the leaf moves focus to its parent owner.
    await fireEvent.keyDown(input, { key: "ArrowLeft" });
    await waitFor(() => {
      const owner = screen.getByRole("option", {
        name: "github.com/import-lab",
      });
      expect(owner.classList.contains("highlighted")).toBe(true);
      expect(
        screen.getByRole("option", { name: "github/github.com/import-lab/api" }).classList.contains("highlighted"),
      ).toBe(false);
    });
  });

  it("toggles selection of the focused row with space", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "api",
        repo_path: "import-lab/api",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "web",
        repo_path: "import-lab/web",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, { props: { selected: undefined, onchange } });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    const input = screen.getByPlaceholderText("Filter repos...");

    // highlight the owner row and select its subtree
    await fireEvent.keyDown(input, { key: "ArrowDown" });
    await fireEvent.keyDown(input, { key: " " });

    expect(onchange).toHaveBeenLastCalledWith("github|github.com/import-lab/api,github|github.com/import-lab/web");
  });

  it("uses provider-qualified values when configured repos collide by host and path", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "gitea",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, { props: { selected: undefined, onchange } });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    await fireEvent.input(screen.getByPlaceholderText("Filter repos..."), {
      target: { value: "gitea/widgets" },
    });
    const giteaRow = screen.getByRole("option", {
      name: "gitea/github.com/acme/widgets",
    });
    expect(giteaRow.querySelector(".repo-tree-label")?.textContent).toBe("gitea/widgets");
    expect(screen.queryByRole("option", { name: "github/github.com/acme/widgets" })).toBeNull();
    await fireEvent.mouseDown(
      screen.getByRole("option", {
        name: "gitea/github.com/acme/widgets",
      }),
    );

    expect(onchange).toHaveBeenLastCalledWith("gitea|github.com/acme/widgets");
  });

  it("keeps provider-qualified selected values valid in desktop validation", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "gitea",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, {
      props: {
        selected: "gitea|github.com/acme/widgets",
        onchange,
      },
    });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Select repository: gitea/github.com/acme/widgets" })).toBeTruthy();
    });
    expect(onchange).not.toHaveBeenCalled();
  });

  it("drops stale provider slash values before desktop validation removes missing repos", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        is_glob: false,
        matched_repo_count: 1,
      },
    ]);

    render(RepoTypeahead, {
      props: {
        selected: "github/github.com/acme/widgets,github|github.com/acme/missing",
        onchange,
      },
    });

    await waitFor(() => {
      expect(onchange).toHaveBeenCalledWith("github|github.com/acme/missing");
    });
  });
});
