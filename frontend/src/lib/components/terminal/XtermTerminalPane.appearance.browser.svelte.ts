import { flushSync, mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { Effect } from "effect";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import { cleanupTheme, initTheme, setAppearanceMode, type AppearanceMode } from "../../stores/theme.svelte.js";
import { STORES_KEY } from "../../context.js";
import XtermTerminalPaneTestHarness from "./XtermTerminalPaneTestHarness.svelte";

class IdleWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  binaryType: BinaryType = "arraybuffer";
  readyState = IdleWebSocket.CONNECTING;
  close(): void {
    this.readyState = IdleWebSocket.CLOSED;
  }
  send(): void {}
}

describe("XtermTerminalPane appearance", () => {
  let runtime: OwnedAppRuntime;
  let target: HTMLElement;

  beforeEach(() => {
    runtime = makeAppRuntime();
    vi.stubGlobal("WebSocket", IdleWebSocket);
    localStorage.setItem("kenn-forge-theme", "light");
    initTheme();
    target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
  });

  afterEach(async () => {
    target.remove();
    localStorage.removeItem("kenn-forge-theme");
    localStorage.removeItem("kenn-forge-theme-name");
    initTheme();
    cleanupTheme();
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
  });

  function mountPane() {
    return mount(XtermTerminalPaneTestHarness, {
      target,
      props: { runtime, websocketPath: "/ws/v1/workspaces/ws-1/runtime/sessions/s1/terminal", active: true },
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
  }

  // xterm paints its theme background onto the scrollable element.
  async function viewportBackground(): Promise<string> {
    let scrollable: HTMLElement | null = null;
    await vi.waitFor(() => {
      scrollable = target.querySelector<HTMLElement>(".xterm-scrollable-element");
      expect(scrollable).not.toBeNull();
    });
    return scrollable!.style.backgroundColor;
  }

  function switchTo(mode: AppearanceMode): void {
    setAppearanceMode(mode);
    flushSync();
  }

  // Plain e-reader forces black ink (21:1); color e-reader keeps ANSI hues at
  // 7:1. WebGL output hides contrast, so read it from the live instance.
  it("follows the app between light and dark", async () => {
    const component = mountPane();
    try {
      expect(await viewportBackground()).toBe("rgb(217, 215, 211)");
      const xterm = target.querySelector(".xterm");

      switchTo("dark");
      await vi.waitFor(async () => expect(await viewportBackground()).toBe("rgb(13, 17, 23)"));

      switchTo("light");
      await vi.waitFor(async () => expect(await viewportBackground()).toBe("rgb(217, 215, 211)"));
      expect(component.appearanceOptions()?.minimumContrastRatio).toBe(4.5);
      expect(target.querySelector(".xterm")).toBe(xterm);
    } finally {
      unmount(component);
    }
  });

  it("repaints the mounted terminal when entering and leaving e-reader modes", async () => {
    const component = mountPane();
    try {
      expect(await viewportBackground()).toBe("rgb(217, 215, 211)");
      expect(component.appearanceOptions()?.minimumContrastRatio).toBe(4.5);
      const xterm = target.querySelector(".xterm");

      switchTo("e-reader");
      await vi.waitFor(async () => expect(await viewportBackground()).toBe("rgb(255, 255, 255)"));
      expect(component.appearanceOptions()?.minimumContrastRatio).toBe(21);

      switchTo("e-reader-color");
      await vi.waitFor(() => expect(component.appearanceOptions()?.minimumContrastRatio).toBe(7));
      expect(await viewportBackground()).toBe("rgb(255, 255, 255)");

      switchTo("light");
      await vi.waitFor(async () => expect(await viewportBackground()).toBe("rgb(217, 215, 211)"));
      expect(component.appearanceOptions()?.minimumContrastRatio).toBe(4.5);
      expect(target.querySelector(".xterm")).toBe(xterm);
    } finally {
      unmount(component);
    }
  });

  it("starts with e-reader colors and contrast when the mode is already selected", async () => {
    for (const [mode, contrast] of [
      ["e-reader", 21],
      ["e-reader-color", 7],
    ] as const) {
      switchTo(mode);
      const component = mountPane();
      try {
        expect(await viewportBackground()).toBe("rgb(255, 255, 255)");
        expect(component.appearanceOptions()?.minimumContrastRatio).toBe(contrast);
      } finally {
        unmount(component);
      }
    }
  });
});
