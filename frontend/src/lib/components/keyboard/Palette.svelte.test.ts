import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import Palette from "./PaletteRuntimeHarness.svelte";
import { dispatchKeydown as dispatchKeydownWithRuntime } from "../../stores/keyboard/dispatch.svelte.js";
import { makeAppRuntime } from "../../app/runtime.js";
import { closePalette, openPalette, resetPaletteState } from "../../stores/keyboard/palette-state.svelte.js";
import { registerScopedActions, resetRegistry } from "../../stores/keyboard/registry.svelte.js";
import { RECENTS_KEY } from "../../stores/keyboard/recents.svelte.js";
import type { Action, Context } from "../../stores/keyboard/types.js";
import { resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";

const noop = (): void => {};
const trueWhen = (): boolean => true;
const keyboardRuntime = makeAppRuntime();

function dispatchKeydown(event: KeyboardEvent, contextProvider: () => Context): void {
  dispatchKeydownWithRuntime(event, contextProvider, keyboardRuntime);
}

afterAll(async () => {
  await Effect.runPromise(keyboardRuntime.disposeEffect);
});

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
    vi.useRealTimers();
    vi.restoreAllMocks();
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
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    expect(dialog).not.toBeNull();
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    closePalette();
    await rerender({});
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("closes with Cmd-Shift-K", async () => {
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const input = screen.getByRole("textbox", { name: "Search command palette" });
    const event = new KeyboardEvent("keydown", {
      key: "k",
      metaKey: true,
      shiftKey: true,
      cancelable: true,
    });
    Object.defineProperty(event, "target", { value: input });

    dispatchKeydown(event, () => ({}) as Context);

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull());
  });

  it("renders preview placeholder when no results", async () => {
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const preview = screen.getByRole("dialog", { name: "Command palette" }).querySelector(".palette-preview");
    expect(preview).not.toBeNull();
    expect(preview!.textContent ?? "").toContain("Highlight a result to preview it");
  });

  it("preview reflects the highlighted command when results exist", async () => {
    registerScopedActions("test", [
      action("test.first", "First Action", "view-pulls"),
      action("test.second", "Second Action", "global"),
    ]);
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const preview = screen.getByRole("dialog", { name: "Command palette" }).querySelector(".palette-preview");
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
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    const input = screen.getByRole("textbox", { name: "Search command palette" });
    await fireEvent.keyDown(input, { key: "ArrowDown" });
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
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    const input = screen.getByRole("textbox", { name: "Search command palette" });
    await fireEvent.keyDown(input, { key: "ArrowUp" });
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
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    const input = screen.getByRole("textbox", { name: "Search command palette" });
    await fireEvent.keyDown(input, { key: "Enter" });
    await rerender({});
    expect(ran).toBe(true);
    expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();
  });

  it("runs an Effect-valued command selected from the palette", async () => {
    let ran = false;
    registerScopedActions("test-run-effect", [
      {
        id: "test.effect",
        label: "Test Effect",
        scope: "global",
        binding: null,
        priority: 0,
        when: trueWhen,
        handler: () =>
          Effect.sync(() => {
            ran = true;
          }),
      },
    ]);
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});

    await fireEvent.keyDown(screen.getByRole("textbox", { name: "Search command palette" }), { key: "Enter" });

    await waitFor(() => expect(ran).toBe(true));
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
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    const row = dialog.querySelector(".palette-row");
    expect(row).not.toBeNull();
    await fireEvent.click(row!);
    await rerender({});
    expect(ran).toBe(true);
    expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull();
  });

  it("keeps focus claimed by a command after the palette closes", async () => {
    const previous = document.createElement("button");
    const destination = document.createElement("button");
    document.body.append(previous, destination);
    previous.focus();
    registerScopedActions("test-focus-command", [
      {
        id: "test.focus",
        label: "Focus destination",
        scope: "global",
        binding: null,
        priority: 0,
        when: trueWhen,
        handler: () => destination.focus(),
      },
    ]);

    try {
      const { rerender } = render(Palette, { props: {} });
      openPalette();
      await rerender({});
      await fireEvent.click(screen.getByRole("button", { name: /Focus destination/ }));
      await rerender({});

      await waitFor(() => expect(screen.queryByRole("dialog", { name: "Command palette" })).toBeNull());
      expect(document.activeElement).toBe(destination);
    } finally {
      previous.remove();
      destination.remove();
    }
  });

  it("renders no Recently used header when localStorage is empty", async () => {
    const { rerender } = render(Palette, { props: {} });
    openPalette();
    await rerender({});
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    const headers = Array.from(dialog.querySelectorAll(".palette-group-header")).map((el) => el.textContent ?? "");
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
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    const headersBefore = Array.from(dialog.querySelectorAll(".palette-group-header")).map(
      (el) => el.textContent ?? "",
    );
    expect(headersBefore).toContain("Recently used");

    const input = screen.getByRole("textbox", { name: "Search command palette" });
    await fireEvent.input(input, { target: { value: "x" } });
    await rerender({});
    const headersAfter = Array.from(dialog.querySelectorAll(".palette-group-header")).map((el) => el.textContent ?? "");
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
    const dialog = screen.getByRole("dialog", {
      name: "Command palette",
    });
    const recentGroup = Array.from(dialog.querySelectorAll(".palette-group")).find((g) =>
      (g.querySelector(".palette-group-header")?.textContent ?? "").includes("Recently used"),
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
    expect(Date.parse(persisted.items[0].lastSelectedAt)).toBeGreaterThan(Date.parse(seedAt));
  });

  it("renders Docs search results without a global Kata section", async () => {
    const docsSearch = vi.fn(async () => ({
      query: "budget",
      truncated: false,
      hits: [
        {
          folder: "notes",
          folder_name: "Notes",
          name: "budget.md",
          rel_path: "finance/budget.md",
          score: 12,
          hit_type: "body" as const,
          line: 4,
          snippet: { text: "monthly budget", matches: [{ start: 8, end: 14 }] },
        },
      ],
    }));

    const { rerender } = render(Palette, { props: { docsSearch } });
    openPalette();
    await rerender({ docsSearch });
    const dialog = screen.getByRole("dialog", { name: "Command palette" });
    const input = screen.getByRole("textbox", { name: "Search command palette" });
    await fireEvent.input(input, { target: { value: "budget" } });
    const list = dialog.querySelector<HTMLElement>(".palette-list");
    expect(list).not.toBeNull();

    await waitFor(() => {
      expect(docsSearch).toHaveBeenCalledWith("budget", 11, expect.any(AbortSignal));
      expect(within(list!).queryByText("Kata tasks")).toBeNull();
      expect(within(list!).getByText("Docs")).toBeTruthy();
      expect(within(list!).getByText("finance/budget.md")).toBeTruthy();
    });
  });

  it("aborts a superseded Docs search", async () => {
    let firstSignal: AbortSignal | undefined;
    const docsSearch = vi.fn((query: string, _limit: number, signal?: AbortSignal) => {
      if (query === "first") {
        firstSignal = signal;
        return new Promise<never>(() => {});
      }
      return Promise.resolve({
        query,
        truncated: false,
        hits: [
          {
            folder: "notes",
            folder_name: "Notes",
            name: "second.md",
            rel_path: "second.md",
            score: 1,
            hit_type: "filename" as const,
          },
        ],
      });
    });
    const props = { docsSearch };
    const { rerender } = render(Palette, { props });
    openPalette();
    await rerender(props);
    const input = screen.getByRole("textbox", { name: "Search command palette" });

    await fireEvent.input(input, { target: { value: "first" } });
    await waitFor(() => expect(docsSearch).toHaveBeenCalledWith("first", 11, expect.any(AbortSignal)));
    await fireEvent.input(input, { target: { value: "second" } });
    await waitFor(() => expect(screen.getAllByText("second.md")).toHaveLength(2));
    await waitFor(() => expect(firstSignal?.aborted).toBe(true));
    expect(screen.queryAllByText("first.md")).toHaveLength(0);
    expect(screen.getAllByText("second.md")).toHaveLength(2);
  });

  it("selects injected Docs results with their callback", async () => {
    const onOpenDoc = vi.fn();
    const docsSearch = vi.fn(async () => ({
      query: "budget",
      truncated: false,
      hits: [
        {
          folder: "notes",
          folder_name: "Notes",
          name: "budget.md",
          rel_path: "finance/budget.md",
          score: 12,
          hit_type: "filename" as const,
        },
      ],
    }));

    const props = { docsSearch, onOpenDoc };
    const { rerender } = render(Palette, { props });
    openPalette();
    await rerender(props);
    let dialog = screen.getByRole("dialog", { name: "Command palette" });
    let input = screen.getByRole("textbox", { name: "Search command palette" });
    await fireEvent.input(input, { target: { value: "budget" } });
    let list = dialog.querySelector<HTMLElement>(".palette-list");
    expect(list).not.toBeNull();
    await waitFor(() => expect(within(list!).getByText("finance/budget.md")).toBeTruthy());
    await fireEvent.click(within(list!).getByText("finance/budget.md"));
    expect(onOpenDoc).toHaveBeenCalledWith("notes", "finance/budget.md");
  });
});
