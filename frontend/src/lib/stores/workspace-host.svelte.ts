import type { Attachment } from "svelte/attachments";
import type {
  InlineDockMode,
  InlineWorkspaceController,
  InlineWorkspaceSurface,
  WorkspaceItemIdentity,
  WorkspaceRefLite,
} from "@middleman/ui";
import { canonicalItemType } from "@middleman/ui";
import { canonicalProvider, resolvedPlatformHost } from "@middleman/ui/api/provider-routes";
import {
  clearCreatedWorkspaceById,
  createdWorkspaceRef,
  nextWorkspaceLifecycleTick,
} from "@middleman/ui/stores/workspace-create-pending";
import { getStackDepth } from "@middleman/ui/stores/keyboard/modal-stack";
import { forgetWorkspaceRoute, getRoute, navigate } from "./router.svelte.ts";

export type HostedWorkspaceKey = { workspaceId: string; hostKey: string | undefined };
export type HostSlot = "tab" | InlineWorkspaceSurface;

type InlineClaim = { identity: WorkspaceItemIdentity; ref: WorkspaceRefLite };

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
let dockModes = $state<Record<InlineWorkspaceSurface, InlineDockMode>>({
  activity: "split",
  prs: "split",
  issues: "split",
});
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
// Claims are released when their view unmounts, but a deletion can arrive
// later from an unrelated surface (Workspaces tab, terminal tab). Without
// this map the deletion would find no claim to tombstone, and the stale
// detail envelope cached by the list view would re-claim the dead workspace
// on the next visit until a refetch. Deliberately kept after release;
// entries are dropped on deletion or test reset.
const workspaceIdentityById = new Map<string, WorkspaceItemIdentity>();

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

export function isHostVisible(): boolean {
  const slot = desiredSlot();
  if (slot === null) return false;
  if (slot === "tab") return true;
  return dockModes[slot] !== "collapsed";
}

function effectiveRef(
  identity: WorkspaceItemIdentity,
  envelopeRef: WorkspaceRefLite | null | undefined,
): WorkspaceRefLite | null {
  const override = overrides[identityKey(identity)];
  if (isTombstone(override)) {
    if (envelopeRef && envelopeRef.id !== override.deletedId) return envelopeRef;
    // A controller-less recreation (focus/mobile create after the delete)
    // records only the shared created entry, under a fresh ID. The
    // tombstone masks its own deleted ID, not the recreation — hiding it
    // would re-offer "Create Workspace" for a workspace that exists.
    const created = createdWorkspaceRef(identity);
    if (created && created.id !== override.deletedId) return created;
    return null;
  }
  if (override) return override.ref;
  // Shared created-record fallback: a create that started in a
  // controller-less view (focus/mobile) publishes only there — if the
  // layout switched to an inline surface before the response landed,
  // no controller recordCreated ever ran, and without this fallback the
  // inline view would re-offer "Create Workspace" for a workspace that
  // exists. Deletions clear the record by ID, so a tombstoned identity
  // cannot resurface through it.
  return envelopeRef ?? createdWorkspaceRef(identity) ?? null;
}

function setClaim(surface: InlineWorkspaceSurface, identity: WorkspaceItemIdentity, ref: WorkspaceRefLite): void {
  const existing = claims[surface];
  if (existing && sameIdentity(existing.identity, identity) && sameRef(existing.ref, ref)) return;
  // Replacing one item's claim directly with another's (selection change
  // whose new detail already matches) never gives WorkspaceDockPanel an
  // inactive gap, so its reset-on-inactive effect can't run; without this
  // the new item's detail would open hidden behind a fullscreen terminal.
  // Same-identity re-asserts (ref status changes) must not collapse an
  // expanded dock.
  if (existing && !sameIdentity(existing.identity, identity) && dockModes[surface] === "expanded") {
    dockModes = { ...dockModes, [surface]: "split" };
  }
  claims = { ...claims, [surface]: { identity, ref } };
  workspaceIdentityById.set(ref.id, identity);
  lastInlineKey = { workspaceId: ref.id, hostKey: undefined };
}

function clearClaim(surface: InlineWorkspaceSurface): void {
  if (!claims[surface]) return;
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
    // The shared created-record (which detail instances without an inline
    // controller consult) must not keep advertising a deleted workspace.
    clearCreatedWorkspaceById(workspaceId);
  }
  if (lastInlineKey.workspaceId === workspaceId && lastInlineKey.hostKey === hostKey) {
    lastInlineKey = { workspaceId: "", hostKey: undefined };
  }
  // The Workspaces tab restores the last /terminal route; it must not
  // restore one whose workspace was just deleted.
  forgetWorkspaceRoute(workspaceId, hostKey);
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
    getDockMode: () => dockModes[surface],
    setDockMode: (mode) => {
      // A modal frame is open: refuse to expand the dock over it. Any other
      // mode change (split/collapsed) is unaffected by the modal guard.
      if (mode === "expanded" && getStackDepth() > 0) return;
      if (dockModes[surface] === mode) return;
      dockModes = { ...dockModes, [surface]: mode };
    },
    focusTerminal: () => {
      // A modal frame is open: leave the layout alone and don't pull
      // focus out of the dialog.
      if (getStackDepth() > 0) return;
      // Reveal without maximizing: a collapsed dock reopens in split —
      // the same layout the workspace first appeared in — and a dock
      // already visible keeps its mode. Maximizing over the detail is
      // the terminal toolbar's own expand action, never a side effect
      // of asking for focus.
      if (dockModes[surface] === "collapsed") {
        controller.setDockMode("split");
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
  };
  controllers.set(surface, controller);
  return controller;
}

export function resetWorkspaceHostForTest(): void {
  claims = {};
  overrides = {};
  dockModes = { activity: "split", prs: "split", issues: "split" };
  lastInlineKey = { workspaceId: "", hostKey: undefined };
  hostEl = null;
  parkingEl = null;
  pendingHostFocus = false;
  for (const key of Object.keys(slotEls) as HostSlot[]) slotEls[key] = null;
  invalidationListeners.clear();
  workspaceIdentityById.clear();
}
