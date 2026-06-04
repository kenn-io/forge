<script module lang="ts">
  import { init as initGhostty } from "ghostty-web";

  let ghosttyInitPromise: Promise<void> | null = null;

  function ensureGhosttyInitialized(): Promise<void> {
    ghosttyInitPromise ??= initGhostty();
    return ghosttyInitPromise;
  }
</script>

<script lang="ts">
  import { onMount } from "svelte";
  import { getStores } from "@middleman/ui";
  import { FitAddon, Terminal } from "ghostty-web";
  import { workspaceTmuxWebSocketPath } from "../../api/workspace-runtime.js";
  import {
    isMultilinePaste,
    sanitizeTerminalPasteText,
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
  let ws: WebSocket | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let restartTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectDelay = 1000;
  let resizeObserver: ResizeObserver | null = null;
  let refreshFrame: number | null = null;
  let disposed = false;
  let exited = false;
  const encoder = new TextEncoder();
  const tmuxMouseDragFilter = createTmuxMouseDragFilter();

  type TerminalInputData = string | ArrayBuffer | ArrayBufferView;

  const MAX_RECONNECT_DELAY = 30000;

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
  const terminalCursorBlink = $derived(
    settingsStore.getTerminalCursorBlink(),
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

  function sendTerminalInput(data: TerminalInputData): void {
    if (ws?.readyState !== WebSocket.OPEN) return;

    if (typeof data === "string") {
      const filteredData = tmuxMouseDragFilter.filter(data);
      if (filteredData) {
        ws.send(encoder.encode(filteredData));
      }
      return;
    }
    if (data instanceof ArrayBuffer) {
      ws.send(data);
      return;
    }
    ws.send(arrayBufferFromView(data));
  }

  function arrayBufferFromView(view: ArrayBufferView): ArrayBuffer {
    const bytes = new Uint8Array(
      view.buffer,
      view.byteOffset,
      view.byteLength,
    );
    return bytes.slice().buffer;
  }

  function handleTerminalPaste(event: ClipboardEvent): void {
    if (ws?.readyState !== WebSocket.OPEN || !terminal) return;

    const pastedText =
      event.clipboardData?.getData("text/plain") ||
      event.clipboardData?.getData("text") ||
      "";
    if (!isMultilinePaste(pastedText)) return;

    event.preventDefault();
    event.stopImmediatePropagation();
    terminal.paste(sanitizeTerminalPasteText(pastedText));
  }

  function refreshVisibleTerminal(): void {
    if (!terminal) return;

    fitAddon?.fit();
    sendRefresh(terminal.cols, terminal.rows);
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
    if (ws) {
      ws.onclose = null;
      ws.onerror = null;
      ws.onmessage = null;
      ws.close();
      ws = null;
    }
    containerEl?.removeEventListener("paste", handleTerminalPaste, true);
    if (terminal) {
      terminal.dispose();
      terminal = null;
    }
  }

  $effect(() => {
    if (!terminal) return;
    terminal.options.fontFamily = terminalFontFamily;
    terminal.options.fontSize = terminalFontSize;
    terminal.options.scrollback = terminalScrollback;
    terminal.options.cursorBlink = terminalCursorBlink;
    fitAddon?.fit();
  });

  $effect(() => {
    if (!terminal || !active) return;
    scheduleTerminalRefresh();
  });

  onMount(() => {
    let started = false;

    async function start(): Promise<void> {
      if (started || disposed) return;
      started = true;

      await ensureGhosttyInitialized();
      if (disposed) return;

      const term = new Terminal({
        theme: {
          background: "#0d1117",
          foreground: "#c9d1d9",
          cursor: "#58a6ff",
        },
        cursorBlink: terminalCursorBlink,
        fontFamily: terminalFontFamily,
        fontSize: terminalFontSize,
        scrollback: terminalScrollback,
      });
      terminal = term;

      term.open(containerEl);
      containerEl.addEventListener("paste", handleTerminalPaste, true);

      const fit = new FitAddon();
      fitAddon = fit;
      term.loadAddon(fit);

      fit.fit();

      term.onData((data: string) => {
        sendTerminalInput(data as TerminalInputData);
      });

      resizeObserver = new ResizeObserver(() => {
        if (!fitAddon || !terminal) return;
        fitAddon.fit();
        sendResize(terminal.cols, terminal.rows);
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

    // Custom fonts may still be loading when the pane mounts. Waiting
    // keeps terminal cell metrics aligned with what gets painted.
    const fontsReady = document.fonts?.ready;
    if (fontsReady) {
      void fontsReady.then(() => void start());
    } else {
      void start();
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
