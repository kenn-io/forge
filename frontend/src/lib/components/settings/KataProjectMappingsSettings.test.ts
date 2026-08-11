import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { defaultProviderCapabilities } from "../repositories/repoSummary.js";
import KataProjectMappingsSettings from "./KataProjectMappingsSettingsRuntimeHarness.svelte";

const { mockPersistSettings, mockFetchKataDaemons, mockGetKataProjectMappings } = vi.hoisted(() => ({
  mockPersistSettings: vi.fn(),
  mockFetchKataDaemons: vi.fn(),
  mockGetKataProjectMappings: vi.fn(),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      persist: (request) => Effect.promise(() => mockPersistSettings(request())),
    }),
  };
});

vi.mock("../../stores/embed-config.svelte.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/embed-config.svelte.js")>()),
  isEmbedded: () => false,
}));

vi.mock("../../api/kata/integration.js", async () => {
  const { Effect } = await import("effect");
  const asPromise = <T>(value: T) => (Effect.isEffect(value) ? Effect.runPromise(value) : value);
  return {
    fetchKataDaemons: (...args: unknown[]) => asPromise(mockFetchKataDaemons(...args)),
    getKataProjectMappings: (...args: unknown[]) => asPromise(mockGetKataProjectMappings(...args)),
  };
});

describe("KataProjectMappingsSettings", () => {
  beforeEach(() => {
    mockFetchKataDaemons.mockReturnValue(
      Effect.succeed([{ id: "work", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }]),
    );
    mockGetKataProjectMappings.mockReturnValue(Effect.succeed({ daemon_id: "work", projects: [], targets: [] }));
  });

  afterEach(() => {
    cleanup();
    mockPersistSettings.mockReset();
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

  it("shows the effective mapping and prefills a registered-project override", async () => {
    mockGetKataProjectMappings.mockReturnValue(
      Effect.succeed({
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
            display_name: "Kenn Forge",
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
      }),
    );

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
    expect(screen.getByRole("button", { name: /project-kata repository target/ }).textContent).toContain("Kenn Forge");
  });

  it("saves a Kata mapping to a selected known Kenn Forge project", async () => {
    const savedMappings = [
      {
        daemon_id: "work",
        project_uid: "project-kata",
        provider: "github",
        platform_host: "github.com",
        repo_path: "kenn-io/middleman",
      },
    ];
    mockPersistSettings.mockResolvedValue({ kata_projects: savedMappings });
    mockGetKataProjectMappings.mockReturnValue(
      Effect.succeed({
        daemon_id: "work",
        projects: [],
        targets: [
          {
            display_name: "Kenn Forge",
            repo: {
              provider: "github",
              platform_host: "github.com",
              owner: "kenn-io",
              name: "middleman",
              repo_path: "kenn-io/middleman",
              capabilities: defaultProviderCapabilities,
            },
          },
          {
            display_name: "Tools",
            repo: {
              provider: "github",
              platform_host: "github.com",
              owner: "acme",
              name: "tools",
              repo_path: "acme/tools",
              capabilities: defaultProviderCapabilities,
            },
          },
        ],
      }),
    );
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

    const pickerName = "Kata project project-kata repository target";
    await fireEvent.click(screen.getByRole("button", { name: pickerName }));
    const query = screen.getByRole("combobox", { name: pickerName });
    await fireEvent.input(query, { target: { value: "middle" } });

    expect(screen.getByRole("option", { name: "Kenn Forge · kenn-io/middleman" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: "Tools · acme/tools" })).toBeNull();
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Kenn Forge · kenn-io/middleman" }));

    await fireEvent.click(screen.getByRole("button", { name: "Save Kata mappings" }));

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({ kata_projects: savedMappings });
      expect(onUpdate).toHaveBeenCalledWith(savedMappings);
    });
  });

  it("keeps a newer daemon selection when the post-save diagnostics refresh settles late", async () => {
    const configuredMapping = {
      project_uid: "project-old",
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/old",
    };
    const workDiagnostics = { daemon_id: "work", projects: [], targets: [] };
    const personalDiagnostics = { daemon_id: "personal", projects: [], targets: [] };
    let completePostSaveRefresh: ((value: typeof workDiagnostics) => void) | undefined;
    const postSaveRefresh = new Promise<typeof workDiagnostics>((resolve) => {
      completePostSaveRefresh = resolve;
    });
    mockFetchKataDaemons.mockReturnValue(
      Effect.succeed([
        { id: "work", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
        { id: "personal", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
      ]),
    );
    mockGetKataProjectMappings
      .mockReturnValueOnce(Effect.succeed(workDiagnostics))
      .mockReturnValueOnce(Effect.promise(() => postSaveRefresh))
      .mockReturnValueOnce(Effect.succeed(personalDiagnostics));
    mockPersistSettings.mockResolvedValue({ kata_projects: [] });
    render(KataProjectMappingsSettings, {
      props: { mappings: [configuredMapping], onUpdate: vi.fn() },
    });

    await screen.findByRole("combobox", { name: /Kata mapping daemon: work/ });
    await fireEvent.click(screen.getByRole("button", { name: "Remove Kata project mapping project-old" }));
    await fireEvent.click(screen.getByRole("button", { name: "Save Kata mappings" }));
    await waitFor(() => expect(mockGetKataProjectMappings).toHaveBeenCalledTimes(2));

    await fireEvent.click(screen.getByRole("combobox", { name: /Kata mapping daemon: work/ }));
    await fireEvent.click(screen.getByRole("option", { name: /personal.*Health: connected/ }));
    await screen.findByRole("combobox", { name: /Kata mapping daemon: personal/ });
    if (!completePostSaveRefresh) throw new Error("post-save diagnostics refresh did not start");
    completePostSaveRefresh(workDiagnostics);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(screen.getByRole("combobox", { name: /Kata mapping daemon: personal/ })).toBeTruthy();
  });

  it("keeps the daemon roster available when the default daemon diagnostics fail", async () => {
    mockFetchKataDaemons.mockReturnValue(
      Effect.succeed([
        {
          id: "home",
          url: "http://127.0.0.1:7777",
          default: true,
          auth: "none",
          health: "unreachable",
          hint: "Local daemon is not running",
        },
        {
          id: "work",
          url: "http://127.0.0.1:8888",
          default: false,
          auth: "none",
          health: "connected",
          api_schema_version: "0.10.0",
        },
      ]),
    );
    mockGetKataProjectMappings.mockImplementation((daemonID?: string) => {
      if (daemonID === "home") return Effect.fail(new Error("Default Kata daemon is unavailable"));
      return Effect.succeed({ daemon_id: "work", projects: [], targets: [] });
    });

    render(KataProjectMappingsSettings, { props: { mappings: [], onUpdate: vi.fn() } });

    await screen.findByText("Default Kata daemon is unavailable");
    const picker = screen.getByRole("combobox", { name: /Kata mapping daemon: home/ });
    await fireEvent.click(picker);
    expect(screen.getByRole("option", { name: /home.*Local daemon is not running/ })).toBeTruthy();
    expect(screen.getByRole("option", { name: /work.*Health: connected.*API schema 0\.10\.0/ })).toBeTruthy();

    await fireEvent.click(screen.getByRole("option", { name: /work.*Health: connected/ }));

    await screen.findByRole("combobox", { name: /Kata mapping daemon: work/ });
    expect(screen.getByText("This Kata daemon reports no projects.")).toBeTruthy();
    expect(mockFetchKataDaemons).toHaveBeenCalledTimes(1);
    expect(mockGetKataProjectMappings).toHaveBeenNthCalledWith(1, "home");
    expect(mockGetKataProjectMappings).toHaveBeenNthCalledWith(2, "work");
  });

  it("does not publish a pending settings save after the panel unmounts", async () => {
    const savedMappings = [
      {
        daemon_id: "work",
        project_uid: "project-kata",
        provider: "github",
        platform_host: "github.com",
        repo_path: "kenn-io/middleman",
      },
    ];
    let completeSave: ((settings: { kata_projects: typeof savedMappings }) => void) | undefined;
    const pendingSave = new Promise<{ kata_projects: typeof savedMappings }>((resolve) => {
      completeSave = resolve;
    });
    mockPersistSettings.mockReturnValue(pendingSave);
    mockGetKataProjectMappings.mockReturnValue(
      Effect.succeed({
        daemon_id: "work",
        projects: [],
        targets: [
          {
            display_name: "Kenn Forge",
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
      }),
    );
    const onUpdate = vi.fn();
    const view = render(KataProjectMappingsSettings, {
      props: { mappings: [], onUpdate },
    });
    await fireEvent.click(await screen.findByRole("button", { name: "Add mapping" }));
    await fireEvent.input(screen.getByLabelText("Kata project mapping 1 daemon ID"), {
      target: { value: "work" },
    });
    await fireEvent.input(screen.getByLabelText("Kata project mapping 1 UID"), {
      target: { value: "project-kata" },
    });
    const pickerName = "Kata project project-kata repository target";
    await fireEvent.click(screen.getByRole("button", { name: pickerName }));
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Kenn Forge · kenn-io/middleman" }));
    await fireEvent.click(screen.getByRole("button", { name: "Save Kata mappings" }));
    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({ kata_projects: savedMappings });
    });

    view.unmount();
    if (!completeSave) throw new Error("settings save did not start");
    completeSave({ kata_projects: savedMappings });

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("requires an explicit repository choice when inference has no selectable target", async () => {
    mockGetKataProjectMappings.mockReturnValue(
      Effect.succeed({
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
      }),
    );

    render(KataProjectMappingsSettings, { props: { mappings: [], onUpdate: vi.fn() } });
    await fireEvent.click(await screen.findByRole("button", { name: "Add override" }));

    expect(screen.getByRole("button", { name: /project-unmapped repository target/ }).textContent).toContain(
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
