import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { MCPSettings as MCPSettingsType } from "../../api/types.js";

const mockPersistSettings = vi.hoisted(() => vi.fn());

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      persist: (request) => mockPersistSettings(request),
    }),
  };
});

vi.mock("../../stores/embed-config.svelte.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/embed-config.svelte.js")>()),
  isEmbedded: () => false,
}));

import MCPSettings from "./MCPSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

function mcpSettings(overrides: Partial<MCPSettingsType> = {}): MCPSettingsType {
  return {
    enabled: false,
    restart_required: false,
    active_requires_auth: false,
    ...overrides,
  };
}

function renderMCPSettings(mcp: MCPSettingsType, onUpdate = vi.fn()): void {
  render(SettingsRuntimeHarness, {
    props: { component: MCPSettings, componentProps: { mcp, onUpdate } },
  });
}

describe("MCPSettings", () => {
  afterEach(() => {
    cleanup();
    mockPersistSettings.mockReset();
  });

  it("saves blank overrides as backend defaults and shows the restart state", async () => {
    const onUpdate = vi.fn();
    const saved = mcpSettings({ enabled: true, restart_required: true });
    mockPersistSettings.mockReturnValue(Effect.succeed({ mcp: saved }));
    renderMCPSettings(mcpSettings(), onUpdate);

    await fireEvent.click(screen.getByRole("checkbox", { name: "Enable MCP companion" }));
    await fireEvent.click(screen.getByRole("button", { name: "Save MCP companion" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({
      mcp: { enabled: true, port: 0, diff_cache_mb: 0 },
    });
    expect(onUpdate).toHaveBeenCalledWith(saved);
    expect(screen.getByText("The MCP companion will start after the Forge daemon restarts.")).toBeTruthy();
  });

  it("shows the active endpoint and a token-safe client configuration", () => {
    renderMCPSettings(
      mcpSettings({
        enabled: true,
        active_url: "http://127.0.0.1:8092/mcp",
        active_requires_auth: true,
      }),
    );

    expect(screen.getByText("http://127.0.0.1:8092/mcp")).toBeTruthy();
    const copyButton = screen.getByRole("button", {
      name: "Copy MCP client configuration",
    });
    const configuration = copyButton.closest(".kit-code-block")?.textContent ?? "";
    expect(configuration).toContain("Bearer ${KENN_FORGE_API_TOKEN}");
    expect(configuration).not.toContain("secret-token");
    const authNote = document.querySelector(".auth-note")?.textContent ?? "";
    expect(authNote).toContain("token_path");
    expect(authNote).toContain("kenn-forge daemon status --json");
  });

  it("distinguishes a companion that remains active until restart", () => {
    renderMCPSettings(
      mcpSettings({
        enabled: false,
        restart_required: true,
        active_url: "http://127.0.0.1:8092/mcp",
      }),
    );

    expect(screen.getByText("The active companion will stop after the Forge daemon restarts.")).toBeTruthy();
    expect(screen.getByText(/It remains active until the daemon restarts/)).toBeTruthy();
  });
});
