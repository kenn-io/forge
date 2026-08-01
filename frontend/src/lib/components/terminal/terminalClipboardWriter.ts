import { writeTerminalClipboardThroughServer } from "./terminalClipboardFallback.js";

const KEYBOARD_AUTHORIZATION_MS = 10_000;
const POINTER_GESTURE_WATCHDOG_MS = 60_000;
const POINTER_RELEASE_GRACE_MS = 1_000;

export interface TerminalClipboardPort {
  beginDeferredWrite(text: Promise<string>): Promise<void>;
  writeLocalText(text: string): Promise<void>;
  writeText(text: string): Promise<void>;
}

export interface TerminalClipboardWriter {
  beginPointerGesture(): void;
  cancelAuthorization(): void;
  cancelPointerGesture(): void;
  confirmPointerSelection(): void;
  endPointerGesture(): void;
  authorizeKeyboardGesture(): void;
  write(text: string): Promise<TerminalClipboardWriteResult>;
  dispose(): void;
}

export interface TerminalClipboardWriterOptions {
  onPointerGestureTimeout?: () => void;
}

export type TerminalClipboardWriteResult = "written" | "unauthorized" | "blocked";

interface PendingClipboardWrite {
  resolve(text: string): void;
  reject(reason: unknown): void;
  outcome: Promise<boolean>;
  failed: boolean;
  source: "keyboard" | "pointer-confirmed" | "pointer-prepared";
}

export function createBrowserTerminalClipboardPort(): TerminalClipboardPort {
  return {
    beginDeferredWrite(text) {
      if (!navigator.clipboard?.write || typeof ClipboardItem === "undefined") {
        throw new DOMException("Deferred clipboard writes are unavailable", "NotSupportedError");
      }
      const payload = text.then((value) => new Blob([value], { type: "text/plain" }));
      // ClipboardItem does not consistently observe a rejected deferred payload.
      // Keep the rejection intact so the write is canceled, but mark it handled.
      void payload.catch(() => undefined);
      const item = new ClipboardItem({ "text/plain": payload });
      return navigator.clipboard.write([item]);
    },
    writeLocalText(text) {
      return writeTerminalClipboardThroughServer(text);
    },
    writeText(text) {
      if (!navigator.clipboard?.writeText) {
        return Promise.reject(new DOMException("Clipboard writes are unavailable", "NotSupportedError"));
      }
      // kit-ui-check-ignore: OSC 52 is Clipboard API-only; do not fall back to legacy DOM copy.
      return navigator.clipboard.writeText(text);
    },
  };
}

export function createTerminalClipboardWriter(
  port: TerminalClipboardPort,
  options: TerminalClipboardWriterOptions = {},
): TerminalClipboardWriter {
  let pending: PendingClipboardWrite | null = null;
  let expirationTimer: ReturnType<typeof setTimeout> | null = null;
  let pointerGestureTimer: ReturnType<typeof setTimeout> | null = null;
  let pointerGestureActive = false;
  let pointerAuthorizationPending = false;
  let pointerGestureAuthorizationConsumed = false;
  let disposed = false;
  let revocationGeneration = 0;

  function clearExpiration(): void {
    if (expirationTimer === null) return;
    clearTimeout(expirationTimer);
    expirationTimer = null;
  }

  function clearPointerGestureTimer(): void {
    if (pointerGestureTimer === null) return;
    clearTimeout(pointerGestureTimer);
    pointerGestureTimer = null;
  }

  function expirePending(): void {
    clearExpiration();
    const expired = pending;
    pending = null;
    pointerAuthorizationPending = false;
    expired?.reject(new DOMException("Terminal clipboard authorization expired", "AbortError"));
  }

  function scheduleExpiration(delayMs: number): void {
    clearExpiration();
    expirationTimer = setTimeout(expirePending, delayMs);
  }

  function arm(source: PendingClipboardWrite["source"]): void {
    if (disposed) return;
    if (pending && !pending.failed) {
      pending.source = source;
      return;
    }
    if (pending) expirePending();

    let resolve!: (text: string) => void;
    let reject!: (reason: unknown) => void;
    const text = new Promise<string>((resolveText, rejectText) => {
      resolve = resolveText;
      reject = rejectText;
    });
    void text.catch(() => undefined);

    let outcome: Promise<boolean>;
    try {
      outcome = Promise.resolve(port.beginDeferredWrite(text)).then(
        () => true,
        () => false,
      );
    } catch {
      outcome = Promise.resolve(false);
    }
    const armed = {
      resolve,
      reject,
      outcome,
      failed: false,
      source,
    };
    pending = armed;
    void outcome.then((written) => {
      if (!written) armed.failed = true;
    });
  }

  async function writeDirect(text: string): Promise<boolean> {
    try {
      await port.writeText(text);
      return true;
    } catch {
      return false;
    }
  }

  async function writeLocal(text: string): Promise<boolean> {
    try {
      await port.writeLocalText(text);
      return true;
    } catch {
      return false;
    }
  }

  function cancelPointerGesture(): void {
    if (!pointerGestureActive && !pointerAuthorizationPending) return;
    revocationGeneration += 1;
    clearPointerGestureTimer();
    pointerGestureActive = false;
    pointerGestureAuthorizationConsumed = false;
    if (pointerAuthorizationPending) expirePending();
  }

  function timeoutPointerGesture(): void {
    cancelPointerGesture();
    options.onPointerGestureTimeout?.();
  }

  return {
    beginPointerGesture() {
      if (disposed) return;
      clearPointerGestureTimer();
      pointerGestureActive = true;
      pointerGestureAuthorizationConsumed = false;
      clearExpiration();
      arm("pointer-prepared");
      pointerAuthorizationPending = pending !== null;
      pointerGestureTimer = setTimeout(timeoutPointerGesture, POINTER_GESTURE_WATCHDOG_MS);
    },
    cancelAuthorization() {
      revocationGeneration += 1;
      clearPointerGestureTimer();
      pointerGestureActive = false;
      pointerGestureAuthorizationConsumed = false;
      expirePending();
    },
    cancelPointerGesture,
    confirmPointerSelection() {
      if (disposed || !pointerGestureActive) return;
      if (pending?.source === "pointer-prepared") pending.source = "pointer-confirmed";
    },
    endPointerGesture() {
      if (disposed || !pointerGestureActive) return;
      clearPointerGestureTimer();
      pointerGestureActive = false;
      const selectionConfirmed = pointerGestureAuthorizationConsumed || pending?.source === "pointer-confirmed";
      if (!selectionConfirmed) {
        if (pending?.source === "pointer-prepared") expirePending();
        pointerAuthorizationPending = false;
        pointerGestureAuthorizationConsumed = false;
        return;
      }
      if (!pointerGestureAuthorizationConsumed) {
        arm("pointer-confirmed");
      }
      pointerAuthorizationPending = pending !== null;
      pointerGestureAuthorizationConsumed = false;
      if (pending) scheduleExpiration(POINTER_RELEASE_GRACE_MS);
    },
    authorizeKeyboardGesture() {
      if (disposed) return;
      arm("keyboard");
      if (pending) {
        pointerAuthorizationPending = pointerGestureActive;
        scheduleExpiration(KEYBOARD_AUTHORIZATION_MS);
      }
    },
    async write(text) {
      if (disposed) return "unauthorized";

      clearExpiration();
      const authorized = pending;
      if (authorized?.source === "pointer-prepared") return "unauthorized";
      pending = null;
      if (!authorized) return "unauthorized";
      const writeGeneration = revocationGeneration;
      if (pointerGestureActive && pointerAuthorizationPending) {
        pointerGestureAuthorizationConsumed = true;
      }
      pointerAuthorizationPending = false;

      authorized.resolve(text);
      const deferredWritten = await authorized.outcome;
      if (disposed || writeGeneration !== revocationGeneration) return "unauthorized";
      if (deferredWritten) return "written";
      const directWritten = await writeDirect(text);
      if (disposed || writeGeneration !== revocationGeneration) return "unauthorized";
      if (directWritten) return "written";
      const localWritten = await writeLocal(text);
      if (disposed || writeGeneration !== revocationGeneration) return "unauthorized";
      return localWritten ? "written" : "blocked";
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      revocationGeneration += 1;
      clearPointerGestureTimer();
      pointerGestureActive = false;
      pointerAuthorizationPending = false;
      pointerGestureAuthorizationConsumed = false;
      expirePending();
    },
  };
}
