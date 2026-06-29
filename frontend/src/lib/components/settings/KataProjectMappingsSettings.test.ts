import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import KataProjectMappingsSettings from "./KataProjectMappingsSettings.svelte";

const { mockUpdateSettings } = vi.hoisted(() => ({
  mockUpdateSettings: vi.fn(),
}));

vi.mock("../../api/settings.js", () => ({
  updateSettings: mockUpdateSettings,
}));

vi.mock("../../stores/embed-config.svelte.js", () => ({
  isEmbedded: () => false,
}));

describe("KataProjectMappingsSettings", () => {
  afterEach(() => {
    cleanup();
    mockUpdateSettings.mockReset();
  });

  it("treats missing Kata project mappings as empty settings", () => {
    render(KataProjectMappingsSettings, {
      props: {
        mappings: undefined,
        repos: [],
        onUpdate: vi.fn(),
      },
    });

    expect(screen.getByRole("button", { name: "Add mapping" })).toBeTruthy();
    expect(screen.getByText("No exact watched repositories are configured.")).toBeTruthy();
  });

  it("saves a Kata project mapping to an exact watched repository", async () => {
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
    const onUpdate = vi.fn();

    render(KataProjectMappingsSettings, {
      props: {
        mappings: [],
        repos: [
          {
            provider: "github",
            platform_host: "github.com",
            owner: "kenn-io",
            name: "middleman",
            repo_path: "kenn-io/middleman",
            is_glob: false,
            matched_repo_count: 1,
          },
          {
            provider: "github",
            platform_host: "github.com",
            owner: "kenn-io",
            name: "*",
            repo_path: "kenn-io/*",
            is_glob: true,
            matched_repo_count: 3,
          },
        ],
        onUpdate,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add mapping" }));
    await fireEvent.input(screen.getByLabelText("Kata project mapping 1 daemon ID"), {
      target: { value: "work" },
    });
    await fireEvent.input(screen.getByLabelText("Kata project mapping 1 UID"), {
      target: { value: "project-kata" },
    });

    await fireEvent.click(screen.getByRole("combobox", { name: /repository/ }));
    expect(screen.getByRole("option", { name: "github / github.com / kenn-io/middleman" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: "github / github.com / kenn-io/*" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Save Kata mappings" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({ kata_projects: savedMappings });
      expect(onUpdate).toHaveBeenCalledWith(savedMappings);
    });
  });
});
