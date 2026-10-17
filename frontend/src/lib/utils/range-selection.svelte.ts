/**
 * File-manager style row selection for sidebar lists: shift-click selects the
 * on-screen range from the anchor, Cmd/Ctrl-click toggles a row, and a plain
 * click clears the selection and opens the row. The opened row is
 * route-owned; it only seeds the selection and anchors the first range.
 */
export function createRangeSelection<T>(options: {
  /** Rows in on-screen order; rows hidden by collapse or filters are excluded. */
  rows: () => readonly T[];
  key: (row: T) => string;
  openedKey: () => string | null;
}) {
  let selectedKeys = $state.raw<ReadonlySet<string>>(new Set());
  let anchorKey = $state<string | null>(null);
  // Rows that scrolled out of the list (filtered, collapsed, or removed)
  // drop out of the selection instead of being acted on invisibly.
  const selected = $derived(options.rows().filter((row) => selectedKeys.has(options.key(row))));
  const visibleSelectedKeys = $derived(new Set(selected.map(options.key)));

  function renderedOpenedKey(keys: readonly string[]): string | null {
    const opened = options.openedKey();
    return opened !== null && keys.includes(opened) ? opened : null;
  }

  function selectRange(row: T): void {
    const keys = options.rows().map(options.key);
    const clicked = options.key(row);
    const anchor =
      (anchorKey !== null && keys.includes(anchorKey) ? anchorKey : null) ?? renderedOpenedKey(keys) ?? clicked;
    const from = keys.indexOf(anchor);
    const to = keys.indexOf(clicked);
    selectedKeys = new Set(keys.slice(Math.min(from, to), Math.max(from, to) + 1));
    anchorKey = anchor;
  }

  function toggle(row: T): void {
    const key = options.key(row);
    const next = new Set(visibleSelectedKeys);
    if (next.size === 0) {
      const opened = renderedOpenedKey(options.rows().map(options.key));
      if (opened !== null) next.add(opened);
    }
    if (next.has(key)) next.delete(key);
    else next.add(key);
    selectedKeys = next;
    anchorKey = key;
  }

  function clear(): void {
    if (selectedKeys.size === 0 && anchorKey === null) return;
    selectedKeys = new Set();
    anchorKey = null;
  }

  return {
    /** Selected rows in on-screen order. */
    get selected(): readonly T[] {
      return selected;
    },
    get active(): boolean {
      return selected.length > 0;
    },
    has(row: T): boolean {
      return visibleSelectedKeys.has(options.key(row));
    },
    clear,
    /** Handles a row click; returns true when the click should open the row. */
    activate(event: MouseEvent | KeyboardEvent, row: T): boolean {
      if (event.shiftKey) {
        selectRange(row);
        return false;
      }
      if (event.metaKey || event.ctrlKey) {
        toggle(row);
        return false;
      }
      clear();
      return true;
    },
  };
}

/** Stops shift-click from selecting the text between rows. */
export function preventShiftTextSelection(event: MouseEvent): void {
  if (event.shiftKey) event.preventDefault();
}
