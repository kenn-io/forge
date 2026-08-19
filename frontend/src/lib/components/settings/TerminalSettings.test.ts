import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { StartupSnapshot } from "../../app/startup-workflow.js";

const {
  mockEmbedded,
  mockGetTerminalSettings,
  mockSetTerminalSettings,
  mockTerminalStore,
  mockUpdateSettings,
  runtime,
} = vi.hoisted(() => {
  const defaultTerminal = {
    font_family: "",
    font_size: 14,
    scrollback: 1000,
    line_height: 1,
    letter_spacing: 0,
    cursor_blink: true,
    font_ligatures: false,
    hide_tmux_status: false,
    retained_sessions: 10,
  };
  const store = { terminal: { ...defaultTerminal } };
  return {
    mockEmbedded: { value: false },
    mockGetTerminalSettings: vi.fn(() => store.terminal),
    mockSetTerminalSettings: vi.fn((terminal: typeof defaultTerminal) => {
      store.terminal = terminal;
    }),
    mockTerminalStore: { defaultTerminal, store },
    mockUpdateSettings: vi.fn(),
    runtime: { current: undefined as unknown as OwnedAppRuntime },
  };
});

vi.mock("../../context.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../context.js")>();
  return {
    ...actual,
    getStores: () => ({
      settings: {
        getTerminalSettings: mockGetTerminalSettings,
        setTerminalSettings: mockSetTerminalSettings,
      },
    }),
  };
});

vi.mock("../../api/types.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/types.js")>();
  return {
    ...actual,
    DEFAULT_TERMINAL_SETTINGS: mockTerminalStore.defaultTerminal,
  };
});

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => runtime.current,
}));

vi.mock("../../stores/embed-config.svelte.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/embed-config.svelte.js")>();
  return {
    ...actual,
    isEmbedded: () => mockEmbedded.value,
  };
});

import TerminalSettings from "./TerminalSettings.svelte";

function settingsResponse(terminal: StartupSnapshot["terminal"]): StartupSnapshot {
  return {
    activity: {
      view_mode: "threaded",
      time_range: "7d",
      hide_closed: false,
      hide_bots: false,
      collapse_threads: false,
      default_branch_retention_days: 90,
      default_branch_max_commits: 5000,
      use_workspace_activity_for_recency: false,
    },
    agents: [],
    fleet: {
      enabled: false,
      sessions: {},
      peers: [],
      ssh_peers: [],
      restart_required: false,
    },
    issues: { hide_bots: true },
    kata_projects: [],
    launch_targets: [],
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
    pull_requests: {
      allow_mid_stack_merges: false,
      prefer_github_native_stacks: false,
    },
    repos: [],
    terminal,
    workspaces: { auto_assign_on_create: false, default_sidebar_view: "diff" },
  };
}

describe("TerminalSettings", () => {
  afterEach(async () => {
    cleanup();
    await Effect.runPromise(runtime.current.disposeEffect);
    vi.unstubAllGlobals();
    mockSetTerminalSettings.mockReset();
    mockSetTerminalSettings.mockImplementation((terminal) => {
      mockTerminalStore.store.terminal = terminal;
    });
    mockGetTerminalSettings.mockClear();
    mockTerminalStore.store.terminal = {
      ...mockTerminalStore.defaultTerminal,
    };
    mockUpdateSettings.mockReset();
    mockEmbedded.value = false;
  });

  beforeEach(() => {
    runtime.current = makeAppRuntime();
    const fetch: typeof globalThis.fetch = async (input, init) => {
      const request = input instanceof Request ? input : new Request(input, init);
      const body = await request.clone().json();
      if (
        typeof body !== "object" ||
        body === null ||
        !("terminal" in body) ||
        typeof body.terminal !== "object" ||
        body.terminal === null
      ) {
        return Response.json({ detail: "invalid terminal settings" }, { status: 400 });
      }
      const updated = await mockUpdateSettings({ terminal: body.terminal });
      return Response.json(settingsResponse(updated.terminal));
    };
    vi.stubGlobal("fetch", fetch);
  });

  it("persists zero retained sessions without falling back to the default", async () => {
    const updated = {
      ...mockTerminalStore.defaultTerminal,
      retained_sessions: 0,
    };
    mockUpdateSettings.mockResolvedValue({ terminal: updated });
    const onUpdate = vi.fn();

    render(TerminalSettings, {
      props: {
        terminal: { ...mockTerminalStore.defaultTerminal },
        onUpdate,
      },
    });

    const retentionInput = screen.getByLabelText(/^Retained terminal sessions/) as HTMLInputElement;
    expect(retentionInput.min).toBe("0");
    expect(retentionInput.max).toBe("20");
    await fireEvent.input(retentionInput, {
      target: { value: "0" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({ terminal: updated });
      expect(onUpdate).toHaveBeenCalledWith(updated);
    });

    await fireEvent.click(screen.getByRole("button", { name: "Reset" }));
    expect(retentionInput.value).toBe("10");
  });

  it("enables save after editing and persists the font family", async () => {
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        font_family: '"Iosevka Term", monospace',
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
        retained_sessions: 10,
      },
    });
    const onUpdate = vi.fn();

    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        onUpdate,
      },
    });

    const input = screen.getByLabelText("Monospace font family");
    const saveButton = screen.getByRole("button", { name: "Save" });

    await fireEvent.input(input, {
      target: { value: '"Iosevka Term", monospace' },
    });

    await waitFor(() => {
      expect((saveButton as HTMLButtonElement).disabled).toBe(false);
    });

    await fireEvent.click(saveButton);

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
        terminal: {
          font_family: '"Iosevka Term", monospace',
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
      });
    });
    await waitFor(() => {
      expect(onUpdate).toHaveBeenCalledWith({
        font_family: '"Iosevka Term", monospace',
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
        retained_sessions: 10,
      });
    });
    expect(mockSetTerminalSettings).toHaveBeenCalledWith({
      font_family: '"Iosevka Term", monospace',
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      hide_tmux_status: false,
      retained_sessions: 10,
    });
  });

  it("persists terminal sizing options", async () => {
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        font_family: "",
        font_size: 18,
        scrollback: 5000,
        line_height: 1.15,
        letter_spacing: 1,
        cursor_blink: false,
        font_ligatures: false,
        hide_tmux_status: false,
        retained_sessions: 10,
      },
    });
    const onUpdate = vi.fn();

    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        onUpdate,
      },
    });

    await fireEvent.input(screen.getByLabelText("Font size"), {
      target: { value: "18" },
    });
    await fireEvent.input(screen.getByLabelText("Scrollback"), {
      target: { value: "5000" },
    });
    await fireEvent.input(screen.getByLabelText("Line height"), {
      target: { value: "1.15" },
    });
    await fireEvent.input(screen.getByLabelText("Letter spacing"), {
      target: { value: "1" },
    });
    await fireEvent.click(screen.getByLabelText("Cursor blink"));
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
        terminal: {
          font_family: "",
          font_size: 18,
          scrollback: 5000,
          line_height: 1.15,
          letter_spacing: 1,
          cursor_blink: false,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
      });
    });
  });

  it("persists font ligatures for xterm.js", async () => {
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: true,
        hide_tmux_status: false,
        retained_sessions: 10,
      },
    });
    const onUpdate = vi.fn();

    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        onUpdate,
      },
    });

    await fireEvent.click(screen.getByLabelText("Font ligatures"));
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: true,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
      });
    });
  });

  it("persists hidden tmux status preference for new sessions", async () => {
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: true,
        retained_sessions: 10,
      },
    });

    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        onUpdate: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByLabelText("Hide tmux status line in new sessions"));
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: true,
          retained_sessions: 10,
        },
      });
    });
  });

  it("does not update when saving terminal settings fails", async () => {
    mockUpdateSettings.mockRejectedValueOnce(new Error("validation failed"));
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const onUpdate = vi.fn();

    try {
      render(TerminalSettings, {
        props: {
          terminal: {
            font_family: "",
            font_size: 14,
            scrollback: 1000,
            line_height: 1,
            letter_spacing: 0,
            cursor_blink: true,
            font_ligatures: false,
            hide_tmux_status: false,
            retained_sessions: 10,
          },
          onUpdate,
        },
      });

      await fireEvent.input(screen.getByLabelText("Font size"), {
        target: { value: "17" },
      });
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() => {
        expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
      });
      expect(onUpdate).not.toHaveBeenCalled();
    } finally {
      warnSpy.mockRestore();
    }
  });

  it("normalizes empty numeric drafts before saving", async () => {
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
        retained_sessions: 10,
      },
    });

    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 18,
          scrollback: 5000,
          line_height: 1.15,
          letter_spacing: 1,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        onUpdate: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Font size"), {
      target: { value: "" },
    });
    await fireEvent.input(screen.getByLabelText("Scrollback"), {
      target: { value: "" },
    });
    await fireEvent.input(screen.getByLabelText("Line height"), {
      target: { value: "" },
    });
    await fireEvent.input(screen.getByLabelText("Letter spacing"), {
      target: { value: "" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
      });
    });
  });

  it("reverts unsaved live preview settings on unmount", async () => {
    const { unmount } = render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        livePreview: true,
        onUpdate: vi.fn(),
      },
    });
    mockSetTerminalSettings.mockClear();

    await fireEvent.input(screen.getByLabelText("Font size"), {
      target: { value: "19" },
    });
    expect(mockSetTerminalSettings).toHaveBeenLastCalledWith({
      font_family: "",
      font_size: 19,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      hide_tmux_status: false,
      retained_sessions: 10,
    });

    unmount();

    expect(mockSetTerminalSettings).toHaveBeenLastCalledWith({
      font_family: "",
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      hide_tmux_status: false,
      retained_sessions: 10,
    });
  });

  it("keeps the saved live preview baseline when unmounted after saving", async () => {
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        font_family: "",
        font_size: 19,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        hide_tmux_status: false,
        retained_sessions: 10,
      },
    });
    const { unmount } = render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        livePreview: true,
        onUpdate: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Font size"), {
      target: { value: "19" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledTimes(1);
    });
    mockSetTerminalSettings.mockClear();

    unmount();

    expect(mockSetTerminalSettings).not.toHaveBeenCalled();
    expect(mockGetTerminalSettings()).toEqual({
      font_family: "",
      font_size: 19,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      hide_tmux_status: false,
      retained_sessions: 10,
    });
  });

  it("previews draft terminal settings when live preview is enabled", async () => {
    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        livePreview: true,
        onUpdate: vi.fn(),
      },
    });
    mockSetTerminalSettings.mockClear();

    await fireEvent.input(screen.getByLabelText("Font size"), {
      target: { value: "19" },
    });

    expect(mockSetTerminalSettings).toHaveBeenLastCalledWith({
      font_family: "",
      font_size: 19,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      hide_tmux_status: false,
      retained_sessions: 10,
    });
  });

  it("filters fonts in the chooser", async () => {
    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        onUpdate: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Choose" }));

    const filter = screen.getByRole("searchbox", { name: "Filter fonts" });
    await fireEvent.input(filter, {
      target: { value: "fira" },
    });

    expect(screen.getByRole("button", { name: /Fira Code/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /JetBrains Mono/ })).toBeNull();

    await fireEvent.keyDown(filter, { key: "Escape" });

    expect((filter as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("dialog", { name: "Choose monospace font" })).toBeTruthy();
  });

  it("persists a font selected from the chooser", async () => {
    const selectedFontFamily = '"Fira Code", monospace';
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        ...mockTerminalStore.defaultTerminal,
        font_family: selectedFontFamily,
      },
    });
    const onUpdate = vi.fn();

    render(TerminalSettings, {
      props: {
        terminal: { ...mockTerminalStore.defaultTerminal },
        onUpdate,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Choose" }));
    await fireEvent.click(screen.getByRole("button", { name: /Fira Code/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
        terminal: {
          ...mockTerminalStore.defaultTerminal,
          font_family: selectedFontFamily,
        },
      });
      expect(onUpdate).toHaveBeenCalledWith({
        ...mockTerminalStore.defaultTerminal,
        font_family: selectedFontFamily,
      });
    });
  });

  it("persists a selected font from an embedded terminal", async () => {
    mockEmbedded.value = true;
    const selectedFontFamily = '"Fira Code", monospace';
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        ...mockTerminalStore.defaultTerminal,
        font_family: selectedFontFamily,
      },
    });

    render(TerminalSettings, {
      props: {
        terminal: { ...mockTerminalStore.defaultTerminal },
        onUpdate: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Choose" }));
    await fireEvent.click(screen.getByRole("button", { name: /Fira Code/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(mockUpdateSettings).toHaveBeenCalledWith({
        terminal: {
          ...mockTerminalStore.defaultTerminal,
          font_family: selectedFontFamily,
        },
      });
    });
  });

  it("replaces the preferred font while preserving fallbacks", async () => {
    render(TerminalSettings, {
      props: {
        terminal: {
          font_family: '"Iosevka Term", "SF Mono", Menlo, monospace',
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
          retained_sessions: 10,
        },
        onUpdate: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Choose" }));
    await fireEvent.click(screen.getByRole("button", { name: /Fira Code/ }));

    expect((screen.getByLabelText("Monospace font family") as HTMLInputElement).value).toBe(
      '"Fira Code", "SF Mono", Menlo, monospace',
    );
  });
});
