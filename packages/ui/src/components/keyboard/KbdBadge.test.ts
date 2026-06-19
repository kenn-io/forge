import { cleanup, render, screen } from "@testing-library/svelte";
import { compile } from "svelte/compiler";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import KbdBadge from "./KbdBadge.svelte";
import componentSource from "./KbdBadge.svelte?raw";

function compiledStyle(source: string, selectorParts: string[]): CSSStyleDeclaration {
  const css = compile(source, { filename: "component.svelte" }).css?.code ?? "";
  const style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);

  for (const rule of Array.from(style.sheet?.cssRules ?? [])) {
    if (!("selectorText" in rule) || !("style" in rule)) continue;
    if (selectorParts.every((part) => String(rule.selectorText).includes(part))) {
      return rule.style as CSSStyleDeclaration;
    }
  }
  throw new Error(`Could not find compiled style rule for ${selectorParts.join(" ")}`);
}

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

  it("uses app text metrics for compact macOS shortcuts", () => {
    const badge = compiledStyle(componentSource, [".kbd-badge", '[data-joiner="compact"]']);

    expect(badge.getPropertyValue("font-family")).toBe("var(--font-sans)");
    expect(badge.getPropertyValue("letter-spacing")).toBe("0.07em");
  });

  it("includes a screen-reader-only expanded label", () => {
    render(KbdBadge, {
      props: { binding: { key: "k", ctrlOrMeta: true } },
    });
    expect(screen.getByText(/(Command|Control)-K/i)).toBeTruthy();
  });
});
