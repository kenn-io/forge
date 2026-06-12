// Converted from frontend/tests/e2e-full/repo-filter-multiselect.spec.ts.
//
// These cases exercise the RepoTypeahead component contract (rendered rows,
// emitted onchange values, and persistence through the real filter store)
// against the same repo set the seeded e2e server exposes: the configured
// repos come from src/lib/testing/e2e-fixtures/settings.json and the synced
// repos from src/lib/testing/e2e-fixtures/repos.json, fed through the same
// two inputs the app uses (the settings store and the /repos API fetch).
//
// The original Playwright assertions about which issue list items remain
// visible after filtering are intentionally dropped here: list filtering is
// server-side behavior of the issues endpoint, not part of this component's
// contract. The "keyboard navigation survives a real checkbox click" case
// stays in Playwright because it depends on the browser's native
// mousedown -> focus default action, which jsdom does not perform.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vite-plus/test";

import type { ConfigRepo, Repo } from "@middleman/ui/api/types";
import { createSettingsStore } from "@middleman/ui/stores/settings";
import { client } from "../api/runtime.js";
import { getGlobalRepo, setGlobalRepo } from "../stores/filter.svelte.js";
import settingsFixture from "../testing/e2e-fixtures/settings.json";
import reposFixture from "../testing/e2e-fixtures/repos.json";
import RepoTypeahead from "./RepoTypeahead.svelte";

let settingsStore: ReturnType<typeof createSettingsStore>;

vi.mock("@middleman/ui", () => ({
  getStores: () => ({
    settings: settingsStore,
  }),
}));

vi.mock("../api/runtime.js", () => ({
  client: {
    GET: vi.fn(() => Promise.resolve({ data: [], error: undefined })),
  },
}));

const getRepos = client.GET as unknown as Mock<() => Promise<{ data: Repo[]; error: undefined }>>;

const configuredRepos = settingsFixture.repos as ConfigRepo[];
const fetchedRepos = reposFixture as unknown as Repo[];

function checkboxOf(option: HTMLElement): HTMLInputElement {
  const box = option.querySelector("input[type='checkbox']");
  expect(box).not.toBeNull();
  return box as HTMLInputElement;
}

describe("RepoTypeahead multi-select (e2e fixture repo set)", () => {
  beforeEach(() => {
    // The expansion store persists collapsed nodes and the filter store
    // persists the selection to localStorage; reset both so each case starts
    // from "All repos" with every tree node expanded.
    setGlobalRepo(undefined);
    localStorage.clear();
    settingsStore = createSettingsStore();
    settingsStore.setConfiguredRepos(configuredRepos);
    getRepos.mockResolvedValue({ data: fetchedRepos, error: undefined });
  });

  afterEach(() => {
    cleanup();
  });

  it("repository selector filters dashboard lists by multiple selected repos", async () => {
    // Mirror AppHeader's wiring: onchange feeds the real filter store (which
    // persists to localStorage) and `selected` reflects it back.
    const emitted: (string | undefined)[] = [];
    const onchange = (repo: string | undefined) => {
      emitted.push(repo);
      setGlobalRepo(repo);
    };

    const view = render(RepoTypeahead, {
      props: { selected: getGlobalRepo(), onchange },
    });

    await fireEvent.click(screen.getByTitle("Select repository"));

    await waitFor(() => {
      expect(screen.getByRole("option", { name: "github.com/acme/widgets" })).toBeTruthy();
    });

    await fireEvent.mouseDown(screen.getByRole("option", { name: "github.com/acme/widgets" }));
    expect(emitted.at(-1)).toBe("github.com/acme/widgets");
    await view.rerender({ selected: getGlobalRepo(), onchange });
    expect(checkboxOf(screen.getByRole("option", { name: "github.com/acme/widgets" })).checked).toBe(true);

    await fireEvent.mouseDown(screen.getByRole("option", { name: "github.com/acme/tools" }));
    expect(emitted.at(-1)).toBe("github.com/acme/widgets,github.com/acme/tools");
    await view.rerender({ selected: getGlobalRepo(), onchange });
    expect(checkboxOf(screen.getByRole("option", { name: "github.com/acme/widgets" })).checked).toBe(true);
    expect(checkboxOf(screen.getByRole("option", { name: "github.com/acme/tools" })).checked).toBe(true);

    // Escape closes the dropdown and the trigger summarizes the selection.
    await fireEvent.keyDown(screen.getByPlaceholderText("Filter repos..."), { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
    const trigger = screen.getByTitle("Select repository");
    expect(trigger.querySelector(".typeahead-value")?.textContent).toBe("2 repos");

    // The real filter store persisted the selection.
    expect(localStorage.getItem("middleman-filter-repo")).toBe("github.com/acme/widgets,github.com/acme/tools");
  });

  it("flattened single-repo owner shows the owner/repo path, not a bare repo name", async () => {
    // gitlab.example.com/group has one repo "project", so it auto-flattens to
    // a single row. That row must read "group/project" (visible text) to stay
    // distinguishable from a same-named repo under another owner, while its
    // accessible name remains the full path.
    render(RepoTypeahead, {
      props: { selected: undefined, onchange: vi.fn() },
    });

    await fireEvent.click(screen.getByTitle("Select repository"));

    await waitFor(() => {
      expect(screen.getByRole("option", { name: "gitlab.example.com/group/project" })).toBeTruthy();
    });
    const row = screen.getByRole("option", { name: "gitlab.example.com/group/project" });
    // visible label is "group/project", not the bare "project"
    expect(row.querySelector(".repo-tree-label")?.textContent?.trim()).toBe("group/project");
  });

  it("repository selector cascades an owner group to all its repos", async () => {
    const emitted: (string | undefined)[] = [];
    const onchange = (repo: string | undefined) => {
      emitted.push(repo);
      setGlobalRepo(repo);
    };

    render(RepoTypeahead, {
      props: { selected: getGlobalRepo(), onchange },
    });

    await fireEvent.click(screen.getByTitle("Select repository"));

    await waitFor(() => {
      expect(screen.getByRole("option", { name: "github.com/acme" })).toBeTruthy();
    });

    // The owner row's checkbox cascades selection to every repo under that
    // owner. The row body would only toggle expand/collapse, so the checkbox
    // is the deliberate target. Selection is wired to mousedown (see
    // RepoTreeNode.checkboxMouseDown), so dispatch that event directly.
    const ownerCheckbox = checkboxOf(screen.getByRole("option", { name: "github.com/acme" }));
    await fireEvent.mouseDown(ownerCheckbox);

    const value = emitted.at(-1);
    expect(value).toContain("github.com/acme/widgets");
    expect(value).toContain("github.com/acme/tools");
    expect(value).toContain("github.com/acme/archived");

    // The real filter store persisted the cascaded selection.
    const stored = localStorage.getItem("middleman-filter-repo");
    expect(stored).toContain("github.com/acme/widgets");
    expect(stored).toContain("github.com/acme/tools");
    expect(stored).toContain("github.com/acme/archived");
  });
});
