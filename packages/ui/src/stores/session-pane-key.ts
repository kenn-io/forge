/**
 * The layout identity of a promoted terminal session.
 *
 * Deliberately NOT the registry identity: that one also carries the session's
 * `created_at` generation, so a relaunched session gets a fresh live subtree.
 * This one omits it, so the relaunched session reappears in the pane the user
 * put it in.
 */

const SESSION_PANE_PREFIX = "session:";

export interface SessionPaneRef {
  workspaceId: string;
  hostKey: string | undefined;
  sessionKey: string;
}

/**
 * Percent-encoded parts joined with `/`, never colon-separated.
 *
 * Session keys are opaque and routinely contain colons (`ws-1:helper`), so a
 * `session:<workspace>:<host>:<session>` form could not be parsed back and two
 * different sessions could spell one key — which would alias their placements
 * and their cleanup.
 */
export function sessionPaneKey(workspaceId: string, hostKey: string | undefined, sessionKey: string): string {
  return SESSION_PANE_PREFIX + [workspaceId, hostKey ?? "", sessionKey].map(encodeURIComponent).join("/");
}

/** Null unless the key is well-formed: three segments, all decodable, and only
 * the host allowed to be empty (that is how the provider default host is
 * spelled). A malformed key from an older build must be prunable, not kept
 * forever by a prefix check. */
export function parseSessionPaneKey(key: string): SessionPaneRef | null {
  if (!key.startsWith(SESSION_PANE_PREFIX)) return null;
  const parts = key.slice(SESSION_PANE_PREFIX.length).split("/");
  if (parts.length !== 3) return null;
  let decoded: string[];
  try {
    decoded = parts.map((part) => decodeURIComponent(part));
  } catch {
    return null;
  }
  const [workspaceId, hostKey, sessionKey] = decoded as [string, string, string];
  if (workspaceId === "" || sessionKey === "") return null;
  return { workspaceId, hostKey: hostKey === "" ? undefined : hostKey, sessionKey };
}

export function isSessionPaneKey(key: string): boolean {
  return parseSessionPaneKey(key) !== null;
}

/** Whether a pane key belongs to this workspace on this host. */
export function sessionPaneKeyMatchesWorkspace(key: string, workspaceId: string, hostKey: string | undefined): boolean {
  const ref = parseSessionPaneKey(key);
  return ref !== null && ref.workspaceId === workspaceId && ref.hostKey === hostKey;
}
