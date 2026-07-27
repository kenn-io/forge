import type { Attachment } from "svelte/attachments";

/**
 * Registry of live per-session terminal subtrees.
 *
 * `workspace-host.svelte.ts` keeps exactly one live workspace subtree and
 * reparents it between slots. That singleton cannot hold sessions once a session
 * can be promoted out of the workspace pane into a detail pane of its own, so
 * this is the same idea one level down: one live subtree PER SESSION, reparented
 * into whichever slot renders it and parked when none does.
 */

export type SessionHostKey = string;

/**
 * A session's registry identity.
 *
 * Workspace and fleet host are part of it because a session key is unique only
 * within a workspace on a host: two workspaces both having an `agent` session is
 * the normal case, and a bare key would alias their terminals.
 *
 * `generation` is the session's `created_at`. Without it a session relaunched
 * under a reused key would adopt the dead session's subtree and its closed
 * socket. The layout's pane key deliberately omits the generation, so a
 * relaunched session reappears in the pane the user put it in.
 */
export function sessionHostKey(
  workspaceId: string,
  hostKey: string | undefined,
  sessionKey: string,
  generation: string,
): SessionHostKey {
  return [workspaceId, hostKey ?? "", sessionKey, generation].map(encodeURIComponent).join("/");
}

/** What the pool needs to render one session's terminal. */
export interface MountedSession {
  hostKey: SessionHostKey;
  websocketPath: string;
  status: string;
}

let parkingEl: HTMLElement | null = null;
const slotEls = $state<Record<SessionHostKey, HTMLElement | null>>({});
const slotVisible = $state<Record<SessionHostKey, boolean>>({});
let mounted = $state<readonly MountedSession[]>([]);

export function registerSessionSlot(key: SessionHostKey, el: HTMLElement | null): void {
  // A targeted property write, not `slotEls = { ...slotEls, [key]: el }`. This
  // runs from inside an attachment's own effect, and a spread reassignment reads
  // every key while writing the same binding, which Svelte treats as an effect
  // that depends on itself (effect_update_depth_exceeded). The targeted form
  // still invalidates fine-grained readers of `slotEls[key]`.
  slotEls[key] = el;
  if (el === null) slotVisible[key] = false;
}

/**
 * Publish whether the registered slot is on screen.
 *
 * Separate from element registration because they change independently: an
 * inactive tab panel keeps its slot mounted under `visibility: hidden`, so
 * presence in the DOM says nothing about whether the user can see the terminal.
 */
export function setSessionSlotVisible(key: SessionHostKey, visible: boolean): void {
  slotVisible[key] = visible;
}

export function getSessionSlotElement(key: SessionHostKey): HTMLElement | null {
  return slotEls[key] ?? null;
}

/**
 * Whether the session is on screen, which is the only thing that may make its
 * terminal active. A terminal left active behind a hidden tab claims focus and
 * competes for keystrokes with the visible one.
 */
export function isSessionSlotVisible(key: SessionHostKey): boolean {
  return slotEls[key] != null && slotVisible[key] === true;
}

export function sessionSlotAttachment(key: SessionHostKey): Attachment<HTMLElement> {
  return (node) => {
    registerSessionSlot(key, node);
    return () => {
      // Safety net: park the subtree before this slot leaves the DOM, or it is
      // removed along with the slot and the websocket dies with it.
      const parking = parkingEl;
      const wrapper = node.firstElementChild;
      if (parking !== null && wrapper !== null) parking.appendChild(wrapper);
      registerSessionSlot(key, null);
    };
  };
}

export function registerSessionParking(el: HTMLElement | null): void {
  parkingEl = el;
}

export function getSessionParking(): HTMLElement | null {
  return parkingEl;
}

/** Sessions the pool keeps live. Promotion never changes this set: a promoted
 * session is the same session in a different slot. */
export function mountedSessions(): readonly MountedSession[] {
  return mounted;
}

export function isSessionMounted(key: SessionHostKey): boolean {
  return mounted.some((session) => session.hostKey === key);
}

export function noteSessionMounted(session: MountedSession): void {
  const existing = mounted.find((candidate) => candidate.hostKey === session.hostKey);
  if (existing) {
    if (existing.websocketPath === session.websocketPath && existing.status === session.status) return;
    mounted = mounted.map((candidate) => (candidate.hostKey === session.hostKey ? session : candidate));
    return;
  }
  mounted = [...mounted, session];
}

export function noteSessionUnmounted(key: SessionHostKey): void {
  if (!isSessionMounted(key)) return;
  mounted = mounted.filter((session) => session.hostKey !== key);
  registerSessionSlot(key, null);
}

export function resetSessionHostForTest(): void {
  parkingEl = null;
  for (const key of Object.keys(slotEls)) delete slotEls[key];
  for (const key of Object.keys(slotVisible)) delete slotVisible[key];
  mounted = [];
}
