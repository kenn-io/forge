import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "@kenn-forge/ui/api/types";
import { saveTerminalSettings, type TerminalSettingsStore } from "@kenn-forge/ui/stores/terminal-settings-persistence";

export const MIN_TERMINAL_FONT_SIZE = 8;
export const MAX_TERMINAL_FONT_SIZE = 32;
export const RESET_TERMINAL_FONT_SIZE = DEFAULT_TERMINAL_SETTINGS.font_size;

interface TerminalZoomControllerOptions {
  store: TerminalSettingsStore;
  persist: (settings: TerminalSettings) => Promise<TerminalSettings>;
  reportError: (error: unknown) => void;
  onPendingChange?: (pending: boolean) => void;
}

export interface TerminalZoomController {
  decrease: () => void;
  increase: () => void;
  reset: () => void;
  setFontSize: (fontSize: number) => void;
  whenIdle: () => Promise<void>;
}

function clampFontSize(fontSize: number): number {
  return Math.min(MAX_TERMINAL_FONT_SIZE, Math.max(MIN_TERMINAL_FONT_SIZE, Math.round(fontSize)));
}

export function createTerminalZoomController({
  store,
  persist,
  reportError,
  onPendingChange,
}: TerminalZoomControllerOptions): TerminalZoomController {
  let saveQueue = Promise.resolve();
  let pendingSaves = 0;

  function setFontSize(fontSize: number): void {
    const nextFontSize = clampFontSize(fontSize);
    const current = store.getTerminalSettings();
    if (current.font_size === nextFontSize) return;

    const save = saveTerminalSettings({
      baseline: current,
      changes: { font_size: nextFontSize },
      persist,
      store,
    });
    pendingSaves += 1;
    if (pendingSaves === 1) onPendingChange?.(true);
    saveQueue = save
      .then(
        () => undefined,
        (error) => {
          reportError(error);
        },
      )
      .finally(() => {
        pendingSaves -= 1;
        if (pendingSaves === 0) onPendingChange?.(false);
      });
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

  return {
    decrease,
    increase,
    reset,
    setFontSize,
    whenIdle: () => saveQueue,
  };
}
