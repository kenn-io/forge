import { cleanup, render, waitFor } from "@testing-library/svelte";
import type { ComponentProps } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mockFit = vi.fn();
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

vi.mock("ghostty-web", () => ({
  init: (...args: []) => mockInit(...args),
  FitAddon: vi.fn().mockImplementation(() => ({
    fit: mockFit,
  })),
  Terminal: vi.fn().mockImplementation((options) => {
    terminalCtor(options);
    return {
      cols: 80,
      rows: 24,
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
    window.__MIDDLEMAN_DEV_API_URL__ = "http://127.0.0.1:8091";
    terminalCtor.mockReset();
    mockFit.mockReset();
    mockOpen.mockReset();
    mockLoadAddon.mockReset();
    mockOnData.mockReset();
    mockPaste.mockReset();
    mockPaste.mockImplementation((text: string) => {
      const dataHandler = mockOnData.mock.calls[0]?.[0] as
        | ((data: string) => void)
        | undefined;
      dataHandler?.(`\x1b[200~${text}\x1b[201~`);
    });
    mockDispose.mockReset();
    mockInit.mockClear();
    terminalWrite.mockReset();
    sockets = [];

    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe(): void {}
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

  async function renderStarted(
    props: Partial<ComponentProps<typeof GhosttyTerminalPane>> = {},
  ) {
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
      websocketPath:
        "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://127.0.0.1:8091");
    expect(url.pathname).toBe(
      "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    );
    expect(url.searchParams.get("cols")).toBe("80");
    expect(url.searchParams.get("rows")).toBe("24");
  });

  it("keeps /ws paths on the current dev origin for Vite proxying", async () => {
    await renderStarted({
      websocketPath:
        "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://localhost:3000");
    expect(url.pathname).toBe(
      "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    );
  });

  it("does not duplicate the base path for explicit websocket paths", async () => {
    window.__BASE_PATH__ = "/middleman/";

    await renderStarted({
      websocketPath:
        "/middleman/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    });

    expect(sockets).toHaveLength(1);
    const url = new URL(socketAt(0).url);
    expect(url.origin).toBe("ws://localhost:3000");
    expect(url.pathname).toBe(
      "/middleman/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
    );
  });

  it("refreshes the terminal when a hidden pane becomes active", async () => {
    const { rerender } = await renderStarted({
      websocketPath:
        "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      active: false,
    });

    expect(socketAt(0).sent).toEqual([]);

    await rerender({
      websocketPath:
        "/ws/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      active: true,
    });

    expect(mockFit).toHaveBeenCalled();
    expect(socketAt(0).sent).toContain(
      JSON.stringify({ type: "refresh", cols: 80, rows: 24 }),
    );
  });

  it("filters tiny tmux mouse drags before sending terminal input", async () => {
    await renderStarted({ workspaceId: "ws-123" });
    const dataHandler = mockOnData.mock.calls[0]?.[0] as
      | ((data: string) => void)
      | undefined;
    expect(dataHandler).toBeDefined();

    socketAt(0).sent = [];
    dataHandler?.("\x1b[<0;10;5M\x1b[<32;12;5M\x1b[<0;12;5m");

    expect(sentText(socketAt(0), 0)).toBe("\x1b[<0;10;5M\x1b[<0;12;5m");
  });

  it("sends terminal byte payloads as raw WebSocket bytes", async () => {
    await renderStarted({ workspaceId: "ws-123" });
    const dataHandler = mockOnData.mock.calls[0]?.[0] as
      | ((data: Uint8Array) => void)
      | undefined;
    expect(dataHandler).toBeDefined();

    socketAt(0).sent = [];
    dataHandler?.(new Uint8Array([0, 0xff, 0x1b]));

    const sent = socketAt(0).sent[0];
    expect(sent).toBeInstanceOf(ArrayBuffer);
    expect(Array.from(new Uint8Array(sent as ArrayBuffer))).toEqual([
      0, 0xff, 0x1b,
    ]);
  });

  it("sends terminal ArrayBuffer payloads as raw WebSocket bytes", async () => {
    await renderStarted({ workspaceId: "ws-123" });
    const dataHandler = mockOnData.mock.calls[0]?.[0] as
      | ((data: ArrayBuffer) => void)
      | undefined;
    expect(dataHandler).toBeDefined();

    socketAt(0).sent = [];
    dataHandler?.(new Uint8Array([0x80, 0x81]).buffer);

    const sent = socketAt(0).sent[0];
    expect(sent).toBeInstanceOf(ArrayBuffer);
    expect(Array.from(new Uint8Array(sent as ArrayBuffer))).toEqual([
      0x80, 0x81,
    ]);
  });

  it("sends browser multiline paste through ghostty bracketed paste handling", async () => {
    const { container } = await renderStarted({ workspaceId: "ws-123" });

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
        getData: vi.fn((type: string) =>
          type === "text/plain"
            ? "first\x1b[201~\nsecond\nthird"
            : "",
        ),
      },
    });

    const defaultAllowed = terminalContainer!.dispatchEvent(event);

    expect(defaultAllowed).toBe(false);
    expect(laterPasteListener).not.toHaveBeenCalled();
    expect(mockPaste).toHaveBeenCalledWith("first[201~\nsecond\nthird");
    expect(sentText(socketAt(0), 0)).toBe(
      "\x1b[200~first[201~\nsecond\nthird\x1b[201~",
    );
  });

  it("leaves single-line browser paste for ghostty default handling", async () => {
    const { container } = await renderStarted({ workspaceId: "ws-123" });

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
        getData: vi.fn((type: string) =>
          type === "text/plain" ? "single line" : "",
        ),
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
      websocketPath:
        "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      reconnectOnExit: false,
      initialStatus: "exited",
    });

    expect(sockets).toHaveLength(0);
    expect(terminalWrite).toHaveBeenCalledWith(
      expect.stringContaining("[Process exited]"),
    );
  });

  it("does not open a websocket when initialStatus is error", async () => {
    await renderStarted({
      websocketPath:
        "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
      reconnectOnExit: false,
      initialStatus: "error",
    });

    expect(sockets).toHaveLength(0);
    expect(terminalWrite).toHaveBeenCalledWith(
      expect.stringContaining("[Session unavailable]"),
    );
  });

  it("does not restart sessions when reconnectOnExit is false", async () => {
    const onExit = vi.fn();

    await renderStarted({
      websocketPath:
        "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
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
    expect(terminalWrite).toHaveBeenCalledWith(
      expect.stringContaining("[Process exited]"),
    );
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
