// Guards terminal panes against stealing focus during async font loading.
// A pane can be mounted while a settings popover is open above it or while
// the user is typing in an unrelated field, so focus must only move into the
// terminal when nothing else has claimed it in the meantime.

// kit-ui-check-ignore -- CSS selector for closest()/matches() detection, not rendered markup.
const SACRED_CONTAINER_SELECTOR = '[role="dialog"], [role="menu"], [role="listbox"]';
const SACRED_FORM_SELECTOR = 'input, textarea, select, [contenteditable="true"]';

/**
 * Reports whether `el` is a focus target that a terminal pane must never
 * steal focus from: form controls, and anything inside an open dialog,
 * menu, or listbox. Plain buttons are not sacred, since clicking a launch
 * tile focuses a button and the newly created terminal must still take
 * focus in that case.
 */
export function focusIsSacred(el: Element | null): boolean {
  if (!el) return false;
  if (el.closest(SACRED_CONTAINER_SELECTOR)) return true;
  return el.matches(SACRED_FORM_SELECTOR);
}

export interface FocusIntent {
  /**
   * True only when the element focused at intent creation is still
   * focused now, and that element is not a sacred focus target.
   */
  shouldFocus(): boolean;
}

/**
 * Captures `document.activeElement` at the moment a terminal pane begins
 * mounting. Call once per pane instance, at component initialization —
 * not inside the async `start()` continuation, which may run long after
 * focus has moved elsewhere.
 */
export function createInitialFocusIntent(): FocusIntent {
  const capturedElement = document.activeElement;
  return {
    shouldFocus(): boolean {
      if (document.activeElement !== capturedElement) return false;
      return !focusIsSacred(capturedElement);
    },
  };
}
