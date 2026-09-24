import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import type { ComponentProps } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { LaunchTarget } from "../../api/types.js";

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
    await fireEvent.keyDown(screen.getByRole("combobox"), { key: "Enter" });
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
    expect(saveButton().disabled).toBe(true);
    expect(mockPersistSettings).not.toHaveBeenCalled();
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
});
