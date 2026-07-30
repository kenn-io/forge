export const TABBED_PANEL_TAB_DRAG_MIME = "application/x-kenn-forge-tabbed-panel-tab";

/**
 * Drag scope for a workspace's own panel tree.
 *
 * Scope comparison is plain string equality, so every tree that uses this
 * primitive has to namespace its scope or two unrelated trees become mutually
 * droppable. Built here rather than inline at the call site so the namespaces stay
 * in one place with the detail surfaces' `detail:<surface>`.
 */
export function workspaceTabDragScope(workspaceId: string): string {
  return `workspace:${workspaceId}`;
}

/**
 * Reject a scope that is not namespaced.
 *
 * Scope comparison is plain string equality, so a bare id — a workspace id, a
 * surface key — is one collision away from making two unrelated trees mutually
 * droppable. Throwing is deliberate: the alternative is a silent cross-tree drop
 * that only shows up as a pane landing somewhere impossible.
 */
export function assertNamespacedDragScope(scope: string): string {
  if (!/^[a-z-]+:.+/.test(scope)) {
    throw new Error(`tabbed panel drag scope must be namespaced as "<kind>:<id>", got ${JSON.stringify(scope)}`);
  }
  return scope;
}

export interface TabbedPanelTabDragPayload {
  scope: string;
  tabKey: string;
}

let activeTabbedPanelTabDrag: TabbedPanelTabDragPayload | null = null;
let activeTabbedPanelTabDragToken: string | null = null;
let dragTokenSequence = 0;
const dragEndListeners = new Set<() => void>();

export function startTabbedPanelTabDrag(
  event: DragEvent,
  payload: TabbedPanelTabDragPayload,
  plainTextLabel = "Kenn Forge panel tab",
): void {
  const token = newDragToken();
  activeTabbedPanelTabDrag = payload;
  activeTabbedPanelTabDragToken = token;
  writeDragToken(event, TABBED_PANEL_TAB_DRAG_MIME, token);
  writePlainTextLabel(event, plainTextLabel);
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
  }
}

export function readTabbedPanelTabDrag(event: DragEvent, scope: string): string | null {
  const payload = readTabbedPanelTabDragPayload(event);
  if (!payload || payload.scope !== scope || !payload.tabKey) {
    return null;
  }
  return payload.tabKey;
}

export function clearActiveTabbedPanelDrag(): void {
  activeTabbedPanelTabDrag = null;
  activeTabbedPanelTabDragToken = null;
  for (const listener of dragEndListeners) listener();
}

/**
 * Called when any drag ends, wherever it ended.
 *
 * A tab strip clears its own drag state from the dragged tab's `dragend`, which
 * never fires when the drop moved that tab into another leaf: the element is gone
 * before the event would reach it, and the strip it left keeps the gap and the
 * dragging styling. The strip that ACCEPTED the drop also adopts the dragged key to
 * preview an insertion, so "this leaf no longer holds it" cannot tell a leftover
 * from a live preview - only the end of the drag can.
 */
export function onTabbedPanelDragEnd(listener: () => void): () => void {
  dragEndListeners.add(listener);
  return () => dragEndListeners.delete(listener);
}

function readTabbedPanelTabDragPayload(event: DragEvent): TabbedPanelTabDragPayload | null {
  const token = event.dataTransfer?.getData(TABBED_PANEL_TAB_DRAG_MIME) ?? "";
  if (token && token !== activeTabbedPanelTabDragToken) return null;
  return activeTabbedPanelTabDrag;
}

function writeDragToken(event: DragEvent, mime: string, token: string): void {
  event.dataTransfer?.setData(mime, token);
}

function writePlainTextLabel(event: DragEvent, label: string): void {
  event.dataTransfer?.setData("text/plain", label);
}

function newDragToken(): string {
  return globalThis.crypto?.randomUUID?.() ?? `drag-${++dragTokenSequence}`;
}
