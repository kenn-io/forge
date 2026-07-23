import type { TerminalSettings } from "@middleman/ui/api/types";

export const MIN_TERMINAL_FONT_SIZE = 8;
export const MAX_TERMINAL_FONT_SIZE = 32;
export const RESET_TERMINAL_FONT_SIZE = 14;

interface TerminalZoomControllerOptions {
  getSettings: () => TerminalSettings;
  setSettings: (settings: TerminalSettings) => void;
  persist: (settings: TerminalSettings) => Promise<TerminalSettings>;
  reportError: (error: unknown) => void;
}

export interface TerminalZoomController {
  decrease: () => void;
  handleKeydown: (event: KeyboardEvent) => boolean;
  increase: () => void;
  reset: () => void;
  setFontSize: (fontSize: number) => void;
  whenIdle: () => Promise<void>;
}

function clampFontSize(fontSize: number): number {
  return Math.min(MAX_TERMINAL_FONT_SIZE, Math.max(MIN_TERMINAL_FONT_SIZE, Math.round(fontSize)));
}

type TerminalZoomAction = "decrease" | "increase" | "reset";

function terminalShortcutAction(event: KeyboardEvent): TerminalZoomAction | null {
  if ((!event.metaKey && !event.ctrlKey) || event.altKey) return null;
  if (!(event.target instanceof Element) || !event.target.closest(".terminal-container")) return null;
  if (event.key === "0" || event.code === "Digit0" || event.code === "Numpad0") return "reset";
  if (event.key === "+" || event.key === "=" || event.code === "Equal" || event.code === "NumpadAdd") {
    return "increase";
  }
  if (event.key === "-" || event.key === "_" || event.code === "Minus" || event.code === "NumpadSubtract") {
    return "decrease";
  }
  return null;
}

export function createTerminalZoomController({
  getSettings,
  setSettings,
  persist,
  reportError,
}: TerminalZoomControllerOptions): TerminalZoomController {
  let confirmedFontSize = getSettings().font_size;
  let latestRequest = 0;
  let pendingRequests = 0;
  let saveQueue = Promise.resolve();

  function setFontSize(fontSize: number): void {
    const nextFontSize = clampFontSize(fontSize);
    const current = getSettings();
    if (current.font_size === nextFontSize) return;
    if (pendingRequests === 0) {
      confirmedFontSize = current.font_size;
    }

    const request = ++latestRequest;
    pendingRequests += 1;
    const pending = { ...current, font_size: nextFontSize };
    setSettings(pending);

    saveQueue = saveQueue.then(async () => {
      try {
        const saved = await persist(pending);
        confirmedFontSize = saved.font_size;
        if (request === latestRequest) {
          setSettings({ ...getSettings(), font_size: saved.font_size });
        }
      } catch (error) {
        if (request === latestRequest) {
          setSettings({ ...getSettings(), font_size: confirmedFontSize });
        }
        reportError(error);
      } finally {
        pendingRequests -= 1;
      }
    });
  }

  function increase(): void {
    setFontSize(getSettings().font_size + 1);
  }

  function decrease(): void {
    setFontSize(getSettings().font_size - 1);
  }

  function reset(): void {
    setFontSize(RESET_TERMINAL_FONT_SIZE);
  }

  function handleKeydown(event: KeyboardEvent): boolean {
    const action = terminalShortcutAction(event);
    if (!action) return false;
    event.preventDefault();
    event.stopPropagation();

    if (action === "reset") {
      reset();
    } else if (action === "increase") {
      increase();
    } else {
      decrease();
    }
    return true;
  }

  return {
    decrease,
    handleKeydown,
    increase,
    reset,
    setFontSize,
    whenIdle: () => saveQueue,
  };
}
