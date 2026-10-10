import type { ITheme } from "@xterm/xterm";

export type TerminalAppearanceMode = "default" | "e-reader" | "e-reader-color";

export interface TerminalAppearance {
  theme: ITheme;
  minimumContrastRatio: number;
}

const DEFAULT_THEME: ITheme = { background: "#0d1117", foreground: "#c9d1d9", cursor: "#58a6ff" };
const E_READER_THEME: ITheme = {
  background: "#ffffff",
  foreground: "#000000",
  cursor: "#000000",
  cursorAccent: "#ffffff",
  selectionBackground: "#dbeafe",
};

/**
 * Plain e-reader mode forces every cell to maximum contrast, which renders
 * all ANSI colors as black. Color e-reader keeps ANSI hues but has xterm
 * darken them until each reaches 7:1 against the white page.
 */
export function terminalAppearance(mode: TerminalAppearanceMode): TerminalAppearance {
  switch (mode) {
    case "e-reader":
      return { theme: E_READER_THEME, minimumContrastRatio: 21 };
    case "e-reader-color":
      return { theme: E_READER_THEME, minimumContrastRatio: 7 };
    case "default":
      return { theme: DEFAULT_THEME, minimumContrastRatio: 4.5 };
  }
}
