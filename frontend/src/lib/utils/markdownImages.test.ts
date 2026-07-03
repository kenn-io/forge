import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import { getStackDepth, getTopFrame, resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";
import { expandMarkdownImages } from "./markdownImages";

const appCss = readFileSync("src/app.css", "utf8");

function declarationsFor(selector: string): Map<string, string> {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = appCss.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  if (match?.[1]) {
    return new Map(
      match[1]
        .split(";")
        .map((declaration) => declaration.trim())
        .filter(Boolean)
        .map((declaration) => {
          const separator = declaration.indexOf(":");
          return [declaration.slice(0, separator).trim(), declaration.slice(separator + 1).trim()];
        }),
    );
  }
  throw new Error(`Missing CSS rule for ${selector}`);
}

describe("expandMarkdownImages", () => {
  beforeEach(() => {
    resetModalStack();
  });

  afterEach(() => {
    resetModalStack();
    document.querySelectorAll(".markdown-image-lightbox").forEach((node) => node.remove());
  });

  test("adds a top-right control that opens the markdown image in an overlay", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><p><img src="/shots/dashboard.png" alt="Quality dashboard"></p></div>';

    const enhanced = expandMarkdownImages(root);
    const button = root.querySelector<HTMLButtonElement>('button[aria-label="Open image in expanded view"]');

    expect(enhanced).toBe(1);
    expect(button).not.toBeNull();
    expect(button?.closest(".markdown-image-expander")?.querySelector("img")?.getAttribute("src")).toBe(
      "/shots/dashboard.png",
    );

    button?.click();

    const overlay = document.querySelector<HTMLElement>(".markdown-image-lightbox");
    const expanded = overlay?.querySelector<HTMLImageElement>("img");
    expect(overlay?.getAttribute("role")).toBe("dialog");
    expect(overlay?.getAttribute("aria-modal")).toBe("true");
    expect(expanded?.getAttribute("src")).toBe("/shots/dashboard.png");
    expect(expanded?.getAttribute("alt")).toBe("Quality dashboard");
    expect(document.activeElement).toBe(overlay);
  });

  test("keeps zoom controls outside linked markdown images", () => {
    const root = document.createElement("div");
    root.innerHTML = [
      '<div class="markdown-body"><p>',
      '<a href="/shots/dashboard-full.png"><img src="/shots/dashboard.png" alt="Quality dashboard"></a>',
      "</p></div>",
    ].join("");

    const enhanced = expandMarkdownImages(root);
    const link = root.querySelector<HTMLAnchorElement>("a");
    const button = root.querySelector<HTMLButtonElement>('button[aria-label="Open image in expanded view"]');

    expect(enhanced).toBe(1);
    expect(link?.parentElement?.classList.contains("markdown-image-expander")).toBe(true);
    expect(link?.querySelector("img")).not.toBeNull();
    expect(button?.closest("a")).toBeNull();
  });

  test("skips images inside links that also contain text", () => {
    const root = document.createElement("div");
    root.innerHTML = [
      '<div class="markdown-body"><p>',
      '<a href="/shots/dashboard-full.png">Open <img src="/shots/dashboard.png" alt="Quality dashboard"> full size</a>',
      "</p></div>",
    ].join("");

    const enhanced = expandMarkdownImages(root);

    expect(enhanced).toBe(0);
    expect(root.querySelector(".markdown-image-expander")).toBeNull();
    expect(root.querySelector('button[aria-label="Open image in expanded view"]')).toBeNull();
  });

  test("blocks global shortcuts while the expanded image overlay is open", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><p><img src="/shots/dashboard.png" alt="Quality dashboard"></p></div>';
    const windowShortcut = vi.fn();
    window.addEventListener("keydown", windowShortcut);

    try {
      expandMarkdownImages(root);
      root.querySelector<HTMLButtonElement>('button[aria-label="Open image in expanded view"]')?.click();

      const overlay = document.querySelector<HTMLElement>(".markdown-image-lightbox");
      const closeButton = overlay?.querySelector<HTMLButtonElement>('button[aria-label="Close expanded image"]');
      expect(getTopFrame()?.frameId).toBe("markdown-image-lightbox");
      expect(getStackDepth()).toBe(1);

      closeButton?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "1" }));
      expect(windowShortcut).not.toHaveBeenCalled();

      const escape = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key: "Escape" });
      closeButton?.dispatchEvent(escape);
      expect(escape.defaultPrevented).toBe(true);
      expect(document.querySelector(".markdown-image-lightbox")).toBeNull();
      expect(getStackDepth()).toBe(0);
    } finally {
      window.removeEventListener("keydown", windowShortcut);
    }
  });

  test("keeps keyboard focus inside the expanded image overlay", () => {
    const root = document.createElement("div");
    root.innerHTML = '<div class="markdown-body"><p><img src="/shots/dashboard.png" alt="Quality dashboard"></p></div>';

    expandMarkdownImages(root);
    root.querySelector<HTMLButtonElement>('button[aria-label="Open image in expanded view"]')?.click();

    const overlay = document.querySelector<HTMLElement>(".markdown-image-lightbox");
    const closeButton = overlay?.querySelector<HTMLButtonElement>('button[aria-label="Close expanded image"]');
    expect(document.activeElement).toBe(overlay);

    const tabFromOverlay = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key: "Tab" });
    overlay?.dispatchEvent(tabFromOverlay);
    expect(tabFromOverlay.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(closeButton);

    const tabFromClose = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key: "Tab" });
    closeButton?.dispatchEvent(tabFromClose);
    expect(tabFromClose.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(closeButton);

    const shiftTabFromClose = new KeyboardEvent("keydown", {
      bubbles: true,
      cancelable: true,
      key: "Tab",
      shiftKey: true,
    });
    closeButton?.dispatchEvent(shiftTabFromClose);
    expect(shiftTabFromClose.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(closeButton);
  });

  test("lets the expanded image use the viewport instead of a fixed-height canvas", () => {
    const panelStyle = declarationsFor(".markdown-image-lightbox__panel");
    const imageStyle = declarationsFor(".markdown-image-lightbox__panel img");

    expect(panelStyle.get("width")).toBe("fit-content");
    expect(panelStyle.get("height")).toBe("fit-content");
    expect(panelStyle.get("max-width")).toBe("calc(100vw - 56px)");
    expect(panelStyle.get("max-height")).toBe("calc(100vh - 56px)");
    expect(panelStyle.get("overflow")).toBe("visible");
    expect(imageStyle.get("max-width")).toBe("calc(100vw - 56px)");
    expect(imageStyle.get("max-height")).toBe("calc(100vh - 56px)");
  });

  test("renders the expanded image without panel border chrome", () => {
    const panelStyle = declarationsFor(".markdown-image-lightbox__panel");

    expect(panelStyle.get("background")).toBe("transparent");
    expect(panelStyle.get("border")).toBe("none");
    expect(panelStyle.get("border-radius")).toBe("0");
  });

  test("places the expanded image overlay above shared modal layers", () => {
    const overlayStyle = declarationsFor(".markdown-image-lightbox");

    expect(Number(overlayStyle.get("z-index"))).toBeGreaterThan(94);
  });

  test("keeps the zoom affordance hidden until image hover or keyboard focus", () => {
    const buttonStyle = declarationsFor(".markdown-image-expander__button");

    expect(buttonStyle.get("opacity")).toBe("0");
    expect(buttonStyle.get("pointer-events")).toBe("none");
    expect(appCss).toContain(
      [
        ".markdown-image-expander:hover .markdown-image-expander__button,",
        ".markdown-image-expander:focus-within .markdown-image-expander__button {",
        "  opacity: 1;",
        "  pointer-events: auto;",
        "}",
      ].join("\n"),
    );
  });

  test("keeps image controls available on touch pointers", () => {
    expect(appCss).toContain(
      [
        "@media (hover: none), (pointer: coarse) {",
        "  .markdown-image-expander__button,",
        "  .markdown-image-lightbox__close {",
        "    opacity: 1;",
        "    pointer-events: auto;",
        "  }",
        "}",
      ].join("\n"),
    );
  });

  test("keeps wrapped lazy images visible before browser image decode finishes", () => {
    const imageStyle = declarationsFor(".markdown-image-expander img");

    expect(imageStyle.get("display")).toBe("block");
    expect(imageStyle.get("min-width")).toBe("1px");
    expect(imageStyle.get("min-height")).toBe("1px");
  });

  test("keeps the overlay close control hidden until overlay hover or keyboard focus", () => {
    const closeStyle = declarationsFor(".markdown-image-lightbox__close");
    const visibleStyle = declarationsFor(
      ".markdown-image-lightbox__panel:hover .markdown-image-lightbox__close,\n.markdown-image-lightbox__panel:focus-within .markdown-image-lightbox__close",
    );

    expect(closeStyle.get("opacity")).toBe("0");
    expect(closeStyle.get("pointer-events")).toBe("none");
    expect(visibleStyle.get("background")).toBe("var(--viewer-control-bg)");
    expect(visibleStyle.get("color")).toBe("var(--viewer-control-text)");
    expect(appCss).toContain(
      [
        ".markdown-image-lightbox__panel:hover .markdown-image-lightbox__close,",
        ".markdown-image-lightbox__panel:focus-within .markdown-image-lightbox__close {",
        "  background: var(--viewer-control-bg);",
        "  border-color: var(--viewer-control-border);",
        "  color: var(--viewer-control-text);",
        "  box-shadow: var(--viewer-control-shadow);",
        "  opacity: 1;",
        "  pointer-events: auto;",
        "}",
      ].join("\n"),
    );
  });
});
