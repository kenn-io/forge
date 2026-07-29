const EDITABLE_SELECTOR = "input, textarea, select, [contenteditable='true']";

// Subtrees that hold nothing but a terminal, so anything inside one is the
// terminal's: xterm's root and hidden helper textarea, the pane container, and
// the pooled session wrapper (whose only child is the terminal pane).
const TERMINAL_SUBTREE_SELECTOR = ".terminal-container, .xterm, [data-session-host]";

// The unpromoted workspace's Focus Terminal path parks focus on this wrapper
// itself. It is NOT a terminal-only subtree — the workspace sidebar, dock
// header, launcher, and dialogs all live under it — so it counts only when it
// is the event target. Matching it as an ancestor would hand the terminal every
// shortcut typed into the workspace's own controls.
const TERMINAL_FOCUS_HOLDER_SELECTOR = ".workspace-host-wrapper";

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
 * way of xterm's hidden textarea, so focus resting on either wrapper leaks the
 * keystroke to the global registry.
 */
export function isTerminalKeyboardTarget(target: EventTarget | null): boolean {
  const element = elementFor(target);
  if (element === null) {
    return false;
  }

  return element.closest(TERMINAL_SUBTREE_SELECTOR) != null || element.matches(TERMINAL_FOCUS_HOLDER_SELECTOR);
}

function closestMatch(target: EventTarget | null, selector: string): boolean {
  return elementFor(target)?.closest(selector) != null;
}

function elementFor(target: EventTarget | null): Element | null {
  if (!(target instanceof Node)) {
    return null;
  }

  return target instanceof Element ? target : target.parentElement;
}
