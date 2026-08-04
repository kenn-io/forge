<script lang="ts">
  import { onMount } from "svelte";
  import { getStores } from "@kenn-forge/ui";
  import { showFlash } from "@kenn-forge/ui/stores/flash";
  import { Terminal } from "@xterm/xterm";
  import type { ILinkHandler } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { LigaturesAddon } from "@xterm/addon-ligatures/lib/addon-ligatures.mjs";
  import { WebLinksAddon } from "@xterm/addon-web-links";
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
  import { supportsReplayBoundary } from "./terminalReplayBoundary.js";
  import {
    clearSharedTerminalTextureAtlas,
    registerTerminalTextureAtlasParticipant,
  } from "./sharedTerminalTextureAtlas.js";
  import { createTmuxMouseDragAutoscroll } from "./tmuxMouseDragAutoscroll.js";

  interface TerminalPaneProps {
    workspaceId?: string | undefined;
    websocketPath?: string | undefined;
    reconnectOnExit?: boolean | undefined;
    active?: boolean | undefined;
    autoFocus?: boolean | undefined;
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
    autoFocus = true,
    disabled = false,
    onExit,
    initialStatus,
  }: TerminalPaneProps = $props();
  const { settings: settingsStore } = getStores();

  const basePath = (window.__BASE_PATH__ ?? "/").replace(/\/$/, "");
  const terminalLinkUsesMetaKey = /Mac/.test(navigator.platform);
  const terminalLinkModifierLabel = terminalLinkUsesMetaKey ? "Cmd" : "Ctrl";

  let containerEl: HTMLDivElement;
  let terminal: Terminal | null = $state(null);
  let hoveredTerminalLink: string | null = $state(null);
  let fitAddon: FitAddon | null = null;
  let ligaturesAddon: LigaturesAddon | null = null;
  let webglAddon: WebglAddon | null = null;
  let ws: WebSocket | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let restartTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectDelay = 1000;
  let resizeObserver: ResizeObserver | null = null;
  let unregisterTextureAtlasParticipant: (() => void) | null = null;
  let refreshFrame: number | null = null;
  let resizeFrame: number | null = null;
  // What the PTY was last told. Resends are skipped against this, so a
  // ResizeObserver burst costs one frame, not one per callback.
  let sentCols = 0;
  let sentRows = 0;
  let sentResizeActive: boolean | null = null;
  let resizeReady = true;
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
  let pointerOrigin: { clientX: number; clientY: number } | null = null;
  let explicitFocusRequested = false;
  const encoder = new TextEncoder();
  const clipboardWriter = createTerminalClipboardWriter(
    createBrowserTerminalClipboardPort(),
    { onPointerGestureTimeout: cancelTerminalPointerGesture },
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

  export function focus(): void {
    if (terminal !== null) {
      terminal.focus();
      return;
    }
    explicitFocusRequested = true;
  }

  const MAX_RECONNECT_DELAY = 30000;
  const TERMINAL_SMOOTH_SCROLL_DURATION = 0;
  const TERMINAL_MINIMUM_CONTRAST_RATIO = 4.5;
  const TERMINAL_FONT_WAIT_MS = 300;
  const TERMINAL_FONT_LOAD_GLYPHS = "0MWim@#";
  const TERMINAL_SEQUENCE_CANCEL = new Uint8Array([0x18]);
  const POINTER_SELECTION_INTENT_MIN_PX = 4;

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

  function cancelPendingTerminalSequence(callback?: () => void): void {
    terminal?.write(TERMINAL_SEQUENCE_CANCEL, callback);
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

  function openTerminalLink(event: MouseEvent, uri: string): void {
    if (!terminalLinkModifierPressed(event)) return;

    let url: URL;
    try {
      url = new URL(uri);
    } catch {
      return;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") return;

    window.open(url.href, "_blank", "noopener,noreferrer");
  }

  function terminalLinkModifierPressed(event: MouseEvent): boolean {
    return terminalLinkUsesMetaKey ? event.metaKey : event.ctrlKey;
  }

  function showTerminalLink(_event: MouseEvent, uri: string): void {
    hoveredTerminalLink = uri;
  }

  function hideTerminalLink(): void {
    hoveredTerminalLink = null;
  }

  const terminalLinkHandler: ILinkHandler = {
    activate: openTerminalLink,
    hover: showTerminalLink,
    leave: hideTerminalLink,
  };

  function handleTerminalPointerDown(event: PointerEvent): void {
    if (disposed || disabled || event.button !== 0 || !event.isTrusted) return;
    if (activePointerId !== null) {
      cancelTerminalPointerGesture();
    }
    // Over an active link, pointer capture retargets mouseup to the container
    // and prevents xterm's linkifier from activating it. Modified non-link
    // gestures still need the normal clipboard and drag lifecycle below.
    if (hoveredTerminalLink !== null && terminalLinkModifierPressed(event)) return;
    activePointerId = event.pointerId;
    pointerOrigin = { clientX: event.clientX, clientY: event.clientY };
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
    pointerOrigin = null;
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
    pointerOrigin = null;
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
    pointerOrigin = null;
    clipboardWriter.cancelPointerGesture();
    mouseDragAutoscroll.endPointerGesture();
  }

  function cancelTerminalClipboardAuthorization(): void {
    cancelTerminalPointerGesture();
    clipboardWriter.cancelAuthorization();
  }

  function handleTerminalFocusOut(event: FocusEvent): void {
    const nextTarget = event.relatedTarget;
    if (nextTarget instanceof Node && containerEl.contains(nextTarget)) return;
    // Pointer capture can briefly produce a focusout without a destination.
    // A concrete external target is an actual focus transfer and must revoke.
    if (nextTarget === null && activePointerId !== null) return;
    cancelTerminalClipboardAuthorization();
  }

  function handleDocumentPointerDown(event: PointerEvent): void {
    const target = event.target;
    if (target instanceof Node && containerEl.contains(target)) return;
    cancelTerminalClipboardAuthorization();
  }

  function handleWindowBlur(): void {
    cancelTerminalClipboardAuthorization();
  }

  function handleDocumentVisibilityChange(): void {
    if (document.visibilityState !== "visible") {
      cancelTerminalClipboardAuthorization();
    }
  }

  $effect(() => {
    if (active && !disabled) return;
    cancelTerminalClipboardAuthorization();
  });

  function handleTerminalKeyDown(event: KeyboardEvent): void {
    if (disposed || disabled || event.isComposing || !event.isTrusted) return;
    clipboardWriter.authorizeKeyboardGesture();
  }

  function hasPointerSelectionIntent(event: PointerEvent): boolean {
    if (pointerOrigin === null) return false;
    const cell = containerEl.querySelector<HTMLElement>(".xterm-helper-textarea")?.getBoundingClientRect();
    if (!cell || cell.width <= 0 || cell.height <= 0) return false;

    const deltaX = event.clientX - pointerOrigin.clientX;
    const deltaY = event.clientY - pointerOrigin.clientY;
    const pixelsMovedSquared = deltaX * deltaX + deltaY * deltaY;
    const columnsMoved = deltaX / cell.width;
    const rowsMoved = deltaY / cell.height;
    return (
      pixelsMovedSquared >= POINTER_SELECTION_INTENT_MIN_PX ** 2 &&
      columnsMoved * columnsMoved + rowsMoved * rowsMoved >= 1
    );
  }

  function handleWindowPointerMove(event: PointerEvent): void {
    if (disposed || disabled || !terminal) return;
    if (activePointerId === event.pointerId && hasPointerSelectionIntent(event)) {
      clipboardWriter.confirmPointerSelection();
      pointerOrigin = null;
    }
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

  function appendConnectionParams(
    url: string,
    size: { cols: number; rows: number } | null,
    replayBoundary: boolean,
  ): string {
    const sep = url.includes("?") ? "&" : "?";
    const resizeActive = resizeAuthorityRegionSize() !== null ? "1" : "0";
    const { traceparent, baggage } = traceHeadersForRequest();
    const sizeParams = size
      ? `cols=${size.cols}&rows=${size.rows}&`
      : "";
    const replayBoundaryParam = replayBoundary ? "replay_boundary=1&" : "";
    let result = `${url}${sep}${sizeParams}${replayBoundaryParam}resize_active=${resizeActive}&traceparent=${encodeURIComponent(traceparent)}`;
    if (baggage !== null) result += `&baggage=${encodeURIComponent(baggage)}`;
    return result;
  }

  function buildWsUrl(
    size: { cols: number; rows: number } | null,
    replayBoundary: boolean,
  ): string | null {
    const path = websocketPath ?? defaultWebsocketPath();
    if (!path) return null;

    const withConnectionParams = appendConnectionParams(path, size, replayBoundary);
    if (/^wss?:\/\//.test(withConnectionParams)) {
      return withConnectionParams;
    }
    const embeddedUrl = embeddedWebSocketUrl(withBasePath(withConnectionParams));
    if (embeddedUrl) return embeddedUrl;
    const devUrl = buildDevApiWsUrl(withConnectionParams);
    if (devUrl) return devUrl;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    return `${proto}://${location.host}${withBasePath(withConnectionParams)}`;
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
    const apiUrl = window.__KENN_FORGE_DEV_API_URL__?.trim();
    if (!apiUrl || !path.startsWith("/api/")) return null;

    try {
      const base = new URL(apiUrl);
      const requested = new URL(path, "http://forge.local");
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

  function sendResize(cols: number, rows: number): boolean {
    return sendControl("resize", cols, rows);
  }

  function sendRefresh(cols: number, rows: number): boolean {
    return sendControl("refresh", cols, rows);
  }

  function sendResizeActive(nextActive: boolean): boolean {
    if (sentResizeActive === nextActive) return false;
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "resize_active", active: nextActive }));
      sentResizeActive = nextActive;
      return true;
    }
    return false;
  }

  function sendControl(
    type: "resize" | "refresh",
    cols: number,
    rows: number,
  ): boolean {
    if (!resizeReady || ws?.readyState !== WebSocket.OPEN) return false;
    ws.send(JSON.stringify({ type, cols, rows }));
    return true;
  }

  function refreshVisibleTerminal(): void {
    const size = resizeAuthorityRegionSize();
    if (!size || !terminal) return;

    fitAddon?.fit();
    const fittedSize = { cols: terminal.cols, rows: terminal.rows };
    terminal.refresh(0, Math.max(0, fittedSize.rows - 1));
    // The server resizes on a refresh's dimensions too, so a delivered refresh
    // counts as the size the PTY now has.
    if (sendRefresh(fittedSize.cols, fittedSize.rows)) {
      sentCols = fittedSize.cols;
      sentRows = fittedSize.rows;
    }
  }

  function redrawTerminalTextureAtlas(): void {
    if (!terminal) return;

    clearSharedTerminalTextureAtlas(terminal);
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
      const wgl = new WebglAddon({ customGlyphs: true });
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

  /**
   * The size this terminal's region actually is, or null when it has none.
   *
   * A parked terminal sits in a `display:none` node, whose content box is zero,
   * so the fit addon proposes nothing usable for it — measuring that is what
   * used to resize a live tmux pane to one row. The measurement itself is the
   * geometry check. Painted state is applied separately because
   * `visibility:hidden` retains dimensions.
   */
  function terminalRegionSize(): { cols: number; rows: number } | null {
    if (!fitAddon || !terminal || !containerEl.isConnected) return null;

    const proposed = fitAddon.proposeDimensions();
    if (!proposed) return null;
    if (!Number.isFinite(proposed.cols) || !Number.isFinite(proposed.rows)) return null;
    if (proposed.cols < 1 || proposed.rows < 1) return null;

    return { cols: proposed.cols, rows: proposed.rows };
  }

  function resizeAuthorityRegionSize(): { cols: number; rows: number } | null {
    if (!active) return null;
    return terminalRegionSize();
  }

  /**
   * Push the region's size whenever it differs from what the PTY was last told.
   *
   * Painted state is not focus: every visible split leaf is active and owns its
   * own size. Requiring it here prevents a visibility-hidden tab, whose geometry
   * remains measurable, from resizing the PTY. Sending only on a real change
   * keeps a ResizeObserver burst from turning into a burst of resize frames.
   */
  function resizeVisibleTerminal(): void {
    const size = terminalRegionSize();
    const resizeActive = active && size !== null;
    const authorityChanged = sendResizeActive(resizeActive);
    if (!resizeActive || !size || !terminal) return;

    fitAddon?.fit();
    const fittedSize = { cols: terminal.cols, rows: terminal.rows };
    terminal.refresh(0, Math.max(0, fittedSize.rows - 1));
    // Report the dimensions fit() actually applied. It takes its own fresh
    // measurement, so the region can cross a row or column boundary after the
    // authority preflight above but before xterm is resized.
    // Re-send unchanged dimensions when reclaiming authority because another
    // attachment may have resized the PTY while this region had no geometry.
    if (!authorityChanged && fittedSize.cols === sentCols && fittedSize.rows === sentRows) return;
    // Recorded only once the socket carried it — a resize computed before the
    // socket opened would otherwise be suppressed forever as already sent.
    if (sendResize(fittedSize.cols, fittedSize.rows)) {
      sentCols = fittedSize.cols;
      sentRows = fittedSize.rows;
    }
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
    const replayBoundary = supportsReplayBoundary(websocketPath);
    const url = buildWsUrl(replayBoundary ? null : { cols, rows }, replayBoundary);
    if (!url) return;
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    sentResizeActive = null;
    resizeReady = !replayBoundary;
    ws = socket;

    socket.onopen = () => {
      switchTimer.record("socket-open");
      reconnectDelay = 1000;
      sendResizeActive(resizeAuthorityRegionSize() !== null);
      const size = resizeAuthorityRegionSize();
      if (resizeReady && size && (size.cols !== sentCols || size.rows !== sentRows)) {
        scheduleTerminalRefresh();
      }
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
          if (msg.type === "replay_ready" && replayBoundary) {
            cancelPendingTerminalSequence(() => {
              if (disposed || ws !== socket) return;
              resizeReady = true;
              scheduleTerminalRefresh();
            });
          } else if (msg.type === "exited") {
            cancelPendingTerminalSequence();
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
      cancelPendingTerminalSequence();
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
        cancelPendingTerminalSequence();
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
    pointerOrigin = null;
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
      unregisterTextureAtlasParticipant?.();
      unregisterTextureAtlasParticipant = null;
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
    // Authority requires painted state plus geometry, never focus: every
    // painted split leaf stays eligible, while hidden and parked terminals do
    // not dictate a size for the pane the user is actually looking at.
    sendResizeActive(resizeAuthorityRegionSize() !== null);
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
        cursorBlink: terminalCursorBlink,
        drawBoldTextInBrightColors: true,
        fontFamily: terminalFontFamily,
        fontSize: terminalFontSize,
        scrollback: terminalScrollback,
        letterSpacing: terminalLetterSpacing,
        lineHeight: terminalLineHeight,
        linkHandler: terminalLinkHandler,
        minimumContrastRatio: TERMINAL_MINIMUM_CONTRAST_RATIO,
        rescaleOverlappingGlyphs: true,
        scrollOnEraseInDisplay: true,
        smoothScrollDuration: TERMINAL_SMOOTH_SCROLL_DURATION,
        vtExtensions: {
          kittyKeyboard: true,
        },
        disableStdin: disabled,
      });
      terminal = term;
      unregisterTextureAtlasParticipant =
        registerTerminalTextureAtlasParticipant(terminal);

      term.open(containerEl);
      term.parser.registerOscHandler(52, handleOsc52Clipboard);
      switchTimer.record("terminal-constructed");
      containerEl.addEventListener("paste", handleTerminalPaste, true);

      const fit = new FitAddon();
      fitAddon = fit;
      term.loadAddon(fit);
      term.loadAddon(
        new WebLinksAddon(openTerminalLink, {
          hover: showTerminalLink,
          leave: hideTerminalLink,
        }),
      );

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

      // Renderer autofocus runs only at terminal creation. Explicit pool/host
      // requests use focus() above, but reveal- or enable-driven re-runs of the
      // active effect must not steal focus from controls. The font-load wait is
      // async, so creation autofocus only moves focus when the mount-time context
      // is still current and isn't a dialog/menu/input.
      const focusExplicitly = explicitFocusRequested;
      explicitFocusRequested = false;
      if (active && !disabled && (focusExplicitly || (autoFocus && focusIntent.shouldFocus()))) term.focus();

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
        clearSharedTerminalTextureAtlas(terminal);
        // New metrics mean a new measurement; resizeVisibleTerminal re-fits,
        // repaints, and pushes the size only if the region now works out
        // differently.
        resizeVisibleTerminal();
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
<svelte:document
  onpointerdowncapture={handleDocumentPointerDown}
  onvisibilitychange={handleDocumentVisibilityChange}
/>

<div
  class="terminal-container"
  bind:this={containerEl}
  onpointerdowncapture={handleTerminalPointerDown}
  onlostpointercapture={handleTerminalLostPointerCapture}
  onkeydowncapture={handleTerminalKeyDown}
  onfocusout={handleTerminalFocusOut}
>
  {#if hoveredTerminalLink}
    <div class="terminal-link-tooltip">
      <span>{hoveredTerminalLink}</span>
      <small>{terminalLinkModifierLabel}+Click to open link</small>
    </div>
    {/if}
</div>

<style>
  .terminal-container {
    position: relative;
    width: 100%;
    height: 100%;
    background: var(--terminal-bg);
  }

  .terminal-link-tooltip {
    position: absolute;
    z-index: 5;
    bottom: var(--space-4);
    left: var(--space-4);
    display: flex;
    max-width: calc(100% - (2 * var(--space-4)));
    flex-direction: column;
    gap: var(--space-1);
    padding: var(--space-2) var(--space-3);
    overflow: hidden;
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--font-size-sm);
    line-height: 1.25;
    pointer-events: none;
    background: var(--bg-surface);
    border: var(--border-width) solid var(--border-default);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-md);
  }

  .terminal-link-tooltip span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .terminal-link-tooltip small {
    color: var(--text-muted);
    font-size: inherit;
  }

  .terminal-container :global(.xterm),
  .terminal-container :global(.xterm-viewport),
  .terminal-container :global(.xterm-screen) {
    background: var(--terminal-bg);
  }
</style>
