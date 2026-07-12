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
    expect(screen.getByText("No known repository targets are available.")).toBeTruthy();
  });

  it("does not load diagnostics while Kata mode is disabled", async () => {
    render(KataProjectMappingsSettings, {
      props: { mappings: [], enabled: false, onUpdate: vi.fn() },
    });

    await Promise.resolve();
    expect(mockFetchKataDaemons).not.toHaveBeenCalled();
    expect(mockGetKataProjectMappings).not.toHaveBeenCalled();
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
    expect(screen.getByRole("combobox", { name: /project-kata repository target/ }).textContent).toContain("Middleman");
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

    await fireEvent.click(screen.getByRole("combobox", { name: /repository target/ }));
    const option = screen.getByRole("option", { name: "Middleman · kenn-io/middleman" });
    expect(option).toBeTruthy();
    await fireEvent.click(option);

    await fireEvent.click(screen.getByRole("button", { name: "Save Kata mappings" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({ kata_projects: savedMappings });
      expect(onUpdate).toHaveBeenCalledWith(savedMappings);
    });
  });

  it("requires an explicit repository choice when inference has no selectable target", async () => {
    mockGetKataProjectMappings.mockResolvedValue({
      daemon_id: "work",
      projects: [
        {
          daemon_id: "work",
          project_uid: "project-unmapped",
          project_name: "Unmapped",
          status: "unmapped",
        },
      ],
      targets: [
        {
          display_name: "Unrelated",
          repo: {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "other",
            repo_path: "acme/other",
            capabilities: defaultProviderCapabilities,
          },
        },
      ],
    });

    render(KataProjectMappingsSettings, { props: { mappings: [], onUpdate: vi.fn() } });
    await fireEvent.click(await screen.findByRole("button", { name: "Add override" }));

    expect(screen.getByRole("combobox", { name: /project-unmapped repository target/ }).textContent).toContain(
      "Select a repository",
    );
    expect((screen.getByRole("button", { name: "Save Kata mappings" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("keeps an unavailable configured mapping visible so it can be removed", async () => {
    render(KataProjectMappingsSettings, {
      props: {
        mappings: [
          {
            project_uid: "project-old",
            provider: "github",
            platform_host: "github.com",
            repo_path: "acme/old",
          },
        ],
        onUpdate: vi.fn(),
      },
    });

    expect(screen.getByText("acme/old · unavailable")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove Kata project mapping project-old" })).toBeTruthy();
  });
});
