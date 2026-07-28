import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";

import { createInitialFocusIntent, focusIsSacred } from "./terminal-focus.js";

describe("focusIsSacred", () => {
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("returns false for null", () => {
    expect(focusIsSacred(null)).toBe(false);
  });

  it("returns false for a plain button", () => {
    const button = document.createElement("button");
    document.body.appendChild(button);
    expect(focusIsSacred(button)).toBe(false);
  });

  it.each(["input", "textarea", "select"])("returns true for a %s element", (tag) => {
    const el = document.createElement(tag);
    document.body.appendChild(el);
    expect(focusIsSacred(el)).toBe(true);
  });

  it("returns true for a contenteditable element", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    expect(focusIsSacred(el)).toBe(true);
  });

  it.each(["dialog", "menu", "listbox"])("returns true for a descendant of a role=%s container", (role) => {
    const container = document.createElement("div");
    container.setAttribute("role", role);
    const child = document.createElement("button");
    container.appendChild(child);
    document.body.appendChild(container);
    expect(focusIsSacred(child)).toBe(true);
  });

  it("returns false for a button outside any dialog/menu/listbox", () => {
    const container = document.createElement("div");
    const child = document.createElement("button");
    container.appendChild(child);
    document.body.appendChild(container);
    expect(focusIsSacred(child)).toBe(false);
  });
});

describe("createInitialFocusIntent", () => {
  beforeEach(() => {
    document.body.replaceChildren();
  });

  afterEach(() => {
    document.body.replaceChildren();
  });

  it("allows focus when the captured element is unchanged and not sacred", () => {
    const button = document.createElement("button");
    document.body.appendChild(button);
    button.focus();

    const intent = createInitialFocusIntent();

    expect(intent.shouldFocus()).toBe(true);
  });

  it("allows focus when the document body is the captured active element", () => {
    document.body.focus();

    const intent = createInitialFocusIntent();

    expect(intent.shouldFocus()).toBe(true);
  });

  it("cancels focus when the active element changed since capture", () => {
    const first = document.createElement("button");
    const second = document.createElement("button");
    document.body.appendChild(first);
    document.body.appendChild(second);
    first.focus();

    const intent = createInitialFocusIntent();
    second.focus();

    expect(intent.shouldFocus()).toBe(false);
  });

  it("cancels focus when the captured element is inside an open dialog", () => {
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const input = document.createElement("input");
    dialog.appendChild(input);
    document.body.appendChild(dialog);
    input.focus();

    const intent = createInitialFocusIntent();

    expect(intent.shouldFocus()).toBe(false);
  });

  it("cancels focus when the captured element is a text input", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();

    const intent = createInitialFocusIntent();

    expect(intent.shouldFocus()).toBe(false);
  });
});
