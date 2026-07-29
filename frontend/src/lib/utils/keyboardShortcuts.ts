const EDITABLE_SELECTOR = "input, textarea, select, [contenteditable='true']";

// A live terminal, by any of the wrappers a keydown can land on: xterm's own
// root and hidden helper textarea, the pane container, and the session wrapper
// (which is what holds focus when a pane is focused without clicking into the
// grid itself).
const TERMINAL_SELECTOR = ".terminal-container, .xterm, [data-session-host]";

export function shouldIgnoreGlobalShortcutTarget(target: EventTarget | null): boolean {
  return closestMatch(target, EDITABLE_SELECTOR);
}

/**
 * Whether a focused terminal owns this keystroke outright.
 *
 * A terminal is a whole second keyboard consumer, not a text field: TUIs bind
 * Escape, function keys, and Ctrl/Alt chords, and every one of those is a key
 * the app would otherwise claim. The editable guard is not enough on its own —
 * it lets modified bindings through, and it only matches a terminal at all by
 * way of xterm's hidden textarea, so focus resting on the session wrapper
 * leaks the keystroke to the global registry.
 */
export function isTerminalKeyboardTarget(target: EventTarget | null): boolean {
  return closestMatch(target, TERMINAL_SELECTOR);
}

function closestMatch(target: EventTarget | null, selector: string): boolean {
  if (!(target instanceof Node)) {
    return false;
  }

  const element = target instanceof Element ? target : target.parentElement;

  return element?.closest(selector) != null;
}
