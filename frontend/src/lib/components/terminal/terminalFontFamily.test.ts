import { describe, expect, it } from "vite-plus/test";

import { buildTerminalFontFamily, primaryTerminalFontFamily } from "./terminalFontFamily.js";

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

describe("primaryTerminalFontFamily", () => {
  it("selects the first effective family from a quoted fallback list", () => {
    expect(primaryTerminalFontFamily('"Berkeley Mono, Variable", "Symbols Nerd Font Mono", monospace')).toBe(
      '"Berkeley Mono, Variable"',
    );
  });

  it("keeps a generic-only stack valid", () => {
    expect(primaryTerminalFontFamily("monospace, sans-serif")).toBe("monospace");
  });
});
