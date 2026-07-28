import { writeTerminalClipboardThroughServer } from "./terminalClipboardFallback.js";

const KEYBOARD_AUTHORIZATION_MS = 10_000;
const POINTER_RELEASE_GRACE_MS = 1_000;

export interface TerminalClipboardPort {
  beginDeferredWrite(text: Promise<string>): Promise<void>;
  writeLocalText(text: string): Promise<void>;
  writeText(text: string): Promise<void>;
}

export interface TerminalClipboardWriter {
  beginPointerGesture(): void;
  endPointerGesture(): void;
  authorizeKeyboardGesture(): void;
  write(text: string): Promise<TerminalClipboardWriteResult>;
  dispose(): void;
}

export type TerminalClipboardWriteResult = "written" | "unauthorized" | "blocked";

interface PendingClipboardWrite {
  resolve(text: string): void;
  reject(reason: unknown): void;
  outcome: Promise<boolean>;
  failed: boolean;
}

export function createBrowserTerminalClipboardPort(): TerminalClipboardPort {
  return {
    beginDeferredWrite(text) {
      if (!navigator.clipboard?.write || typeof ClipboardItem === "undefined") {
        throw new DOMException("Deferred clipboard writes are unavailable", "NotSupportedError");
      }
      const item = new ClipboardItem({
        "text/plain": text.then((value) => new Blob([value], { type: "text/plain" })),
      });
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

export function createTerminalClipboardWriter(port: TerminalClipboardPort): TerminalClipboardWriter {
  let pending: PendingClipboardWrite | null = null;
  let expirationTimer: ReturnType<typeof setTimeout> | null = null;
  let pointerGestureActive = false;
  let pointerGestureAuthorizationConsumed = false;
  let disposed = false;

  function clearExpiration(): void {
    if (expirationTimer === null) return;
    clearTimeout(expirationTimer);
    expirationTimer = null;
  }

  function expirePending(): void {
    clearExpiration();
    const expired = pending;
    pending = null;
    expired?.reject(new DOMException("Terminal clipboard authorization expired", "AbortError"));
  }

  function scheduleExpiration(delayMs: number): void {
    clearExpiration();
    expirationTimer = setTimeout(expirePending, delayMs);
  }

  function arm(): void {
    if (disposed || (pending && !pending.failed)) return;
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
    const armed = { resolve, reject, outcome, failed: false };
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

  return {
    beginPointerGesture() {
      if (disposed) return;
      pointerGestureActive = true;
      pointerGestureAuthorizationConsumed = false;
      clearExpiration();
      arm();
    },
    endPointerGesture() {
      if (disposed || !pointerGestureActive) return;
      pointerGestureActive = false;
      if (!pointerGestureAuthorizationConsumed) {
        arm();
      }
      pointerGestureAuthorizationConsumed = false;
      if (pending) scheduleExpiration(POINTER_RELEASE_GRACE_MS);
    },
    authorizeKeyboardGesture() {
      if (disposed) return;
      arm();
      if (!pointerGestureActive && pending) {
        scheduleExpiration(KEYBOARD_AUTHORIZATION_MS);
      }
    },
    async write(text) {
      if (disposed) return "unauthorized";

      clearExpiration();
      const authorized = pending;
      pending = null;
      if (!authorized) return "unauthorized";
      if (pointerGestureActive) {
        pointerGestureAuthorizationConsumed = true;
      }

      authorized.resolve(text);
      if (await authorized.outcome) return "written";
      if (await writeDirect(text)) return "written";
      return (await writeLocal(text)) ? "written" : "blocked";
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      pointerGestureActive = false;
      pointerGestureAuthorizationConsumed = false;
      expirePending();
    },
  };
}
