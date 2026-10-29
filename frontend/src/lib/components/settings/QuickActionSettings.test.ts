import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import type { ComponentProps } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { ApiProblemError } from "../../api/effect-errors.js";
import type { LaunchTarget } from "../../api/types.js";

const { mockPersistSettings, mockProbeACPOptions } = vi.hoisted(() => ({
  mockPersistSettings: vi.fn(),
  mockProbeACPOptions: vi.fn(),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      persist: (request) => Effect.promise(() => mockPersistSettings(request())),
      probeACPOptions: (targetKey, acpOptions) => mockProbeACPOptions(targetKey, acpOptions),
    }),
  };
});

import QuickActionSettings from "./QuickActionSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

const launchTargets: LaunchTarget[] = [
  {
    key: "codex",
    label: "Codex",
    kind: "agent",
    source: "builtin",
    command: ["codex"],
    available: true,
    disabled_reason: "",
  },
  {
    key: "opencode",
    label: "opencode",
    kind: "agent",
    source: "builtin",
    command: ["opencode"],
    available: false,
    disabled_reason: "opencode not found on PATH",
  },
  {
    key: "shell",
    label: "Shell",
    kind: "plain_shell",
    source: "system",
    command: [],
    available: true,
    disabled_reason: "",
  },
];

function renderQuickActionSettings(componentProps: ComponentProps<typeof QuickActionSettings>) {
  return render(SettingsRuntimeHarness, {
    props: { component: QuickActionSettings, componentProps },
  });
}

function saveButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: "Save quick actions" }) as HTMLButtonElement;
}

describe("QuickActionSettings", () => {
  afterEach(() => {
    cleanup();
    mockPersistSettings.mockReset();
    mockProbeACPOptions.mockReset();
  });

  it("treats missing quick actions as an empty list", () => {
    renderQuickActionSettings({ quickActions: undefined, launchTargets, onUpdate: vi.fn() });

    expect(screen.getByText("No quick actions configured.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  it("adds an action with the first available agent preselected and persists the whole list", async () => {
    const onUpdate = vi.fn();
    const saved = [{ label: "Rebase", agent: "codex", prompt: "rebase this pull request onto main" }];
    mockPersistSettings.mockResolvedValue({ quick_actions: saved });
    renderQuickActionSettings({ quickActions: [], launchTargets, onUpdate });

    await fireEvent.click(screen.getByRole("button", { name: "Add quick action" }));

    expect(saveButton().disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Search agents...: Codex" })).toBeTruthy();

    await fireEvent.input(screen.getByLabelText("Quick action label"), { target: { value: " Rebase " } });
    await fireEvent.input(screen.getByLabelText("Quick action prompt"), {
      target: { value: "rebase this pull request onto main" },
    });

    expect(saveButton().disabled).toBe(false);
    await fireEvent.click(saveButton());

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({ quick_actions: saved });
    });
    await waitFor(() => {
      expect(onUpdate).toHaveBeenCalledWith(saved);
    });
    expect(saveButton().disabled).toBe(true);
  });

  it("sorts agent names and saves the agent selected through search", async () => {
    const saved = [{ label: "Review", agent: "codex", prompt: "review" }];
    mockPersistSettings.mockResolvedValue({ quick_actions: saved });
    renderQuickActionSettings({
      quickActions: [{ label: "Review", agent: "missing", prompt: "review" }],
      launchTargets: [...launchTargets.toReversed(), { ...launchTargets[0]!, key: "aider", label: "aider" }],
      onUpdate: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Search agents...: missing" }));
    expect(screen.getAllByRole("option").map((option) => option.textContent?.trim())).toEqual([
      "aider",
      "Codex",
      "missing Not a configured agent",
      "opencode opencode not found on PATH",
    ]);
    await fireEvent.input(screen.getByRole("combobox"), { target: { value: "CODEX" } });
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option", { name: "Codex" }).querySelector(".kit-harness-icon--openai")).not.toBeNull();
    await fireEvent.keyDown(screen.getByRole("combobox"), { key: "Enter" });
    expect(
      screen.getByRole("button", { name: "Search agents...: Codex" }).querySelector(".kit-harness-icon--openai"),
    ).not.toBeNull();
    await fireEvent.click(saveButton());
    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({ quick_actions: saved });
    });
  });

  it("refuses to save duplicate labels or blank prompts", async () => {
    renderQuickActionSettings({
      quickActions: [{ label: "Rebase", agent: "codex", prompt: "rebase" }],
      launchTargets,
      onUpdate: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add quick action" }));
    const labels = screen.getAllByLabelText("Quick action label");
    const prompts = screen.getAllByLabelText("Quick action prompt");
    await fireEvent.input(labels[1]!, { target: { value: "rebase" } });
    await fireEvent.input(prompts[1]!, { target: { value: "again" } });

    expect(screen.getAllByText("Labels must be unique.")).toHaveLength(2);
    expect(saveButton().disabled).toBe(true);

    await fireEvent.input(labels[1]!, { target: { value: "Triage" } });
    await fireEvent.input(prompts[1]!, { target: { value: "   " } });

    expect(screen.queryByText("Labels must be unique.")).toBeNull();
    expect(screen.getByText("A prompt is required.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
    expect(mockPersistSettings).not.toHaveBeenCalled();
  });

  it("says which required field keeps a new action from saving", async () => {
    renderQuickActionSettings({ quickActions: [], launchTargets, onUpdate: vi.fn() });

    await fireEvent.click(screen.getByRole("button", { name: "Add quick action" }));
    expect(screen.getByRole("alert").textContent).toContain("A label is required.");
    expect(screen.getByRole("alert").textContent).toContain("A prompt is required.");

    await fireEvent.input(screen.getByLabelText("Quick action label"), { target: { value: "Rebase" } });
    expect(screen.queryByText("A label is required.")).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("A prompt is required.");
    expect(screen.getByLabelText("Quick action prompt").getAttribute("aria-invalid")).toBe("true");
    expect(saveButton().disabled).toBe(true);

    await fireEvent.input(screen.getByLabelText("Quick action label"), { target: { value: "" } });
    await fireEvent.input(screen.getByLabelText("Quick action prompt"), { target: { value: "rebase" } });
    expect(screen.getByRole("alert").textContent).toContain("A label is required.");
    expect(screen.queryByText("A prompt is required.")).toBeNull();

    await fireEvent.input(screen.getByLabelText("Quick action label"), { target: { value: "Rebase" } });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(saveButton().disabled).toBe(false);
  });

  it("refuses to save a prompt over the 64 KiB initial-message limit", async () => {
    renderQuickActionSettings({
      quickActions: [{ label: "Big", agent: "codex", prompt: "small" }],
      launchTargets,
      onUpdate: vi.fn(),
    });

    // Multi-byte characters: 40000 two-byte characters is 80000 bytes, over
    // the limit, while 40000 characters alone would pass a length check.
    await fireEvent.input(screen.getByLabelText("Quick action prompt"), { target: { value: "\u00e9".repeat(40000) } });

    expect(screen.getByText("Prompts must not exceed 64 KiB.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);

    await fireEvent.input(screen.getByLabelText("Quick action prompt"), { target: { value: "\u00e9".repeat(1000) } });

    expect(screen.queryByText("Prompts must not exceed 64 KiB.")).toBeNull();
    expect(saveButton().disabled).toBe(false);
  });

  it("refuses to save a prompt containing control characters such as tabs", async () => {
    renderQuickActionSettings({
      quickActions: [{ label: "Tabby", agent: "codex", prompt: "plain" }],
      launchTargets,
      onUpdate: vi.fn(),
    });

    await fireEvent.input(screen.getByLabelText("Quick action prompt"), { target: { value: "step one\tstep two" } });

    expect(screen.getByText("Prompts may only contain printable text and line breaks.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);

    // Line breaks in either convention stay allowed.
    await fireEvent.input(screen.getByLabelText("Quick action prompt"), { target: { value: "step one\r\nstep two" } });

    expect(screen.queryByText("Prompts may only contain printable text and line breaks.")).toBeNull();
    expect(saveButton().disabled).toBe(false);
  });

  it("removes an action and keeps a saved but unconfigured agent selectable", async () => {
    mockPersistSettings.mockResolvedValue({ quick_actions: [] });
    renderQuickActionSettings({
      quickActions: [{ label: "Ghost", agent: "missing", prompt: "haunt" }],
      launchTargets,
      onUpdate: vi.fn(),
    });

    expect(screen.getByRole("button", { name: "Search agents...: missing" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Remove Ghost" }));
    await fireEvent.click(saveButton());

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({ quick_actions: [] });
    });
  });
  describe("ACP agent options", () => {
    const chatTarget: LaunchTarget = {
      key: "chat",
      label: "Chat",
      kind: "acp",
      source: "config",
      command: ["chat-agent"],
      available: true,
      disabled_reason: "",
    };
    const model = {
      id: "model",
      name: "Model",
      category: "model",
      type: "select",
      currentValue: "fast",
      options: [
        {
          group: "models",
          name: "Models",
          options: [
            { value: "fast", name: "Fast" },
            { value: "deep", name: "Deep" },
          ],
        },
      ],
    };
    const effort = {
      id: "effort",
      name: "Effort",
      category: "thought_level",
      type: "select",
      currentValue: "low",
      options: [
        { value: "low", name: "Low" },
        { value: "high", name: "High" },
      ],
    };
    // Like real agents, effort is offered only once the deep model is chosen.
    function offeredFor(_targetKey: string, options: Record<string, string>) {
      return Effect.succeed({ options: options["model"] === "deep" ? [model, effort] : [model] });
    }

    async function choose(trigger: RegExp | string, query: string): Promise<void> {
      await fireEvent.click(await screen.findByRole("button", { name: trigger }));
      await fireEvent.input(screen.getByRole("combobox"), { target: { value: query } });
      await fireEvent.keyDown(screen.getByRole("combobox"), { key: "Enter" });
    }

    it("reads the agent's options on demand and saves the chosen ones for that action", async () => {
      mockProbeACPOptions.mockImplementation(offeredFor);
      const saved = [
        { label: "Review", agent: "chat", prompt: "review", acp_options: { effort: "high", model: "deep" } },
      ];
      mockPersistSettings.mockResolvedValue({ quick_actions: saved });
      renderQuickActionSettings({
        quickActions: [{ label: "Review", agent: "chat", prompt: "review" }],
        launchTargets: [...launchTargets, chatTarget],
        onUpdate: vi.fn(),
      });

      expect(mockProbeACPOptions).not.toHaveBeenCalled();
      const toggle = screen.getByRole("button", { name: /Agent options/ });
      expect(toggle.textContent).toContain("Remembered choices");
      await fireEvent.click(toggle);
      await waitFor(() => expect(mockProbeACPOptions).toHaveBeenCalledWith("chat", {}));

      await choose("Search model...: Remembered choice", "deep");
      await waitFor(() => expect(mockProbeACPOptions).toHaveBeenLastCalledWith("chat", { model: "deep" }));

      await choose("Search effort...: Remembered choice", "high");
      expect(toggle.textContent).toContain("effort: high, model: deep");

      await fireEvent.click(saveButton());
      await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledWith({ quick_actions: saved }));
    });

    it("lets a choice the agent no longer reports be cleared", async () => {
      mockProbeACPOptions.mockImplementation(offeredFor);
      mockPersistSettings.mockResolvedValue({ quick_actions: [] });
      renderQuickActionSettings({
        quickActions: [{ label: "Review", agent: "chat", prompt: "review", acp_options: { effort: "high" } }],
        launchTargets: [...launchTargets, chatTarget],
        onUpdate: vi.fn(),
      });

      expect(saveButton().disabled).toBe(true);
      await fireEvent.click(screen.getByRole("button", { name: /Agent options/ }));
      expect(await screen.findByText("effort: high is not offered by this agent.")).toBeTruthy();
      await fireEvent.click(screen.getByRole("button", { name: "Clear" }));

      await fireEvent.click(saveButton());
      await waitFor(() =>
        expect(mockPersistSettings).toHaveBeenCalledWith({
          quick_actions: [{ label: "Review", agent: "chat", prompt: "review" }],
        }),
      );
    });

    it("drops the options when the action switches to another agent", async () => {
      mockPersistSettings.mockResolvedValue({ quick_actions: [] });
      renderQuickActionSettings({
        quickActions: [{ label: "Review", agent: "chat", prompt: "review", acp_options: { model: "deep" } }],
        launchTargets: [...launchTargets, chatTarget],
        onUpdate: vi.fn(),
      });

      await choose("Search agents...: Chat", "codex");

      expect(screen.queryByRole("button", { name: /Agent options/ })).toBeNull();
      await fireEvent.click(saveButton());
      await waitFor(() =>
        expect(mockPersistSettings).toHaveBeenCalledWith({
          quick_actions: [{ label: "Review", agent: "codex", prompt: "review" }],
        }),
      );
    });

    it("reports an agent that could not be read and retries", async () => {
      mockProbeACPOptions
        .mockReturnValueOnce(
          Effect.fail(
            new ApiProblemError({
              operation: "probe ACP options",
              problem: { status: 502, title: "Bad Gateway", detail: "read ACP options: agent exited" },
            }),
          ),
        )
        .mockImplementation(offeredFor);
      renderQuickActionSettings({
        quickActions: [{ label: "Review", agent: "chat", prompt: "review" }],
        launchTargets: [...launchTargets, chatTarget],
        onUpdate: vi.fn(),
      });

      await fireEvent.click(screen.getByRole("button", { name: /Agent options/ }));
      expect(await screen.findByText("read ACP options: agent exited")).toBeTruthy();
      await fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(await screen.findByRole("button", { name: "Search model...: Remembered choice" })).toBeTruthy();
      expect(mockProbeACPOptions).toHaveBeenCalledTimes(2);
    });
  });
});
