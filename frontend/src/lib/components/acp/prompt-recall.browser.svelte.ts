// Line measurement depends on real text layout, so this runs in Chromium.

import { afterEach, describe, expect, it } from "vite-plus/test";

import { caretOnEdgeLine } from "./prompt-recall.js";

afterEach(() => {
  document.body.replaceChildren();
});

describe("caretOnEdgeLine (browser)", () => {
  it("keeps a caret on the last line when the prompt ends in a space that hangs at a wrap", () => {
    const field = document.createElement("textarea");
    field.style.cssText = "font: 14px sans-serif; line-height: 20px; padding: 0; border: 0;";
    field.value = "Explain how the sync worker retries rate limited requests ";
    document.body.append(field);
    const wrong: string[] = [];
    // Some width leaves the final space hanging at the end of a full line.
    for (let width = 120; width <= 420; width += 1) {
      field.style.width = `${width}px`;
      for (const caret of [field.value.length, field.value.length - 3]) {
        field.setSelectionRange(caret, caret);
        if (!caretOnEdgeLine(field, "last")) wrong.push(`${width}px at ${caret}`);
      }
    }
    expect(wrong).toEqual([]);
  });
});
