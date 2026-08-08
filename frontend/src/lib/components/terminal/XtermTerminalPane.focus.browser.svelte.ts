import { flushSync, mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { Effect } from "effect";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import { STORES_KEY } from "../../context.js";
import XtermTerminalPaneTestHarness from "./XtermTerminalPaneTestHarness.svelte";

const controlledSockets: ControlledWebSocket[] = [];

class ControlledWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly CONNECTING = ControlledWebSocket.CONNECTING;
  readonly OPEN = ControlledWebSocket.OPEN;
  readonly CLOSING = ControlledWebSocket.CLOSING;
  readonly CLOSED = ControlledWebSocket.CLOSED;
  binaryType: BinaryType = "arraybuffer";
  readyState = ControlledWebSocket.CONNECTING;
  readonly sent: Array<string | ArrayBufferView> = [];

  constructor(readonly url: string) {
    super();
    controlledSockets.push(this);
    queueMicrotask(() => {
      this.readyState = ControlledWebSocket.OPEN;
      this.dispatchEvent(new Event("open"));
    });
  }

  close(): void {
    this.readyState = ControlledWebSocket.CLOSED;
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    if (typeof data === "string" || ArrayBuffer.isView(data)) this.sent.push(data);
  }
}

describe("XtermTerminalPane focus", () => {
  let runtime: OwnedAppRuntime;

  beforeEach(() => {
    runtime = makeAppRuntime();
    controlledSockets.length = 0;
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
  });

  it("turns an unscrollable agent wheel gesture into cursor input", async () => {
    vi.stubGlobal("WebSocket", ControlledWebSocket);

    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const props = $state({
      runtime,
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
      cursorWheelInput: true,
    });
    const component = mount(XtermTerminalPaneTestHarness, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(controlledSockets).toHaveLength(1);
        expect(target.querySelector(".xterm-screen")).not.toBeNull();
      });
      controlledSockets[0]!.sent.length = 0;

      const screen = target.querySelector(".xterm-screen");
      expect(screen).not.toBeNull();
      const defaultAllowed = screen!.dispatchEvent(
        new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: -120 }),
      );

      expect(defaultAllowed).toBe(false);
      await vi.waitFor(() => {
        const frames = controlledSockets[0]!.sent.map((frame) =>
          typeof frame === "string" ? frame : new TextDecoder().decode(frame),
        );
        expect(frames).toContain("\x1b[A");
      });
    } finally {
      unmount(component);
      target.remove();
    }
  });

  it("moves keyboard focus into the terminal when it is created active", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const props = $state({
      runtime,
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
    });
    const component = mount(XtermTerminalPaneTestHarness, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      await vi.waitFor(() => {
        expect(document.activeElement).toBe(target.querySelector(".xterm-helper-textarea"));
      });
    } finally {
      unmount(component);
      target.remove();
    }
  });

  it("constructs without stealing focus when autofocus is disabled", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const button = document.createElement("button");
    document.body.appendChild(button);
    button.focus();
    const props = $state({
      runtime,
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
      autoFocus: false,
    });
    const component = mount(XtermTerminalPaneTestHarness, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(button);
    } finally {
      unmount(component);
      target.remove();
      button.remove();
    }
  });

  it("does not steal focus when an already-mounted terminal becomes active", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const button = document.createElement("button");
    document.body.appendChild(button);
    const props = $state({
      runtime,
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: false,
    });
    const component = mount(XtermTerminalPaneTestHarness, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      button.focus();
      expect(document.activeElement).toBe(button);

      props.active = true;
      flushSync();
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(button);
    } finally {
      unmount(component);
      target.remove();
      button.remove();
    }
  });

  it("does not steal focus from a button focused during the async init window", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const button = document.createElement("button");
    document.body.appendChild(button);
    const props = $state({
      runtime,
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
    });
    const component = mount(XtermTerminalPaneTestHarness, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      // Mount captures the focus intent synchronously, before the font-load
      // race in start() resolves — focusing the button here lands inside
      // that async window, the same way live-remounting under an open
      // settings popover would.
      button.focus();
      expect(document.activeElement).toBe(button);

      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(button);
    } finally {
      unmount(component);
      target.remove();
      button.remove();
    }
  });

  it("does not steal focus from a control inside an open dialog", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const dialogInput = document.createElement("input");
    dialog.appendChild(dialogInput);
    document.body.appendChild(dialog);
    dialogInput.focus();
    expect(document.activeElement).toBe(dialogInput);

    const props = $state({
      runtime,
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
    });
    const component = mount(XtermTerminalPaneTestHarness, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(dialogInput);
    } finally {
      unmount(component);
      target.remove();
      dialog.remove();
    }
  });
});
