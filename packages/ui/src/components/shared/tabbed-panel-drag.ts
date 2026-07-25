export const TABBED_PANEL_TAB_DRAG_MIME = "application/x-middleman-tabbed-panel-tab";

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

export interface TabbedPanelTabDragPayload {
  scope: string;
  tabKey: string;
}

let activeTabbedPanelTabDrag: TabbedPanelTabDragPayload | null = null;
let activeTabbedPanelTabDragToken: string | null = null;
let dragTokenSequence = 0;

export function startTabbedPanelTabDrag(
  event: DragEvent,
  payload: TabbedPanelTabDragPayload,
  plainTextLabel = "Middleman panel tab",
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
