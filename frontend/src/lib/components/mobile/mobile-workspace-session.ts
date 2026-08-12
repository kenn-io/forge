import type { RuntimeSession } from "../../api/types.js";

function storageKey(workspaceId: string, hostKey?: string): string {
  return `kenn-forge:mobile-workspace-session:${encodeURIComponent(hostKey ?? "local")}:${encodeURIComponent(workspaceId)}`;
}

function getStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

export function loadMobileWorkspaceSession(workspaceId: string, hostKey?: string): string | null {
  try {
    return getStorage()?.getItem(storageKey(workspaceId, hostKey)) ?? null;
  } catch {
    return null;
  }
}

export function saveMobileWorkspaceSession(
  workspaceId: string,
  hostKey: string | undefined,
  sessionKey: string | null,
): void {
  const storage = getStorage();
  if (!storage) return;
  try {
    if (sessionKey === null) storage.removeItem(storageKey(workspaceId, hostKey));
    else storage.setItem(storageKey(workspaceId, hostKey), sessionKey);
  } catch {
    // Selection remains valid for the mounted view when storage is unavailable.
  }
}

export function selectMobileWorkspaceSession(
  sessions: readonly RuntimeSession[],
  preferredKey: string | null,
): string | null {
  if (preferredKey && sessions.some((session) => session.key === preferredKey)) {
    return preferredKey;
  }
  return sessions[0]?.key ?? null;
}
