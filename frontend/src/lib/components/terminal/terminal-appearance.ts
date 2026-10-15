import type { ITheme } from "@xterm/xterm";

export type TerminalAppearanceMode = "dark" | "light" | "e-reader" | "e-reader-color";

export interface TerminalAppearance {
  theme: ITheme;
  minimumContrastRatio: number;
}

const DARK_THEME: ITheme = { background: "#0d1117", foreground: "#c9d1d9", cursor: "#58a6ff" };
// Matches the quiet light silver surface and ink. Every ANSI text hue clears
// 4.5:1 on the page; the contrast floor still darkens any color a TUI picked for a dark
// background, so light-gray output never disappears into the page.
const LIGHT_THEME: ITheme = {
  background: "#d9d7d3",
  foreground: "#2a2927",
  cursor: "#1d4580",
  cursorAccent: "#d9d7d3",
  selectionBackground: "#1d458038",
  black: "#2a2927",
  red: "#9b2c1f",
  green: "#355a1c",
  yellow: "#6f4a03",
  blue: "#1d4f91",
  magenta: "#86377b",
  cyan: "#135a5e",
  white: "#bfbcb7",
  brightBlack: "#5e5b57",
  brightRed: "#9f3a27",
  brightGreen: "#3e6620",
  brightYellow: "#7d5606",
  brightBlue: "#2b5c9d",
  brightMagenta: "#8b4081",
  brightCyan: "#1d6468",
  brightWhite: "#eceae6",
};
const E_READER_THEME: ITheme = {
  background: "#ffffff",
  foreground: "#000000",
  cursor: "#000000",
  cursorAccent: "#ffffff",
  selectionBackground: "#dbeafe",
};

/**
 * The terminal follows the app's light or dark theme. Plain e-reader mode
 * forces every cell to maximum contrast, which renders all ANSI colors as
 * black. Color e-reader keeps ANSI hues but has xterm darken them until each
 * reaches 7:1 against the white page.
 */
export function terminalAppearance(mode: TerminalAppearanceMode): TerminalAppearance {
  switch (mode) {
    case "e-reader":
      return { theme: E_READER_THEME, minimumContrastRatio: 21 };
    case "e-reader-color":
      return { theme: E_READER_THEME, minimumContrastRatio: 7 };
    case "light":
      return { theme: LIGHT_THEME, minimumContrastRatio: 4.5 };
    case "dark":
      return { theme: DARK_THEME, minimumContrastRatio: 4.5 };
  }
}
