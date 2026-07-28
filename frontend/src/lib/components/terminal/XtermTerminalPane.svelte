<script lang="ts">
  import { onMount } from "svelte";
  import { getStores } from "@middleman/ui";
  import { showFlash } from "@middleman/ui/stores/flash";
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { LigaturesAddon } from "@xterm/addon-ligatures/lib/addon-ligatures.mjs";
  import { WebglAddon } from "@xterm/addon-webgl";
  import "@xterm/xterm/css/xterm.css";
  import { workspaceTmuxWebSocketPath } from "../../api/workspace-runtime.js";
  import { createWorkspaceSwitchPaneTimer } from "../../instrumentation/workspaceSwitchTiming.js";
  import { traceHeadersForRequest } from "../../instrumentation/traceContext.js";
  import {
    createTerminalPastePayload,
    isMultilinePaste,
  } from "./bracketedPaste.js";
  import { embeddedWebSocketUrl } from "./embeddedWebSocket.js";
  import { parseOsc52ClipboardWrite } from "./osc52Clipboard.js";
  import {
    createBrowserTerminalClipboardPort,
    createTerminalClipboardWriter,
  } from "./terminalClipboardWriter.js";
  import {
    buildTerminalFontFamily,
    primaryTerminalFontFamily,
  } from "./terminalFontFamily.js";
  import { createInitialFocusIntent } from "./terminal-focus.js";
  import { createTmuxMouseDragAutoscroll } from "./tmuxMouseDragAutoscroll.js";

  interface TerminalPaneProps {
    workspaceId?: string | undefined;
    websocketPath?: string | undefined;
    reconnectOnExit?: boolean | undefined;
    active?: boolean | undefined;
    disabled?: boolean;
    onExit?: ((code: number) => void) | undefined;
    // When the session is not attachable at mount time, skip the
    // WebSocket connect — the server's attach endpoint returns 404
    // for non-running sessions, which would loop scheduleReconnect.
    initialStatus?: string | undefined;
  }

  let {
    workspaceId,
    websocketPath,
    reconnectOnExit = true,
    active = true,
    disabled = false,
    onExit,
    initialStatus,
  }: TerminalPaneProps = $props();
  const { settings: settingsStore } = getStores();

  const basePath = (window.__BASE_PATH__ ?? "/").replace(/\/$/, "");

  let containerEl: HTMLDivElement;
  let terminal: Terminal | null = $state(null);
  let fitAddon: FitAddon | null = null;
  let ligaturesAddon: LigaturesAddon | null = null;
  let webglAddon: WebglAddon | null = null;
  let ws: WebSocket | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let restartTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectDelay = 1000;
  let resizeObserver: ResizeObserver | null = null;
  let refreshFrame: number | null = null;
  let resizeFrame: number | null = null;
  let appliedTerminalFontFamily = "";
  let appliedFontSize = 0;
  let appliedScrollback = 0;
  let appliedLineHeight = 0;
  let appliedLetterSpacing = 0;
  let appliedCursorBlink = true;
  let appliedFontLigatures = false;
  let disposed = false;
  let exited = false;
  let sawFirstBytes = false;
  let clipboardFailureReported = false;
  let activePointerId: number | null = null;
  const encoder = new TextEncoder();
  const clipboardWriter = createTerminalClipboardWriter(
    createBrowserTerminalClipboardPort(),
  );
  const mouseDragAutoscroll = createTmuxMouseDragAutoscroll({
    send(data) {
      if (disabled || ws?.readyState !== WebSocket.OPEN) return;
      ws.send(encoder.encode(data));
    },
  });
  // Binds this pane to the workspace switch that was live at creation;
  // panes surviving from a previous workspace record nothing.
  const switchTimer = createWorkspaceSwitchPaneTimer();
  // Captured at mount, before the async font-load race in start() below,
  // so a later focus steal is detected even though start() itself cannot
  // observe when focus moved during the wait.
  const focusIntent = createInitialFocusIntent();

  const MAX_RECONNECT_DELAY = 30000;
  const TERMINAL_SMOOTH_SCROLL_DURATION = 0;
  const TERMINAL_MINIMUM_CONTRAST_RATIO = 4.5;
  const TERMINAL_FONT_WAIT_MS = 300;
  const TERMINAL_FONT_LOAD_GLYPHS = "0MWim@#";

  function isAttachableInitialStatus(status: string | undefined): boolean {
    return status === undefined || status === "running" || status === "starting";
  }

  function reportClipboardFailure(): void {
    if (disposed || clipboardFailureReported) return;
    clipboardFailureReported = true;
    showFlash("Could not write the terminal selection to the clipboard.", {
      tone: "danger",
    });
  }

  function handleOsc52Clipboard(data: string): boolean {
    const result = parseOsc52ClipboardWrite(data);
    if (result.status === "rejected") return true;
    if (disposed || disabled || ws?.readyState !== WebSocket.OPEN) return true;

    void clipboardWriter.write(result.text).then((outcome) => {
      if (outcome === "blocked") reportClipboardFailure();
    });
    return true;
  }

  function handleTerminalPointerDown(event: PointerEvent): void {
    if (disposed || disabled || event.button !== 0 || !event.isTrusted) return;
    if (activePointerId !== null) {
      cancelTerminalPointerGesture();
    }
    activePointerId = event.pointerId;
    clipboardWriter.beginPointerGesture();
    try {
      containerEl.setPointerCapture(event.pointerId);
    } catch {
      // The watchdog and global cancellation handlers still bound the gesture.
    }
  }

  function handleTerminalPointerEnd(event: PointerEvent): void {
    if (!event.isTrusted || activePointerId !== event.pointerId) return;
    activePointerId = null;
    releaseTerminalPointerCapture(event.pointerId);
    clipboardWriter.endPointerGesture();
    mouseDragAutoscroll.endPointerGesture();
  }

  function releaseTerminalPointerCapture(pointerId: number): void {
    try {
      if (containerEl.hasPointerCapture(pointerId)) {
        containerEl.releasePointerCapture(pointerId);
      }
    } catch {
      // Capture may already be gone after focus or visibility loss.
    }
  }

  function cancelTerminalPointerGesture(pointerId?: number): void {
    if (pointerId !== undefined && activePointerId !== pointerId) return;
    const capturedPointerId = activePointerId;
    activePointerId = null;
    if (capturedPointerId !== null) {
      releaseTerminalPointerCapture(capturedPointerId);
    }
    clipboardWriter.cancelPointerGesture();
    mouseDragAutoscroll.endPointerGesture();
  }

  function handleTerminalPointerCancel(event: PointerEvent): void {
    cancelTerminalPointerGesture(event.pointerId);
  }

  function handleTerminalLostPointerCapture(event: PointerEvent): void {
    if (activePointerId !== event.pointerId) return;
    activePointerId = null;
    clipboardWriter.cancelPointerGesture();
    mouseDragAutoscroll.endPointerGesture();
  }

  function handleWindowBlur(): void {
    cancelTerminalPointerGesture();
  }

  function handleDocumentVisibilityChange(): void {
    if (document.visibilityState !== "visible") {
      cancelTerminalPointerGesture();
    }
  }

  function handleTerminalKeyDown(event: KeyboardEvent): void {
    if (disposed || disabled || event.isComposing || !event.isTrusted) return;
    clipboardWriter.authorizeKeyboardGesture();
  }

  function handleWindowPointerMove(event: PointerEvent): void {
    if (disposed || disabled || !terminal) return;
    const screen = containerEl.querySelector<HTMLElement>(".xterm-screen");
    const bounds = (screen ?? containerEl).getBoundingClientRect();
    mouseDragAutoscroll.updatePointer({
      clientX: event.clientX,
      clientY: event.clientY,
      bounds,
      cols: terminal.cols,
      rows: terminal.rows,
    });
  }

  function initialStatusMessage(status: string | undefined): string {
    return status === "exited" ? "Process exited" : "Session unavailable";
  }

  function defaultTerminalFontFamily(): string {
    const rootFontFamily = getComputedStyle(
      document.documentElement,
    )
      .getPropertyValue("--font-mono")
      .trim();
    return rootFontFamily || "monospace";
  }

  const terminalFontFamily = $derived.by(() => {
    const configured = settingsStore
      .getTerminalFontFamily()
      .trim();
    return buildTerminalFontFamily(configured, defaultTerminalFontFamily());
  });
  const terminalFontSize = $derived(settingsStore.getTerminalFontSize());
  const terminalScrollback = $derived(settingsStore.getTerminalScrollback());
  const terminalLineHeight = $derived(settingsStore.getTerminalLineHeight());
  const terminalLetterSpacing = $derived(
    settingsStore.getTerminalLetterSpacing(),
  );
  const terminalCursorBlink = $derived(
    settingsStore.getTerminalCursorBlink(),
  );
  const terminalFontLigatures = $derived(
    settingsStore.getTerminalFontLigatures(),
  );

  function defaultWebsocketPath(): string {
    if (!workspaceId) return "";
    return workspaceTmuxWebSocketPath(workspaceId);
  }

  function appendSizeParams(
    url: string,
    cols: number,
    rows: number,
  ): string {
    const sep = url.includes("?") ? "&" : "?";
    const resizeActive = active ? "1" : "0";
    const { traceparent, baggage } = traceHeadersForRequest();
    let result = `${url}${sep}cols=${cols}&rows=${rows}&resize_active=${resizeActive}&traceparent=${encodeURIComponent(traceparent)}`;
    if (baggage !== null) result += `&baggage=${encodeURIComponent(baggage)}`;
    return result;
  }

  function buildWsUrl(
    cols: number,
    rows: number,
  ): string | null {
    const path = websocketPath ?? defaultWebsocketPath();
    if (!path) return null;

    const withSize = appendSizeParams(path, cols, rows);
    if (/^wss?:\/\//.test(withSize)) {
      return withSize;
    }
    const embeddedUrl = embeddedWebSocketUrl(withBasePath(withSize));
    if (embeddedUrl) return embeddedUrl;
    const devUrl = buildDevApiWsUrl(withSize);
    if (devUrl) return devUrl;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    return `${proto}://${location.host}${withBasePath(withSize)}`;
  }

  function withBasePath(path: string): string {
    const normalizedPath = path.startsWith("/") ? path : `/${path}`;
    if (!basePath) return normalizedPath;
    if (
      normalizedPath === basePath ||
      normalizedPath.startsWith(`${basePath}/`)
    ) {
      return normalizedPath;
    }
    return `${basePath}${normalizedPath}`;
  }

  function buildDevApiWsUrl(path: string): string | null {
    if (!import.meta.env.DEV) return null;
    const apiUrl = window.__MIDDLEMAN_DEV_API_URL__?.trim();
    if (!apiUrl || !path.startsWith("/api/")) return null;

    try {
      const base = new URL(apiUrl);
      const requested = new URL(path, "http://middleman.local");
      const basePath = base.pathname.replace(/\/$/, "");
      base.protocol = base.protocol === "https:" ? "wss:" : "ws:";
      base.pathname = `${basePath}${requested.pathname}`;
      base.search = requested.search;
      base.hash = "";
      return base.toString();
    } catch {
      return null;
    }
  }

  function sendResize(cols: number, rows: number): void {
    sendControl("resize", cols, rows);
  }

  function sendRefresh(cols: number, rows: number): void {
    sendControl("refresh", cols, rows);
  }

  function sendResizeActive(nextActive: boolean): void {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "resize_active", active: nextActive }));
    }
  }

  function sendControl(
    type: "resize" | "refresh",
    cols: number,
    rows: number,
  ): void {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type, cols, rows }));
    }
  }

  function refreshVisibleTerminal(): void {
    if (!terminal) return;

    fitAddon?.fit();
    terminal.refresh(0, Math.max(0, terminal.rows - 1));
    sendRefresh(terminal.cols, terminal.rows);
  }

  function redrawTerminalTextureAtlas(): void {
    if (!terminal) return;

    terminal.clearTextureAtlas();
    terminal.refresh(0, Math.max(0, terminal.rows - 1));
  }

  function isFirefox(): boolean {
    return navigator.userAgent.toLowerCase().includes("firefox/");
  }

  function recreateWebglAddon(): void {
    if (!terminal) return;
    webglAddon?.dispose();
    webglAddon = null;
    if (isFirefox()) {
      scheduleTerminalResize();
      return;
    }
    try {
      const wgl = new WebglAddon();
      wgl.onContextLoss(() => {
        wgl.dispose();
        if (webglAddon === wgl) webglAddon = null;
        scheduleTerminalResize();
      });
      terminal.loadAddon(wgl);
      webglAddon = wgl;
      scheduleTerminalResize();
    } catch {
      // WebGL unavailable; canvas renderer used as fallback.
    }
  }

  function syncLigaturesAddon(): void {
    if (!terminal) return;
    ligaturesAddon?.dispose();
    ligaturesAddon = null;
    if (terminalFontLigatures) {
      ligaturesAddon = new LigaturesAddon();
      terminal.loadAddon(ligaturesAddon);
    }
    recreateWebglAddon();
  }

  function scheduleTerminalRefresh(): void {
    if (refreshFrame !== null) {
      cancelAnimationFrame(refreshFrame);
    }
    refreshFrame = requestAnimationFrame(() => {
      refreshFrame = null;
      refreshVisibleTerminal();
    });
  }

  function scheduleTerminalResize(): void {
    if (resizeFrame !== null) {
      cancelAnimationFrame(resizeFrame);
    }
    resizeFrame = requestAnimationFrame(() => {
      resizeFrame = null;
      resizeVisibleTerminal();
    });
  }

  function resizeVisibleTerminal(): void {
    if (!fitAddon || !terminal || !active) return;

    fitAddon.fit();
    terminal.refresh(0, Math.max(0, terminal.rows - 1));
    sendResize(terminal.cols, terminal.rows);
  }

  function handleTerminalPaste(event: ClipboardEvent): void {
    if (disabled) {
      event.preventDefault();
      event.stopImmediatePropagation();
      return;
    }
    if (ws?.readyState !== WebSocket.OPEN) return;

    const pastedText =
      event.clipboardData?.getData("text/plain") ||
      event.clipboardData?.getData("text") ||
      "";
    if (!isMultilinePaste(pastedText)) return;

    event.preventDefault();
    event.stopImmediatePropagation();
    ws.send(
      encoder.encode(
        createTerminalPastePayload(
          pastedText,
          terminal?.modes.bracketedPasteMode ?? false,
        ),
      ),
    );
  }

  function connect(): void {
    if (disposed || !terminal) return;

    mouseDragAutoscroll.reset();
    const cols = terminal.cols;
    const rows = terminal.rows;
    const url = buildWsUrl(cols, rows);
    if (!url) return;
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    ws = socket;

    socket.onopen = () => {
      switchTimer.record("socket-open");
      reconnectDelay = 1000;
      sendResizeActive(active);
      if (active) scheduleTerminalRefresh();
    };

    socket.onmessage = (ev: MessageEvent) => {
      if (!terminal) return;
      if (ev.data instanceof ArrayBuffer) {
        const bytes = new Uint8Array(ev.data);
        if (!sawFirstBytes) {
          sawFirstBytes = true;
          // first-paint must describe the same pane as first-bytes, so
          // only the pane whose first-bytes actually recorded chains
          // the paint measurement. The write callback fires once the
          // payload is parsed; the double animation frame lands after
          // the frame showing that content has painted.
          if (switchTimer.record("first-bytes", { byteLength: bytes.byteLength })) {
            terminal.write(bytes, () => {
              requestAnimationFrame(() => {
                requestAnimationFrame(() => {
                  switchTimer.record("first-paint");
                });
              });
            });
          } else {
            terminal.write(bytes);
          }
        } else {
          terminal.write(bytes);
        }
      } else if (typeof ev.data === "string") {
        try {
          const msg = JSON.parse(ev.data) as {
            type: string;
            code?: number;
          };
          if (msg.type === "exited") {
            onExit?.(msg.code ?? 0);
            exited = true;
            if (reconnectOnExit) {
              terminal.write(
                "\r\n\x1b[90m[Process exited — reconnecting...]\x1b[0m\r\n",
              );
              scheduleSessionRestart();
            } else {
              terminal.write(
                "\r\n\x1b[90m[Process exited]\x1b[0m\r\n",
              );
            }
          }
        } catch {
          // Non-JSON text frame; ignore.
        }
      }
    };

    socket.onclose = () => {
      mouseDragAutoscroll.reset();
      scheduleReconnect();
    };

    socket.onerror = () => {
      socket.close();
    };
  }

  function scheduleSessionRestart(): void {
    if (disposed) return;
    if (restartTimer) clearTimeout(restartTimer);
    restartTimer = setTimeout(() => {
      restartTimer = null;
      if (disposed) return;
      // Close stale socket so its onclose handler
      // cannot schedule a duplicate reconnect.
      if (ws) {
        ws.onclose = null;
        ws.onerror = null;
        ws.onmessage = null;
        ws.close();
        ws = null;
      }
      exited = false;
      reconnectDelay = 1000;
      connect();
    }, 2000);
  }

  function scheduleReconnect(): void {
    if (disposed || exited) return;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      reconnectDelay = Math.min(
        reconnectDelay * 2,
        MAX_RECONNECT_DELAY,
      );
      connect();
    }, reconnectDelay);
  }

  function cleanup(): void {
    disposed = true;
    clipboardWriter.dispose();
    mouseDragAutoscroll.dispose();
    if (resizeObserver) {
      resizeObserver.disconnect();
      resizeObserver = null;
    }
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    if (restartTimer !== null) {
      clearTimeout(restartTimer);
      restartTimer = null;
    }
    if (refreshFrame !== null) {
      cancelAnimationFrame(refreshFrame);
      refreshFrame = null;
    }
    if (resizeFrame !== null) {
      cancelAnimationFrame(resizeFrame);
      resizeFrame = null;
    }
    if (ws) {
      ws.onclose = null;
      ws.onerror = null;
      ws.onmessage = null;
      ws.close();
      ws = null;
    }
    containerEl?.removeEventListener("paste", handleTerminalPaste, true);
    if (terminal) {
      ligaturesAddon?.dispose();
      ligaturesAddon = null;
      webglAddon?.dispose();
      webglAddon = null;
      terminal.dispose();
      terminal = null;
    }
  }

  $effect(() => {
    if (!terminal) return;
    if (
      terminalFontFamily === appliedTerminalFontFamily &&
      terminalFontSize === appliedFontSize &&
      terminalScrollback === appliedScrollback &&
      terminalLineHeight === appliedLineHeight &&
      terminalLetterSpacing === appliedLetterSpacing &&
      terminalCursorBlink === appliedCursorBlink &&
      terminalFontLigatures === appliedFontLigatures
    ) return;
    const ligaturesChanged = terminalFontLigatures !== appliedFontLigatures;
    appliedTerminalFontFamily = terminalFontFamily;
    appliedFontSize = terminalFontSize;
    appliedScrollback = terminalScrollback;
    appliedLineHeight = terminalLineHeight;
    appliedLetterSpacing = terminalLetterSpacing;
    appliedCursorBlink = terminalCursorBlink;
    appliedFontLigatures = terminalFontLigatures;
    terminal.options.fontFamily = terminalFontFamily;
    terminal.options.fontSize = terminalFontSize;
    terminal.options.scrollback = terminalScrollback;
    terminal.options.lineHeight = terminalLineHeight;
    terminal.options.letterSpacing = terminalLetterSpacing;
    terminal.options.cursorBlink = terminalCursorBlink;
    terminal.options.disableStdin = disabled;
    if (ligaturesChanged) {
      syncLigaturesAddon();
    }
    redrawTerminalTextureAtlas();
    fitAddon?.fit();
    if (active) scheduleTerminalRefresh();
  });

  $effect(() => {
    if (!terminal) return;
    terminal.options.disableStdin = disabled;
  });

  $effect(() => {
    if (!terminal) return;
    sendResizeActive(active);
    if (active) scheduleTerminalRefresh();
  });

  onMount(() => {
    let started = false;
    let fontWaitTimedOut = false;
    let lateFontRebuilt = false;
    let fontWaitTimer: ReturnType<typeof setTimeout> | null = null;

    function start(): void {
      if (started || disposed) return;
      started = true;

      const term = new Terminal({
        theme: {
          background: "#0d1117",
          foreground: "#c9d1d9",
          cursor: "#58a6ff",
        },
        // The ligatures addon registers a character joiner, which xterm
        // exposes as proposed API. This is constructor-only and must be on
        // before a user enables ligatures at runtime.
        allowProposedApi: true,
        allowTransparency: false,
        customGlyphs: true,
        cursorBlink: terminalCursorBlink,
        drawBoldTextInBrightColors: true,
        fontFamily: terminalFontFamily,
        fontSize: terminalFontSize,
        scrollback: terminalScrollback,
        letterSpacing: terminalLetterSpacing,
        lineHeight: terminalLineHeight,
        minimumContrastRatio: TERMINAL_MINIMUM_CONTRAST_RATIO,
        rescaleOverlappingGlyphs: true,
        scrollOnEraseInDisplay: true,
        smoothScrollDuration: TERMINAL_SMOOTH_SCROLL_DURATION,
        disableStdin: disabled,
      });
      terminal = term;

      term.open(containerEl);
      term.parser.registerOscHandler(52, handleOsc52Clipboard);
      switchTimer.record("terminal-constructed");
      containerEl.addEventListener("paste", handleTerminalPaste, true);

      const fit = new FitAddon();
      fitAddon = fit;
      term.loadAddon(fit);

      if (terminalFontLigatures) {
        ligaturesAddon = new LigaturesAddon();
        term.loadAddon(ligaturesAddon);
      }
      recreateWebglAddon();

      appliedTerminalFontFamily = terminalFontFamily;
      appliedFontSize = terminalFontSize;
      appliedScrollback = terminalScrollback;
      appliedLineHeight = terminalLineHeight;
      appliedLetterSpacing = terminalLetterSpacing;
      appliedCursorBlink = terminalCursorBlink;
      appliedFontLigatures = terminalFontLigatures;
      fit.fit();

      // Focus only at terminal creation: launching/opening a session mounts a
      // fresh pane, which is the one moment focus is unambiguously intended.
      // Reveal- or enable-driven re-runs of the active effect must not steal
      // focus from controls managed by WorkspaceHost/WorkspaceDockPanel. The
      // font-load wait above is async, so focus only moves if the mount-time
      // focus context is still current and isn't a dialog/menu/input.
      if (active && !disabled && focusIntent.shouldFocus()) term.focus();

      term.onData((data: string) => {
        if (disabled) return;
        if (ws?.readyState === WebSocket.OPEN) {
          ws.send(encoder.encode(data));
          mouseDragAutoscroll.observeTerminalData(data);
        }
      });

      term.onBinary((data: string) => {
        if (disabled) return;
        if (ws?.readyState === WebSocket.OPEN) {
          const buf = new Uint8Array(data.length);
          for (let i = 0; i < data.length; i++) {
            buf[i] = data.charCodeAt(i) & 0xff;
          }
          ws.send(buf.buffer);
        }
      });

      resizeObserver = new ResizeObserver(() => {
        scheduleTerminalResize();
      });
      resizeObserver.observe(containerEl);

      if (!isAttachableInitialStatus(initialStatus)) {
        exited = true;
        term.write(
          `\r\n\x1b[90m[${initialStatusMessage(initialStatus)}]\x1b[0m\r\n`,
        );
        return;
      }
      connect();
    }

    function finishInitialFontWait(detail?: Record<string, unknown>): void {
      if (started || disposed) return;
      if (fontWaitTimer !== null) {
        clearTimeout(fontWaitTimer);
        fontWaitTimer = null;
      }
      switchTimer.record("fonts-ready", detail);
      start();
    }

    const fontSet = document.fonts;
    if (typeof fontSet?.load !== "function") {
      switchTimer.record("fonts-ready", { unsupported: true });
      start();
      return cleanup;
    }

    // Loading only the selected terminal face avoids making xterm wait
    // for unrelated page fonts. The bound keeps terminal construction
    // moving when the face is slow or unavailable; a late completion
    // then repairs the fallback metrics once, provided the pane lives.
    const fontDescriptor = `${terminalFontSize}px ${primaryTerminalFontFamily(terminalFontFamily)}`;
    let selectedFontLoad: Promise<FontFace[]>;
    try {
      selectedFontLoad = fontSet.load(fontDescriptor, TERMINAL_FONT_LOAD_GLYPHS);
    } catch {
      finishInitialFontWait({ error: true });
      return cleanup;
    }
    void selectedFontLoad.then(
      () => {
        if (!started) {
          finishInitialFontWait();
          return;
        }
        if (!fontWaitTimedOut || lateFontRebuilt || disposed || !terminal) return;

        lateFontRebuilt = true;
        terminal.clearTextureAtlas();
        fitAddon?.fit();
        terminal.refresh(0, Math.max(0, terminal.rows - 1));
        if (active) sendResize(terminal.cols, terminal.rows);
      },
      () => finishInitialFontWait({ error: true }),
    );
    fontWaitTimer = setTimeout(() => {
      fontWaitTimer = null;
      fontWaitTimedOut = true;
      finishInitialFontWait({ timedOut: true });
    }, TERMINAL_FONT_WAIT_MS);

    return () => {
      if (fontWaitTimer !== null) {
        clearTimeout(fontWaitTimer);
        fontWaitTimer = null;
      }
      cleanup();
    };
  });
</script>

<svelte:window
  onblur={handleWindowBlur}
  onpointermove={handleWindowPointerMove}
  onpointerup={handleTerminalPointerEnd}
  onpointercancel={handleTerminalPointerCancel}
/>
<svelte:document onvisibilitychange={handleDocumentVisibilityChange} />

<div
  class="terminal-container"
  bind:this={containerEl}
  onpointerdowncapture={handleTerminalPointerDown}
  onlostpointercapture={handleTerminalLostPointerCapture}
  onkeydowncapture={handleTerminalKeyDown}
></div>

<style>
  .terminal-container {
    width: 100%;
    height: 100%;
    background: var(--terminal-bg);
  }

  .terminal-container :global(.xterm),
  .terminal-container :global(.xterm-viewport),
  .terminal-container :global(.xterm-screen) {
    background: var(--terminal-bg);
  }
</style>
