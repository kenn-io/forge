import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import KbdBadge from "./KbdBadge.svelte";

function compactText(element: HTMLElement): string {
  return element.textContent?.replace(/\s+/g, "") ?? "";
}

describe("KbdBadge", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders Cmd glyph on macOS", () => {
    vi.stubGlobal("navigator", {
      platform: "MacIntel",
      userAgent: "Mac",
    });
    render(KbdBadge, {
      props: { binding: { key: "k", ctrlOrMeta: true } },
    });
    expect(compactText(screen.getByLabelText(/Command-k/i))).toMatch(/^⌘K/);
  });

  it("renders Ctrl glyph on Linux", () => {
    vi.stubGlobal("navigator", {
      platform: "Linux x86_64",
      userAgent: "X11",
    });
    render(KbdBadge, {
      props: { binding: { key: "k", ctrlOrMeta: true } },
    });
    expect(screen.getByText(/Ctrl.*K/i)).toBeTruthy();
  });

  it("includes a screen-reader-only expanded label", () => {
    render(KbdBadge, {
      props: { binding: { key: "k", ctrlOrMeta: true } },
    });
    expect(screen.getByText(/(Command|Control)-K/i)).toBeTruthy();
  });
});
