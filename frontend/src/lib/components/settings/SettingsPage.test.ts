import { cleanup, render, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS, type Settings } from "../../api/types.js";
import { makeStartupSnapshot } from "../../../test/startupSnapshot.js";

const { loadSettings, setDetailSettings, setLaunchTargets, setRepoPresets } = vi.hoisted(() => ({
  loadSettings: vi.fn(),
  setDetailSettings: vi.fn(),
  setLaunchTargets: vi.fn(),
  setRepoPresets: vi.fn(),
}));

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    settings: {
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
  });
}

describe("SettingsPage", () => {
  afterEach(() => {
    cleanup();
    loadSettings.mockReset();
    setLaunchTargets.mockReset();
    setDetailSettings.mockReset();
    setRepoPresets.mockReset();
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
});
