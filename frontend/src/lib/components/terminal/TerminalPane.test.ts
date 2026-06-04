import { cleanup, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const {
  ghosttyTerminalCtor,
  ligaturesAddonCtor,
  mockGhosttyInit,
  mockWebglCtor,
  resizeObserverCallbacks,
  xtermFitAddons,
  xtermInstances,
  xtermOnDataHandlers,
  xtermTerminalCtor,
  xtermOpen,
} = vi.hoisted(() => ({
  ghosttyTerminalCtor: vi.fn(),
  ligaturesAddonCtor: vi.fn(),
  mockGhosttyInit: vi.fn().mockResolvedValue(undefined),
  mockWebglCtor: vi.fn(),
  resizeObserverCallbacks: [] as ResizeObserverCallback[],
  xtermFitAddons: [] as Array<{ fit: ReturnType<typeof vi.fn> }>,
  xtermInstances: [] as Array<{
    clearTextureAtlas: ReturnType<typeof vi.fn>;
    cols: number;
    modes: { bracketedPasteMode: boolean };
    refresh: ReturnType<typeof vi.fn>;
    rows: number;
    write: ReturnType<typeof vi.fn>;
  }>,
  xtermOnDataHandlers: [] as Array<(data: string) => void>,
  xtermTerminalCtor: vi.fn(),
  xtermOpen: vi.fn(),
}));

let configuredRenderer: "xterm" | "ghostty-web" = "xterm";
let configuredFontFamily = "";
let configuredFontSize = 14;
let configuredScrollback = 1000;
let configuredLineHeight = 1;
let configuredLetterSpacing = 0;
let configuredCursorBlink = true;
let configuredFontLigatures = false;
let mockSockets: MockWebSocket[] = [];

class MockWebSocket {
  static OPEN = 1;
  readyState = 1;
  binaryType = "arraybuffer";
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  sent: Array<string | ArrayBuffer | ArrayBufferView> = [];

  constructor(public url: string) {
    mockSockets.push(this);
  }
  send(data: string | ArrayBuffer | ArrayBufferView): void {
    this.sent.push(data);
  }
  close(): void {}
}

vi.mock("@middleman/ui", () => ({
  getStores: () => ({
    settings: {
      getTerminalFontFamily: () => configuredFontFamily,
      getTerminalFontSize: () => configuredFontSize,
      getTerminalScrollback: () => configuredScrollback,
      getTerminalLineHeight: () => configuredLineHeight,
      getTerminalLetterSpacing: () => configuredLetterSpacing,
      getTerminalCursorBlink: () => configuredCursorBlink,
      getTerminalFontLigatures: () => configuredFontLigatures,
      getTerminalRenderer: () => configuredRenderer,
    },
  }),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: vi.fn().mockImplementation((options) => {
    xtermTerminalCtor(options);
    const terminal = {
      cols: 80,
      rows: 24,
      modes: { bracketedPasteMode: false },
      options: { ...options },
      clearTextureAtlas: vi.fn(),
      dispose: vi.fn(),
      loadAddon: vi.fn(),
      onBinary: vi.fn(),
      onData: vi.fn((handler: (data: string) => void) => {
        xtermOnDataHandlers.push(handler);
        return { dispose: vi.fn() };
      }),
      open: xtermOpen,
      refresh: vi.fn(),
      write: vi.fn(),
    };
    xtermInstances.push(terminal);
    return terminal;
  }),
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: vi.fn().mockImplementation(() => {
    const addon = { fit: vi.fn() };
    xtermFitAddons.push(addon);
    return addon;
  }),
}));

vi.mock("@xterm/addon-ligatures/lib/addon-ligatures.mjs", () => ({
  LigaturesAddon: vi.fn().mockImplementation(() => {
    ligaturesAddonCtor();
    return { dispose: vi.fn() };
  }),
}));

vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: vi.fn().mockImplementation((options) => {
    mockWebglCtor(options);
    return {
      dispose: vi.fn(),
      onContextLoss: vi.fn(),
    };
  }),
}));

vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

vi.mock("ghostty-web", () => ({
  init: (...args: []) => mockGhosttyInit(...args),
  FitAddon: vi.fn().mockImplementation(() => ({
    fit: vi.fn(),
  })),
  Terminal: vi.fn().mockImplementation((options) => {
    ghosttyTerminalCtor(options);
    return {
      cols: 80,
      rows: 24,
      options: { ...options },
      dispose: vi.fn(),
      loadAddon: vi.fn(),
      onData: vi.fn(),
      open: vi.fn(),
      write: vi.fn(),
    };
  }),
}));

import TerminalPane from "./TerminalPane.svelte";

describe("TerminalPane", () => {
  beforeEach(() => {
    configuredRenderer = "xterm";
    configuredFontFamily = "";
    configuredFontSize = 14;
    configuredScrollback = 1000;
    configuredLineHeight = 1;
    configuredLetterSpacing = 0;
    configuredCursorBlink = true;
    configuredFontLigatures = false;
    ghosttyTerminalCtor.mockReset();
    ligaturesAddonCtor.mockReset();
    mockGhosttyInit.mockClear();
    mockWebglCtor.mockReset();
    resizeObserverCallbacks.length = 0;
    xtermFitAddons.length = 0;
    xtermInstances.length = 0;
    xtermTerminalCtor.mockReset();
    xtermOpen.mockReset();
    xtermOnDataHandlers.length = 0;
    mockSockets = [];

    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: ResizeObserverCallback) {
          resizeObserverCallbacks.push(callback);
        }
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

  it("uses xterm.js by default", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());

    expect(ghosttyTerminalCtor).not.toHaveBeenCalled();
    expect(mockGhosttyInit).not.toHaveBeenCalled();
  });

  it("matches VS Code's stable xterm rendering defaults", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());

    expect(xtermTerminalCtor).toHaveBeenCalledWith(
      expect.objectContaining({
        allowProposedApi: true,
        allowTransparency: false,
        customGlyphs: true,
        cursorBlink: true,
        fontSize: 14,
        scrollback: 1000,
        letterSpacing: 0,
        lineHeight: 1,
        minimumContrastRatio: 4.5,
        rescaleOverlappingGlyphs: true,
        scrollOnEraseInDisplay: true,
        smoothScrollDuration: 0,
      }),
    );
    expect(mockWebglCtor).toHaveBeenCalledWith(undefined);
  });

  it("uses configured terminal metrics for xterm.js", async () => {
    configuredFontSize = 17;
    configuredScrollback = 5000;
    configuredLineHeight = 1.2;
    configuredLetterSpacing = 1;
    configuredCursorBlink = false;

    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());

    expect(xtermTerminalCtor).toHaveBeenCalledWith(
      expect.objectContaining({
        cursorBlink: false,
        fontSize: 17,
        scrollback: 5000,
        lineHeight: 1.2,
        letterSpacing: 1,
      }),
    );
  });

  it("loads the ligatures addon for xterm.js when enabled", async () => {
    configuredFontLigatures = true;

    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());

    expect(ligaturesAddonCtor).toHaveBeenCalledTimes(1);
  });

  it("does not rebuild the WebGL atlas during initial mount refresh", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermInstances).toHaveLength(1));

    expect(xtermInstances[0]!.clearTextureAtlas).not.toHaveBeenCalled();
  });

  it("repaints after container resize without rebuilding the WebGL atlas", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(resizeObserverCallbacks).toHaveLength(1));
    const terminal = xtermInstances[0]!;
    const fitAddon = xtermFitAddons[0]!;
    terminal.clearTextureAtlas.mockClear();
    terminal.refresh.mockClear();
    fitAddon.fit.mockClear();
    mockSockets[0]!.sent = [];

    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(fitAddon.fit).toHaveBeenCalled();
    expect(terminal.clearTextureAtlas).not.toHaveBeenCalled();
    expect(terminal.refresh).toHaveBeenCalledWith(0, 23);
    expect(mockSockets[0]!.sent).toContain(
      JSON.stringify({ type: "resize", cols: 80, rows: 24 }),
    );
  });

  it("uses ghostty-web when selected", async () => {
    configuredRenderer = "ghostty-web";

    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(ghosttyTerminalCtor).toHaveBeenCalled());

    expect(xtermTerminalCtor).not.toHaveBeenCalled();
    expect(mockGhosttyInit).toHaveBeenCalledTimes(1);
  });

  it("filters tiny tmux mouse drags before sending terminal input", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermOnDataHandlers).toHaveLength(1));
    expect(mockSockets).toHaveLength(1);

    xtermOnDataHandlers[0]!("\x1b[<0;10;5M\x1b[<32;12;5M\x1b[<0;12;5m");

    expect(sentText(mockSockets[0]!, mockSockets[0]!.sent.length - 1)).toBe(
      "\x1b[<0;10;5M\x1b[<0;12;5m",
    );
  });

  it("does not update drag filter state while disconnected", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermOnDataHandlers).toHaveLength(1));
    const socket = mockSockets[0]!;
    socket.readyState = 0;
    socket.sent = [];

    xtermOnDataHandlers[0]!("\x1b[<0;10;5M");
    socket.readyState = MockWebSocket.OPEN;
    xtermOnDataHandlers[0]!("\x1b[<32;12;5M");

    expect(sentText(socket, 0)).toBe("\x1b[<32;12;5M");
  });

  it("does not attach xterm sessions with unavailable initial status", async () => {
    render(TerminalPane, {
      props: {
        websocketPath:
          "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
        reconnectOnExit: false,
        initialStatus: "error",
      },
    });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());

    expect(mockSockets).toHaveLength(0);
    expect(xtermInstances[0]!.write).toHaveBeenCalledWith(
      expect.stringContaining("[Session unavailable]"),
    );
  });

  it("sends browser multiline paste as one bracketed paste payload", async () => {
    const { container } = render(TerminalPane, {
      props: { workspaceId: "ws-123" },
    });

    await waitFor(() => expect(xtermOnDataHandlers).toHaveLength(1));
    xtermInstances[0]!.modes.bracketedPasteMode = true;
    mockSockets[0]!.sent = [];
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
    expect(sentText(mockSockets[0]!, 0)).toBe(
      "\x1b[200~first[201~\nsecond\nthird\x1b[201~",
    );
  });

  it("sends browser multiline paste raw when bracketed paste is disabled", async () => {
    const { container } = render(TerminalPane, {
      props: { workspaceId: "ws-123" },
    });

    await waitFor(() => expect(xtermOnDataHandlers).toHaveLength(1));
    mockSockets[0]!.sent = [];
    const terminalContainer = container.querySelector(".terminal-container");
    expect(terminalContainer).toBeDefined();

    const event = new Event("paste", {
      bubbles: true,
      cancelable: true,
    }) as ClipboardEvent;
    Object.defineProperty(event, "clipboardData", {
      value: {
        getData: vi.fn((type: string) =>
          type === "text/plain" ? "first\nsecond\nthird" : "",
        ),
      },
    });

    const defaultAllowed = terminalContainer!.dispatchEvent(event);

    expect(defaultAllowed).toBe(false);
    expect(sentText(mockSockets[0]!, 0)).toBe("first\nsecond\nthird");
  });

  it("leaves single-line browser paste for xterm.js default handling", async () => {
    const { container } = render(TerminalPane, {
      props: { workspaceId: "ws-123" },
    });

    await waitFor(() => expect(xtermOnDataHandlers).toHaveLength(1));
    mockSockets[0]!.sent = [];
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
    expect(mockSockets[0]!.sent).toHaveLength(0);
  });
});

function sentText(socket: MockWebSocket, index: number): string {
  const value = socket.sent[index];
  if (typeof value === "string") return value;
  if (value instanceof ArrayBuffer) {
    return new TextDecoder().decode(value);
  }
  return new TextDecoder().decode(value);
}
