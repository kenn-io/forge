import { Effect, FiberHandle, FiberSet } from "effect";
import type { Scope } from "effect/Scope";

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
  outcome: Effect.Effect<boolean>;
  failed: boolean;
  source: "keyboard" | "pointer-confirmed" | "pointer-prepared";
}

function writeTextThroughCopyEvent(text: string): boolean {
  if (typeof document.execCommand !== "function") return false;

  let handled = false;
  const handleCopy = (event: ClipboardEvent): void => {
    if (!event.clipboardData) return;
    // kit-ui-check-ignore: gesture-authorized OSC 52 fallback for insecure HTTP origins.
    event.clipboardData.setData("text/plain", text);
    event.preventDefault();
    event.stopImmediatePropagation();
    handled = true;
  };
  document.addEventListener("copy", handleCopy, true);
  try {
    // kit-ui-check-ignore: gesture-authorized OSC 52 fallback for insecure HTTP origins.
    return document.execCommand("copy") && handled;
  } finally {
    document.removeEventListener("copy", handleCopy, true);
  }
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
    async writeText(text) {
      if (navigator.clipboard?.writeText) {
        try {
          // kit-ui-check-ignore: gesture-authorized OSC 52 browser clipboard write.
          await navigator.clipboard.writeText(text);
          return;
        } catch {
          // A trusted copy event remains available on insecure HTTP origins.
        }
      }
      if (writeTextThroughCopyEvent(text)) return;
      throw new DOMException("Clipboard writes are unavailable", "NotSupportedError");
    },
  };
}

export function makeTerminalClipboardWriter(
  port: TerminalClipboardPort,
  options: TerminalClipboardWriterOptions = {},
): Effect.Effect<TerminalClipboardWriter, never, Scope> {
  return Effect.gen(function* () {
    const runExpiration = yield* FiberHandle.makeRuntime<never>();
    const runPointerWatchdog = yield* FiberHandle.makeRuntime<never>();
    const runOutcomeObserver = yield* FiberHandle.makeRuntime<never>();
    const runWrite = yield* FiberSet.makeRuntimePromise<never>();
    let pending: PendingClipboardWrite | null = null;
    let expirationScheduled = false;
    let pointerWatchdogScheduled = false;
    let pointerGestureActive = false;
    let pointerAuthorizationPending = false;
    let pointerGestureAuthorizationConsumed = false;
    let disposed = false;
    let revocationGeneration = 0;

    function clearExpiration(): void {
      if (!expirationScheduled) return;
      expirationScheduled = false;
      runExpiration(Effect.void);
    }

    function clearPointerWatchdog(): void {
      if (!pointerWatchdogScheduled) return;
      pointerWatchdogScheduled = false;
      runPointerWatchdog(Effect.void);
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
      expirationScheduled = true;
      runExpiration(
        Effect.sleep(`${delayMs} millis`).pipe(
          Effect.andThen(
            Effect.sync(() => {
              expirationScheduled = false;
              expirePending();
            }),
          ),
        ),
      );
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

      let outcome: Effect.Effect<boolean>;
      try {
        const write = port.beginDeferredWrite(text);
        outcome = Effect.tryPromise({ try: () => write, catch: (cause) => cause }).pipe(
          Effect.match({ onFailure: () => false, onSuccess: () => true }),
        );
      } catch {
        outcome = Effect.succeed(false);
      }
      const armed: PendingClipboardWrite = {
        resolve,
        reject,
        outcome,
        failed: false,
        source,
      };
      pending = armed;
      runOutcomeObserver(
        outcome.pipe(
          Effect.tap((written) =>
            Effect.sync(() => {
              if (!written) armed.failed = true;
            }),
          ),
          Effect.asVoid,
        ),
      );
    }

    const writeDirect = (text: string): Effect.Effect<boolean> =>
      Effect.tryPromise({ try: () => port.writeText(text), catch: (cause) => cause }).pipe(
        Effect.match({ onFailure: () => false, onSuccess: () => true }),
      );

    const writeLocal = (text: string): Effect.Effect<boolean> =>
      Effect.tryPromise({ try: () => port.writeLocalText(text), catch: (cause) => cause }).pipe(
        Effect.match({ onFailure: () => false, onSuccess: () => true }),
      );

    function cancelPointerGesture(): void {
      if (!pointerGestureActive && !pointerAuthorizationPending) return;
      revocationGeneration += 1;
      clearPointerWatchdog();
      pointerGestureActive = false;
      pointerGestureAuthorizationConsumed = false;
      if (pointerAuthorizationPending) expirePending();
    }

    function timeoutPointerGesture(): void {
      pointerWatchdogScheduled = false;
      cancelPointerGesture();
      options.onPointerGestureTimeout?.();
    }

    function dispose(): void {
      if (disposed) return;
      disposed = true;
      revocationGeneration += 1;
      clearPointerWatchdog();
      pointerGestureActive = false;
      pointerAuthorizationPending = false;
      pointerGestureAuthorizationConsumed = false;
      expirePending();
    }

    const writer: TerminalClipboardWriter = {
      beginPointerGesture() {
        if (disposed) return;
        clearPointerWatchdog();
        pointerGestureActive = true;
        pointerGestureAuthorizationConsumed = false;
        clearExpiration();
        arm("pointer-prepared");
        pointerAuthorizationPending = pending !== null;
        pointerWatchdogScheduled = true;
        runPointerWatchdog(
          Effect.sleep(`${POINTER_GESTURE_WATCHDOG_MS} millis`).pipe(
            Effect.andThen(Effect.sync(timeoutPointerGesture)),
          ),
        );
      },
      cancelAuthorization() {
        revocationGeneration += 1;
        clearPointerWatchdog();
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
        clearPointerWatchdog();
        pointerGestureActive = false;
        const selectionConfirmed = pointerGestureAuthorizationConsumed || pending?.source === "pointer-confirmed";
        if (!selectionConfirmed) {
          if (pending?.source === "pointer-prepared") expirePending();
          pointerAuthorizationPending = false;
          pointerGestureAuthorizationConsumed = false;
          return;
        }
        if (!pointerGestureAuthorizationConsumed) arm("pointer-confirmed");
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
      write: (text) =>
        runWrite(
          Effect.gen(function* () {
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
            const deferredWritten = yield* authorized.outcome;
            if (disposed || writeGeneration !== revocationGeneration) return "unauthorized";
            if (deferredWritten) return "written";
            const directWritten = yield* writeDirect(text);
            if (disposed || writeGeneration !== revocationGeneration) return "unauthorized";
            if (directWritten) return "written";
            const localWritten = yield* writeLocal(text);
            if (disposed || writeGeneration !== revocationGeneration) return "unauthorized";
            return localWritten ? "written" : "blocked";
          }),
        ),
      dispose,
    };
    yield* Effect.addFinalizer(() => Effect.sync(dispose));
    return writer;
  });
}
