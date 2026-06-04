<script lang="ts">
  import { onMount } from "svelte";
  import { getStores } from "@middleman/ui";
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { LigaturesAddon } from "@xterm/addon-ligatures/lib/addon-ligatures.mjs";
  import { WebglAddon } from "@xterm/addon-webgl";
  import "@xterm/xterm/css/xterm.css";
  import { workspaceTmuxWebSocketPath } from "../../api/workspace-runtime.js";
  import {
    createTerminalPastePayload,
    isMultilinePaste,
  } from "./bracketedPaste.js";
  import { buildTerminalFontFamily } from "./terminalFontFamily.js";
  import { createTmuxMouseDragFilter } from "./tmuxMouseDragFilter.js";

  interface TerminalPaneProps {
    workspaceId?: string;
    websocketPath?: string;
    reconnectOnExit?: boolean;
    active?: boolean;
    onExit?: (code: number) => void;
    // When the session is not attachable at mount time, skip the
    // WebSocket connect — the server's attach endpoint returns 404
    // for non-running sessions, which would loop scheduleReconnect.
    initialStatus?: string;
  }

  const {
    workspaceId,
    websocketPath,
    reconnectOnExit = true,
    active = true,
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
  const encoder = new TextEncoder();
  const tmuxMouseDragFilter = createTmuxMouseDragFilter();

  const MAX_RECONNECT_DELAY = 30000;
  const TERMINAL_SMOOTH_SCROLL_DURATION = 0;
  const TERMINAL_MINIMUM_CONTRAST_RATIO = 4.5;

  function isAttachableInitialStatus(status: string | undefined): boolean {
    return status === undefined || status === "running" || status === "starting";
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
    return `${url}${sep}cols=${cols}&rows=${rows}`;
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

  function recreateWebglAddon(): void {
    if (!terminal) return;
    webglAddon?.dispose();
    webglAddon = null;
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
    if (!fitAddon || !terminal) return;

    fitAddon.fit();
    terminal.refresh(0, Math.max(0, terminal.rows - 1));
    sendResize(terminal.cols, terminal.rows);
  }

  function handleTerminalPaste(event: ClipboardEvent): void {
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

    const cols = terminal.cols;
    const rows = terminal.rows;
    const url = buildWsUrl(cols, rows);
    if (!url) return;
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    ws = socket;

    socket.onopen = () => {
      reconnectDelay = 1000;
      if (active) scheduleTerminalRefresh();
    };

    socket.onmessage = (ev: MessageEvent) => {
      if (!terminal) return;
      if (ev.data instanceof ArrayBuffer) {
        terminal.write(new Uint8Array(ev.data));
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
    if (ligaturesChanged) {
      syncLigaturesAddon();
    }
    redrawTerminalTextureAtlas();
    fitAddon?.fit();
  });

  $effect(() => {
    if (!terminal || !active) return;
    scheduleTerminalRefresh();
  });

  onMount(() => {
    let started = false;

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
      });
      terminal = term;

      term.open(containerEl);
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

      term.onData((data: string) => {
        if (ws?.readyState !== WebSocket.OPEN) return;

        const filteredData = tmuxMouseDragFilter.filter(data);
        if (filteredData) {
          ws.send(encoder.encode(filteredData));
        }
      });

      term.onBinary((data: string) => {
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

    // Custom fonts (JetBrains Mono, etc.) may still be loading when
    // the pane mounts. Initializing xterm before fonts settle locks
    // in fallback-font cell metrics, so the WebGL atlas and the
    // measured cols/rows drift away from what gets painted — which
    // looks like cursor/prompt overlap in the running shell.
    const fontsReady = document.fonts?.ready;
    if (fontsReady) {
      void fontsReady.then(start);
    } else {
      start();
    }

    return cleanup;
  });
</script>

<div class="terminal-container" bind:this={containerEl}></div>

<style>
  .terminal-container {
    width: 100%;
    height: 100%;
  }
</style>
