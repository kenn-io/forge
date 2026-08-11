import { flushSync, mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { DEFAULT_TERMINAL_SETTINGS } from "../../api/types.js";

import {
  consumeSessionFocus,
  isSessionMounted,
  noteSessionConnection,
  noteSessionMounted,
  noteSessionDiscarded,
  noteSessionReleased,
  registerSessionSlot,
  requestSessionFocus,
  resetSessionHostForTest,
  sessionHostKey,
  setRetainedSessionLimit,
  setSessionSlotVisible,
} from "../../stores/session-host.svelte.ts";
import SessionTerminalPool from "./SessionTerminalPoolRuntimeHarness.svelte";
import SessionTerminalSlotTransferHarness from "./SessionTerminalSlotTransferHarness.svelte";

const WAIT = 10_000;

const agent = sessionHostKey("ws-1", undefined, "agent", "2026-04-29T00:00:00Z");
const shell = sessionHostKey("ws-1", undefined, "shell", "2026-04-29T00:00:00Z");

const settingsStore = {
  getTerminalSettings: () => DEFAULT_TERMINAL_SETTINGS,
  getTerminalFontFamily: () => DEFAULT_TERMINAL_SETTINGS.font_family,
  getTerminalFontSize: () => DEFAULT_TERMINAL_SETTINGS.font_size,
  getTerminalScrollback: () => DEFAULT_TERMINAL_SETTINGS.scrollback,
  getTerminalLineHeight: () => DEFAULT_TERMINAL_SETTINGS.line_height,
  getTerminalLetterSpacing: () => DEFAULT_TERMINAL_SETTINGS.letter_spacing,
  getTerminalCursorBlink: () => DEFAULT_TERMINAL_SETTINGS.cursor_blink,
  getTerminalFontLigatures: () => DEFAULT_TERMINAL_SETTINGS.font_ligatures,
};

// The placement effect parks synchronously, defers the real move by a tick, then
// activates on the next animation frame. Two frames comfortably clear both.
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
      props: { settings: settingsStore },
    });
  }

  function showIn(hostKey: string, slot: HTMLElement | null): void {
    registerSessionSlot(hostKey, slot);
    if (slot !== null) setSessionSlotVisible(hostKey, slot, true);
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

  it("gives a mounted session's terminal somewhere for focus to land", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    const wrapper = await vi.waitFor(() => {
      const el = wrapperFor(agent);
      expect(el).not.toBeNull();
      return el as HTMLElement;
    }, WAIT);
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);

    wrapper.focus();

    // Focus Terminal on a promoted session reaches this wrapper and nothing else:
    // the pool renders it outside the workspace host, so without a focusable
    // wrapper the command silently leaves the keyboard where it was.
    expect(document.activeElement).toBe(wrapper);
  });

  it("focuses an existing session whose hidden pane is revealed", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    const wrapper = await vi.waitFor(() => {
      const el = wrapperFor(agent);
      expect(el).not.toBeNull();
      return el as HTMLElement;
    }, WAIT);
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);

    showIn(agent, null);
    await waitForReparent();
    const button = document.createElement("button");
    document.body.append(button);
    button.focus();

    // The production order for Focus Terminal on a promoted session: the
    // request is made while its pane is still hidden, then the pool reparents
    // the existing terminal across a tick and a frame. Focusing its inert,
    // still-parked wrapper immediately would do nothing.
    requestSessionFocus(agent);
    showIn(agent, slotB);
    await waitForReparent();

    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);
    button.remove();
  });

  it("focuses an already visible session's terminal when requested", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    const button = document.createElement("button");
    document.body.append(button);
    button.focus();

    requestSessionFocus(agent);

    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);
    button.remove();
  });

  it("keeps keyboard focus inside the terminal across a pane move", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    requestSessionFocus(agent);
    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);

    // A pane move: the slot changes and nothing queues a focus request. The
    // reparent through the display:none parking node blurs xterm's helper
    // textarea, so unless the pool restores what it took, tmux copy/paste
    // keys land on <body> until the user clicks the terminal again.
    showIn(agent, slotB);
    await waitForReparent();

    expect(wrapperFor(agent)?.parentElement).toBe(slotB);
    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);
  });

  // The production path, unlike the registry-driven move above: a real pane
  // move unmounts the source SessionTerminalSlot, whose cleanup parks the
  // wrapper — silently blurring xterm, with no focusout — before the pool's
  // placement effect runs. Focus must survive that ordering too.
  for (const order of ["source-first", "destination-first"] as const) {
    it(`keeps keyboard focus across a real slot transfer (${order})`, async () => {
      mountSession(agent);
      mountPool();
      const harnessTarget = document.createElement("div");
      document.body.append(harnessTarget);
      const props = $state({ hostKey: agent, showSource: true, showDestination: false, order });
      const harness = mount(SessionTerminalSlotTransferHarness, { target: harnessTarget, props });
      await waitForReparent();

      requestSessionFocus(agent);
      await vi.waitFor(() => {
        expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
      }, WAIT);

      flushSync(() => {
        props.showSource = false;
        props.showDestination = true;
      });
      await waitForReparent();

      const wrapper = wrapperFor(agent) as HTMLElement;
      expect(wrapper.parentElement?.parentElement?.dataset.slot).toBe("destination");
      await vi.waitFor(() => {
        expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
      }, WAIT);

      flushSync(() => unmount(harness as never));
      harnessTarget.remove();
    });
  }

  it("does not steal focus another element claimed during the move", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    requestSessionFocus(agent);
    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);

    const input = document.createElement("input");
    document.body.append(input);

    showIn(agent, slotB);
    // Claimed between parking and attachment: the keyboard went somewhere on
    // purpose, and the restore must not take it back.
    input.focus();

    const wrapper = wrapperFor(agent) as HTMLElement;
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);
    expect(document.activeElement).toBe(input);
    input.remove();
  });

  it("drops ownership when its pane closes, even with no focus claim after", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    requestSessionFocus(agent);
    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);

    // The pane closes while the terminal holds focus, and the user touches
    // nothing focusable afterwards: focus sits on <body>. A later, unrelated
    // reveal must not replay ownership the close already took away.
    showIn(agent, null);
    await waitForReparent();

    showIn(agent, slotB);
    const wrapper = wrapperFor(agent) as HTMLElement;
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);
    expect(document.activeElement?.closest(".terminal-container")).toBeNull();
  });

  it("keeps ownership through a cross-flush transfer's transient park", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    requestSessionFocus(agent);
    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);

    // A promotion can unregister the source slot in one flush and register the
    // destination in the next: the terminal passes through a no-destination
    // park. That transient must not be mistaken for the pane closing.
    showIn(agent, null);
    showIn(agent, slotB);
    await waitForReparent();

    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);
  });

  it("drops the restore intent once focus was claimed elsewhere while parked", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();

    requestSessionFocus(agent);
    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);

    showIn(agent, null);
    await waitForReparent();

    // The user worked somewhere else while the pane was closed and left focus
    // on nothing. A much later reveal must not replay the stale intent.
    const input = document.createElement("input");
    document.body.append(input);
    input.focus();
    input.blur();

    showIn(agent, slotB);
    const wrapper = wrapperFor(agent) as HTMLElement;
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);
    expect(document.activeElement?.closest(".terminal-container")).toBeNull();
    input.remove();
  });

  it("honors a soft focus request over a plain button's focus", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();
    const wrapper = wrapperFor(agent) as HTMLElement;
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);

    // A PR list row is a plain button: like a launch tile, it is not a focus
    // target the terminal must defer to when navigation asks for acquisition.
    const button = document.createElement("button");
    document.body.append(button);
    button.focus();

    requestSessionFocus(agent, { soft: true });
    await vi.waitFor(() => {
      expect(document.activeElement?.closest(".terminal-container")).not.toBeNull();
    }, WAIT);
    button.remove();
  });

  it("declines a soft focus request while a sacred element holds focus", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();
    const wrapper = wrapperFor(agent) as HTMLElement;
    await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);

    const input = document.createElement("input");
    document.body.append(input);
    input.focus();

    requestSessionFocus(agent, { soft: true });
    flushSync();

    // The user is typing somewhere: navigation-driven acquisition must lose,
    // and the declined request must not stay armed for a later reveal.
    expect(document.activeElement).toBe(input);
    expect(consumeSessionFocus(agent)).toBe(false);
    input.remove();
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
    setSessionSlotVisible(agent, slotA, false);
    flushSync();

    expect(wrapper.parentElement).toBe(slotA);
    expect(wrapper.inert).toBe(true);
  });

  // The tests above drive the registry directly. These go through the real
  // SessionTerminalSlot instead, so the attachment, the visibility effect, and
  // the pool's placement all take part in one same-flush transfer.
  //
  // They do NOT prove the ownership guard in releaseSessionSlot: Svelte tears
  // down removed blocks before creating new ones whichever way the template is
  // ordered, so the destination always registers last here and an unconditional
  // release passes too. The guard's regression coverage is the store-level
  // "ignores a superseded slot" tests, which can produce the other order.
  for (const order of ["source-first", "destination-first"] as const) {
    it(`moves the terminal to the destination slot when the template is ${order}`, async () => {
      mountSession(agent);
      mountPool();
      const harnessTarget = document.createElement("div");
      document.body.append(harnessTarget);
      // A state proxy, not a plain object: Svelte 5 reads mount() props
      // reactively only through one, and a plain object would leave the
      // transfer below a no-op that passes for the wrong reason.
      const props = $state({ hostKey: agent, showSource: true, showDestination: false, order });
      const harness = mount(SessionTerminalSlotTransferHarness, { target: harnessTarget, props });
      await waitForReparent();

      const wrapper = wrapperFor(agent) as HTMLElement;
      expect(wrapper.parentElement?.parentElement?.dataset.slot).toBe("source");

      // Both in one update, exactly as a promotion does it: the source's cleanup
      // and the destination's registration land in the same flush.
      flushSync(() => {
        props.showSource = false;
        props.showDestination = true;
      });
      await waitForReparent();

      expect(wrapperFor(agent)).toBe(wrapper);
      expect(wrapper.parentElement?.parentElement?.dataset.slot).toBe("destination");
      // A superseded slot clearing the key would leave the terminal parked and
      // inert with no way back.
      await vi.waitFor(() => expect(wrapper.inert).toBe(false), WAIT);

      flushSync(() => unmount(harness as never));
      harnessTarget.remove();
    });
  }

  it("removes a terminal whose session unmounts, wherever it was parented", async () => {
    mountSession(agent);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();
    expect(wrapperFor(agent)).not.toBeNull();

    noteSessionDiscarded(agent);
    flushSync();

    // Svelte cannot remove a node this component reparented out of its own
    // fragment, so the pool has to; otherwise a dead terminal stays on screen.
    expect(wrapperFor(agent)).toBeNull();
    expect(slotA.childElementCount).toBe(0);
  });

  it("removes a released terminal when the retention limit evicts it", async () => {
    mountSession(agent);
    noteSessionConnection(agent, true);
    mountPool();
    showIn(agent, slotA);
    await waitForReparent();
    expect(wrapperFor(agent)).not.toBeNull();

    noteSessionReleased(agent);
    setRetainedSessionLimit(0);
    flushSync();

    expect(isSessionMounted(agent)).toBe(false);
    expect(wrapperFor(agent)).toBeNull();
    expect(slotA.childElementCount).toBe(0);
  });
});
