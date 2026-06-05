import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

type TerminalSettings = {
  font_family: string;
  font_size: number;
  scrollback: number;
  line_height: number;
  letter_spacing: number;
  cursor_blink: boolean;
  font_ligatures: boolean;
  renderer: "xterm" | "ghostty-web";
};

const { currentTerminal, defaultTerminal, mockSetTerminalSettings, mockUpdateSettings } = vi.hoisted(() => {
  const defaults: TerminalSettings = {
    font_family: "",
    font_size: 14,
    scrollback: 1000,
    line_height: 1,
    letter_spacing: 0,
    cursor_blink: true,
    font_ligatures: false,
    renderer: "xterm",
  };
  return {
    currentTerminal: { value: { ...defaults } },
    defaultTerminal: defaults,
    mockSetTerminalSettings: vi.fn((settings: TerminalSettings) => {
      currentTerminal.value = settings;
    }),
    mockUpdateSettings: vi.fn(),
  };
});

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
      getTerminalSettings: () => currentTerminal.value,
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

import TerminalOptionsMenu from "./TerminalOptionsMenu.svelte";

describe("TerminalOptionsMenu", () => {
  afterEach(() => {
    cleanup();
    currentTerminal.value = { ...defaultTerminal };
    mockSetTerminalSettings.mockClear();
    mockUpdateSettings.mockReset();
  });

  it("keeps the popover mounted while a save is in flight", async () => {
    let resolveSave: ((settings: { terminal: TerminalSettings }) => void) | undefined;
    mockUpdateSettings.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveSave = resolve;
        }),
    );

    render(TerminalOptionsMenu);

    await fireEvent.click(screen.getByRole("button", { name: "Terminal options" }));
    await fireEvent.input(screen.getByLabelText("Font size"), {
      target: { value: "19" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Saving..." })).toBeTruthy();
    });

    await fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.getByRole("dialog", { name: "Terminal options" })).toBeTruthy();

    resolveSave?.({
      terminal: {
        ...currentTerminal.value,
        font_size: 19,
      },
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
    });

    await fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Terminal options" })).toBeNull();
    });
    expect(currentTerminal.value.font_size).toBe(19);
  });
});
