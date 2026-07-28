import { cleanup, render, waitFor } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const {
  clipboardAddon,
  clipboardAddonCtor,
  ligaturesAddonCtor,
  mockWebglCtor,
  resizeObserverCallbacks,
  xtermFitAddons,
  xtermInstances,
  xtermOnDataHandlers,
  xtermTerminalCtor,
  xtermOpen,
} = vi.hoisted(() => ({
  clipboardAddon: { dispose: vi.fn() },
  clipboardAddonCtor: vi.fn(),
  ligaturesAddonCtor: vi.fn(),
  mockWebglCtor: vi.fn(),
  resizeObserverCallbacks: [] as ResizeObserverCallback[],
  xtermFitAddons: [] as Array<{ fit: ReturnType<typeof vi.fn>; proposeDimensions: ReturnType<typeof vi.fn> }>,
  xtermInstances: [] as Array<{
    clearTextureAtlas: ReturnType<typeof vi.fn>;
    cols: number;
    focus: ReturnType<typeof vi.fn>;
    loadAddon: ReturnType<typeof vi.fn>;
    modes: { bracketedPasteMode: boolean };
    refresh: ReturnType<typeof vi.fn>;
    rows: number;
    write: ReturnType<typeof vi.fn>;
  }>,
  xtermOnDataHandlers: [] as Array<(data: string) => void>,
  xtermTerminalCtor: vi.fn(),
  xtermOpen: vi.fn(),
}));

let configuredFontFamily = "";
let configuredFontSize = 14;
let configuredScrollback = 1000;
let configuredLineHeight = 1;
let configuredLetterSpacing = 0;
let configuredCursorBlink = true;
let configuredFontLigatures = false;
let mockSockets: MockWebSocket[] = [];
// What the fit addon measures the region as. undefined models a container with
// no content box (a parked terminal), for which the real addon proposes nothing.
let fitDimensions: { cols: number; rows: number } | undefined = { cols: 80, rows: 24 };
const originalDocumentFonts = Object.getOwnPropertyDescriptor(document, "fonts");

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve;
  });
  return { promise, resolve };
}

function stubFontLoad(promise: Promise<FontFace[]>): ReturnType<typeof vi.fn> {
  const load = vi.fn().mockReturnValue(promise);
  Object.defineProperty(document, "fonts", {
    configurable: true,
    value: {
      load,
      ready: new Promise<FontFaceSet>(() => undefined),
    },
  });
  return load;
}

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
    },
  }),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: vi.fn().mockImplementation(function (options) {
    xtermTerminalCtor(options);
    const terminal = {
      cols: 80,
      rows: 24,
      modes: { bracketedPasteMode: false },
      options: { ...options },
      clearTextureAtlas: vi.fn(),
      dispose: vi.fn(),
      focus: vi.fn(),
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
  FitAddon: vi.fn().mockImplementation(function () {
    // proposeDimensions is the pane's measurement of its own region: the real
    // addon returns undefined, or a zero, for a container with no content box
    // (a parked terminal), and the pane only pushes a size when it gets one.
    const addon = { fit: vi.fn(), proposeDimensions: vi.fn(() => fitDimensions) };
    xtermFitAddons.push(addon);
    return addon;
  }),
}));

vi.mock("@xterm/addon-clipboard", () => ({
  ClipboardAddon: vi.fn().mockImplementation(function () {
    clipboardAddonCtor();
    return clipboardAddon;
  }),
}));

vi.mock("@xterm/addon-ligatures/lib/addon-ligatures.mjs", () => ({
  LigaturesAddon: vi.fn().mockImplementation(function () {
    ligaturesAddonCtor();
    return { dispose: vi.fn() };
  }),
}));

vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: vi.fn().mockImplementation(function (options) {
    mockWebglCtor(options);
    return {
      dispose: vi.fn(),
      onContextLoss: vi.fn(),
    };
  }),
}));

vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

import TerminalPane from "./TerminalPane.svelte";

function resizeFramesOf(socket: MockWebSocket): string[] {
  return socket.sent.map(String).filter((frame) => frame.includes('"type":"resize"'));
}

describe("TerminalPane", () => {
  beforeEach(() => {
    configuredFontFamily = "";
    configuredFontSize = 14;
    configuredScrollback = 1000;
    configuredLineHeight = 1;
    configuredLetterSpacing = 0;
    configuredCursorBlink = true;
    configuredFontLigatures = false;
    fitDimensions = { cols: 80, rows: 24 };
    clipboardAddon.dispose.mockReset();
    clipboardAddonCtor.mockReset();
    ligaturesAddonCtor.mockReset();
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
    vi.useRealTimers();
    vi.unstubAllGlobals();
    if (originalDocumentFonts) {
      Object.defineProperty(document, "fonts", originalDocumentFonts);
    } else {
      Reflect.deleteProperty(document, "fonts");
    }
  });

  it("uses xterm.js", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());
  });

  it("loads the clipboard addon into xterm", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());

    expect(clipboardAddonCtor).toHaveBeenCalledTimes(1);
    expect(xtermInstances[0]!.loadAddon).toHaveBeenCalledWith(clipboardAddon);
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

  it("constructs xterm when the selected font resolves", async () => {
    configuredFontFamily = '"MesloLGS NF", monospace';
    const fontLoad = deferred<FontFace[]>();
    const load = stubFontLoad(fontLoad.promise);

    render(TerminalPane, { props: { workspaceId: "ws-123" } });
    await tick();

    expect(load).toHaveBeenCalledWith('14px "MesloLGS NF"', "0MWim@#");
    expect(xtermTerminalCtor).not.toHaveBeenCalled();

    fontLoad.resolve([]);
    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalledTimes(1));
  });

  it("constructs xterm after the selected-font wait reaches 300 ms", async () => {
    vi.useFakeTimers();
    const fontLoad = deferred<FontFace[]>();
    stubFontLoad(fontLoad.promise);

    render(TerminalPane, { props: { workspaceId: "ws-123" } });
    await tick();
    await vi.advanceTimersByTimeAsync(299);

    expect(xtermTerminalCtor).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);

    expect(xtermTerminalCtor).toHaveBeenCalledTimes(1);
  });

  it("constructs xterm when the selected font descriptor is rejected synchronously", async () => {
    Object.defineProperty(document, "fonts", {
      configurable: true,
      value: {
        load: vi.fn(() => {
          throw new DOMException("Invalid font shorthand", "SyntaxError");
        }),
        ready: new Promise<FontFaceSet>(() => undefined),
      },
    });

    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalledTimes(1));
  });

  it("constructs xterm when the selected font load rejects asynchronously", async () => {
    stubFontLoad(Promise.reject(new DOMException("Font load failed", "NetworkError")));

    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalledTimes(1));
  });

  it("rebuilds the xterm atlas once when the selected font resolves after the bound", async () => {
    vi.useFakeTimers();
    const fontLoad = deferred<FontFace[]>();
    stubFontLoad(fontLoad.promise);

    render(TerminalPane, { props: { workspaceId: "ws-123" } });
    await tick();
    await vi.advanceTimersByTimeAsync(300);

    const terminal = xtermInstances[0]!;
    const fitAddon = xtermFitAddons[0]!;
    terminal.clearTextureAtlas.mockClear();
    terminal.refresh.mockClear();
    fitAddon.fit.mockClear();
    mockSockets[0]!.sent = [];

    fontLoad.resolve([]);
    await vi.advanceTimersByTimeAsync(0);

    expect(terminal.clearTextureAtlas).toHaveBeenCalledTimes(1);
    expect(fitAddon.fit).toHaveBeenCalledTimes(1);
    expect(terminal.refresh).toHaveBeenCalledTimes(1);
    expect(mockSockets[0]!.sent).toContain(JSON.stringify({ type: "resize", cols: 80, rows: 24 }));
  });

  it("pushes the re-measured size when a font resolves late in a painted pane", async () => {
    vi.useFakeTimers();
    const fontLoad = deferred<FontFace[]>();
    stubFontLoad(fontLoad.promise);

    render(TerminalPane, { props: { workspaceId: "ws-123", active: true } });
    await tick();
    await vi.advanceTimersByTimeAsync(300);

    const terminal = xtermInstances[0]!;
    const fitAddon = xtermFitAddons[0]!;
    terminal.clearTextureAtlas.mockClear();
    fitAddon.fit.mockClear();
    mockSockets[0]!.sent = [];
    // Different metrics, so the region works out to a different size.
    fitDimensions = { cols: 70, rows: 20 };

    fontLoad.resolve([]);
    await vi.advanceTimersByTimeAsync(0);

    expect(terminal.clearTextureAtlas).toHaveBeenCalledTimes(1);
    expect(fitAddon.fit).toHaveBeenCalledTimes(1);
    expect(resizeFramesOf(mockSockets[0]!)).toEqual([JSON.stringify({ type: "resize", cols: 70, rows: 20 })]);
  });

  it("does not rebuild a disposed xterm when the selected font resolves late", async () => {
    vi.useFakeTimers();
    const fontLoad = deferred<FontFace[]>();
    stubFontLoad(fontLoad.promise);

    const { unmount } = render(TerminalPane, { props: { workspaceId: "ws-123" } });
    await tick();
    await vi.advanceTimersByTimeAsync(300);

    const terminal = xtermInstances[0]!;
    const fitAddon = xtermFitAddons[0]!;
    terminal.clearTextureAtlas.mockClear();
    terminal.refresh.mockClear();
    fitAddon.fit.mockClear();
    unmount();

    fontLoad.resolve([]);
    await vi.advanceTimersByTimeAsync(0);

    expect(terminal.clearTextureAtlas).not.toHaveBeenCalled();
    expect(fitAddon.fit).not.toHaveBeenCalled();
    expect(terminal.refresh).not.toHaveBeenCalled();
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

  it("claims resize authority for a painted measurable region", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123", active: true } });

    await waitFor(() => expect(mockSockets).toHaveLength(1));
    expect(mockSockets[0]!.url).toContain("resize_active=1");

    mockSockets[0]!.onopen?.();
    mockSockets[0]!.sent = [];
    fitDimensions = { cols: 100, rows: 40 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(resizeFramesOf(mockSockets[0]!)).toEqual([JSON.stringify({ type: "resize", cols: 100, rows: 40 })]);
  });

  it("does not claim resize authority for a hidden but measurable region", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123", active: false } });

    await waitFor(() => expect(mockSockets).toHaveLength(1));
    expect(mockSockets[0]!.url).toContain("resize_active=0");

    mockSockets[0]!.onopen?.();
    expect(mockSockets[0]!.sent).toContain(JSON.stringify({ type: "resize_active", active: false }));

    mockSockets[0]!.sent = [];
    fitDimensions = { cols: 100, rows: 40 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(mockSockets[0]!.sent).toHaveLength(0);
  });

  it("revokes authority and ignores later measurements when its tab becomes hidden", async () => {
    const { rerender } = render(TerminalPane, {
      props: { workspaceId: "ws-123", active: true },
    });
    await waitFor(() => expect(mockSockets).toHaveLength(1));
    mockSockets[0]!.onopen?.();
    mockSockets[0]!.sent = [];

    await rerender({ workspaceId: "ws-123", active: false });
    expect(mockSockets[0]!.sent).toContain(JSON.stringify({ type: "resize_active", active: false }));

    mockSockets[0]!.sent = [];
    fitDimensions = { cols: 100, rows: 40 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(mockSockets[0]!.sent).toHaveLength(0);
  });

  it("neither claims authority nor pushes a size for an unmeasurable region", async () => {
    // A parked terminal sits in a display:none node: the fit addon proposes
    // nothing for it, and measuring it anyway is what used to resize a live
    // tmux pane to one row.
    fitDimensions = undefined;
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(mockSockets).toHaveLength(1));
    expect(mockSockets[0]!.url).toContain("resize_active=0");

    mockSockets[0]!.onopen?.();
    mockSockets[0]!.sent = [];
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(resizeFramesOf(mockSockets[0]!)).toHaveLength(0);
  });

  it("claims authority before resizing when an active region gains geometry", async () => {
    fitDimensions = undefined;
    render(TerminalPane, { props: { workspaceId: "ws-123", active: true } });

    await waitFor(() => expect(mockSockets).toHaveLength(1));
    expect(mockSockets[0]!.url).toContain("resize_active=0");
    mockSockets[0]!.onopen?.();
    mockSockets[0]!.sent = [];

    fitDimensions = { cols: 100, rows: 40 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(mockSockets[0]!.sent.map(String)).toEqual([
      JSON.stringify({ type: "resize_active", active: true }),
      JSON.stringify({ type: "resize", cols: 100, rows: 40 }),
    ]);
  });

  it("revokes and reclaims authority as active region geometry changes", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123", active: true } });

    await waitFor(() => expect(mockSockets).toHaveLength(1));
    mockSockets[0]!.onopen?.();
    mockSockets[0]!.sent = [];

    fitDimensions = undefined;
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    fitDimensions = { cols: 80, rows: 24 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(mockSockets[0]!.sent.map(String)).toEqual([
      JSON.stringify({ type: "resize_active", active: false }),
      JSON.stringify({ type: "resize_active", active: true }),
      JSON.stringify({ type: "resize", cols: 80, rows: 24 }),
    ]);
  });

  it("sends nothing more for a burst that measures the same size", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(resizeObserverCallbacks).toHaveLength(1));
    mockSockets[0]!.onopen?.();

    mockSockets[0]!.sent = [];
    fitDimensions = { cols: 120, rows: 50 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(resizeFramesOf(mockSockets[0]!)).toEqual([JSON.stringify({ type: "resize", cols: 120, rows: 50 })]);
  });

  it("sends a size measured before socket open once the connection opens", async () => {
    // The first measurement lands before the socket opens. Recording it as sent
    // anyway would let the dedupe suppress it forever, leaving the PTY at the
    // size it launched with.
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(resizeObserverCallbacks).toHaveLength(1));
    mockSockets[0]!.readyState = 0;
    fitDimensions = { cols: 90, rows: 30 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);
    expect(resizeFramesOf(mockSockets[0]!)).toHaveLength(0);

    mockSockets[0]!.readyState = 1;
    mockSockets[0]!.onopen?.();

    expect(mockSockets[0]!.sent).toContain(JSON.stringify({ type: "refresh", cols: 90, rows: 30 }));
  });

  it("focuses the xterm terminal once it initializes while active", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermInstances.length).toBe(1));
    expect(xtermInstances[0]!.focus).toHaveBeenCalled();
  });

  it("does not steal focus when an existing terminal becomes active", async () => {
    const { rerender } = render(TerminalPane, {
      props: { workspaceId: "ws-123", active: false },
    });

    await waitFor(() => expect(xtermInstances.length).toBe(1));
    expect(xtermInstances[0]!.focus).not.toHaveBeenCalled();

    await rerender({ workspaceId: "ws-123", active: true });
    await tick();
    expect(xtermInstances[0]!.focus).not.toHaveBeenCalled();
  });

  it("does not focus a disabled terminal", async () => {
    render(TerminalPane, {
      props: { workspaceId: "ws-123", active: true, disabled: true },
    });

    await waitFor(() => expect(xtermInstances.length).toBe(1));
    expect(xtermInstances[0]!.focus).not.toHaveBeenCalled();
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

    fitDimensions = { cols: 80, rows: 24 };
    resizeObserverCallbacks[0]!([], {} as ResizeObserver);

    expect(fitAddon.fit).toHaveBeenCalled();
    expect(terminal.clearTextureAtlas).not.toHaveBeenCalled();
    expect(terminal.refresh).toHaveBeenCalledWith(0, 23);
  });

  it("forwards tmux mouse input unchanged", async () => {
    render(TerminalPane, { props: { workspaceId: "ws-123" } });

    await waitFor(() => expect(xtermOnDataHandlers).toHaveLength(1));
    expect(mockSockets).toHaveLength(1);

    const input = "\x1b[<0;10;5M\x1b[<32;12;5M\x1b[<0;12;5m";
    xtermOnDataHandlers[0]!(input);

    expect(sentText(mockSockets[0]!, mockSockets[0]!.sent.length - 1)).toBe(input);
  });

  it("does not replay input received while disconnected", async () => {
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
        websocketPath: "/api/v1/workspaces/ws-123/runtime/sessions/ws-123%3Ahelper/terminal",
        reconnectOnExit: false,
        initialStatus: "error",
      },
    });

    await waitFor(() => expect(xtermTerminalCtor).toHaveBeenCalled());

    expect(mockSockets).toHaveLength(0);
    expect(xtermInstances[0]!.write).toHaveBeenCalledWith(expect.stringContaining("[Session unavailable]"));
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
        getData: vi.fn((type: string) => (type === "text/plain" ? "first\x1b[201~\nsecond\nthird" : "")),
      },
    });

    const defaultAllowed = terminalContainer!.dispatchEvent(event);

    expect(defaultAllowed).toBe(false);
    expect(laterPasteListener).not.toHaveBeenCalled();
    expect(sentText(mockSockets[0]!, 0)).toBe("\x1b[200~first[201~\nsecond\nthird\x1b[201~");
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
        getData: vi.fn((type: string) => (type === "text/plain" ? "first\nsecond\nthird" : "")),
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
        getData: vi.fn((type: string) => (type === "text/plain" ? "single line" : "")),
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
