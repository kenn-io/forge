import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import type { ComponentProps } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const { mockPersistSettings } = vi.hoisted(() => ({
  mockPersistSettings: vi.fn(),
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

Object.defineProperty(Element.prototype, "animate", {
  configurable: true,
  value: () => ({
    cancel: vi.fn(),
    finished: Promise.resolve(),
  }),
});

import AgentSettings from "./AgentSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

function renderAgentSettings(componentProps: ComponentProps<typeof AgentSettings>) {
  return render(SettingsRuntimeHarness, {
    props: { component: AgentSettings, componentProps },
  });
}

async function expandAgent(name: string): Promise<void> {
  await fireEvent.click(screen.getByRole("button", { name: `Edit ${name}` }));
}

describe("AgentSettings", () => {
  afterEach(() => {
    cleanup();
    mockPersistSettings.mockReset();
  });

  it("persists built-in agent binary and argument overrides", async () => {
    mockPersistSettings.mockResolvedValue({
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: ["/opt/codex", "--full-auto"],
          enabled: true,
        },
      ],
    });
    const onUpdate = vi.fn();

    renderAgentSettings({
      agents: [],
      onUpdate,
    });

    await expandAgent("Codex");
    await fireEvent.input(screen.getByLabelText("Codex binary"), {
      target: { value: "/opt/codex" },
    });
    await fireEvent.input(screen.getByLabelText("Codex arguments"), {
      target: { value: "--full-auto" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          {
            key: "codex",
            label: "Codex",
            command: ["/opt/codex", "--full-auto"],
            enabled: true,
          },
        ],
      });
    });
    expect(onUpdate).toHaveBeenCalledWith(
      [
        {
          key: "codex",
          label: "Codex",
          command: ["/opt/codex", "--full-auto"],
          enabled: true,
        },
      ],
      [],
    );
  });

  it("preserves quoted empty arguments when saving", async () => {
    mockPersistSettings.mockResolvedValue({
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: ["codex", ""],
          enabled: true,
        },
      ],
    });
    const onUpdate = vi.fn();

    renderAgentSettings({
      agents: [],
      onUpdate,
    });

    await expandAgent("Codex");
    await fireEvent.input(screen.getByLabelText("Codex arguments"), {
      target: { value: '""' },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          {
            key: "codex",
            label: "Codex",
            command: ["codex", ""],
            enabled: true,
          },
        ],
      });
    });
  });

  it("does not mark explicit default built-in agents dirty", () => {
    const onUpdate = vi.fn();

    renderAgentSettings({
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: ["codex"],
          enabled: true,
        },
      ],
      onUpdate,
    });

    expect(screen.getByLabelText("Codex")).toBeTruthy();
    expect(screen.queryByLabelText("Codex binary")).toBeNull();
    expect(
      (
        screen.getByRole("button", {
          name: "Save workspace agents",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
  });

  it("preserves explicit default built-in agents when saving other changes", async () => {
    mockPersistSettings.mockResolvedValue({
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: ["codex"],
          enabled: true,
        },
        {
          key: "claude",
          label: "Claude",
          command: ["claude", "--permission-mode", "acceptEdits"],
          enabled: true,
        },
      ],
    });
    const onUpdate = vi.fn();

    renderAgentSettings({
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: ["codex"],
          enabled: true,
        },
      ],
      onUpdate,
    });

    await expandAgent("Claude");
    await fireEvent.input(screen.getByLabelText("Claude arguments"), {
      target: { value: "--permission-mode acceptEdits" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          {
            key: "claude",
            label: "Claude",
            command: ["claude", "--permission-mode", "acceptEdits"],
            enabled: true,
          },
          {
            key: "codex",
            label: "Codex",
            command: ["codex"],
            enabled: true,
          },
        ],
      });
    });
  });

  it("preserves disabled built-in agents with empty commands when saving other changes", async () => {
    mockPersistSettings.mockResolvedValue({
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: [],
          enabled: false,
        },
        {
          key: "claude",
          label: "Claude",
          command: ["claude", "--permission-mode", "acceptEdits"],
          enabled: true,
        },
      ],
    });
    const onUpdate = vi.fn();

    renderAgentSettings({
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: [],
          enabled: false,
        },
      ],
      onUpdate,
    });

    await expandAgent("Claude");
    await fireEvent.input(screen.getByLabelText("Claude arguments"), {
      target: { value: "--permission-mode acceptEdits" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          {
            key: "claude",
            label: "Claude",
            command: ["claude", "--permission-mode", "acceptEdits"],
            enabled: true,
          },
          {
            key: "codex",
            label: "Codex",
            command: [],
            enabled: false,
          },
        ],
      });
    });
  });

  it("adds custom agents to the saved settings", async () => {
    mockPersistSettings.mockResolvedValue({
      agents: [
        {
          key: "review",
          label: "Review Agent",
          command: ["review-agent", "--strict"],
          enabled: true,
        },
      ],
    });
    const onUpdate = vi.fn();

    renderAgentSettings({
      agents: [],
      onUpdate,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add custom agent" }));
    await fireEvent.input(screen.getByLabelText("Custom agent key"), {
      target: { value: "review" },
    });
    await fireEvent.input(screen.getByLabelText("Custom agent label"), {
      target: { value: "Review Agent" },
    });
    await fireEvent.input(screen.getByLabelText("Review Agent binary"), {
      target: { value: "review-agent" },
    });
    await fireEvent.input(screen.getByLabelText("Review Agent arguments"), {
      target: { value: "--strict" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          {
            key: "review",
            label: "Review Agent",
            command: ["review-agent", "--strict"],
            enabled: true,
          },
        ],
      });
    });
    expect(onUpdate).toHaveBeenCalledWith(
      [
        {
          key: "review",
          label: "Review Agent",
          command: ["review-agent", "--strict"],
          enabled: true,
        },
      ],
      [],
    );
  });

  it("uses launch targets returned by save without requiring a reload", async () => {
    const launchTargets = [
      {
        key: "codex",
        label: "Codex",
        kind: "agent" as const,
        source: "configured" as const,
        available: true,
      },
    ];
    const agents = [
      {
        key: "codex",
        label: "Codex",
        command: ["/opt/codex"],
        enabled: true,
      },
    ];
    mockPersistSettings.mockResolvedValue({
      agents,
      launch_targets: launchTargets,
    });
    const onUpdate = vi.fn();

    renderAgentSettings({
      agents: [],
      launchTargets: [
        {
          key: "codex",
          label: "Codex",
          kind: "agent",
          source: "builtin",
          available: false,
          disabled_reason: "codex not found on PATH",
        },
      ],
      onUpdate,
    });

    await expandAgent("Codex");
    await fireEvent.input(screen.getByLabelText("Codex binary"), {
      target: { value: "/opt/codex" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(onUpdate).toHaveBeenCalledWith(agents, launchTargets);
    });
  });
});
