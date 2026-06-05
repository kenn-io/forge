const EDITABLE_SELECTOR = "input, textarea, select, [contenteditable='true']";

export function shouldIgnoreGlobalShortcutTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Node)) {
    return false;
  }

  const element = target instanceof Element ? target : target.parentElement;

  return element?.closest(EDITABLE_SELECTOR) !== null;
}
