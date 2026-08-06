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

/**
 * The `sessionHostKey` prefix shared by every session of one workspace on one
 * host, so a view can reconcile its own workspace's entries without disturbing
 * terminals another surface is keeping alive.
 */
export function sessionHostPrefix(workspaceId: string, hostKey: string | undefined): string {
  return [workspaceId, hostKey ?? ""].map(encodeURIComponent).join("/") + "/";
}

/** What the pool needs to render one session's terminal. */
export interface MountedSession {
  hostKey: SessionHostKey;
  websocketPath: string;
  status: string;
  disabled?: boolean;
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
  slotVisible[key] = false;
}

/**
 * Give up a slot, but only if it is still the registered one.
 *
 * Promotion mounts the destination slot and unmounts the source slot in the same
 * flush, in whichever order Svelte picks. An unconditional clear would let the
 * departing slot's cleanup wipe the arriving slot's registration and leave the
 * terminal parked with nowhere to go.
 */
export function releaseSessionSlot(key: SessionHostKey, el: HTMLElement): void {
  if (slotEls[key] !== el) return;
  slotEls[key] = null;
  slotVisible[key] = false;
}

/**
 * Publish whether the registered slot is on screen.
 *
 * Separate from element registration because they change independently: an
 * inactive tab panel keeps its slot mounted under `visibility: hidden`, so
 * presence in the DOM says nothing about whether the user can see the terminal.
 *
 * Scoped to the owning element for the same reason as `releaseSessionSlot`: a
 * superseded slot must not be able to hide, or reveal, the terminal now shown
 * somewhere else.
 */
export function setSessionSlotVisible(key: SessionHostKey, el: HTMLElement, visible: boolean): void {
  if (slotEls[key] !== el) return;
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
      // removed along with the slot and the websocket dies with it. Only when
      // this slot still owns the key — during a promotion the terminal may
      // already have moved to the destination slot.
      if (slotEls[key] === node) {
        const parking = parkingEl;
        const wrapper = node.firstElementChild;
        if (parking !== null && wrapper !== null) parking.appendChild(wrapper);
      }
      releaseSessionSlot(key, node);
    };
  };
}

export function registerSessionParking(el: HTMLElement | null): void {
  parkingEl = el;
}

export function getSessionParking(): HTMLElement | null {
  return parkingEl;
}

/**
 * Sessions the pool keeps live. Promotion never changes this set: a promoted
 * session is the same session in a different slot.
 *
 * Append-only ordering is load-bearing. The pool renders these in a keyed
 * `{#each}` whose wrappers are reparented out of its own fragment, so a new item
 * is inserted relative to whichever sibling node the block still believes is
 * next. Appending means that anchor is always the block's own trailing one,
 * which never moves; reordering this list would insert a wrapper into another
 * session's slot.
 */
export function mountedSessions(): readonly MountedSession[] {
  return mounted;
}

export function isSessionMounted(key: SessionHostKey): boolean {
  return mounted.some((session) => session.hostKey === key);
}

export function noteSessionMounted(session: MountedSession): void {
  const existing = mounted.find((candidate) => candidate.hostKey === session.hostKey);
  if (existing) {
    if (
      existing.websocketPath === session.websocketPath &&
      existing.status === session.status &&
      (existing.disabled ?? false) === (session.disabled ?? false)
    ) {
      return;
    }
    // Replaced in place rather than appended: see the ordering note above.
    mounted = mounted.map((candidate) => (candidate.hostKey === session.hostKey ? session : candidate));
    return;
  }
  mounted = [...mounted, session];
}

export function noteSessionUnmounted(key: SessionHostKey): void {
  if (!isSessionMounted(key)) return;
  // A focus request this session never got to consume dies with it. Left armed, it
  // waits for a subtree under the same key to mount - a revisit, or the pane being
  // reopened for its own reasons - and steals focus for a Focus Terminal the user
  // pressed long before, somewhere else.
  if (pendingFocusKey === key) {
    pendingFocusKey = null;
    pendingFocusSoft = false;
  }
  mounted = mounted.filter((session) => session.hostKey !== key);
  registerSessionSlot(key, null);
}

const exitListeners = new Set<(key: SessionHostKey, code: number) => void>();

/**
 * A pooled terminal's exit, routed back to whoever mounted the session.
 *
 * The pool renders every workspace's terminals and has no access to the runtime
 * session records, so it reports the key and lets the mounter decide what a exit
 * means for that session.
 */
export function onSessionExited(cb: (key: SessionHostKey, code: number) => void): () => void {
  exitListeners.add(cb);
  return () => exitListeners.delete(cb);
}

export function noteSessionExited(key: SessionHostKey, code: number): void {
  for (const listener of [...exitListeners]) listener(key, code);
}

// A Focus Terminal aimed at a promoted session whose terminal is not on screen
// yet. The pool mounts and reparents across a tick and a frame, so the store
// cannot focus the wrapper the moment it reveals the pane - and unlike the
// workspace host, a pooled session has no placement effect of its own to hand the
// request to. Deliberately a single slot: only one focus request can be
// outstanding, and a newer one supersedes it.
let pendingFocusKey = $state<SessionHostKey | null>(null);
let pendingFocusSoft = false;

/**
 * Queue a focus request for a session's terminal. Explicit requests (Focus
 * Terminal, a fresh launch) always land. Soft requests come from navigation —
 * a detail surface switched to another item — and the pool declines them when
 * focus is somewhere sacred, so switching PRs never pulls the keyboard out of
 * a form field or dialog.
 */
export function requestSessionFocus(key: SessionHostKey, opts?: { soft?: boolean }): void {
  pendingFocusKey = key;
  pendingFocusSoft = opts?.soft === true;
}

/** Drop an outstanding request, for when focus moves somewhere else entirely. */
export function clearSessionFocusRequest(): void {
  pendingFocusKey = null;
  pendingFocusSoft = false;
}

/** Read the outstanding request without consuming it so a Svelte effect can react to it safely. */
export function pendingSessionFocus(key: SessionHostKey): "explicit" | "soft" | false {
  if (pendingFocusKey !== key) return false;
  return pendingFocusSoft ? "soft" : "explicit";
}

/**
 * The outstanding request's flavor when this session owns it, which it
 * consumes; false otherwise.
 */
export function consumeSessionFocus(key: SessionHostKey): "explicit" | "soft" | false {
  if (pendingFocusKey !== key) return false;
  pendingFocusKey = null;
  const soft = pendingFocusSoft;
  pendingFocusSoft = false;
  return soft ? "soft" : "explicit";
}

export function resetSessionHostForTest(): void {
  parkingEl = null;
  for (const key of Object.keys(slotEls)) delete slotEls[key];
  for (const key of Object.keys(slotVisible)) delete slotVisible[key];
  mounted = [];
  pendingFocusKey = null;
  pendingFocusSoft = false;
  exitListeners.clear();
}
