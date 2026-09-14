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
    expect(screen.getByRole("combobox", { name: "Quick action agent: Codex" })).toBeTruthy();

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

  it("removes an action and keeps a saved but unconfigured agent selectable", async () => {
    mockPersistSettings.mockResolvedValue({ quick_actions: [] });
    renderQuickActionSettings({
      quickActions: [{ label: "Ghost", agent: "missing", prompt: "haunt" }],
      launchTargets,
      onUpdate: vi.fn(),
    });

    expect(screen.getByRole("combobox", { name: "Quick action agent: missing (Not a configured agent)" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Remove Ghost" }));
    await fireEvent.click(saveButton());

    await waitFor(() => {
      expect(mockPersistSettings).toHaveBeenCalledWith({ quick_actions: [] });
    });
  });
});
