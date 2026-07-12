import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { defaultProviderCapabilities } from "../repositories/repoSummary.js";
import KataProjectMappingsSettings from "./KataProjectMappingsSettings.svelte";

const { mockUpdateSettings, mockFetchKataDaemons, mockGetKataProjectMappings } = vi.hoisted(() => ({
  mockUpdateSettings: vi.fn(),
  mockFetchKataDaemons: vi.fn(),
  mockGetKataProjectMappings: vi.fn(),
}));

vi.mock("../../api/settings.js", () => ({
  updateSettings: mockUpdateSettings,
}));

vi.mock("../../stores/embed-config.svelte.js", () => ({
  isEmbedded: () => false,
}));

vi.mock("../../api/kata/daemons.js", () => ({
  fetchKataDaemons: mockFetchKataDaemons,
}));

vi.mock("../../api/kata/workspaces.js", () => ({
  getKataProjectMappings: mockGetKataProjectMappings,
}));

describe("KataProjectMappingsSettings", () => {
  beforeEach(() => {
    mockFetchKataDaemons.mockResolvedValue([
      { id: "work", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
    ]);
    mockGetKataProjectMappings.mockResolvedValue({ daemon_id: "work", projects: [], targets: [] });
  });

  afterEach(() => {
    cleanup();
    mockUpdateSettings.mockReset();
    mockFetchKataDaemons.mockReset();
    mockGetKataProjectMappings.mockReset();
  });

  it("treats missing Kata project mappings as empty settings", () => {
    render(KataProjectMappingsSettings, {
      props: {
        mappings: undefined,
        onUpdate: vi.fn(),
      },
    });

    expect(screen.getByRole("button", { name: "Add mapping" })).toBeTruthy();
    expect(screen.getByText("No registered Middleman projects with repository identity are available.")).toBeTruthy();
  });

  it("shows the effective mapping and prefills a registered-project override", async () => {
    mockGetKataProjectMappings.mockResolvedValue({
      daemon_id: "work",
      projects: [
        {
          daemon_id: "work",
          project_uid: "project-kata",
          project_name: "Kata",
          status: "mapped",
          source: "registered_project",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "kenn-io",
            name: "middleman",
            repo_path: "kenn-io/middleman",
            capabilities: defaultProviderCapabilities,
          },
        },
      ],
      targets: [
        {
          project_id: "project-middleman",
          display_name: "Middleman",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "kenn-io",
            name: "middleman",
            repo_path: "kenn-io/middleman",
            capabilities: defaultProviderCapabilities,
          },
        },
      ],
    });

    render(KataProjectMappingsSettings, {
      props: { mappings: [], onUpdate: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByText("Registered project")).toBeTruthy();
      expect(screen.getByText("kenn-io/middleman")).toBeTruthy();
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add override" }));

    expect((screen.getByLabelText("Kata project project-kata daemon ID") as HTMLInputElement).value).toBe("work");
    expect((screen.getByLabelText("Kata project project-kata UID") as HTMLInputElement).value).toBe("project-kata");
    expect(screen.getByRole("combobox", { name: /project-kata Middleman project/ }).textContent).toContain("Middleman");
  });

  it("saves a Kata mapping to a selected known Middleman project", async () => {
    const savedMappings = [
      {
        daemon_id: "work",
        project_uid: "project-kata",
        provider: "github",
        platform_host: "github.com",
        repo_path: "kenn-io/middleman",
      },
    ];
    mockUpdateSettings.mockResolvedValue({ kata_projects: savedMappings });
    mockGetKataProjectMappings.mockResolvedValue({
      daemon_id: "work",
      projects: [],
      targets: [
        {
          project_id: "project-middleman",
          display_name: "Middleman",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "kenn-io",
            name: "middleman",
            repo_path: "kenn-io/middleman",
            capabilities: defaultProviderCapabilities,
          },
        },
      ],
    });
    const onUpdate = vi.fn();

    render(KataProjectMappingsSettings, {
      props: {
        mappings: [],
        onUpdate,
      },
    });

    await waitFor(() => {
      expect((screen.getByRole("button", { name: "Add mapping" }) as HTMLButtonElement).disabled).toBe(false);
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add mapping" }));
    await fireEvent.input(screen.getByLabelText("Kata project mapping 1 daemon ID"), {
      target: { value: "work" },
    });
    await fireEvent.input(screen.getByLabelText("Kata project mapping 1 UID"), {
      target: { value: "project-kata" },
    });

    await fireEvent.click(screen.getByRole("combobox", { name: /Middleman project/ }));
    expect(screen.getByRole("option", { name: "Middleman · kenn-io/middleman" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Save Kata mappings" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({ kata_projects: savedMappings });
      expect(onUpdate).toHaveBeenCalledWith(savedMappings);
    });
  });
});
