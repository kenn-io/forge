import { flushSync, mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS } from "@middleman/ui";

import { STORES_KEY } from "../../../../../packages/ui/src/context.js";
import {
  noteSessionMounted,
  noteSessionUnmounted,
  registerSessionSlot,
  resetSessionHostForTest,
  sessionHostKey,
  setSessionSlotVisible,
} from "../../stores/session-host.svelte.ts";
import SessionTerminalPool from "./SessionTerminalPool.svelte";

const WAIT = 10_000;

const agent = sessionHostKey("ws-1", undefined, "agent", "2026-04-29T00:00:00Z");
const shell = sessionHostKey("ws-1", undefined, "shell", "2026-04-29T00:00:00Z");

const settingsStore = {
  getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
  getTerminalRenderer: () => DEFAULT_TERMINAL_SETTINGS.renderer,
  getTerminalFontFamily: () => DEFAULT_TERMINAL_SETTINGS.font_family,
  getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
  getTerminalScrollback: () => DEFAULT_TERMINAL_SETTINGS.scrollback,
  getTerminalLineHeight: () => DEFAULT_TERMINAL_SETTINGS.line_height,
  getTerminalLetterSpacing: () => DEFAULT_TERMINAL_SETTINGS.letter_spacing,
  getTerminalCursorBlink: () => DEFAULT_TERMINAL_SETTINGS.cursor_blink,
  getTerminalFontLigatures: () => DEFAULT_TERMINAL_SETTINGS.font_ligatures,
};

// The placement effect parks synchronously, defers the real move by a tick, then
// reveals once the wrapper reports non-zero geometry via a rAF poll. Two frames
// clear both, matching WorkspaceHost.browser.svelte.ts.
function waitForReparent(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve(undefined)));
  });
}

function makeSlot(): HTMLElement {
  const el = document.createElement("div");
  // Definite dimensions: `.session-host-wrapper` is height:100%, which only
  // resolves to non-zero pixels under a parent with a definite height, and the
  // reveal loop waits on exactly that.
  el.style.cssText = "width: 400px; height: 300px;";
  document.body.append(el);
  return el;
}

function wrapperFor(hostKey: string): HTMLElement | null {
  return document.querySelector(`[data-session-host="${CSS.escape(hostKey)}"]`);
}

// "exited" keeps TerminalPane from opening a WebSocket against a backend this
// tier does not run; the wrapper and its reparenting are what is under test.
function mountSession(hostKey: string): void {
  noteSessionMounted({ hostKey, websocketPath: `/ws/${hostKey}`, status: "exited" });
}

describe("SessionTerminalPool", () => {
  let container: HTMLElement;
  let slotA: HTMLElement;
  let slotB: HTMLElement;
  let instance: object | null = null;

  beforeEach(() => {
    resetSessionHostForTest();
    container = document.createElement("div");
    document.body.append(container);
    slotA = makeSlot();
    slotB = makeSlot();
  });

  afterEach(() => {
    if (instance) flushSync(() => unmount(instance as never));
    instance = null;
    container.remove();
    slotA.remove();
    slotB.remove();
    resetSessionHostForTest();
  });

  function mountPool(): void {
    instance = mount(SessionTerminalPool, {
      target: container,
      context: new Map([[STORES_KEY, { settings: settingsStore }]]),
    });
  }

  function showIn(hostKey: string, slot: HTMLElement | null): void {
    registerSessionSlot(hostKey, slot);
    setSessionSlotVisible(hostKey, slot !== null);
    flushSync();
  }

  it("moves one live terminal subtree between slots without recreating it", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    const wrapper = await vi.waitFor(() => {
      const el = wrapperFor(agent);
      expect(el).not.toBeNull();
      return el as HTMLElement;
    }, WAIT);
    expect(wrapper.parentElement).toBe(slotA);
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);

    showIn(agent, slotB);
    await waitForReparent();

    // The same node, not an equal one: a recreated wrapper is a dropped
    // websocket and a blank terminal.
    expect(wrapperFor(agent)).toBe(wrapper);
    expect(wrapper.parentElement).toBe(slotB);
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);
  });

  it("keeps two sessions live at once", async () => {
    mountSession(agent);
    mountSession(shell);
    mountPool();
    showIn(agent, slotA);
    showIn(shell, slotB);
    await waitForReparent();

    const agentWrapper = wrapperFor(agent);
    const shellWrapper = wrapperFor(shell);
    expect(agentWrapper?.parentElement).toBe(slotA);
    expect(shellWrapper?.parentElement).toBe(slotB);
    // The whole point of a registry rather than a singleton: promoting one
    // session out of the workspace pane must not blank the other.
    expect(agentWrapper).not.toBe(shellWrapper);
  });

  it("parks a session whose slot unregisters and keeps it alive", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();
    const wrapper = wrapperFor(agent) as HTMLElement;
    expect(wrapper.parentElement).toBe(slotA);

    showIn(agent, null);
    await waitForReparent();

    // Closing the pane that showed a session is exactly what the parking area
    // exists for: the terminal keeps its socket until the session ends.
    expect(wrapperFor(agent)).toBe(wrapper);
    expect(wrapper.parentElement?.classList.contains("session-pool-parking")).toBe(true);
    expect(wrapper.inert).toBe(true);
  });

  it("deactivates a terminal whose slot is mounted but hidden", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();
    const wrapper = wrapperFor(agent) as HTMLElement;
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);

    // Two sessions tabbed into one leaf: the inactive tab's panel stays mounted
    // under visibility:hidden, so its slot is still registered.
    setSessionSlotVisible(agent, false);
    flushSync();

    expect(wrapper.parentElement).toBe(slotA);
    expect(wrapper.inert).toBe(true);
  });

  it("removes a terminal whose session unmounts, wherever it was parented", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();
    expect(wrapperFor(agent)).not.toBeNull();

    noteSessionUnmounted(agent);
    flushSync();

    // Svelte cannot remove a node this component reparented out of its own
    // fragment, so the pool has to; otherwise a dead terminal stays on screen.
    expect(wrapperFor(agent)).toBeNull();
    expect(slotA.childElementCount).toBe(0);
  });
});
