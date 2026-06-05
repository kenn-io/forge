import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

const { mockSetTerminalSettings, mockUpdateSettings } = vi.hoisted(() => ({
  mockSetTerminalSettings: vi.fn(),
  mockUpdateSettings: vi.fn(),
}));

vi.mock("@middleman/ui", () => ({
  DEFAULT_TERMINAL_SETTINGS: {
    font_family: "",
    font_size: 14,
    scrollback: 1000,
    line_height: 1,
    letter_spacing: 0,
    cursor_blink: true,
    font_ligatures: false,
    renderer: "xterm",
  },
  getStores: () => ({
    settings: {
      setTerminalSettings: mockSetTerminalSettings,
    },
  }),
}));

vi.mock("../../api/settings.js", () => ({
  updateSettings: mockUpdateSettings,
}));

vi.mock("../../stores/embed-config.svelte.js", () => ({
  isEmbedded: () => false,
}));

import TerminalSettings from "./TerminalSettings.svelte";

describe("TerminalSettings", () => {
  afterEach(() => {
    cleanup();
    mockSetTerminalSettings.mockReset();
    mockUpdateSettings.mockReset();
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
        renderer: "xterm",
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
          renderer: "xterm",
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
          renderer: "xterm",
        },
      });
    });
    expect(onUpdate).toHaveBeenCalledWith({
      font_family: '"Iosevka Term", monospace',
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      renderer: "xterm",
    });
    expect(mockSetTerminalSettings).toHaveBeenCalledWith({
      font_family: '"Iosevka Term", monospace',
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      renderer: "xterm",
    });
  });

  it("persists the selected terminal renderer", async () => {
    mockUpdateSettings.mockResolvedValue({
      terminal: {
        font_family: "",
        font_size: 14,
        scrollback: 1000,
        line_height: 1,
        letter_spacing: 0,
        cursor_blink: true,
        font_ligatures: false,
        renderer: "ghostty-web",
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
          renderer: "xterm",
        },
        onUpdate,
      },
    });

    await fireEvent.change(screen.getByLabelText("Terminal renderer"), {
      target: { value: "ghostty-web" },
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
          renderer: "ghostty-web",
        },
      });
    });
    expect(onUpdate).toHaveBeenCalledWith({
      font_family: "",
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      renderer: "ghostty-web",
    });
    expect(mockSetTerminalSettings).toHaveBeenCalledWith({
      font_family: "",
      font_size: 14,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      renderer: "ghostty-web",
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
        renderer: "xterm",
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
          renderer: "xterm",
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
          renderer: "xterm",
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
        renderer: "xterm",
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
          renderer: "xterm",
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
          renderer: "xterm",
        },
      });
    });
  });

  it("disables xterm-only controls for ghostty-web", async () => {
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
          renderer: "ghostty-web",
        },
        onUpdate: vi.fn(),
      },
    });

    expect((screen.getByLabelText("Line height") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText("Letter spacing") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText("Font ligatures") as HTMLInputElement).disabled).toBe(true);
    expect(
      screen.getByText("ghostty-web does not expose line height, letter spacing, or ligature controls."),
    ).toBeTruthy();
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
            renderer: "xterm",
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
        renderer: "xterm",
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
          renderer: "xterm",
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
          renderer: "xterm",
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
          renderer: "xterm",
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
      renderer: "xterm",
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
      renderer: "xterm",
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
        renderer: "xterm",
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
          renderer: "xterm",
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

    expect(mockSetTerminalSettings).toHaveBeenLastCalledWith({
      font_family: "",
      font_size: 19,
      scrollback: 1000,
      line_height: 1,
      letter_spacing: 0,
      cursor_blink: true,
      font_ligatures: false,
      renderer: "xterm",
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
          renderer: "xterm",
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
      renderer: "xterm",
    });
  });

  it("selects a common monospace font from the chooser", async () => {
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
          renderer: "xterm",
        },
        onUpdate: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Choose" }));
    await fireEvent.click(screen.getByRole("button", { name: /Fira Code/ }));

    expect((screen.getByLabelText("Monospace font family") as HTMLInputElement).value).toBe('"Fira Code", monospace');
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
          renderer: "xterm",
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
