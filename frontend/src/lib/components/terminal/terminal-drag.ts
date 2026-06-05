import type { WorkflowTabKey } from "./terminal-layout";

export const WORKFLOW_TAB_DRAG_MIME = "application/x-middleman-workflow-tab";
export const RUNTIME_SESSION_DRAG_MIME = "application/x-middleman-runtime-session";

interface RuntimeSessionDragPayload {
  workspaceId: string;
  sessionKey: string;
}

interface WorkflowTabDragPayload {
  workspaceId: string;
  tabKey: WorkflowTabKey;
}

let activeRuntimeSessionDrag: RuntimeSessionDragPayload | null = null;
let activeWorkflowTabDrag: WorkflowTabDragPayload | null = null;
let activeRuntimeSessionDragToken: string | null = null;
let activeWorkflowTabDragToken: string | null = null;
let dragTokenSequence = 0;

export function startRuntimeSessionDrag(event: DragEvent, payload: RuntimeSessionDragPayload): void {
  const token = newDragToken();
  activeRuntimeSessionDrag = payload;
  activeRuntimeSessionDragToken = token;
  activeWorkflowTabDrag = null;
  activeWorkflowTabDragToken = null;
  writeDragToken(event, RUNTIME_SESSION_DRAG_MIME, token);
  writePlainTextLabel(event, "Middleman terminal session");
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
  }
}

export function startWorkflowTabDrag(event: DragEvent, payload: WorkflowTabDragPayload): void {
  const token = newDragToken();
  activeWorkflowTabDrag = payload;
  activeWorkflowTabDragToken = token;
  const sessionKey = sessionKeyForTab(payload.tabKey);
  activeRuntimeSessionDrag = sessionKey ? { workspaceId: payload.workspaceId, sessionKey } : null;
  activeRuntimeSessionDragToken = activeRuntimeSessionDrag ? token : null;
  writeDragToken(event, WORKFLOW_TAB_DRAG_MIME, token);
  if (activeRuntimeSessionDrag) {
    writeDragToken(event, RUNTIME_SESSION_DRAG_MIME, token);
  } else {
    activeRuntimeSessionDragToken = null;
  }
  writePlainTextLabel(event, "Middleman workflow tab");
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
  }
}

export function clearActiveTerminalDrag(): void {
  activeRuntimeSessionDrag = null;
  activeWorkflowTabDrag = null;
  activeRuntimeSessionDragToken = null;
  activeWorkflowTabDragToken = null;
}

export function readRuntimeSessionDrag(event: DragEvent, workspaceId: string): string | null {
  const payload = readRuntimeSessionDragPayload(event) ?? activeRuntimeSessionDrag;
  if (!payload || payload.workspaceId !== workspaceId || !payload.sessionKey) {
    return null;
  }
  return payload.sessionKey;
}

export function readWorkflowTabDrag(event: DragEvent, workspaceId: string): WorkflowTabKey | null {
  const workflowPayload = readWorkflowTabDragPayload(event) ?? activeWorkflowTabDrag;
  if (workflowPayload?.workspaceId === workspaceId && isWorkflowTabKey(workflowPayload.tabKey)) {
    return workflowPayload.tabKey;
  }
  const sessionKey = readRuntimeSessionDrag(event, workspaceId);
  return sessionKey ? `session:${sessionKey}` : null;
}

export function isWorkflowTabKey(value: string): value is WorkflowTabKey {
  return value === "home" || value === "shell" || value === "terminal" || value.startsWith("session:");
}

function readWorkflowTabDragPayload(event: DragEvent): WorkflowTabDragPayload | null {
  const payload = readTokenPayload(event, WORKFLOW_TAB_DRAG_MIME, activeWorkflowTabDragToken, activeWorkflowTabDrag);
  if (!payload?.workspaceId || !payload.tabKey) return null;
  if (!isWorkflowTabKey(payload.tabKey)) return null;
  return {
    workspaceId: payload.workspaceId,
    tabKey: payload.tabKey,
  };
}

function readRuntimeSessionDragPayload(event: DragEvent): RuntimeSessionDragPayload | null {
  const payload = readTokenPayload(
    event,
    RUNTIME_SESSION_DRAG_MIME,
    activeRuntimeSessionDragToken,
    activeRuntimeSessionDrag,
  );
  if (!payload?.workspaceId || !payload.sessionKey) return null;
  return {
    workspaceId: payload.workspaceId,
    sessionKey: payload.sessionKey,
  };
}

function readTokenPayload<T>(
  event: DragEvent,
  mime: string,
  activeToken: string | null,
  activePayload: T | null,
): T | null {
  const token = event.dataTransfer?.getData(mime) ?? "";
  if (token) {
    return token === activeToken ? activePayload : null;
  }
  return activePayload;
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

function sessionKeyForTab(tabKey: WorkflowTabKey): string | null {
  return tabKey.startsWith("session:") ? tabKey.slice("session:".length) : null;
}
