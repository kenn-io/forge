import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "@middleman/ui/api/types";
import { saveTerminalSettings, type TerminalSettingsStore } from "./terminalSettingsPersistence";

export const MIN_TERMINAL_FONT_SIZE = 8;
export const MAX_TERMINAL_FONT_SIZE = 32;
export const RESET_TERMINAL_FONT_SIZE = DEFAULT_TERMINAL_SETTINGS.font_size;

interface TerminalZoomControllerOptions {
  store: TerminalSettingsStore;
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
  store,
  persist,
  reportError,
}: TerminalZoomControllerOptions): TerminalZoomController {
  let saveQueue = Promise.resolve();

  function setFontSize(fontSize: number): void {
    const nextFontSize = clampFontSize(fontSize);
    const current = store.getTerminalSettings();
    if (current.font_size === nextFontSize) return;

    saveQueue = saveTerminalSettings({
      baseline: current,
      changes: { font_size: nextFontSize },
      persist,
      store,
    }).then(
      () => undefined,
      (error) => {
        reportError(error);
      },
    );
  }

  function increase(): void {
    setFontSize(store.getTerminalSettings().font_size + 1);
  }

  function decrease(): void {
    setFontSize(store.getTerminalSettings().font_size - 1);
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
