import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

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

const {
  currentTerminal,
  mockSetTerminalSettings,
  mockUpdateSettings,
  normalizeTerminalSettings,
} = vi.hoisted(() => {
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
  const normalize = (
    terminal: Partial<TerminalSettings> | null | undefined,
  ): TerminalSettings => ({
    font_family: terminal?.font_family ?? defaults.font_family,
    font_size: terminal?.font_size ?? defaults.font_size,
    scrollback: terminal?.scrollback ?? defaults.scrollback,
    line_height: terminal?.line_height ?? defaults.line_height,
    letter_spacing: terminal?.letter_spacing ?? defaults.letter_spacing,
    cursor_blink: terminal?.cursor_blink ?? defaults.cursor_blink,
    font_ligatures:
      terminal?.font_ligatures ?? defaults.font_ligatures,
    renderer:
      terminal?.renderer === "ghostty-web" ? "ghostty-web" : "xterm",
  });
  return {
    currentTerminal: { value: { ...defaults } },
    mockSetTerminalSettings: vi.fn(
      (settings: Partial<TerminalSettings> | null | undefined) => {
        currentTerminal.value = normalize(settings);
      },
    ),
    mockUpdateSettings: vi.fn(),
    normalizeTerminalSettings: normalize,
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
  normalizeTerminalSettings,
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
    currentTerminal.value = normalizeTerminalSettings(null);
    mockSetTerminalSettings.mockClear();
    mockUpdateSettings.mockReset();
  });

  it("keeps the popover mounted while a save is in flight", async () => {
    let resolveSave:
      | ((settings: { terminal: TerminalSettings }) => void)
      | undefined;
    mockUpdateSettings.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveSave = resolve;
        }),
    );

    render(TerminalOptionsMenu);

    await fireEvent.click(
      screen.getByRole("button", { name: "Terminal options" }),
    );
    await fireEvent.input(screen.getByLabelText("Font size"), {
      target: { value: "19" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Saving..." })).toBeTruthy();
    });

    await fireEvent.keyDown(window, { key: "Escape" });
    expect(
      screen.getByRole("dialog", { name: "Terminal options" }),
    ).toBeTruthy();

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
      expect(
        screen.queryByRole("dialog", { name: "Terminal options" }),
      ).toBeNull();
    });
    expect(currentTerminal.value.font_size).toBe(19);
  });
});
