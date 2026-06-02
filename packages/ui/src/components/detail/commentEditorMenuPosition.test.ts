import { describe, expect, it } from "vite-plus/test";

import { computeCommentEditorMenuPosition } from "./commentEditorMenuPosition";

describe("computeCommentEditorMenuPosition", () => {
  it("anchors the menu below the caret by default", () => {
    const position = computeCommentEditorMenuPosition({
      caretRect: { left: 120, top: 200, bottom: 220, width: 0 },
      viewportWidth: 1200,
      viewportHeight: 900,
      menuHeight: 180,
    });

    expect(position.top).toBe(226);
    expect(position.left).toBe(120);
    expect(position.maxWidth).toBe(420);
  });

  it("caps the menu width instead of matching the editor width", () => {
    const position = computeCommentEditorMenuPosition({
      caretRect: { left: 32, top: 140, bottom: 160, width: 0 },
      viewportWidth: 360,
      viewportHeight: 800,
      menuHeight: 180,
    });

    expect(position.width).toBe(328);
    expect(position.maxWidth).toBe(328);
  });

  it("flips above only when there is not enough room below", () => {
    const position = computeCommentEditorMenuPosition({
      caretRect: { left: 300, top: 760, bottom: 780, width: 0 },
      viewportWidth: 1200,
      viewportHeight: 820,
      menuHeight: 120,
    });

    expect(position.top).toBe(634);
  });
});
