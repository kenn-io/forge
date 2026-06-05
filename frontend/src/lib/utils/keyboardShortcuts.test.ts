import { describe, expect, it } from "vite-plus/test";

import { shouldIgnoreGlobalShortcutTarget } from "./keyboardShortcuts.js";

describe("shouldIgnoreGlobalShortcutTarget", () => {
  it("ignores shortcuts from nested contenteditable targets", () => {
    const editor = document.createElement("div");
    editor.setAttribute("contenteditable", "true");

    const paragraph = document.createElement("p");
    const text = document.createTextNode("#1");
    paragraph.appendChild(text);
    editor.appendChild(paragraph);

    expect(shouldIgnoreGlobalShortcutTarget(text)).toBe(true);
  });

  it("ignores shortcuts from form controls", () => {
    expect(shouldIgnoreGlobalShortcutTarget(document.createElement("input"))).toBe(true);
    expect(shouldIgnoreGlobalShortcutTarget(document.createElement("textarea"))).toBe(true);
    expect(shouldIgnoreGlobalShortcutTarget(document.createElement("select"))).toBe(true);
  });

  it("allows shortcuts from ordinary elements", () => {
    expect(shouldIgnoreGlobalShortcutTarget(document.createElement("div"))).toBe(false);
  });
});
