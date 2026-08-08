import { Effect, FiberHandle } from "effect";
import type { Scope } from "effect/Scope";

const AUTOSCROLL_INTERVAL_MS = 80;
const ESCAPE = String.fromCharCode(27);
const SGR_MOUSE_REPORT = new RegExp(`${ESCAPE}\\[<(\\d+);(\\d+);(\\d+)([Mm])`, "g");

export interface TmuxMouseDragAutoscrollOptions {
  send(data: string): void;
}

export interface TmuxMousePointerUpdate {
  clientX: number;
  clientY: number;
  bounds: Pick<DOMRectReadOnly, "left" | "right" | "top" | "bottom" | "width" | "height">;
  cols: number;
  rows: number;
}

export interface TmuxMouseDragAutoscroll {
  observeTerminalData(data: string): void;
  updatePointer(update: TmuxMousePointerUpdate): void;
  endPointerGesture(): void;
  reset(): void;
  dispose(): void;
}

interface EdgeReport {
  direction: -1 | 1;
  column: number;
  row: number;
}

export function makeTmuxMouseDragAutoscroll(
  options: TmuxMouseDragAutoscrollOptions,
): Effect.Effect<TmuxMouseDragAutoscroll, never, Scope> {
  return Effect.gen(function* () {
    const runAutoscroll = yield* FiberHandle.makeRuntime<never>();
    let dragActive = false;
    let edgeReport: EdgeReport | null = null;
    let intervalActive = false;
    let disposed = false;

    function stop(): void {
      edgeReport = null;
      if (!intervalActive) return;
      intervalActive = false;
      runAutoscroll(Effect.void);
    }

    function sendEdgeReport(): void {
      if (disposed || !dragActive || !edgeReport) return;
      const wheelCode = edgeReport.direction < 0 ? 64 : 65;
      const position = `${edgeReport.column};${edgeReport.row}M`;
      options.send(`\x1b[<${wheelCode};${position}\x1b[<32;${position}`);
    }

    function start(): void {
      if (intervalActive) return;
      intervalActive = true;
      runAutoscroll(
        Effect.forever(
          Effect.sleep(`${AUTOSCROLL_INTERVAL_MS} millis`).pipe(Effect.andThen(Effect.sync(sendEdgeReport))),
        ),
      );
    }

    const autoscroll: TmuxMouseDragAutoscroll = {
      observeTerminalData(data) {
        if (disposed) return;
        for (const match of data.matchAll(SGR_MOUSE_REPORT)) {
          const code = Number(match[1]);
          const final = match[4];
          if (final === "m") {
            dragActive = false;
            stop();
            continue;
          }
          const motion = (code & 32) !== 0;
          const wheel = (code & 64) !== 0;
          const button = code & 3;
          if (!motion && !wheel && button === 0) {
            dragActive = true;
          }
        }
      },
      updatePointer({ clientX, clientY, bounds, cols, rows }) {
        if (disposed || !dragActive || cols <= 0 || rows <= 0 || bounds.width <= 0 || bounds.height <= 0) {
          stop();
          return;
        }

        const direction = clientY < bounds.top ? -1 : clientY > bounds.bottom ? 1 : 0;
        if (direction === 0) {
          stop();
          return;
        }

        const relativeX = Math.max(0, Math.min(clientX - bounds.left, bounds.width - Number.EPSILON));
        const column = Math.max(1, Math.min(cols, Math.floor((relativeX / bounds.width) * cols) + 1));
        edgeReport = {
          direction,
          column,
          row: direction < 0 ? 1 : rows,
        };
        start();
      },
      endPointerGesture() {
        if (dragActive && edgeReport) {
          options.send(`\x1b[<0;${edgeReport.column};${edgeReport.row}m`);
        }
        dragActive = false;
        stop();
      },
      reset() {
        dragActive = false;
        stop();
      },
      dispose() {
        if (disposed) return;
        disposed = true;
        dragActive = false;
        stop();
      },
    };
    yield* Effect.addFinalizer(() => Effect.sync(autoscroll.dispose));
    return autoscroll;
  });
}
