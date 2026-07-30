import { describe, expect, it, vi } from "vite-plus/test";

import {
  clearSharedTerminalTextureAtlas,
  registerTerminalTextureAtlasParticipant,
} from "./sharedTerminalTextureAtlas.js";

function terminal(rows = 24) {
  return {
    rows,
    clearTextureAtlas: vi.fn(),
    refresh: vi.fn(),
  };
}

describe("shared terminal texture atlas", () => {
  it("repaints sibling terminals after one clears the shared atlas", () => {
    const source = terminal();
    const sibling = terminal(30);
    const unregisterSource = registerTerminalTextureAtlasParticipant(source);
    const unregisterSibling = registerTerminalTextureAtlasParticipant(sibling);

    clearSharedTerminalTextureAtlas(source);

    expect(source.clearTextureAtlas).toHaveBeenCalledTimes(1);
    expect(source.refresh).not.toHaveBeenCalled();
    expect(sibling.refresh).toHaveBeenCalledWith(0, 29);
    unregisterSource();
    unregisterSibling();
  });

  it("does not repaint terminals after they unregister", () => {
    const source = terminal();
    const retired = terminal();
    const unregisterSource = registerTerminalTextureAtlasParticipant(source);
    const unregisterRetired = registerTerminalTextureAtlasParticipant(retired);
    unregisterRetired();

    clearSharedTerminalTextureAtlas(source);

    expect(retired.refresh).not.toHaveBeenCalled();
    unregisterSource();
  });
});
