import { cleanup, render, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS, type Settings } from "../../api/types.js";

const { loadSettings, setLaunchTargets } = vi.hoisted(() => ({
  loadSettings: vi.fn(),
  setLaunchTargets: vi.fn(),
}));

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    settings: {
      getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
      setTerminalSettings: vi.fn(),
      setConfiguredRepos: vi.fn(),
      setModeVisibility: vi.fn(),
      setPullRequestSettings: vi.fn(),
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
vi.mock("./KataProjectMappingsSettings.svelte", async () => ({
  default: (await import("../../testing/AppViewStub.svelte")).default,
}));
vi.mock("./PullRequestSettings.svelte", async () => ({
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
  return {
    repos: [],
    pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
    workspaces: { auto_assign_on_create: false },
    issues: { hide_bots: true },
    kata_projects: [],
    fleet: {
      enabled: false,
      sessions: {},
      peers: [],
      ssh_peers: [],
      restart_required: false,
    },
    activity: {
      view_mode: "threaded",
      time_range: "7d",
      hide_closed: false,
      hide_bots: false,
      collapse_threads: false,
      default_branch_retention_days: 90,
      default_branch_max_commits: 5000,
    },
    terminal: DEFAULT_TERMINAL_SETTINGS,
    modes: {
      activity: true,
      repos: true,
      kata: false,
      docs: false,
      pulls: true,
      issues: true,
      reviews: true,
      workspaces: true,
    },
    notifications: { enabled: true },
    agents: [],
    launch_targets: [codexTarget],
  };
}

describe("SettingsPage", () => {
  afterEach(() => {
    cleanup();
    loadSettings.mockReset();
    setLaunchTargets.mockReset();
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
});
