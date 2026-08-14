import type { Snippet } from "svelte";
import type { Attachment } from "svelte/attachments";
import type {
  InlineDockMode,
  InlineWorkspaceController,
  InlineWorkspaceSurface,
  PromotableSession,
  WorkspaceItemIdentity,
  WorkspaceRefLite,
} from "../workspace-inline.js";
import { canonicalItemType } from "../workspace-inline.js";
import { parseSessionPaneKey, sessionPaneKeyMatchesWorkspace } from "./session-pane-key.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
import {
  clearCreatedWorkspaceById,
  createdWorkspaceRef,
  isWorkspaceIdDeleted,
  markWorkspaceIdDeleted,
  nextWorkspaceLifecycleTick,
  resolveControllerlessWorkspaceRef,
} from "./workspace-create-pending.svelte.js";
import { getStackDepth } from "./keyboard/modal-stack.svelte.js";
import { getPaneLayoutStore, resetPaneLayoutStoresForTest } from "./paneLayout.svelte.js";
import {
  clearSessionFocusRequest,
  discardSessionsWithPrefix,
  getSessionSlotElement,
  requestSessionFocus,
  sessionHostPrefix,
} from "./session-host.svelte.ts";
import { forgetWorkspaceRoute, getRoute, navigate, replaceUrl } from "./router.svelte.ts";

export type HostedWorkspaceKey = { workspaceId: string; hostKey: string | undefined };
export type HostSlot = "tab" | InlineWorkspaceSurface;

type InlineClaim = { identity: WorkspaceItemIdentity; ref: WorkspaceRefLite };

/** Every detail surface that can host an inline workspace, for cross-surface cleanup. */
const INLINE_SURFACES: readonly InlineWorkspaceSurface[] = ["prs", "issues", "activity"];

const PAGE_SURFACE: Partial<Record<string, InlineWorkspaceSurface>> = {
  activity: "activity",
  pulls: "prs",
  issues: "issues",
};

// A tombstone remembers WHICH workspace was deleted: it must keep masking
// stale envelopes that still carry the dead ID, but an envelope carrying a
// different ID is a recreation (Workspaces tab, another client) that must
// surface immediately — an ID-less tombstone would hide it forever, since
// the "workspace absent" envelope it waits for never arrives.
type DeletionTombstone = { deletedId: string };

// A positive override remembers WHEN the creation was recorded (a shared
// lifecycle tick): a "no workspace" envelope can only clear it when its
// request started after that tick — a stale pre-create fetch must not
// wipe a creation, but a post-create fetch reporting the workspace absent
// is authoritative (another client deleted it).
type CreatedOverride = { ref: WorkspaceRefLite; tick: number };
type Override = CreatedOverride | DeletionTombstone;

function isTombstone(override: Override | undefined): override is DeletionTombstone {
  return override !== undefined && "deletedId" in override;
}

let claims = $state<Partial<Record<InlineWorkspaceSurface, InlineClaim>>>({});
let overrides = $state<Record<string, Override>>({});
// Sticky key: parking must not tear down the live workspace.
let lastInlineKey = $state<HostedWorkspaceKey>({ workspaceId: "", hostKey: undefined });

let hostEl: HTMLElement | null = null;
let parkingEl: HTMLElement | null = null;
// Set by focusTerminal() only when its own synchronous focus attempt
// couldn't land (host still parked/inert, e.g. expanding from a collapsed
// dock whose slot element hasn't mounted yet). WorkspaceHost's placement
// effect consumes it once the host actually reveals in its destination.
// Cleared on consumption and on park, so a request that never gets a
// reveal (mode flips back to collapsed, claim released) can't steal focus
// on some later, unrelated reveal.
let pendingHostFocus = false;
// Reactive: WorkspaceHost's placement effect must rerun when a slot's element
// registers/unregisters (page mounts happen after route changes).
let slotEls = $state<Partial<Record<HostSlot, HTMLElement | null>>>({});
const invalidationListeners = new Set<(identity: WorkspaceItemIdentity) => void>();
// The hosted workspace's sessions, published by the live view: only it knows the
// runtime, the labels, and each session's generation. Scoped to one workspace
// because there is one host — a stale list would offer a detail surface panes for
// a workspace it is no longer showing.
type PublishedSession = PromotableSession & {
  /** Registry key (carries the generation), which the pane's slot needs. */
  hostKey: string;
  /**
   * The session the workspace pane currently shows, which is the one a keyboard
   * command promotes. Only the view can decide it: "current" means the active
   * workflow tab when a session owns it, otherwise the open dock's active tab.
   */
  active: boolean;
};
let hostedSessions = $state<{ key: HostedWorkspaceKey; sessions: PublishedSession[] }>({
  key: { workspaceId: "", hostKey: undefined },
  sessions: [],
});
// Supplied by WorkspaceHost, because a snippet can only be written in a
// component: the terminal side stays in the frontend and the views never touch
// the session registry.
let sessionPaneSnippet = $state<Snippet<[{ paneKey: string; visible: boolean }]> | null>(null);
// The hosted workspace's own controls (presets, zoom, terminal options, launch),
// for the same reason: every one of them is wired to the live view's state, so the
// view hands over the rendered chrome rather than the state behind it. Null while
// no view is embedded, which is what tells a detail pane not to offer the button.
let hostedControls = $state<HostedWorkspaceControls | null>(null);
// The workspace key whose controls have a write in flight, or null. The popover
// holding them must not be dismissed mid-save: it owns the pending feedback, and
// unmounting it would strand the user with no idea whether the save landed.
//
// Keyed rather than a bare flag: one embedded view serves every selection on its
// surface, and a write it started for one workspace can outlive the switch to the
// next (the view only clears its own pending flag when the identity still matches).
// A global flag would then be stuck on and hold the NEXT workspace's popover open
// forever, which is worse than the dismissal it was protecting.
let hostedControlsBusy = $state<string | null>(null);
// The pane the user last worked in for a given workspace: the container, or one of
// its promoted session panes. Keyed by WORKSPACE rather than by surface, because
// that is what has to survive a promotion, a demotion, and a trip through another
// selection - the surface's own last-focused tab is whatever pane the user touched
// most recently, which is just as often the conversation.
let lastFocusedWorkspacePane = $state<Record<string, string>>({});
// What a collapse actually hid, keyed by surface AND workspace. Expanding restores
// exactly this set: a promoted pane the user had already closed by hand must not
// reappear just because collapsing the dock swept every pane of that workspace out
// of sight. Keyed by workspace too, because one surface collapses many of them in
// turn - a surface-only ledger lets workspace B's expand restore A's panes and
// consume the record A still needed.
const collapsedPaneKeys = new Map<string, string[]>();
// Opens the hosted view's launcher overlay. Registered by the embedded view, which
// owns the overlay: a palette command or a Focus Terminal with nothing to focus can
// only reach it through the store.
let launcherOpener = $state<(() => void) | null>(null);
// Claims are released when their view unmounts, but a deletion can arrive
// later from an unrelated surface (Workspaces tab, terminal tab). Without
// this map the deletion would find no claim to tombstone, and the stale
// detail envelope cached by the list view would re-claim the dead workspace
// on the next visit until a refetch. Deliberately kept after release;
// entries are dropped on deletion or test reset.
const workspaceIdentityById = new Map<string, WorkspaceItemIdentity>();

function workspaceKeyString(key: HostedWorkspaceKey): string {
  return `${key.workspaceId}\u0000${key.hostKey ?? ""}`;
}

function collapseLedgerKey(surface: InlineWorkspaceSurface): string {
  return `${surface}\u0000${workspaceKeyString(desiredKey())}`;
}

function identityKey(identity: WorkspaceItemIdentity): string {
  // Route segments may carry provider aliases (gh/gl/fj) and callers carry
  // native item-type vocabularies ("pr"/"pull_request" vs "pull") while
  // store data uses canonical names; the same item must never key two
  // claim/override slots or a tombstone could be missed. The item type is
  // part of the key: a PR and an issue can share a repository and number
  // yet own unrelated workspaces.
  return [
    canonicalProvider(identity.provider),
    canonicalItemType(identity.itemType),
    resolvedPlatformHost(identity.provider, identity.platformHost),
    identity.owner,
    identity.name,
    identity.repoPath,
    String(identity.number),
  ].join("\n");
}

function sameIdentity(a: WorkspaceItemIdentity, b: WorkspaceItemIdentity): boolean {
  return identityKey(a) === identityKey(b);
}

function sameRef(a: WorkspaceRefLite | null, b: WorkspaceRefLite | null): boolean {
  return a?.id === b?.id && a?.status === b?.status;
}

export function desiredSlot(): HostSlot | null {
  const page = getRoute().page;
  if (page === "workspaces" || page === "terminal") return "tab";
  const surface = PAGE_SURFACE[page];
  if (surface && claims[surface]) return surface;
  return null;
}

export function desiredKey(): HostedWorkspaceKey {
  const route = getRoute();
  if (route.page === "terminal") {
    return { workspaceId: route.workspaceId, hostKey: route.hostKey };
  }
  if (route.page === "workspaces") {
    return { workspaceId: "", hostKey: undefined };
  }
  // The visible surface's claim is authoritative: `lastInlineKey` is a
  // global that late writes (a stale terminal-route key, another surface's
  // claim) can move, and following it here would switch the hosted
  // workspace underneath a dock that is actively displaying a different
  // claim. Fall back to it only while parked, where it keeps the live
  // socket alive across page detours.
  const surface = PAGE_SURFACE[route.page];
  const claim = surface ? claims[surface] : undefined;
  if (claim) return { workspaceId: claim.ref.id, hostKey: undefined };
  return lastInlineKey;
}

/**
 * The inline dock mode is a VIEW of the surface's pane layout, not state of its
 * own: `expanded` is the workspace pane's leaf holding the zoom, `collapsed` is
 * the pane hidden, `split` is anything else. Storing it separately would let the
 * two disagree — a pane maximized from its own leaf controls while the dock
 * still reported "split".
 */
const WORKSPACE_PANE_KEY = "workspace";

/**
 * Every pane of the hosted workspace on this surface: the container, plus each of
 * its sessions the user promoted out of it.
 *
 * The dock is a view of all of them together. A workspace whose container is
 * hidden while one of its terminals sits in a pane of its own is not collapsed -
 * the workspace is right there - and reporting "collapsed" would offer a Show
 * Terminal button for something already on screen.
 */
function workspacePaneKeysFor(surface: InlineWorkspaceSurface): string[] {
  const layout = getPaneLayoutStore(surface);
  const key = desiredKey();
  const keys = [WORKSPACE_PANE_KEY];
  // From the STORED tree, not from the sessions the view is publishing right now.
  // A stopped, exited, or reconnecting session keeps its pane so a relaunch lands
  // back in it, and a pane that dropped out of dock membership over that gap would
  // escape a collapse and then reappear on relaunch, flipping the dock from
  // collapsed to split with no user action.
  for (const tabKey of layout.storedTabKeys()) {
    if (sessionPaneKeyMatchesWorkspace(tabKey, key.workspaceId, key.hostKey)) keys.push(tabKey);
  }
  return keys;
}

/**
 * The pane an expand or a Focus Terminal acts on: the one holding the workspace's
 * last-focused session, or the container when that session has no pane of its own.
 *
 * Maximizing the container when the user's terminal is in a promoted pane would
 * cover the very terminal they asked to see.
 */
function dockTargetPaneKey(surface: InlineWorkspaceSurface): string {
  const remembered = lastFocusedWorkspacePane[workspaceKeyString(desiredKey())];
  if (remembered === undefined || remembered === WORKSPACE_PANE_KEY) return WORKSPACE_PANE_KEY;
  // Stored membership, for the same reason the dock's pane set uses it: a session
  // between generations is briefly absent from the published list, and falling back
  // to the container there would maximize over the pane the user was working in.
  return workspacePaneKeysFor(surface).includes(remembered) ? remembered : WORKSPACE_PANE_KEY;
}

function dockModeFor(surface: InlineWorkspaceSurface): InlineDockMode {
  const layout = getPaneLayoutStore(surface);
  const keys = workspacePaneKeysFor(surface);
  const hidden = layout.hiddenTabKeys();
  if (keys.every((key) => hidden.includes(key))) return "collapsed";
  const zoomed = layout.zoomedLeafID();
  if (zoomed === null) return "split";
  // Membership in the zoomed leaf is not enough: a workspace pane tabbed behind an
  // active sibling inside that leaf renders nothing, and reporting "expanded" there
  // makes the control refuse the expand the user is asking for.
  return keys.some((key) => !hidden.includes(key) && layout.leafIDForTab(key) === zoomed && layout.isTabActive(key))
    ? "expanded"
    : "split";
}

/**
 * Whether the workspace pane is actually on screen, which is NOT the same as its
 * dock mode: "split" only says the pane is neither hidden nor maximized, while a
 * pane sharing a leaf with an inactive sibling tab, or sitting behind another
 * leaf's zoom, renders nothing. The host is parked in both cases, so conflating
 * the two lets `isHostVisible()` claim a parked host is visible.
 *
 * Read from the physical portal slot rather than derived from the stored tree.
 * The slot element is rendered only while the pane is, so its registration is the
 * one observation that already accounts for availability pruning, the zoom, a
 * sibling tab being active, and the narrow-width flattened strip — where the
 * renderer picks the single visible tab and a tree-only derivation claimed a
 * parked host was on screen.
 */
function workspacePaneVisible(surface: InlineWorkspaceSurface): boolean {
  return getSlotElement(surface) !== null;
}

/**
 * Bring the workspace pane on screen without maximizing it.
 *
 * Unhiding is not enough: the pane may be tabbed behind a sibling, or hidden by a
 * zoom on another leaf. Both leave it structurally present and completely
 * invisible. A zoom held by the workspace's OWN leaf is left alone — revealing
 * must never undo the user's maximize.
 *
 * Noting focus is what makes this work at narrow widths too: the flattened
 * single-strip rendering picks its visible tab from the surface's last-focused
 * one, so activating within the stored tree alone would leave the flat strip on
 * whatever it was already showing.
 */
function revealPane(surface: InlineWorkspaceSurface, tabKey: string): void {
  const layout = getPaneLayoutStore(surface);
  layout.setHidden(tabKey, false);
  layout.activateTab(tabKey);
  layout.noteFocused(tabKey);
  const leafID = layout.leafIDForTab(tabKey);
  const zoomed = layout.zoomedLeafID();
  if (zoomed !== null && zoomed !== leafID) layout.clearZoom();
}

/**
 * Unhide exactly what a collapse of ours hid.
 *
 * Everything, when no collapse is on record - a reload, or panes hidden some
 * other way - but a pane the user closed themselves after collapsing stays
 * closed.
 */
function restoreCollapsedPanes(surface: InlineWorkspaceSurface): void {
  const layout = getPaneLayoutStore(surface);
  const ledgerKey = collapseLedgerKey(surface);
  for (const key of collapsedPaneKeys.get(ledgerKey) ?? workspacePaneKeysFor(surface)) layout.setHidden(key, false);
  collapsedPaneKeys.delete(ledgerKey);
}

function revealWorkspacePane(surface: InlineWorkspaceSurface): void {
  revealPane(surface, WORKSPACE_PANE_KEY);
}

/**
 * Un-maximize the workspace pane, if it is what holds the zoom.
 *
 * A claim replaced directly by another item's (a selection change whose new
 * detail is already cached) gives the layout no availability gap for
 * DetailPaneLayout's reconciliation effect to notice, so the new item's detail
 * would open hidden behind a fullscreen terminal.
 */
function unzoomWorkspacePane(surface: InlineWorkspaceSurface): void {
  const layout = getPaneLayoutStore(surface);
  const zoomed = layout.zoomedLeafID();
  if (zoomed === null) return;
  // Any pane of this workspace, not just the container: a promoted session can hold
  // the zoom too, and leaving that one maximized opens the next item's detail behind
  // a fullscreen terminal belonging to the item the user just left.
  for (const key of workspacePaneKeysFor(surface)) {
    if (layout.leafIDForTab(key) === zoomed) {
      layout.clearZoom();
      return;
    }
  }
}

export function isHostVisible(): boolean {
  const slot = desiredSlot();
  if (slot === null) return false;
  if (slot === "tab") return true;
  return workspacePaneVisible(slot);
}

function effectiveRef(
  identity: WorkspaceItemIdentity,
  envelopeRef: WorkspaceRefLite | null | undefined,
): WorkspaceRefLite | null {
  const override = overrides[identityKey(identity)];
  if (isTombstone(override)) {
    // An envelope survives the masks only when it carries neither this
    // tombstone's deleted ID nor any other workspace deleted this session
    // — an earlier-generation stale envelope is just as dead as the one
    // the tombstone remembers.
    const envelope =
      envelopeRef && envelopeRef.id !== override.deletedId && !isWorkspaceIdDeleted(envelopeRef.id)
        ? envelopeRef
        : null;
    // A controller-less recreation (focus/mobile create after the delete)
    // records only the shared created entry, under a fresh ID. It wins
    // over a different-ID envelope until reconciliation removes it: a
    // stale pre-confirmation envelope must not shadow — or let the dock
    // claim over — the confirmed recreation. A same-ID envelope is the
    // server refreshing that workspace, with fresher status.
    const created = createdWorkspaceRef(identity);
    if (created && created.id !== override.deletedId) {
      return envelope && envelope.id === created.id ? envelope : created;
    }
    return envelope;
  }
  if (override) return override.ref;
  // No override: same resolution as controller-less views — the shared
  // created record wins until reconciled (a create that started in a
  // focus/mobile view publishes only there, and a stale envelope must not
  // shadow it after a layout switch), and session-deleted envelope IDs
  // stay masked.
  return resolveControllerlessWorkspaceRef(identity, envelopeRef);
}

function setClaim(surface: InlineWorkspaceSurface, identity: WorkspaceItemIdentity, ref: WorkspaceRefLite): void {
  const existing = claims[surface];
  if (existing && sameIdentity(existing.identity, identity) && sameRef(existing.ref, ref)) return;
  // Replacing one item's claim directly with another's (selection change whose
  // new detail already matches) never makes the workspace pane unavailable, so
  // the layout host's own zoom reconciliation never fires and the new item's
  // detail would open hidden behind a maximized terminal. Same-identity
  // re-asserts (a ref status change on the same workspace) must NOT un-zoom.
  if (existing && !sameIdentity(existing.identity, identity)) {
    unzoomWorkspacePane(surface);
    // Any deferred focus belonged to the workspace being left. The pool would
    // otherwise hand it to whatever mounts under that key next, pulling the
    // keyboard out of the item the user just opened.
    clearSessionFocusRequest();
  }
  claims = { ...claims, [surface]: { identity, ref } };
  workspaceIdentityById.set(ref.id, identity);
  lastInlineKey = { workspaceId: ref.id, hostKey: undefined };
}

function clearClaim(surface: InlineWorkspaceSurface): void {
  if (!claims[surface]) return;
  // A claim ending while maximized un-zooms here, synchronously, rather than
  // waiting for the layout host to notice the pane went away: a reclaim landing
  // in the same update (selection change to an item whose detail is already
  // cached) leaves no availability gap to notice, and setClaim then sees no
  // previous claim to detect the replacement — so the new item's detail would
  // open hidden behind a maximized terminal. It also covers the view
  // unmounting outright, where the host component's effects never run at all.
  unzoomWorkspacePane(surface);
  clearSessionFocusRequest();
  const next = { ...claims };
  delete next[surface];
  claims = next;
}

// WorkspaceHost calls this on every reactive pass while the terminal route
// is current, so `lastInlineKey` — desiredKey()'s fallback once the route
// leaves /terminal/{id} — never regresses to empty during the async gap
// between navigating to a claimable surface and that surface's claim
// effect actually resolving (it needs a loaded detail fetch first). Without
// this, desiredKey() would momentarily return the initial empty key,
// WorkspaceTerminalView would tear down the terminal for a falsy
// workspaceId, and the claim landing a moment later would rebuild it from
// scratch — a spurious reconnect on the very first inline claim of a
// workspace that was already open in the tab.
export function rememberTerminalRouteKey(key: HostedWorkspaceKey): void {
  lastInlineKey = key;
}

export function notifyWorkspaceDeleted(workspaceId: string, hostKey?: string, identity?: WorkspaceItemIdentity): void {
  discardSessionsWithPrefix(sessionHostPrefix(workspaceId, hostKey));
  // Inline claims only ever hold local workspaces (hostKey undefined);
  // fleet deletions can't match one.
  if (hostKey === undefined) {
    // Tombstone by remembered identity too, not just active claims: the
    // claim may already be released (its view unmounted) when the deletion
    // arrives from the Workspaces or terminal tab. The caller-supplied
    // identity covers workspaces that were never claimed inline at all
    // (opened only in the tab or deleted straight from the sidebar list),
    // where the remembered map has no entry either.
    const deadIdentities = new Map<string, WorkspaceItemIdentity>();
    if (identity) deadIdentities.set(identityKey(identity), identity);
    const remembered = workspaceIdentityById.get(workspaceId);
    if (remembered) deadIdentities.set(identityKey(remembered), remembered);
    for (const surface of Object.keys(claims) as InlineWorkspaceSurface[]) {
      const claim = claims[surface];
      if (claim?.ref.id !== workspaceId) continue;
      deadIdentities.set(identityKey(claim.identity), claim.identity);
      clearClaim(surface);
    }
    for (const [key, identity] of deadIdentities) {
      overrides = { ...overrides, [key]: { deletedId: workspaceId } };
      for (const listener of invalidationListeners) listener(identity);
    }
    workspaceIdentityById.delete(workspaceId);
    dropWorkspaceSessionPanes(workspaceId, hostKey);
    // The shared created-record (which detail instances without an inline
    // controller consult) must not keep advertising a deleted workspace —
    // and a create response for this ID still in flight must not
    // republish it when it lands.
    markWorkspaceIdDeleted(workspaceId);
    clearCreatedWorkspaceById(workspaceId);
  }
  if (lastInlineKey.workspaceId === workspaceId && lastInlineKey.hostKey === hostKey) {
    lastInlineKey = { workspaceId: "", hostKey: undefined };
  }
  // The Workspaces tab restores the last /terminal route; it must not
  // restore one whose workspace was just deleted.
  forgetWorkspaceRoute(workspaceId, hostKey);
  const route = getRoute();
  if (route.page === "terminal" && route.workspaceId === workspaceId && route.hostKey === hostKey) {
    replaceUrl("/workspaces");
  } else if (
    (route.page === "mobile-workspace-terminal" || route.page === "mobile-workspace-item") &&
    route.workspaceId === workspaceId &&
    route.hostKey === hostKey
  ) {
    replaceUrl("/m/workspaces");
  }
}

/**
 * Drop a deleted workspace's promoted panes from every surface's stored tree.
 *
 * Deletion is the ONE authoritative signal here. A session that stopped, exited, or
 * vanished during a reconnect is absent from the runtime in exactly the same way,
 * and keeping its placement is what lets a relaunch reappear in the pane the user
 * put it in - so absence must never drive this. A deleted workspace's panes, by
 * contrast, can never come back: the ID is gone, and leaving them stored would give
 * every surface a permanent pane rendering nothing.
 */
function dropWorkspaceSessionPanes(workspaceId: string, hostKey: string | undefined): void {
  for (const surface of INLINE_SURFACES) {
    const layout = getPaneLayoutStore(surface);
    for (const tabKey of [...layout.storedTabKeys()]) {
      if (sessionPaneKeyMatchesWorkspace(tabKey, workspaceId, hostKey)) layout.demoteTab(tabKey);
    }
  }
  const key = workspaceKeyString({ workspaceId, hostKey });
  for (const surface of INLINE_SURFACES) collapsedPaneKeys.delete(`${surface}\u0000${key}`);
  if (lastFocusedWorkspacePane[key] === undefined) return;
  const next = { ...lastFocusedWorkspacePane };
  delete next[key];
  lastFocusedWorkspacePane = next;
}

export function onIdentityInvalidated(cb: (identity: WorkspaceItemIdentity) => void): () => void {
  invalidationListeners.add(cb);
  return () => invalidationListeners.delete(cb);
}

export function registerHostElement(el: HTMLElement | null): void {
  hostEl = el;
}
export function registerParkingElement(el: HTMLElement | null): void {
  parkingEl = el;
}
export function registerSlotElement(slot: HostSlot, el: HTMLElement | null): void {
  // A targeted property write, not `slotEls = { ...slotEls, [slot]: el }`.
  // The attachment below calls this from inside a `{@attach}` effect (its
  // own mount/cleanup); a full-object reassignment reads every key via the
  // spread and then writes the same `slotEls` binding in one statement,
  // which Svelte treats as an effect that reads and writes the same state
  // and tears down/reattaches forever (effect_update_depth_exceeded). This
  // form still invalidates fine-grained readers of `slotEls[slot]`.
  slotEls[slot] = el;
}
export function getHostElement(): HTMLElement | null {
  return hostEl;
}
export function getParkingElement(): HTMLElement | null {
  return parkingEl;
}
export function getSlotElement(slot: HostSlot): HTMLElement | null {
  return slotEls[slot] ?? null;
}

export function requestHostFocus(): void {
  pendingHostFocus = true;
}

export function consumePendingHostFocus(): boolean {
  if (!pendingHostFocus) return false;
  pendingHostFocus = false;
  return true;
}

export function clearPendingHostFocus(): void {
  pendingHostFocus = false;
}

/** Called by the live view whenever its runtime sessions change. */
export function publishHostedSessions(key: HostedWorkspaceKey, sessions: readonly PublishedSession[]): void {
  const current = hostedSessions;
  if (
    current.key.workspaceId === key.workspaceId &&
    current.key.hostKey === key.hostKey &&
    current.sessions.length === sessions.length &&
    current.sessions.every(
      (session, index) =>
        session.paneKey === sessions[index]?.paneKey &&
        session.label === sessions[index]?.label &&
        session.hostKey === sessions[index]?.hostKey &&
        session.active === sessions[index]?.active,
    )
  ) {
    return;
  }
  hostedSessions = { key, sessions: [...sessions] };
}

/** The registry key of a promoted pane's session, or null when it is not hosted. */
export function hostedSessionRegistryKey(paneKey: string): string | null {
  return hostedSessions.sessions.find((session) => session.paneKey === paneKey)?.hostKey ?? null;
}

export function registerSessionPaneSnippet(snippet: Snippet<[{ paneKey: string; visible: boolean }]> | null): void {
  sessionPaneSnippet = snippet;
}

export interface HostedWorkspaceControls {
  snippet: Snippet;
  /**
   * Non-destructive actions shown in every pane that belongs to the workspace.
   * A promoted session pane still needs a direct route to launch another session,
   * even though it is not the leaf that owns workspace-level destructive actions.
   */
  paneActions?: Snippet;
  /**
   * Owner-only controls that sit in the workspace pane's tab strip rather than
   * behind the popover. Destructive actions must have one visible owner even when
   * a workspace is split across several session panes.
   */
  stripActions?: Snippet;
  /**
   * The workspace's collapsed terminal dock, for surfaces to anchor at their own
   * bottom edge.
   *
   * The dock normally lives inside the container pane, but that pane retires once
   * every session is promoted - and the dock going with it takes away the row the
   * user opens a terminal from. Rendered by the surface only in exactly that case
   * (`InlineWorkspaceController.dockRow`), so it is never on screen twice.
   */
  dockRow?: Snippet;
  /**
   * Whether the container has only its bottom dock left to render.
   *
   * The surface uses this transient fact to retire the container pane and render
   * the dock at its own bottom edge without changing the user's saved pane tree.
   */
  workspacePaneRowOnly?: boolean;
  /**
   * The workspace these controls act on. One embedded view serves every selection
   * on its surface, so the snippet identity survives a switch from one workspace
   * to another - an open popover has to close on this instead, or its buttons
   * silently start acting on a workspace the user did not open them for.
   */
  workspaceKey: string;
}

export function registerWorkspaceControls(controls: HostedWorkspaceControls | null): void {
  if (
    controls?.snippet === hostedControls?.snippet &&
    controls?.paneActions === hostedControls?.paneActions &&
    controls?.stripActions === hostedControls?.stripActions &&
    controls?.dockRow === hostedControls?.dockRow &&
    controls?.workspacePaneRowOnly === hostedControls?.workspacePaneRowOnly &&
    controls?.workspaceKey === hostedControls?.workspaceKey
  ) {
    return;
  }
  hostedControls = controls;
}

/** The hosted workspace's controls, or null when no embedded view is hosting one. */
export function hostedWorkspaceControls(): HostedWorkspaceControls | null {
  return hostedControls;
}

export function registerWorkspaceLauncher(open: (() => void) | null): void {
  launcherOpener = open;
}

/**
 * The hosted workspace's launcher, or null when this surface is not hosting one.
 *
 * Reveals the workspace pane before opening: the overlay is rendered by the
 * embedded view, so a pane that is collapsed, tabbed behind a sibling, or covered
 * by another leaf's zoom has nowhere to draw it, and the command would report
 * success while producing no UI at all.
 */
export function hostedWorkspaceLauncher(surface: InlineWorkspaceSurface): (() => void) | null {
  if (desiredSlot() !== surface) return null;
  const open = launcherOpener;
  if (open === null) return null;
  return () => {
    revealWorkspacePane(surface);
    open();
  };
}

export function setWorkspaceControlsBusy(workspaceKey: string, busy: boolean): void {
  if (busy) {
    hostedControlsBusy = workspaceKey;
    return;
  }
  if (hostedControlsBusy === workspaceKey) hostedControlsBusy = null;
}

/** True while one of the CURRENTLY registered controls has a write in flight. */
export function workspaceControlsBusy(): boolean {
  return hostedControlsBusy !== null && hostedControlsBusy === hostedControls?.workspaceKey;
}

/**
 * Record which of a workspace's panes the user is working in, reported by the view
 * that owns the surface's focus events.
 *
 * Only the container and this workspace's own session panes count: focus lands on
 * the conversation and the file list too, and treating those as "the terminal I was
 * in" would send an expand or a Focus Terminal to the wrong pane. The workspace is
 * read from the pane key itself rather than from the current claim, so a focus that
 * arrives while the surface is switching selections cannot be filed under the
 * wrong workspace.
 */
function notePaneFocused(surface: InlineWorkspaceSurface, tabKey: string): void {
  if (tabKey === WORKSPACE_PANE_KEY) {
    const key = desiredSlot() === surface ? desiredKey() : null;
    if (key === null || key.workspaceId === "") return;
    recordFocusedPane(workspaceKeyString(key), tabKey);
    return;
  }
  const ref = parseSessionPaneKey(tabKey);
  if (ref === null) return;
  recordFocusedPane(workspaceKeyString({ workspaceId: ref.workspaceId, hostKey: ref.hostKey }), tabKey);
}

function recordFocusedPane(workspaceKey: string, tabKey: string): void {
  // Repeat reports for the same pane are routine - the layout reports focus on
  // every activation - and rewriting the record would invalidate its readers for
  // nothing.
  if (lastFocusedWorkspacePane[workspaceKey] === tabKey) return;
  lastFocusedWorkspacePane = { ...lastFocusedWorkspacePane, [workspaceKey]: tabKey };
}

/**
 * Move focus into a promoted session's live terminal.
 *
 * The pool renders it outside the workspace host, so the host's parked-focus
 * handshake does not cover it: the wrapper is found through the registry key the
 * view published for that pane. False when the slot is not mounted yet, which is
 * the caller's cue that nothing was focused.
 */
function focusPromotedSession(paneKey: string): boolean {
  const hostKey = hostedSessionRegistryKey(paneKey);
  if (hostKey === null) return false;
  const slot = getSessionSlotElement(hostKey);
  const wrapper = slot?.querySelector<HTMLElement>(`[data-session-host="${hostKey}"]`) ?? null;
  if (wrapper !== null) {
    wrapper.focus();
    if (document.activeElement === wrapper) return true;
  }
  // Revealing the pane only queues the work: the slot mounts on the next flush and
  // the pool reparents the wrapper a frame after that, so the terminal is not
  // focusable yet. Hand the request to the pool, which consumes it the moment that
  // wrapper is attached and live.
  requestSessionFocus(hostKey);
  return false;
}

function promotableSessionsFor(surface: InlineWorkspaceSurface): readonly PromotableSession[] {
  // Only the surface actually hosting the workspace: there is one live terminal
  // per session, so a second surface claiming the same workspace could not render
  // it, and offering the pane there would give the user an empty one.
  if (desiredSlot() !== surface) return [];
  const key = desiredKey();
  const hosted = hostedSessions;
  if (hosted.key.workspaceId !== key.workspaceId || hosted.key.hostKey !== key.hostKey) return [];
  return hosted.sessions.map((session) => ({ paneKey: session.paneKey, label: session.label }));
}

/**
 * Whether the container pane has nothing left to render: it has sessions, and every
 * one of them is promoted into a pane of its own.
 *
 * Uses the same predicate the view masks with (a promoted pane in the stored tree,
 * hidden or not), so the tab and the container agree about what the container is
 * showing. Without this the surface keeps a pane whose whole body is empty - the
 * hole the user is left staring at after dragging the last session out.
 */
function workspacePaneEmptyFor(surface: InlineWorkspaceSurface): boolean {
  const sessions = promotableSessionsFor(surface);
  if (sessions.length === 0) return false;
  const layout = getPaneLayoutStore(surface);
  return sessions.every((session) => layout.hasTab(session.paneKey));
}

function workspacePaneRowOnlyFor(surface: InlineWorkspaceSurface): boolean {
  if (desiredSlot() !== surface) return false;
  const key = desiredKey();
  const workspaceKey = `${key.workspaceId}\u0000${key.hostKey ?? ""}`;
  return hostedControls?.workspaceKey === workspaceKey && hostedControls.workspacePaneRowOnly === true;
}

/**
 * What the container pane's tab is called: its sole session, when it is showing one
 * and nothing else.
 *
 * "Workspace" whenever the view keeps its own chrome, and it must agree with the
 * view about when that is - a tab named for the session above a header bar naming
 * the workspace is two answers to the same question. So a flattened surface, where
 * the view keeps its toolbar because the surface suppresses per-leaf actions, keeps
 * the neutral name too.
 */
function workspacePaneLabelFor(surface: InlineWorkspaceSurface): string {
  const sessions = promotableSessionsFor(surface);
  if (sessions.length !== 1) return "Workspace";
  if (getPaneLayoutStore(surface).paneRender()?.flattened === true) return "Workspace";
  const session = sessions[0]!;
  return workspacePaneKeysFor(surface).includes(session.paneKey) ? "Workspace" : session.label;
}

/**
 * The session a keyboard command promotes on `surface`: the one the workspace
 * pane is showing there.
 *
 * A palette command sees stores, not components, so the decision of which
 * session is current stays with the view that publishes it. Null whenever the
 * workspace pane is not on this surface, so the command cannot reach a terminal
 * the user is not looking at.
 */
export function activeHostedSession(surface: InlineWorkspaceSurface): PromotableSession | null {
  const promotable = promotableSessionsFor(surface);
  if (promotable.length === 0) return null;
  const active = hostedSessions.sessions.find((session) => session.active);
  if (active === undefined) return null;
  return promotable.find((session) => session.paneKey === active.paneKey) ?? null;
}

function slotAttachmentFor(slot: HostSlot): Attachment<HTMLElement> {
  return (node) => {
    registerSlotElement(slot, node);
    return () => {
      // Safety net: park before this slot element leaves the DOM.
      const parking = getParkingElement();
      const host = getHostElement();
      if (parking && host && host.parentElement === node) parking.appendChild(host);
      registerSlotElement(slot, null);
    };
  };
}

export const tabSlotAttachment: Attachment<HTMLElement> = slotAttachmentFor("tab");

const controllers = new Map<InlineWorkspaceSurface, InlineWorkspaceController>();

export function getInlineWorkspaceController(surface: InlineWorkspaceSurface): InlineWorkspaceController {
  const cached = controllers.get(surface);
  if (cached) return cached;
  const controller: InlineWorkspaceController = {
    surface,
    effectiveWorkspaceRef: (identity, envelopeRef) => effectiveRef(identity, envelopeRef),
    claim: (identity, ref) => setClaim(surface, identity, ref),
    release: () => clearClaim(surface),
    isClaimedFor: (identity) => {
      const claim = claims[surface];
      return !!claim && sameIdentity(claim.identity, identity);
    },
    recordCreated: (identity, ref) => {
      // A delayed create response can lose the race with its own deletion
      // (the workspace appeared in the Workspaces tab and was deleted
      // there before this response landed). Recording it would overwrite
      // the deletion tombstone and resurrect a dead ref. A recreation
      // always carries a fresh ID and supersedes the tombstone below.
      if (isWorkspaceIdDeleted(ref.id)) return;
      overrides = { ...overrides, [identityKey(identity)]: { ref, tick: nextWorkspaceLifecycleTick() } };
      workspaceIdentityById.set(ref.id, identity);
      const claim = claims[surface];
      // Refresh a matching live claim's ref, but never claim an unclaimed
      // surface from here: a create response can land after its component
      // unmounted and the user moved on, and claiming would activate the
      // surface and move the hosted key underneath whatever is displayed
      // now. The live list-view claim effect reads the override just
      // recorded (effectiveWorkspaceRef is reactive) and makes the claim
      // itself when its current selection confirms the identity.
      if (claim && sameIdentity(claim.identity, identity)) setClaim(surface, identity, ref);
    },
    recordDeleted: (identity, workspaceId) => {
      markWorkspaceIdDeleted(workspaceId);
      overrides = { ...overrides, [identityKey(identity)]: { deletedId: workspaceId } };
      const claim = claims[surface];
      if (claim && sameIdentity(claim.identity, identity)) clearClaim(surface);
    },
    reconcile: (identity, envelopeRef, envelopeTick) => {
      const key = identityKey(identity);
      const override = overrides[key];
      if (!override) return;
      // A tombstone reconciles when the envelope no longer carries the
      // deleted workspace — either absent, or a different ID (recreated).
      // A created override reconciles when the envelope confirms the same
      // workspace, or when any post-creation request reports something
      // else — absence (deleted by another client) or a replacement
      // workspace under a different ID (deleted and recreated). Stale
      // pre-create envelopes, null or different-ID, must not clear it.
      const agrees = isTombstone(override)
        ? envelopeRef == null || envelopeRef.id !== override.deletedId
        : (envelopeRef != null && envelopeRef.id === override.ref.id) ||
          (envelopeTick != null && envelopeTick > override.tick);
      if (!agrees) return;
      const next = { ...overrides };
      delete next[key];
      overrides = next;
    },
    getDockMode: () => dockModeFor(surface),
    setDockMode: (mode) => {
      // A modal frame is open: refuse to expand the dock over it. Any other
      // mode change (split/collapsed) is unaffected by the modal guard.
      if (mode === "expanded" && getStackDepth() > 0) return;
      if (dockModeFor(surface) === mode) return;
      const layout = getPaneLayoutStore(surface);
      const keys = workspacePaneKeysFor(surface);
      if (mode === "collapsed") {
        // Every pane of this workspace, or collapsing the dock would leave its
        // promoted terminals on screen while the button claims it is hidden.
        const hidden = layout.hiddenTabKeys();
        const hiding = keys.filter((key) => !hidden.includes(key));
        // Added to any record still outstanding rather than replacing it. The
        // container tab is shared, so another workspace's expand can put this one's
        // container back on screen while its promoted panes stay hidden; collapsing
        // again then only sees the container, and overwriting would drop the promoted
        // pane from the record that is the only way back to it. A record consumed by
        // a restore is gone, so a pane the user closed by hand afterwards still stays
        // closed.
        const ledgerKey = collapseLedgerKey(surface);
        const outstanding = collapsedPaneKeys.get(ledgerKey) ?? [];
        collapsedPaneKeys.set(ledgerKey, [...new Set([...outstanding, ...hiding])]);
        for (const key of hiding) layout.setHidden(key, true);
        return;
      }
      restoreCollapsedPanes(surface);
      if (mode === "expanded") {
        const leafID = layout.leafIDForTab(dockTargetPaneKey(surface));
        if (leafID !== null) layout.toggleZoom(leafID);
        return;
      }
      // Split means the workspace shares the surface with the detail, so no leaf
      // may hold a zoom — not just not this one. Revealing the pane under
      // someone else's maximized leaf would leave it invisible.
      layout.clearZoom();
    },
    notePaneFocused: (tabKey) => notePaneFocused(surface, tabKey),
    focusTerminal: () => {
      // A modal frame is open: leave the layout alone and don't pull
      // focus out of the dialog.
      if (getStackDepth() > 0) return;
      // The inverse of the Collapse Terminal the user just pressed: the panes it hid
      // are the panes that come back. Revealing only the remembered one returns an
      // empty container whenever this workspace's terminals were promoted into panes
      // of their own, because a container masks the sessions it has promoted.
      //
      // A ledger on record is reason enough, without waiting for the dock to still
      // report collapsed: the container tab is shared by every workspace on the
      // surface, so another workspace's expand unhides it and leaves this one reading
      // "split" with its promoted terminal still hidden behind an empty container.
      if (dockModeFor(surface) === "collapsed" || collapsedPaneKeys.has(collapseLedgerKey(surface))) {
        restoreCollapsedPanes(surface);
      }
      // Nothing to focus. A workspace with no session has no terminal, so revealing
      // its pane lands the user on an empty surface; the launcher is what they
      // actually need next.
      if (launcherOpener !== null && promotableSessionsFor(surface).length === 0) {
        revealWorkspacePane(surface);
        launcherOpener();
        return;
      }
      // Reveal without maximizing. "Collapsed" is not the only way to be
      // invisible: the pane can be tabbed behind a sibling or buried under
      // another leaf's zoom, and in both cases its portal slot is unmounted, so
      // focusing the parked host can never land. Maximizing over the detail
      // stays the terminal toolbar's own explicit action.
      const target = dockTargetPaneKey(surface);
      revealPane(surface, target);
      // An earlier request for some other session must not fire later on a reveal
      // the user did not ask it for.
      clearSessionFocusRequest();
      if (target !== WORKSPACE_PANE_KEY) {
        // The user's terminal is in a pane of its own, which the pool renders
        // outside the workspace host: focusing the host would land on the
        // container they left. Best-effort only - the promoted pane has no
        // parked-host handshake to fall back on, and its slot may still be
        // mounting - but the layout has already been told which pane is focused,
        // so the next reveal and the flattened strip agree with the request.
        focusPromotedSession(target);
        return;
      }
      // Best-effort direct attempt: works when the host is already
      // attached and visible (e.g. the dock was open in "split", where
      // its slot element is already mounted). Only falls back to the
      // pending-focus flag when that didn't land, so a successful direct
      // focus never leaves a stale flag armed for some later, unrelated
      // reveal to consume.
      const host = getHostElement();
      host?.focus();
      if (!host || document.activeElement !== host) {
        requestHostFocus();
      }
    },
    openInWorkspaces: (ref) => navigate(`/terminal/${ref.id}`),
    onIdentityInvalidated: (cb) => {
      const wrapped = (identity: WorkspaceItemIdentity) => cb(identity);
      invalidationListeners.add(wrapped);
      return () => invalidationListeners.delete(wrapped);
    },
    slotAttachment: slotAttachmentFor(surface),
    promotableSessions: () => promotableSessionsFor(surface),
    workspacePaneLabel: () => workspacePaneLabelFor(surface),
    workspacePaneEmpty: () => workspacePaneEmptyFor(surface),
    workspacePaneRowOnly: () => workspacePaneRowOnlyFor(surface),
    // Only while the container pane is retired: otherwise the dock is already on
    // screen inside it, and a second copy here would be two rows for one dock.
    dockRow: () =>
      desiredSlot() === surface && (workspacePaneEmptyFor(surface) || workspacePaneRowOnlyFor(surface))
        ? (hostedControls?.dockRow ?? null)
        : null,
    sessionPane: () => sessionPaneSnippet,
  };
  controllers.set(surface, controller);
  return controller;
}

export function resetWorkspaceHostForTest(): void {
  claims = {};
  overrides = {};
  // Dock mode lives in the pane layouts now, so resetting the host means
  // resetting those too or a zoomed/hidden pane leaks into the next test.
  resetPaneLayoutStoresForTest();
  lastInlineKey = { workspaceId: "", hostKey: undefined };
  hostEl = null;
  parkingEl = null;
  pendingHostFocus = false;
  for (const key of Object.keys(slotEls) as HostSlot[]) slotEls[key] = null;
  invalidationListeners.clear();
  workspaceIdentityById.clear();
  hostedSessions = { key: { workspaceId: "", hostKey: undefined }, sessions: [] };
  sessionPaneSnippet = null;
  hostedControls = null;
  hostedControlsBusy = null;
  launcherOpener = null;
  lastFocusedWorkspacePane = {};
  collapsedPaneKeys.clear();
}
