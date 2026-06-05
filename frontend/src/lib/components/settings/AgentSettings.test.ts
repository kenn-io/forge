import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const { mockUpdateSettings } = vi.hoisted(() => ({
  mockUpdateSettings: vi.fn(),
}));

vi.mock("../../api/settings.js", () => ({
  updateSettings: mockUpdateSettings,
}));

vi.mock("../../stores/embed-config.svelte.js", () => ({
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

async function expandAgent(name: string): Promise<void> {
  await fireEvent.click(screen.getByRole("button", { name: `Edit ${name}` }));
}

describe("AgentSettings", () => {
  afterEach(() => {
    cleanup();
    mockUpdateSettings.mockReset();
  });

  it("persists built-in agent binary and argument overrides", async () => {
    mockUpdateSettings.mockResolvedValue({
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

    render(AgentSettings, {
      props: {
        agents: [],
        onUpdate,
      },
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
      expect(mockUpdateSettings).toHaveBeenCalledWith({
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
    expect(onUpdate).toHaveBeenCalledWith([
      {
        key: "codex",
        label: "Codex",
        command: ["/opt/codex", "--full-auto"],
        enabled: true,
      },
    ]);
  });

  it("preserves quoted empty arguments when saving", async () => {
    mockUpdateSettings.mockResolvedValue({
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

    render(AgentSettings, {
      props: {
        agents: [],
        onUpdate,
      },
    });

    await expandAgent("Codex");
    await fireEvent.input(screen.getByLabelText("Codex arguments"), {
      target: { value: '""' },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
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

    render(AgentSettings, {
      props: {
        agents: [
          {
            key: "codex",
            label: "Codex",
            command: ["codex"],
            enabled: true,
          },
        ],
        onUpdate,
      },
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
    mockUpdateSettings.mockResolvedValue({
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

    render(AgentSettings, {
      props: {
        agents: [
          {
            key: "codex",
            label: "Codex",
            command: ["codex"],
            enabled: true,
          },
        ],
        onUpdate,
      },
    });

    await expandAgent("Claude");
    await fireEvent.input(screen.getByLabelText("Claude arguments"), {
      target: { value: "--permission-mode acceptEdits" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
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
    mockUpdateSettings.mockResolvedValue({
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

    render(AgentSettings, {
      props: {
        agents: [
          {
            key: "codex",
            label: "Codex",
            command: [],
            enabled: false,
          },
        ],
        onUpdate,
      },
    });

    await expandAgent("Claude");
    await fireEvent.input(screen.getByLabelText("Claude arguments"), {
      target: { value: "--permission-mode acceptEdits" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
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
    mockUpdateSettings.mockResolvedValue({
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

    render(AgentSettings, {
      props: {
        agents: [],
        onUpdate,
      },
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
      expect(mockUpdateSettings).toHaveBeenCalledWith({
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
    expect(onUpdate).toHaveBeenCalledWith([
      {
        key: "review",
        label: "Review Agent",
        command: ["review-agent", "--strict"],
        enabled: true,
      },
    ]);
  });
});
