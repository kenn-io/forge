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

  it("repaints the mounted terminal when entering and leaving e-reader modes", async () => {
    const component = mountPane();
    try {
      expect(await viewportBackground()).toBe("rgb(13, 17, 23)");
      const xterm = target.querySelector(".xterm");

      switchTo("e-reader");
      await vi.waitFor(async () => expect(await viewportBackground()).toBe("rgb(255, 255, 255)"));

      switchTo("e-reader-color");
      await vi.waitFor(async () => expect(await viewportBackground()).toBe("rgb(255, 255, 255)"));

      switchTo("light");
      await vi.waitFor(async () => expect(await viewportBackground()).toBe("rgb(13, 17, 23)"));
      expect(target.querySelector(".xterm")).toBe(xterm);
    } finally {
      unmount(component);
    }
  });

  it("starts white when e-reader mode is already selected", async () => {
    switchTo("e-reader");
    const component = mountPane();
    try {
      expect(await viewportBackground()).toBe("rgb(255, 255, 255)");
    } finally {
      unmount(component);
    }
  });
});
