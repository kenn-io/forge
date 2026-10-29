import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import type { ComponentProps } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const { mockPersistSettings, mockTestACP } = vi.hoisted(() => ({
  mockPersistSettings: vi.fn(),
  mockTestACP: vi.fn(),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      persist: (request) => Effect.promise(() => mockPersistSettings(request())),
      testACP: (command, disabled, env) => Effect.promise(() => mockTestACP(command, disabled, env)),
    }),
  };
});

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
    mockTestACP.mockReset();
  });

  it("preserves ACP restrictions when probing and saving a changed executable", async () => {
    const agents = [
      {
        key: "chat",
        label: "Chat",
        protocol: "acp" as const,
        command: ["chat-agent"],
        enabled: true,
        disable_forge_tools: true,
      },
    ];
    mockPersistSettings.mockResolvedValue({ agents });
    mockTestACP.mockResolvedValue({ valid: true, message: "ACP connection verified on this host." });
    renderAgentSettings({ agents, onUpdate: vi.fn() });
    await expandAgent("Chat");
    await fireEvent.input(screen.getByLabelText("Chat binary"), { target: { value: "/opt/chat-agent" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));
    await waitFor(() =>
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          {
            key: "chat",
            label: "Chat",
            protocol: "acp",
            command: ["/opt/chat-agent"],
            enabled: true,
            disable_forge_tools: true,
          },
        ],
      }),
    );
    expect(mockTestACP).toHaveBeenCalledWith(["/opt/chat-agent"], true, {});
  });

  it("retains Forge tool opt-outs when editing another agent", async () => {
    const agents = [
      {
        key: "chat",
        label: "Chat",
        protocol: "acp" as const,
        command: ["chat-agent"],
        enabled: true,
        disable_forge_tools: true,
      },
      { key: "codex", label: "Codex", command: ["codex"], enabled: true, disable_forge_tools: true },
    ];
    mockPersistSettings.mockResolvedValue({ agents });
    renderAgentSettings({ agents, onUpdate: vi.fn() });
    await expandAgent("Claude");
    await fireEvent.input(screen.getByLabelText("Claude binary"), { target: { value: "/opt/claude" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));
    await waitFor(() =>
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          {
            key: "chat",
            label: "Chat",
            protocol: "acp",
            command: ["chat-agent"],
            enabled: true,
            disable_forge_tools: true,
          },
          { key: "claude", label: "Claude", command: ["/opt/claude"], enabled: true },
          { key: "codex", label: "Codex", command: ["codex"], enabled: true, disable_forge_tools: true },
        ],
      }),
    );
  });

  it("retains environment variables when editing another agent", async () => {
    const agents = [
      { key: "chat", label: "Chat", protocol: "acp" as const, command: ["chat-agent"], enabled: true, env: { MODE: "full" } },
    ];
    mockPersistSettings.mockResolvedValue({ agents });
    renderAgentSettings({ agents, onUpdate: vi.fn() });
    await expandAgent("Claude");
    await fireEvent.input(screen.getByLabelText("Claude binary"), { target: { value: "/opt/claude" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));
    await waitFor(() =>
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          { key: "chat", label: "Chat", protocol: "acp", command: ["chat-agent"], enabled: true, env: { MODE: "full" } },
          { key: "claude", label: "Claude", command: ["/opt/claude"], enabled: true },
        ],
      }),
    );
    expect(mockTestACP).not.toHaveBeenCalled();
  });

  it("tests and saves an ACP agent with its edited environment", async () => {
    const agents = [{ key: "chat", label: "Chat", protocol: "acp" as const, command: ["chat-agent"], enabled: true }];
    mockPersistSettings.mockResolvedValue({ agents });
    mockTestACP.mockResolvedValue({ valid: true, message: "ACP connection verified on this host." });
    renderAgentSettings({ agents, onUpdate: vi.fn() });
    await expandAgent("Chat");
    await fireEvent.input(screen.getByLabelText("Chat environment"), {
      target: { value: "INITIAL_AGENT_MODE=agent-full-access\n\nQUERY=a=b\n" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));
    const env = { INITIAL_AGENT_MODE: "agent-full-access", QUERY: "a=b" };
    await waitFor(() =>
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [{ key: "chat", label: "Chat", protocol: "acp", command: ["chat-agent"], enabled: true, env }],
      }),
    );
    expect(mockTestACP).toHaveBeenCalledWith(["chat-agent"], false, env);
  });

  it("blocks saving an environment line that is not NAME=value", async () => {
    const agents = [{ key: "chat", label: "Chat", command: ["chat-agent"], enabled: true }];
    renderAgentSettings({ agents, onUpdate: vi.fn() });
    await expandAgent("Chat");
    await fireEvent.input(screen.getByLabelText("Chat environment"), { target: { value: "1BAD=x" } });
    expect((screen.getByRole("button", { name: "Save workspace agents" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('Chat environment line "1BAD=x" needs to be NAME=value.')).toBeTruthy();
  });

  it("blocks saving a changed ACP command when its automatic test fails", async () => {
    const agents = [{ key: "chat", label: "Chat", protocol: "acp" as const, command: ["chat-agent"], enabled: true }];
    mockTestACP.mockResolvedValue({ valid: false, message: "Not an ACP agent" });
    renderAgentSettings({ agents, onUpdate: vi.fn() });
    await expandAgent("Chat");
    await fireEvent.input(screen.getByLabelText("Chat binary"), { target: { value: "wrong-agent" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Not an ACP agent"));
    expect(mockTestACP).toHaveBeenCalledWith(["wrong-agent"], false, {});
    expect(mockPersistSettings).not.toHaveBeenCalled();
    mockTestACP.mockResolvedValue({ valid: true, message: "ACP connection verified on this host." });
    await fireEvent.input(screen.getByLabelText("Chat binary"), { target: { value: "valid-agent" } });
    expect(screen.queryByText("Not an ACP agent")).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Test ACP connection" }));
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("ACP connection verified"));
    expect(mockTestACP).toHaveBeenLastCalledWith(["valid-agent"], false, {});
    expect(mockPersistSettings).not.toHaveBeenCalled();
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

  it("lets the Pi built-in be disabled like other launchable agents", async () => {
    mockPersistSettings.mockResolvedValue({
      agents: [{ key: "pi", label: "Pi", command: ["pi"], enabled: false }],
    });

    renderAgentSettings({ agents: [], onUpdate: vi.fn() });

    await fireEvent.click(screen.getByRole("checkbox", { name: "Pi" }));
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [{ key: "pi", label: "Pi", command: ["pi"], enabled: false }],
      });
    });
  });

  it("lists agents by name with the harness icon the launch menu uses", () => {
    renderAgentSettings({
      agents: [
        { key: "codex-review", label: "Review Agent", command: ["codex"], enabled: true },
        { key: "mystery", label: "mystery", command: ["mystery"], enabled: true },
      ],
      onUpdate: vi.fn(),
    });

    const names = screen.getAllByRole("checkbox").map((box) => box.closest("label")?.textContent?.trim());
    expect(names).toEqual(["aider", "Claude", "Codex", "Gemini", "mystery", "opencode", "Pi", "Review Agent"]);

    const review = screen.getByRole("checkbox", { name: "Review Agent" }).closest("label")!;
    expect(review.querySelector(".kit-harness-icon--openai")).not.toBeNull();
    const mystery = screen.getByRole("checkbox", { name: "mystery" }).closest("label")!;
    expect(mystery.querySelector(".kit-harness-icon")).toBeNull();
    expect(mystery.querySelector(".launch-target-icon svg")).not.toBeNull();
  });

  it("filters agents by name, key, or binary without dropping hidden ones from the save", async () => {
    const agents = [{ key: "review", label: "Review Agent", command: ["/opt/reviewer"], enabled: true }];
    mockPersistSettings.mockResolvedValue({ agents });
    renderAgentSettings({ agents, onUpdate: vi.fn() });
    const filter = screen.getByRole("searchbox", { name: "Filter workspace agents" });

    await fireEvent.input(filter, { target: { value: "REVIEWER" } });
    expect(screen.getAllByRole("checkbox").map((box) => box.closest("label")?.textContent?.trim())).toEqual([
      "Review Agent",
    ]);

    await fireEvent.input(filter, { target: { value: "nothing-matches" } });
    expect(screen.queryAllByRole("checkbox")).toEqual([]);
    expect(screen.getByText('No agents match "nothing-matches"')).toBeTruthy();

    await fireEvent.input(filter, { target: { value: "claude" } });
    await fireEvent.click(screen.getByRole("checkbox", { name: "Claude" }));
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));
    await waitFor(() =>
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [
          { key: "claude", label: "Claude", command: ["claude"], enabled: false },
          { key: "review", label: "Review Agent", command: ["/opt/reviewer"], enabled: true },
        ],
      }),
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

  it("restores a built-in agent's default binary when re-enabling one saved without a command", async () => {
    const agents = [{ key: "codex", label: "codex", command: [], enabled: false }];
    mockPersistSettings.mockResolvedValue({
      agents: [{ key: "codex", label: "codex", command: ["codex"], enabled: true }],
    });
    renderAgentSettings({ agents, onUpdate: vi.fn() });

    await fireEvent.click(screen.getByRole("checkbox", { name: "codex" }));
    await fireEvent.click(screen.getByRole("button", { name: "Save workspace agents" }));

    await waitFor(() =>
      expect(mockPersistSettings).toHaveBeenCalledWith({
        agents: [{ key: "codex", label: "codex", command: ["codex"], enabled: true }],
      }),
    );
  });

  it("names the agent and missing field when an invalid draft blocks saving, even if filtered out", async () => {
    renderAgentSettings({ agents: [], onUpdate: vi.fn() });
    const save = screen.getByRole("button", { name: "Save workspace agents" }) as HTMLButtonElement;

    await fireEvent.click(screen.getByRole("button", { name: "Add custom agent" }));
    await fireEvent.click(screen.getByRole("button", { name: "Add custom agent" }));
    expect(save.disabled).toBe(true);
    expect(save.getAttribute("aria-describedby")).toBeTruthy();
    expect(document.getElementById(save.getAttribute("aria-describedby")!)?.textContent).toContain(
      "Custom agent needs a key and a binary.",
    );

    await fireEvent.input(screen.getAllByLabelText("Custom agent key")[0]!, { target: { value: "review" } });
    await fireEvent.input(screen.getByRole("searchbox", { name: "Filter workspace agents" }), {
      target: { value: "claude" },
    });
    expect(screen.getByText("review needs a binary.")).toBeTruthy();
    expect(save.disabled).toBe(true);
  });
});
