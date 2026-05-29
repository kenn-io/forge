import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import Palette from "./Palette.svelte";
import {
  closePalette,
  openPalette,
  resetPaletteState,
} from "../../stores/keyboard/palette-state.svelte.js";
import {
  registerScopedActions,
  resetRegistry,
} from "../../stores/keyboard/registry.svelte.js";
import { RECENTS_KEY } from "../../stores/keyboard/recents.svelte.js";
import type { Action } from "../../stores/keyboard/types.js";
import { resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";

const noop = (): void => {};
const trueWhen = (): boolean => true;

function action(id: string, label = id, scope: Action["scope"] = "global"): Action {
  return {
    id,
    label,
    scope,
    binding: null,
    priority: 0,
    when: trueWhen,
    handler: noop,
  };
}

describe("Palette", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    resetPaletteState();
    resetModalStack();
    resetRegistry();
    localStorage.clear();
  });

  it("renders only when isPaletteOpen is true", async () => {
    const { rerender } = render(Palette, { props: {} });
    expect(screen.queryByRole("dialog")).toBeNull();
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    expect(dialog).not.toBeNull();
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    closePalette();
    await rerender({});
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders preview placeholder when no results", async () => {
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const preview = screen
      .getByRole("dialog", { name: "Command palette" })
      .querySelector(".palette-preview");
    expect(preview).not.toBeNull();
    expect(preview!.textContent ?? "").toContain(
      "Highlight a result to preview it",
    );
  });

  it("preview reflects the highlighted command when results exist", async () => {
    registerScopedActions("test", [
      action("test.first", "First Action", "view-pulls"),
      action("test.second", "Second Action", "global"),
    ]);
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const preview = screen
      .getByRole("dialog", { name: "Command palette" })
      .querySelector(".palette-preview");
    expect(preview).not.toBeNull();
    const text = preview!.textContent ?? "";
    expect(text).toContain("First Action");
    expect(text).toContain("Scope: view-pulls");
  });

  it("ArrowDown moves highlight to the next row and the preview updates", async () => {
    registerScopedActions("test", [
      action("test.first", "First Action", "view-pulls"),
      action("test.second", "Second Action", "global"),
    ]);
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const input = dialog.querySelector(".palette-input");
    expect(input).not.toBeNull();
    await fireEvent.keyDown(input!, { key: "ArrowDown" });
    await rerender({});
    const preview = dialog.querySelector(".palette-preview");
    expect(preview).not.toBeNull();
    const text = preview!.textContent ?? "";
    expect(text).toContain("Second Action");
    expect(text).toContain("Scope: global");
  });

  it("ArrowUp at the top is a no-op", async () => {
    registerScopedActions("test", [
      action("test.first", "First Action", "view-pulls"),
      action("test.second", "Second Action", "global"),
    ]);
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const input = dialog.querySelector(".palette-input");
    expect(input).not.toBeNull();
    await fireEvent.keyDown(input!, { key: "ArrowUp" });
    await rerender({});
    const preview = dialog.querySelector(".palette-preview");
    expect(preview).not.toBeNull();
    const text = preview!.textContent ?? "";
    expect(text).toContain("First Action");
    expect(text).toContain("Scope: view-pulls");
  });

  it("Enter runs the highlighted command's handler and closes the palette", async () => {
    let ran = false;
    registerScopedActions("test-run-enter", [
      {
        id: "test.run",
        label: "Test run",
        scope: "global",
        binding: null,
        priority: 0,
        when: trueWhen,
        handler: () => {
          ran = true;
        },
      },
    ]);
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const input = dialog.querySelector(".palette-input");
    expect(input).not.toBeNull();
    await fireEvent.keyDown(input!, { key: "Enter" });
    await rerender({});
    expect(ran).toBe(true);
    expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();
  });

  it("clicking a command row runs its handler and closes the palette", async () => {
    let ran = false;
    registerScopedActions("test-run-click", [
      {
        id: "test.click",
        label: "Test click",
        scope: "global",
        binding: null,
        priority: 0,
        when: trueWhen,
        handler: () => {
          ran = true;
        },
      },
    ]);
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const row = dialog.querySelector(".palette-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);
    await rerender({});
    expect(ran).toBe(true);
    expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();
  });

  it("renders no Recently used header when localStorage is empty", async () => {
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const headers = Array.from(
      dialog.querySelectorAll(".palette-group-header"),
    ).map((el) => el.textContent ?? "");
    expect(headers).not.toContain("Recently used");
  });

  it("hides recents section when query is non-empty", async () => {
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          {
            kind: "pr",
            ref: {
              itemType: "pr",
              provider: "github",
              platformHost: "github.com",
              owner: "acme",
              name: "widgets",
              repoPath: "acme/widgets",
              number: 42,
            },
            lastSelectedAt: new Date().toISOString(),
          },
        ],
      }),
    );
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const headersBefore = Array.from(
      dialog.querySelectorAll(".palette-group-header"),
    ).map((el) => el.textContent ?? "");
    expect(headersBefore).toContain("Recently used");

    const input = dialog.querySelector(".palette-input");
    expect(input).not.toBeNull();
    await fireEvent.input(input!, { target: { value: "x" } });
    await rerender({});
    const headersAfter = Array.from(
      dialog.querySelectorAll(".palette-group-header"),
    ).map((el) => el.textContent ?? "");
    expect(headersAfter).not.toContain("Recently used");
  });

  it("clicking a recent row writes a fresh recent and triggers navigation", async () => {
    // Use a recent timestamp so pruneStale (30-day cutoff) doesn't drop the
    // seeded entry before the row renders.
    const seedAt = new Date(Date.now() - 60_000).toISOString();
    localStorage.setItem(
      RECENTS_KEY,
      JSON.stringify({
        version: 1,
        items: [
          {
            kind: "pr",
            ref: {
              itemType: "pr",
              provider: "github",
              platformHost: "github.com",
              owner: "acme",
              name: "widgets",
              repoPath: "acme/widgets",
              number: 42,
            },
            lastSelectedAt: seedAt,
          },
        ],
      }),
    );
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const recentGroup = Array.from(
      dialog.querySelectorAll(".palette-group"),
    ).find((g) =>
      (g.querySelector(".palette-group-header")?.textContent ?? "").includes(
        "Recently used",
      ),
    );
    expect(recentGroup).toBeTruthy();
    const row = recentGroup!.querySelector(".palette-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);
    await rerender({});

    // We can't assert navigation because the router store is not mocked in
    // this fixture; instead assert the localStorage side effect: the same PR
    // is still at the front and its lastSelectedAt has advanced past the
    // seed timestamp.
    const persisted = JSON.parse(localStorage.getItem(RECENTS_KEY) ?? "{}");
    expect(persisted.items).toBeTruthy();
    expect(persisted.items[0].kind).toBe("pr");
    expect(persisted.items[0].ref.number).toBe(42);
    expect(
      Date.parse(persisted.items[0].lastSelectedAt),
    ).toBeGreaterThan(Date.parse(seedAt));
  });
});
