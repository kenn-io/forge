import type { LaunchTarget, RuntimeSession, WorkspaceRuntime } from "./types.js";

export type WorkspaceRuntimeState = Omit<WorkspaceRuntime, "launch_targets" | "sessions"> & {
  launch_targets: LaunchTarget[];
  sessions: RuntimeSession[];
};

function basePath(): string {
  const path = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";
  return path.replace(/\/$/, "");
}

function wsBaseUrl(): string {
  return `${basePath()}/ws/v1`;
}

export function executionHostPrefix(hostKey?: string): string {
  if (hostKey?.startsWith("devbox:")) return `/devboxes/${encodeURIComponent(hostKey.slice(7))}`;
  return hostKey ? `/fleet/hosts/${encodeURIComponent(hostKey)}` : "";
}

export function workspaceSessionWebSocketPath(workspaceId: string, sessionKey: string, hostKey?: string): string {
  return (
    `${wsBaseUrl()}${executionHostPrefix(hostKey)}/workspaces/${encodeURIComponent(workspaceId)}` +
    `/runtime/sessions/${encodeURIComponent(sessionKey)}` +
    "/terminal"
  );
}

export function workspaceTmuxWebSocketPath(workspaceId: string, hostKey?: string): string {
  return `${wsBaseUrl()}${executionHostPrefix(hostKey)}/workspaces/${encodeURIComponent(workspaceId)}` + "/terminal";
}
