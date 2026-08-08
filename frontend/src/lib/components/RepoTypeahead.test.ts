import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vite-plus/test";

import { interactiveConfigRepos } from "@kenn-forge/ui";
import type { Repo } from "@kenn-forge/ui/api/types";
import { createSettingsStore } from "@kenn-forge/ui/stores/settings";
import { client } from "../api/runtime.js";
import RepoTypeahead from "./RepoTypeahead.svelte";

let settingsStore: ReturnType<typeof createSettingsStore>;

vi.mock("@kenn-forge/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@kenn-forge/ui")>();
  return {
    ...actual,
    getStores: () => ({
      settings: settingsStore,
    }),
  };
});

vi.mock("../api/runtime.js", () => ({
  client: {
    GET: vi.fn(() => Promise.resolve({ data: [], error: undefined })),
  },
}));

const getRepos = client.GET as unknown as Mock<() => Promise<{ data: Repo[]; error: undefined }>>;

describe("RepoTypeahead", () => {
  beforeEach(() => {
    // The expansion store persists collapsed nodes to localStorage, so clear
    // it between tests to keep each case from inheriting another's tree state.
    localStorage.clear();
    settingsStore = createSettingsStore();
    settingsStore.setConfiguredRepos([]);
    getRepos.mockImplementation(async () => ({
      data: interactiveConfigRepos(settingsStore.getConfiguredRepos()).map((repo) => ({
        Platform: repo.provider,
        PlatformHost: repo.platform_host,
        Owner: repo.owner,
        Name: repo.name,
      })) as Repo[],
      error: undefined,
    }));
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

  it("omits hidden configured repos and clears an active hidden selection", async () => {
    const onchange = vi.fn();
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "archive",
        repo_path: "import-lab/archive",
        is_glob: false,
        matched_repo_count: 1,
        hide_from_ui: true,
      },
    ]);

    render(RepoTypeahead, {
      props: {
        selected: "github|github.com/import-lab/archive",
        onchange,
      },
    });

    await waitFor(() => {
      expect(onchange).toHaveBeenCalledWith(undefined);
    });
    await fireEvent.click(screen.getByRole("button", { name: /import-lab\/archive/i }));
    expect(screen.queryByRole("option", { name: /import-lab\/archive/i })).toBeNull();
  });

  it("does not re-add an exact repo hidden by an overlapping glob", async () => {
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "archive-*",
        repo_path: "import-lab/archive-*",
        is_glob: true,
        matched_repo_count: 1,
        hide_from_ui: true,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "archive-api",
        repo_path: "import-lab/archive-api",
        is_glob: false,
        matched_repo_count: 1,
        hide_from_ui: false,
      },
    ]);

    render(RepoTypeahead, {
      props: { selected: undefined, onchange: vi.fn() },
    });

    await fireEvent.click(screen.getByRole("button", { name: /all repos/i }));
    expect(screen.queryByRole("option", { name: /import-lab\/archive-api/i })).toBeNull();
  });

  it("does not re-add a resolved stale path when the renamed repository is hidden", async () => {
    const onchange = vi.fn();
    getRepos.mockResolvedValue({ data: [], error: undefined });
    settingsStore.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "legacy-service",
        repo_path: "import-lab/legacy-service",
        is_glob: false,
        matched_repo_count: 1,
      },
      {
        provider: "github",
        platform_host: "github.com",
        owner: "import-lab",
        name: "archive-*",
        repo_path: "import-lab/archive-*",
        is_glob: true,
        matched_repo_count: 1,
        hide_from_ui: true,
      },
    ]);

    render(RepoTypeahead, {
      props: {
        selected: "github|github.com/import-lab/legacy-service",
        onchange,
      },
    });

    await waitFor(() => {
      expect(onchange).toHaveBeenCalledWith(undefined);
    });
    await fireEvent.click(screen.getByRole("button", { name: /legacy-service/i }));
    expect(screen.queryByRole("option", { name: /legacy-service/i })).toBeNull();
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
