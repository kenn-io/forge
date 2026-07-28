import { cleanup, render, waitFor } from "@testing-library/svelte";
import type { ComponentProps } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const {
  clipboardWriterAuthorizeKeyboardGesture,
  clipboardWriterCancelPointerGesture,
  clipboardWriterDispose,
  clipboardWriterWrite,
  mockShowFlash,
} = vi.hoisted(() => ({
  clipboardWriterAuthorizeKeyboardGesture: vi.fn(),
  clipboardWriterCancelPointerGesture: vi.fn(),
  clipboardWriterDispose: vi.fn(),
  clipboardWriterWrite: vi.fn(),
  mockShowFlash: vi.fn(),
}));

const mockFit = vi.fn();
const mockFocus = vi.fn();
const mockOpen = vi.fn();
const mockLoadAddon = vi.fn();
const mockOnData = vi.fn();
const mockPaste = vi.fn();
const mockDispose = vi.fn();
const mockInit = vi.fn().mockResolvedValue(undefined);
const terminalCtor = vi.fn();
const terminalWrite = vi.fn();

let configuredFontFamily = "";
let configuredFontSize = 14;
let configuredScrollback = 1000;
let configuredCursorBlink = true;
let sockets: MockWebSocket[] = [];

class MockWebSocket {
  static OPEN = 1;
  readyState = 1;
  binaryType = "arraybuffer";
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  sent: unknown[] = [];

  constructor(public url: string) {
    sockets.push(this);
  }

  send(data: unknown): void {
    this.sent.push(data);
  }
  close(): void {}
}

function socketAt(index: number): MockWebSocket {
  const socket = sockets[index];
  expect(socket).toBeDefined();
  return socket!;
}

vi.mock("@middleman/ui", () => ({
  getStores: () => ({
    settings: {
      getTerminalFontFamily: () => configuredFontFamily,
      getTerminalFontSize: () => configuredFontSize,
      getTerminalScrollback: () => configuredScrollback,
      getTerminalCursorBlink: () => configuredCursorBlink,
    },
  }),
}));

vi.mock("@middleman/ui/stores/flash", () => ({
  showFlash: mockShowFlash,
}));

vi.mock("./terminalClipboardWriter.js", () => ({
  createBrowserTerminalClipboardPort: vi.fn(() => ({})),
  createTerminalClipboardWriter: vi.fn(() => ({
    beginPointerGesture: vi.fn(),
    cancelPointerGesture: clipboardWriterCancelPointerGesture,
    endPointerGesture: vi.fn(),
    authorizeKeyboardGesture: clipboardWriterAuthorizeKeyboardGesture,
    write: clipboardWriterWrite,
    dispose: clipboardWriterDispose,
  })),
}));

vi.mock("ghostty-web", () => ({
  init: (...args: []) => mockInit(...args),
  FitAddon: vi.fn().mockImplementation(function () {
    return {
      fit: mockFit,
    };
  }),
  Terminal: vi.fn().mockImplementation(function (options) {
    terminalCtor(options);
    return {
      cols: 80,
      rows: 24,
      focus: mockFocus,
      open: mockOpen,
      loadAddon: mockLoadAddon,
      onData: mockOnData,
      paste: mockPaste,
      dispose: mockDispose,
      write: terminalWrite,
      options: { ...options },
    };
  }),
}));

import GhosttyTerminalPane from "./GhosttyTerminalPane.svelte";

describe("GhosttyTerminalPane", () => {
  beforeEach(() => {
    configuredFontFamily = "";
    configuredFontSize = 14;
    configuredScrollback = 1000;
    configuredCursorBlink = true;
    delete window.__BASE_PATH__;
    delete window.__KENN_EMBEDDED_WEBSOCKET_BASE_URL__;
    window.__MIDDLEMAN_DEV_API_URL__ = "http://127.0.0.1:8091";
    terminalCtor.mockReset();
    mockFit.mockReset();
    mockFocus.mockReset();
    mockOpen.mockReset();
    mockLoadAddon.mockReset();
    mockOnData.mockReset();
    mockPaste.mockReset();
    mockPaste.mockImplementation((text: string) => {
      const dataHandler = mockOnData.mock.calls[0]?.[0] as ((data: string) => void) | undefined;
      dataHandler?.(`\x1b[200~${text}\x1b[201~`);
    });
    mockDispose.mockReset();
    mockInit.mockClear();
    mockShowFlash.mockReset();
    terminalWrite.mockReset();
    clipboardWriterAuthorizeKeyboardGesture.mockReset();
    clipboardWriterCancelPointerGesture.mockReset();
    clipboardWriterDispose.mockReset();
    clipboardWriterWrite.mockReset().mockResolvedValue("unauthorized");
    sockets = [];

    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe(): void {}
        unobserve(): void {}
        disconnect(): void {}
      },
    );

    vi.stubGlobal("WebSocket", MockWebSocket);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  async function renderStarted(props: Partial<ComponentProps<typeof GhosttyTerminalPane>> = {}) {
    const result = render(GhosttyTerminalPane, { props });
    await waitFor(() => expect(terminalCtor).toHaveBeenCalled());
    return result;
  }

  it("uses the configured settings font family for ghostty-web", async () => {
    configuredFontFamily = '"Fira Code", monospace';
    configuredFontSize = 16;
    configuredScrollback = 5000;
    configuredCursorBlink = false;

    await renderStarted({ workspaceId: "ws-123" });

    expect(terminalCtor).toHaveBeenCalledWith(
      expect.objectContaining({
        cursorBlink: false,
        fontFamily: '"Fira Code", monospace',
        fontSize: 16,
        scrollback: 5000,
      }),
    );
  });

  it("does not initialize ghostty-web more than once across terminal panes", async () => {
    const initCallsBefore = mockInit.mock.calls.length;

    render(GhosttyTerminalPane, { props: { workspaceId: "ws-123" } });
    render(GhosttyTerminalPane, { props: { workspaceId: "ws-456" } });

    await waitFor(() => expect(terminalCtor).toHaveBeenCalledTimes(2));

    expect(mockInit.mock.calls.length - initCallsBefore).toBeLessThanOrEqual(1);
  });

  it("uses the /ws terminal route for the default workspace socket", async () => {
    await renderStarted({ workspaceId: "ws-123" });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://localhost:3000");
    expect(url.pathname).toBe("/ws/v1/workspaces/ws-123/terminal");
  });

  it("uses the embedding host websocket base for terminal routes", async () => {
    window.__KENN_EMBEDDED_WEBSOCKET_BASE_URL__ = "ws://127.0.0.1:19443/lease-capability/";

    await renderStarted({ workspaceId: "ws-123" });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://127.0.0.1:19443");
    expect(url.pathname).toBe("/lease-capability/ws/v1/workspaces/ws-123/terminal");
    expect(url.searchParams.get("cols")).toBe("80");
    expect(url.searchParams.get("rows")).toBe("24");
  });

  it("preserves the Middleman base path through the embedding host socket", async () => {
    window.__BASE_PATH__ = "/middleman/";
    window.__KENN_EMBEDDED_WEBSOCKET_BASE_URL__ = "ws://127.0.0.1:19443/lease-capability/";

    await renderStarted({ workspaceId: "ws-123" });

    const url = new URL(socketAt(0).url);
    expect(url.pathname).toBe("/lease-capability/middleman/ws/v1/workspaces/ws-123/terminal");
  });

  it("applies the base path to the default workspace socket", async () => {
    window.__BASE_PATH__ = "/middleman/";

    await renderStarted({ workspaceId: "ws-123" });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://localhost:3000");
    expect(url.pathname).toBe("/middleman/ws/v1/workspaces/ws-123/terminal");
  });

  it("connects to an explicit websocket path", async () => {
    await renderStarted({
      websocketPath: "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://127.0.0.1:8091");
    expect(url.pathname).toBe("/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal");
    expect(url.searchParams.get("cols")).toBe("80");
    expect(url.searchParams.get("rows")).toBe("24");
  });

  it("keeps /ws paths on the current dev origin for Vite proxying", async () => {
    await renderStarted({
      websocketPath: "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://localhost:3000");
    expect(url.pathname).toBe("/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal");
  });

  it("does not duplicate the base path for explicit websocket paths", async () => {
    window.__BASE_PATH__ = "/middleman/";

    await renderStarted({
      websocketPath: "/middleman/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://localhost:3000");
    expect(url.pathname).toBe("/middleman/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal");
  });

  it("refreshes the terminal when a hidden pane becomes active", async () => {
    const { rerender } = await renderStarted({
      websocketPath: "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      active: false,
    });

    expect(socketAt(0).sent).toEqual([JSON.stringify({ type: "resize_active", active: false })]);
    socketAt(0).sent = [];

    await rerender({
      websocketPath: "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      active: true,
    });

    expect(mockFit).toHaveBeenCalled();
    expect(socketAt(0).sent).toContain(JSON.stringify({ type: "refresh", cols: 80, rows: 24 }));
  });

  it("focuses the ghostty terminal once it initializes while active", async () => {
    await renderStarted({ workspaceId: "ws-123" });

    expect(mockFocus).toHaveBeenCalled();
  });

  it("does not steal focus when an existing terminal becomes active", async () => {
    const { rerender } = await renderStarted({
      websocketPath: "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      active: false,
    });

    expect(mockFocus).not.toHaveBeenCalled();

    await rerender({
      websocketPath: "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      active: true,
    });

    expect(mockFocus).not.toHaveBeenCalled();
  });

  it("does not focus when focus moves to a button during the async ghostty init window", async () => {
    const button = document.createElement("button");
    document.body.appendChild(button);

    try {
      // The focus intent is captured at mount, before ensureGhosttyInitialized()
      // resolves. Focusing the button in that gap — before terminalCtor is
      // called — mirrors focus moving elsewhere during the async init window.
      render(GhosttyTerminalPane, { props: { workspaceId: "ws-123" } });
      button.focus();
      expect(document.activeElement).toBe(button);
      await waitFor(() => expect(terminalCtor).toHaveBeenCalled());

      expect(mockFocus).not.toHaveBeenCalled();
      expect(document.activeElement).toBe(button);
    } finally {
      button.remove();
    }
  });

  it("does not focus when the mount-time active element is inside an open dialog", async () => {
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const dialogInput = document.createElement("input");
    dialog.appendChild(dialogInput);
    document.body.appendChild(dialog);
    dialogInput.focus();
    expect(document.activeElement).toBe(dialogInput);

    try {
      await renderStarted({ workspaceId: "ws-123" });

      expect(mockFocus).not.toHaveBeenCalled();
      expect(document.activeElement).toBe(dialogInput);
    } finally {
      dialog.remove();
    }
  });

  it("forwards complete tmux mouse drags without a local threshold", async () => {
    await renderStarted({ workspaceId: "ws-123" });
    const dataHandler = mockOnData.mock.calls[0]?.[0] as ((data: string) => void) | undefined;
    expect(dataHandler).toBeDefined();
    const drag = "\x1b[<0;10;5M" + "\x1b[<32;12;5M" + "\x1b[<32;13;5M" + "\x1b[<0;13;5m";

    socketAt(0).sent = [];
    dataHandler?.(drag);

    expect(sentText(socketAt(0), 0)).toBe(drag);
  });

  it("does not authorize clipboard writes from untrusted Ghostty terminal data", async () => {
    await renderStarted({ workspaceId: "ws-123" });
    const dataHandler = mockOnData.mock.calls[0]?.[0] as ((data: string) => void) | undefined;

    dataHandler?.("a");

    expect(clipboardWriterAuthorizeKeyboardGesture).not.toHaveBeenCalled();
  });

  it("replaces accepted OSC 52 output with CAN and forwards its text to the clipboard writer", async () => {
    clipboardWriterWrite.mockResolvedValue("written");
    await renderStarted({ workspaceId: "ws-123" });
    const socket = socketAt(0);
    const data = messageBuffer("visible-before\x1b]52;c;Y29waWVkIHRleHQ=\x07visible-after");

    expect(socket.onmessage).not.toBeNull();
    socket.onmessage?.({ data } as MessageEvent);

    expect(writtenText(terminalWrite.mock.calls[0]?.[0])).toBe("visible-before\x18visible-after");
    expect(clipboardWriterWrite).toHaveBeenCalledWith("copied text");
  });

  it("reports blocked OSC 52 clipboard writes once per pane", async () => {
    clipboardWriterWrite.mockResolvedValue("blocked");
    await renderStarted({ workspaceId: "ws-123" });
    const socket = socketAt(0);

    for (const payload of ["b25l", "dHdv"]) {
      socket.onmessage?.({
        data: messageBuffer(`\x1b]52;c;${payload}\x07`),
      } as MessageEvent);
    }
    await waitFor(() => expect(mockShowFlash).toHaveBeenCalledTimes(1));

    expect(mockShowFlash).toHaveBeenCalledWith("Could not write the terminal selection to the clipboard.", {
      tone: "danger",
    });
  });

  it("disposes clipboard handling with the Ghostty pane", async () => {
    const view = await renderStarted({ workspaceId: "ws-123" });

    view.unmount();

    expect(clipboardWriterDispose).toHaveBeenCalledTimes(1);
  });

  it("sends terminal byte payloads as raw WebSocket bytes", async () => {
    await renderStarted({ workspaceId: "ws-123" });
    const dataHandler = mockOnData.mock.calls[0]?.[0] as ((data: Uint8Array) => void) | undefined;
    expect(dataHandler).toBeDefined();

    socketAt(0).sent = [];
    dataHandler?.(new Uint8Array([0, 0xff, 0x1b]));

    const sent = socketAt(0).sent[0];
    expect(sent).toBeInstanceOf(ArrayBuffer);
    expect(Array.from(new Uint8Array(sent as ArrayBuffer))).toEqual([0, 0xff, 0x1b]);
  });

  it("sends terminal ArrayBuffer payloads as raw WebSocket bytes", async () => {
    await renderStarted({ workspaceId: "ws-123" });
    const dataHandler = mockOnData.mock.calls[0]?.[0] as ((data: ArrayBuffer) => void) | undefined;
    expect(dataHandler).toBeDefined();

    socketAt(0).sent = [];
    dataHandler?.(new Uint8Array([0x80, 0x81]).buffer);

    const sent = socketAt(0).sent[0];
    expect(sent).toBeInstanceOf(ArrayBuffer);
    expect(Array.from(new Uint8Array(sent as ArrayBuffer))).toEqual([0x80, 0x81]);
  });

  it("sends browser multiline paste through ghostty bracketed paste handling", async () => {
    const { container } = await renderStarted({
      workspaceId: "ws-123",
    });

    socketAt(0).sent = [];
    const terminalContainer = container.querySelector(".terminal-container");
    expect(terminalContainer).toBeDefined();
    const laterPasteListener = vi.fn();
    terminalContainer!.addEventListener("paste", laterPasteListener, true);

    const event = new Event("paste", {
      bubbles: true,
      cancelable: true,
    }) as ClipboardEvent;
    Object.defineProperty(event, "clipboardData", {
      value: {
        getData: vi.fn((type: string) => (type === "text/plain" ? "first\x1b[201~\nsecond\nthird" : "")),
      },
    });

    const defaultAllowed = terminalContainer!.dispatchEvent(event);

    expect(defaultAllowed).toBe(false);
    expect(laterPasteListener).not.toHaveBeenCalled();
    expect(mockPaste).toHaveBeenCalledWith("first[201~\nsecond\nthird");
    expect(sentText(socketAt(0), 0)).toBe("\x1b[200~first[201~\nsecond\nthird\x1b[201~");
  });

  it("leaves single-line browser paste for ghostty default handling", async () => {
    const { container } = await renderStarted({
      workspaceId: "ws-123",
    });

    socketAt(0).sent = [];
    const terminalContainer = container.querySelector(".terminal-container");
    expect(terminalContainer).toBeDefined();
    const laterPasteListener = vi.fn();
    terminalContainer!.addEventListener("paste", laterPasteListener, true);

    const event = new Event("paste", {
      bubbles: true,
      cancelable: true,
    }) as ClipboardEvent;
    Object.defineProperty(event, "clipboardData", {
      value: {
        getData: vi.fn((type: string) => (type === "text/plain" ? "single line" : "")),
      },
    });

    const defaultAllowed = terminalContainer!.dispatchEvent(event);

    expect(defaultAllowed).toBe(true);
    expect(laterPasteListener).toHaveBeenCalledTimes(1);
    expect(mockPaste).not.toHaveBeenCalled();
    expect(socketAt(0).sent).toHaveLength(0);
  });

  it("does not open a websocket when initialStatus is exited", async () => {
    await renderStarted({
      websocketPath: "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      reconnectOnExit: false,
      initialStatus: "exited",
    });

    expect(sockets).toHaveLength(0);
    expect(terminalWrite).toHaveBeenCalledWith(expect.stringContaining("[Process exited]"));
  });

  it("does not open a websocket when initialStatus is error", async () => {
    await renderStarted({
      websocketPath: "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      reconnectOnExit: false,
      initialStatus: "error",
    });

    expect(sockets).toHaveLength(0);
    expect(terminalWrite).toHaveBeenCalledWith(expect.stringContaining("[Session unavailable]"));
  });

  it("does not restart sessions when reconnectOnExit is false", async () => {
    const onExit = vi.fn();

    await renderStarted({
      websocketPath: "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      reconnectOnExit: false,
      onExit,
    });
    vi.useFakeTimers();

    expect(sockets).toHaveLength(1);
    const socket = socketAt(0);
    socket.onmessage?.(
      new MessageEvent("message", {
        data: JSON.stringify({ type: "exited", code: 0 }),
      }),
    );
    socket.onclose?.();
    vi.advanceTimersByTime(30000);

    expect(sockets).toHaveLength(1);
    expect(terminalWrite).toHaveBeenCalledWith(expect.stringContaining("[Process exited]"));
    expect(onExit).toHaveBeenCalledWith(0);

    vi.useRealTimers();
  });
});

function sentText(socket: MockWebSocket, index: number): string {
  const value = socket.sent[index];
  if (typeof value === "string") return value;
  if (value instanceof ArrayBuffer) {
    return new TextDecoder().decode(value);
  }
  return new TextDecoder().decode(value as ArrayBufferView);
}

function writtenText(value: unknown): string {
  if (typeof value === "string") return value;
  return new TextDecoder().decode(value as ArrayBufferView);
}

function messageBuffer(value: string): ArrayBuffer {
  const encoded = new TextEncoder().encode(value);
  const buffer = new ArrayBuffer(encoded.byteLength);
  new Uint8Array(buffer).set(encoded);
  return buffer;
}
