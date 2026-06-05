import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";

import Cheatsheet from "./Cheatsheet.svelte";
import {
  closeCheatsheet,
  openCheatsheet,
  resetCheatsheetState,
} from "../../stores/keyboard/cheatsheet-state.svelte.js";
import { registerScopedActions, resetRegistry } from "../../stores/keyboard/registry.svelte.js";
import type { Action } from "../../stores/keyboard/types.js";
import { resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";

const noop = (): void => {};
const trueWhen = (): boolean => true;

function action(id: string, label: string, scope: Action["scope"], binding: Action["binding"]): Action {
  return {
    id,
    label,
    scope,
    binding,
    priority: 0,
    when: trueWhen,
    handler: noop,
  };
}

function sectionByHeader(dialog: Element, header: string): Element | undefined {
  return Array.from(dialog.querySelectorAll(".cheatsheet-section")).find(
    (section) => (section.querySelector(".cheatsheet-section-header")?.textContent ?? "").trim() === header,
  );
}

describe("Cheatsheet", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    resetCheatsheetState();
    resetModalStack();
    resetRegistry();
    localStorage.clear();
  });

  it("renders only when isCheatsheetOpen is true", async () => {
    const { rerender } = render(Cheatsheet, { props: {} });
    expect(screen.queryByRole("dialog")).toBeNull();
    openCheatsheet();
    await rerender({});
    const dialog = screen.getByRole("dialog", {
      name: "Keyboard shortcuts",
    });
    expect(dialog).not.toBeNull();
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    closeCheatsheet();
    await rerender({});
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders the Global section with at least one bound action", async () => {
    registerScopedActions("test", [
      action("test.global", "Test global action", "global", {
        key: "g",
      }),
    ]);
    const { rerender } = render(Cheatsheet, { props: {} });
    openCheatsheet();
    await rerender({});
    const dialog = screen.getByRole("dialog", {
      name: "Keyboard shortcuts",
    });
    const section = sectionByHeader(dialog, "Global");
    expect(section).toBeTruthy();
    expect(section!.textContent ?? "").toContain("Test global action");
  });

  it("renders the Commands section for actions without a binding", async () => {
    registerScopedActions("test", [action("test.cmd", "Test command", "global", null)]);
    const { rerender } = render(Cheatsheet, { props: {} });
    openCheatsheet();
    await rerender({});
    const dialog = screen.getByRole("dialog", {
      name: "Keyboard shortcuts",
    });
    const section = sectionByHeader(dialog, "Commands");
    expect(section).toBeTruthy();
    expect(section!.textContent ?? "").toContain("Test command");
  });

  it("filter input narrows visible actions by label substring", async () => {
    registerScopedActions("test", [
      action("test.alpha", "Alpha command", "global", { key: "a" }),
      action("test.beta", "Beta command", "global", { key: "b" }),
    ]);
    const { rerender } = render(Cheatsheet, { props: {} });
    openCheatsheet();
    await rerender({});
    const dialog = screen.getByRole("dialog", {
      name: "Keyboard shortcuts",
    });
    const before = sectionByHeader(dialog, "Global");
    expect(before).toBeTruthy();
    expect(before!.textContent ?? "").toContain("Alpha command");
    expect(before!.textContent ?? "").toContain("Beta command");

    const input = dialog.querySelector(".cheatsheet-filter");
    expect(input).not.toBeNull();
    await fireEvent.input(input!, { target: { value: "alpha" } });
    await rerender({});

    const dialog2 = screen.getByRole("dialog", {
      name: "Keyboard shortcuts",
    });
    const after = sectionByHeader(dialog2, "Global");
    expect(after).toBeTruthy();
    expect(after!.textContent ?? "").toContain("Alpha command");
    expect(after!.textContent ?? "").not.toContain("Beta command");
  });

  it("clicking the backdrop closes the cheatsheet", async () => {
    const { rerender, container } = render(Cheatsheet, { props: {} });
    openCheatsheet();
    await rerender({});
    expect(screen.getByRole("dialog", { name: "Keyboard shortcuts" })).not.toBeNull();
    const backdrop = container.querySelector(".cheatsheet-backdrop");
    expect(backdrop).not.toBeNull();
    await fireEvent.click(backdrop!);
    await rerender({});
    expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).toBeNull();
  });

  it("omits the Component shortcuts section when no entries exist", async () => {
    registerScopedActions("test", [
      action("test.global", "Test global action", "global", {
        key: "g",
      }),
    ]);
    const { rerender } = render(Cheatsheet, { props: {} });
    openCheatsheet();
    await rerender({});
    const dialog = screen.getByRole("dialog", {
      name: "Keyboard shortcuts",
    });
    expect(sectionByHeader(dialog, "Component shortcuts")).toBeUndefined();
  });
});
