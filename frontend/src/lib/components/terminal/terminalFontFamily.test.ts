import { describe, expect, it } from "vite-plus/test";

import { buildTerminalFontFamily } from "./terminalFontFamily.js";

describe("buildTerminalFontFamily", () => {
  const defaultStack = '"JetBrains Mono", monospace';

  it.each(["", "   "])("uses the fallback stack when configured font is blank: %j", (configuredFont) => {
    expect(buildTerminalFontFamily(configuredFont, defaultStack)).toBe(defaultStack);
  });

  it("keeps configured concrete fonts before default and generic fallbacks", () => {
    expect(buildTerminalFontFamily('"MesloLGS NF", "Symbols Nerd Font Mono", monospace', defaultStack)).toBe(
      '"MesloLGS NF", "Symbols Nerd Font Mono", "JetBrains Mono", monospace',
    );
  });
});
