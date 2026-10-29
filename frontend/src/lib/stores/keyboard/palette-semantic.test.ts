import { afterEach, describe, expect, it } from "vite-plus/test";

import { parsePaletteQuery } from "./palette-search.svelte.js";
import { readSearchMode, SEARCH_MODE_KEY, semanticQueryFor } from "./palette-semantic.js";

describe("palette semantic search", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("ranks by meaning only for item scopes and queries long enough to carry meaning", () => {
    expect(semanticQueryFor("meaning", parsePaletteQuery("flaky ci"))).toEqual({ scope: "all", query: "flaky ci" });
    expect(semanticQueryFor("meaning", parsePaletteQuery("pr: login"))).toEqual({ scope: "pr", query: "login" });
    expect(semanticQueryFor("meaning", parsePaletteQuery("ci"))).toBeNull();
    expect(semanticQueryFor("meaning", parsePaletteQuery(">settings"))).toBeNull();
    expect(semanticQueryFor("text", parsePaletteQuery("flaky ci"))).toBeNull();
  });

  it("falls back to title matching for an unknown stored mode", () => {
    localStorage.setItem(SEARCH_MODE_KEY, "telepathy");
    expect(readSearchMode()).toBe("text");
  });
});
