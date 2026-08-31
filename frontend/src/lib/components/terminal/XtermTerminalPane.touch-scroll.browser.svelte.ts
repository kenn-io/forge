import { mount, unmount } from "svelte";
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

  receive(text: string): void {
    const bytes = new TextEncoder().encode(text);
    this.dispatchEvent(new MessageEvent("message", { data: bytes.buffer }));
  }

  sentText(): string[] {
    return this.sent.map((frame) => (typeof frame === "string" ? frame : new TextDecoder().decode(frame)));
  }
}

const ENABLE_SGR_WHEEL_TRACKING = "\x1b[?1000;1006h";
const SGR_WHEEL_REPORT = /\x1b\[<6[45];(\d+);(\d+)M/;

function touchEvent(type: string, target: Element, clientX: number, clientY: number): TouchEvent {
  const touch = new Touch({
    identifier: 7,
    target,
    clientX,
    clientY,
    pageX: clientX + window.scrollX,
    pageY: clientY + window.scrollY,
  });
  const touches = type === "touchend" ? [] : [touch];
  return new TouchEvent(type, {
    bubbles: true,
    cancelable: true,
    touches,
    targetTouches: touches,
    changedTouches: [touch],
  });
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

describe("XtermTerminalPane touch scrolling", () => {
  let runtime: OwnedAppRuntime;

  beforeEach(() => {
    // xterm only installs its touch gesture recognizer on pages that look
    // like a touch device; headless desktop Chromium reports zero touch points.
    Object.defineProperty(navigator, "maxTouchPoints", { value: 5, configurable: true });
    runtime = makeAppRuntime();
    controlledSockets.length = 0;
  });

  afterEach(async () => {
    await Effect.runPromise(runtime.disposeEffect);
    vi.unstubAllGlobals();
    delete (navigator as { maxTouchPoints?: number }).maxTouchPoints;
  });

  it("keeps momentum-scroll mouse reports positioned when an app tracks the wheel", async () => {
    vi.stubGlobal("WebSocket", ControlledWebSocket);

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
        expect(controlledSockets).toHaveLength(1);
        expect(target.querySelector(".xterm-screen")).not.toBeNull();
      });
      const socket = controlledSockets[0]!;
      const screen = target.querySelector<HTMLElement>(".xterm-screen")!;

      // The app (Codex, tmux, vim, ...) turns on SGR mouse tracking. xterm
      // marks its element once the mode is active, so wait for that before
      // the touch gesture starts.
      socket.receive(ENABLE_SGR_WHEEL_TRACKING);
      const xterm = target.querySelector<HTMLElement>(".xterm")!;
      await vi.waitFor(() => {
        expect(xterm.classList.contains("enable-mouse-events")).toBe(true);
      });
      socket.sent.length = 0;

      // A fast upward flick like Android Chrome produces: finger down, a few
      // fast moves, then lift. The lift starts xterm's inertia scrolling.
      const bounds = screen.getBoundingClientRect();
      const x = bounds.left + 100;
      let y = bounds.top + 300;
      screen.dispatchEvent(touchEvent("touchstart", screen, x, y));
      for (let step = 0; step < 4; step++) {
        await sleep(16);
        y -= 60;
        screen.dispatchEvent(touchEvent("touchmove", screen, x, y));
      }
      screen.dispatchEvent(touchEvent("touchend", screen, x, y));

      // Let momentum run out (xterm's friction stops the fling within ~1s).
      await vi.waitFor(() => {
        expect(socket.sentText().join("")).toContain("\x1b[<6");
      });
      await sleep(1200);

      const frames = socket.sentText();
      const joined = frames.join("");
      expect(JSON.stringify(joined)).not.toContain("NaN");
      const reports = joined.match(new RegExp(SGR_WHEEL_REPORT.source, "g")) ?? [];
      expect(reports.length).toBeGreaterThan(0);
      for (const report of reports) {
        const [, col, row] = report.match(SGR_WHEEL_REPORT)!;
        expect(Number(col)).toBeGreaterThanOrEqual(1);
        expect(Number(row)).toBeGreaterThanOrEqual(1);
      }
    } finally {
      unmount(component);
      target.remove();
    }
  });
});
