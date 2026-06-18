// Browser-tier analog of mountApp() from appHarness.ts. Where the jsdom harness
// has to shim matchMedia/ResizeObserver/IntersectionObserver/canvas (jsdom has
// none), a real Chromium page provides all of them natively, so this harness
// stubs only EventSource: the live-update SSE stream the app shell opens has no
// backend here, and a real EventSource would spin trying to connect. Everything
// else is the genuine browser implementation.
//
// The mock API is wired the same way as the jsdom harness, but via
// globalThis.fetch (real Chromium has a real fetch) rather than vi.stubGlobal.
// We capture and restore the original on unmount so specs cannot leak a mock
// fetch into one another.

import { render } from "vitest-browser-svelte";

import { createMockApiFetch, type MockApiHandle, type MockRouteOverride } from "./mockApiFetch.js";

// A no-op EventSource so the live-update store's stream connect is inert. The
// app shell only needs it not to throw and not to keep retrying; it never
// asserts on streamed events in this tier.
class NoopEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = 0;
  withCredentials = false;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;

  constructor(url: string | URL) {
    this.url = String(url);
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

export interface MountedBrowserApp {
  api: MockApiHandle;
  target: HTMLElement;
  unmount: () => void;
}

export interface MountBrowserAppOptions {
  overrides?: MockRouteOverride[];
}

/**
 * Mount the real App.svelte at the given in-app path in a real Chromium page,
 * with fetch served by the shared mock API fixtures. Browser analog of the
 * jsdom mountApp(): same fixtures, same router seeding, real layout.
 */
export async function mountBrowserApp(path: string, options: MountBrowserAppOptions = {}): Promise<MountedBrowserApp> {
  const api = createMockApiFetch(options.overrides ?? []);
  const originalFetch = globalThis.fetch;
  globalThis.fetch = api.fetch;

  const originalEventSource = globalThis.EventSource;
  globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;

  // Seed the URL before the router module reads it, then drive the route store
  // explicitly the way the jsdom harness does.
  window.history.replaceState(null, "", path);
  const { replaceUrl } = await import("../lib/stores/router.svelte.js");
  replaceUrl(path);

  // App.svelte resolves its container by id (initContainerObserver reads
  // document.getElementById("app") for layout width). A leftover #app from an
  // earlier mount in the same page would shadow this one.
  document.getElementById("app")?.remove();
  const target = document.createElement("div");
  target.id = "app";
  document.body.appendChild(target);

  // vitest-browser-svelte's render forwards `target` straight to Svelte.mount,
  // so App mounts into the same #app element it reads for its width.
  const { default: App } = await import("../App.svelte");
  const { unmount } = render(App, { target });

  return {
    api,
    target,
    unmount: () => {
      unmount();
      target.remove();
      globalThis.fetch = originalFetch;
      globalThis.EventSource = originalEventSource;
    },
  };
}

/**
 * Re-parse the current URL through the router, mirroring what a browser
 * back/forward (popstate) does after history.pushState. Browser-tier analog of
 * appHarness.ts firePopstate(): a real Chromium page dispatches and handles a
 * genuine PopStateEvent, so no act() wrapper is needed (it exists in the jsdom
 * harness only to flush Testing Library's microtask batching).
 */
export function firePopstate(path: string): void {
  window.history.pushState(null, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

/**
 * Reset the keyboard subsystem's module-level singletons between mounts so one
 * spec's palette/cheatsheet/registry/modal-stack state cannot leak into the
 * next. Identical to appHarness.ts resetKeyboardModuleState(), including the
 * dynamic import of "@middleman/ui/stores/keyboard/modal-stack".
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

/**
 * Dispatch a real keydown the way a genuine keystroke reaches the app shell's
 * global handler. Browser-tier analog of appHarness.ts pressKey(): a real
 * Chromium page dispatches a native KeyboardEvent, so the same construction
 * works without any DOM-global stubbing. `target` defaults to the active
 * element (or document.body when nothing is focused).
 */
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
