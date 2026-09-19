import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS, type Settings } from "../../api/types.js";
import { makeStartupSnapshot } from "../../../test/startupSnapshot.js";

const {
  loadSettings,
  setAirplaneMode,
  persistSettings,
  setDetailSettings,
  setLaunchTargets,
  setQuickActions,
  setRepoPresets,
} = vi.hoisted(() => ({
  loadSettings: vi.fn(),
  setAirplaneMode: vi.fn(),
  persistSettings: vi.fn(),
  setDetailSettings: vi.fn(),
  setLaunchTargets: vi.fn(),
  setQuickActions: vi.fn(),
  setRepoPresets: vi.fn(),
}));

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    settings: {
      setAirplaneMode,
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
      setTerminalSettings: vi.fn(),
      setConfiguredRepos: vi.fn(),
      setRepoPresets,
      setModeVisibility: vi.fn(),
      setPullRequestSettings: vi.fn(),
      setDetailSettings,
      getWorkspaceSettings: () => ({ auto_assign_on_create: false, default_sidebar_view: "diff" as const }),
      setWorkspaceSettings: vi.fn(),
      getRoborevSettings: () => ({ init_managed_clones: false }),
      setRoborevSettings: vi.fn(),
      setLaunchTargets,
      setQuickActions,
    },
  }),
}));

vi.mock("../../app/startup-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/startup-workflow.js")>();
  return {
    ...actual,
    StartupWorkflowLive: Layer.succeed(actual.StartupWorkflow)({
      start: Effect.suspend(() => loadSettings()),
      invalidate: Effect.void,
    }),
  };
});

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({ persist: (request) => persistSettings(request) }),
  };
});

vi.mock("./RepoSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./ActivitySettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./TerminalSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./ModeVisibilitySettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./AgentSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./FleetSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./MCPSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./KataProjectMappingsSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./PullRequestSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./DetailSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./WorkspaceSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));

import SettingsPage from "./SettingsPage.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

type LaunchTargets = NonNullable<Settings["launch_targets"]>;

const codexTarget = {
  key: "codex",
  label: "Codex",
  kind: "agent",
  source: "builtin",
  command: ["codex"],
  available: true,
  disabled_reason: "",
} satisfies LaunchTargets[number];

function makeSettings(): Settings {
  return makeStartupSnapshot({
    repo_presets: [
      {
        name: "Review queue",
        repos: [
          { provider: "github", platform_host: "github.com", platform_repo_id: "R_widgets", repo_path: "acme/widgets" },
        ],
      },
    ],
    launch_targets: [codexTarget],
    quick_actions: [{ label: "Rebase", agent: "codex", prompt: "rebase this pull request onto main" }],
  });
}

describe("SettingsPage", () => {
  afterEach(() => {
    cleanup();
    loadSettings.mockReset();
    setAirplaneMode.mockReset();
    persistSettings.mockReset();
    setLaunchTargets.mockReset();
    setQuickActions.mockReset();
    setDetailSettings.mockReset();
    setRepoPresets.mockReset();
  });

  it("persists airplane mode and applies the confirmed value", async () => {
    const settings = makeSettings();
    loadSettings.mockReturnValue(Effect.succeed(settings));
    persistSettings.mockReturnValue(Effect.succeed({ ...settings, airplane_mode: true }));
    render(SettingsRuntimeHarness, { props: { component: SettingsPage, componentProps: {} } });
    await screen.findByText("Providers");
    await fireEvent.click(screen.getByRole("button", { name: /^Sync/ }));
    await fireEvent.click(screen.getByRole("switch", { name: "Airplane mode" }));
    await waitFor(() => expect(setAirplaneMode).toHaveBeenLastCalledWith(true));
    expect(persistSettings.mock.calls[0]?.[0]()).toEqual({ airplane_mode: true });
    expect((screen.getByRole("switch", { name: "Airplane mode" }) as HTMLInputElement).checked).toBe(true);
  });

  it("keeps airplane mode off when saving fails", async () => {
    loadSettings.mockReturnValue(Effect.succeed(makeSettings()));
    persistSettings.mockReturnValue(
      Effect.fail({
        _tag: "TransientTransportError",
        operation: "save settings",
        cause: new Error("offline"),
      }),
    );
    render(SettingsRuntimeHarness, { props: { component: SettingsPage, componentProps: {} } });
    await screen.findByText("Providers");
    await fireEvent.click(screen.getByRole("button", { name: /^Sync/ }));
    await fireEvent.click(screen.getByRole("switch", { name: "Airplane mode" }));
    await screen.findByRole("alert");
    expect((screen.getByRole("switch", { name: "Airplane mode" }) as HTMLInputElement).checked).toBe(false);
    expect(setAirplaneMode).toHaveBeenLastCalledWith(false);
  });

  it("hydrates launch targets into the shared settings store on initial load", async () => {
    const settings = makeSettings();
    loadSettings.mockReturnValue(Effect.succeed(settings));

    render(SettingsRuntimeHarness, {
      props: { component: SettingsPage, componentProps: {} },
    });

    await waitFor(() => {
      expect(setLaunchTargets).toHaveBeenCalledWith(settings.launch_targets);
    });
  });

  it("hydrates quick actions into the shared settings store on initial load", async () => {
    // The PR and issue headers read quick actions from the shared store, so a
    // Settings-page load that recovers from a failed startup hydration must
    // publish them too or the headers keep stale values.
    const settings = makeSettings();
    loadSettings.mockReturnValue(Effect.succeed(settings));

    render(SettingsRuntimeHarness, {
      props: { component: SettingsPage, componentProps: {} },
    });

    await waitFor(() => {
      expect(setQuickActions).toHaveBeenCalledWith(settings.quick_actions);
    });
  });

  it("hydrates repository presets into the shared settings store on initial load", async () => {
    const settings = makeSettings();
    loadSettings.mockReturnValue(Effect.succeed(settings));

    render(SettingsRuntimeHarness, {
      props: { component: SettingsPage, componentProps: {} },
    });

    await waitFor(() => {
      expect(setRepoPresets).toHaveBeenCalledWith(settings.repo_presets);
    });
  });

  it("hydrates detail settings into the shared settings store on initial load", async () => {
    const settings = makeSettings();
    loadSettings.mockReturnValue(Effect.succeed(settings));

    render(SettingsRuntimeHarness, {
      props: { component: SettingsPage, componentProps: {} },
    });

    await waitFor(() => {
      expect(setDetailSettings).toHaveBeenCalledWith(settings.detail);
    });
  });

  it("keeps the declared settings groups on a fleet spoke", async () => {
    const settings = makeSettings();
    settings.fleet.role = "spoke";
    settings.fleet.hub = {
      node_id: "44444444444444444444444444444444",
      base_url: "https://hub.example",
    };
    loadSettings.mockReturnValue(Effect.succeed(settings));

    render(SettingsRuntimeHarness, {
      props: { component: SettingsPage, componentProps: {} },
    });

    expect(await screen.findByText("Providers")).toBeTruthy();
    expect(screen.getByText("Workflow")).toBeTruthy();
    expect(screen.getByText("Workspace")).toBeTruthy();
    expect(screen.getByText("Navigation")).toBeTruthy();
    expect(screen.getByText("System")).toBeTruthy();
    expect(screen.queryByText("Hub policy")).toBeNull();
  });
});
