// Shared jsdom harness for mounting the real App shell (real Provider, real
// stores, real views) with the API mocked at the fetch boundary. Used by
// component tests converted from the Playwright mock-API e2e suite.

import { act, render } from "@testing-library/svelte";
import { vi } from "vite-plus/test";

import { createMockApiFetch, type MockApiHandle, type MockRouteOverride } from "./mockApiFetch.js";

class StubEventSource {
  static instances: StubEventSource[] = [];
  url: string;
  readyState = 0;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    StubEventSource.instances.push(this);
  }

  addEventListener(): void {}
  removeEventListener(): void {}
  dispatchEvent(): boolean {
    return false;
  }
  close(): void {
    this.readyState = 2;
  }
}

export function installAppDomGlobals(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();
    },
  );
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();
      takeRecords = vi.fn(() => []);
    },
  );
  vi.stubGlobal("EventSource", StubEventSource);
  if (typeof Element.prototype.scrollIntoView !== "function") {
    Element.prototype.scrollIntoView = () => {};
  }
  // xterm probes a canvas context during module evaluation; jsdom logs a
  // loud "not implemented" error for it. Returning null keeps it quiet.
  HTMLCanvasElement.prototype.getContext = (() => null) as typeof HTMLCanvasElement.prototype.getContext;
}

export interface MountedApp {
  api: MockApiHandle;
  target: HTMLElement;
  unmount: () => void;
}

export interface MountAppOptions {
  overrides?: MockRouteOverride[];
  /**
   * Simulated layout width in px. jsdom has no real layout, so this drives
   * both window.innerWidth and the #app element's clientWidth, which the
   * container store classifies. Defaults to a desktop width.
   */
  viewportWidth?: number;
}

/**
 * Mount the real App.svelte at the given in-app path with fetch served by
 * the mock API fixtures. Equivalent to the Playwright mock suite's
 * `mockApi(page); page.goto(path)`.
 */
export async function mountApp(path: string, options: MountAppOptions = {}): Promise<MountedApp> {
  const api = createMockApiFetch(options.overrides ?? []);
  vi.stubGlobal("fetch", api.fetch);

  const width = options.viewportWidth ?? 1280;
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true, writable: true });

  window.history.replaceState(null, "", path);
  const { replaceUrl } = await import("../lib/stores/router.svelte.js");
  replaceUrl(path);

  // App.svelte resolves its container by id; a leftover #app from an
  // earlier mount in the same test would shadow this one.
  document.getElementById("app")?.remove();
  const target = document.createElement("div");
  target.id = "app";
  Object.defineProperty(target, "clientWidth", { value: width, configurable: true });
  document.body.appendChild(target);

  const { default: App } = await import("../App.svelte");
  const { unmount } = render(App, { target });

  return {
    api,
    target,
    unmount: () => {
      unmount();
      target.remove();
    },
  };
}

/**
 * Re-parse the current URL through the router, mirroring what a browser
 * back/forward (popstate) does after history.pushState.
 */
export async function firePopstate(path: string): Promise<void> {
  window.history.pushState(null, "", path);
  await act(() => {
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
}

/**
 * Dispatch a keydown on window the way a real keystroke reaches the app
 * shell's global handler. `target` defaults to document.body.
 */
export async function resetKeyboardModuleState(): Promise<void> {
  const { resetPaletteState } = await import("../lib/stores/keyboard/palette-state.svelte.js");
  const { resetCheatsheetState } = await import("../lib/stores/keyboard/cheatsheet-state.svelte.js");
  const { resetRegistry } = await import("../lib/stores/keyboard/registry.svelte.js");
  const { resetModalStack } = await import("@middleman/ui/stores/keyboard/modal-stack");
  resetPaletteState();
  resetCheatsheetState();
  resetRegistry();
  resetModalStack();
}

export function pressKey(
  key: string,
  modifiers: { meta?: boolean; ctrl?: boolean; shift?: boolean; alt?: boolean } = {},
  target: EventTarget | null = document.activeElement ?? document.body,
): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    key,
    metaKey: modifiers.meta ?? false,
    ctrlKey: modifiers.ctrl ?? false,
    shiftKey: modifiers.shift ?? false,
    altKey: modifiers.alt ?? false,
    bubbles: true,
    cancelable: true,
  });
  (target ?? document.body).dispatchEvent(event);
  return event;
}
