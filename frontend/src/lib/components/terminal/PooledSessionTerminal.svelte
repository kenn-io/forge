<script lang="ts">
  import { tick } from "svelte";
  import TerminalPane from "./TerminalPane.svelte";
  import { focusIsSacred } from "./terminal-focus.ts";
  import { consumeSessionFocus, type MountedSession } from "../../stores/session-host.svelte.ts";

  interface Props {
    session: MountedSession;
    parking: HTMLElement | null;
    slotEl: HTMLElement | null;
    active: boolean;
    onExit: (code: number) => void;
  }

  let { session, parking, slotEl, active, onExit }: Props = $props();

  let wrapper = $state<HTMLElement | null>(null);
  let terminalPane = $state<TerminalPane | null>(null);
  // True only once the wrapper actually sits in its destination and the browser
  // has laid it out. Activating a terminal earlier makes the fit addon measure
  // the parking node and resize the real tmux pane to one row.
  let attached = $state(false);

  // The wrapper owns the keyboard when the last focus event landed inside it.
  // Focus events are the only reliable signal: a real pane move tears down the
  // source slot, which rips the focused textarea out of the DOM (no focusout
  // fires on removal) before this component's effects run, so sampling
  // document.activeElement at park time never sees the focus it must restore.
  // Ownership survives that silent loss and is revoked only when another
  // element actually claims focus. Not $state: decisions are made when
  // `attached`/`active` flip, which the focus effect already tracks.
  let ownsFocus = false;

  function handleDocumentFocusIn(event: FocusEvent): void {
    const node = wrapper;
    if (!node) return;
    ownsFocus = event.target instanceof Node && node.contains(event.target);
  }

  // Placement, mirroring WorkspaceHost's: park first, attach after a tick.
  // Parking rather than leaving the wrapper in place matters because the
  // previous slot may be unmounting in this same flush.
  //
  // Unlike WorkspaceHost this does not poll for non-zero geometry. The host has
  // to, because it moves a subtree into slots it knows nothing about, including
  // a display:none parking node. Here the slot itself reports whether it is on
  // screen, so waiting for pixels would only add a failure mode: a destination
  // that legitimately measures zero would keep the terminal inert forever.
  $effect(() => {
    const destination = slotEl;
    const node = wrapper;
    const park = parking;
    if (!node || !park) return;
    attached = false;
    park.appendChild(node);
    let cancelled = false;
    if (!destination || destination === park) {
      // Parked with nowhere to go: the pane closed — unless this park is the
      // transient first half of a cross-flush transfer whose destination
      // registers a moment later (a promotion can do this). Settle on the
      // same tick-then-frame cadence attachment uses before dropping
      // ownership, so focus a close took is never replayed on some later,
      // unrelated reveal, while a transfer mid-handoff keeps it.
      void (async () => {
        await tick();
        if (cancelled) return;
        requestAnimationFrame(() => {
          if (cancelled) return;
          ownsFocus = false;
        });
      })();
      return () => {
        cancelled = true;
      };
    }
    void (async () => {
      await tick();
      if (cancelled) return;
      destination.appendChild(node);
      requestAnimationFrame(() => {
        if (cancelled) return;
        attached = true;
      });
    })();
    return () => {
      cancelled = true;
    };
  });

  // Focus the terminal on an explicit request (whether it arrived before
  // attachment or after it was already visible), or to give back focus a
  // reparent took. Restoration is decided here, at attachment, and only into
  // unclaimed focus: anything else that took the keyboard since the reparent
  // keeps it, and an intent that was not honored now never fires later. The
  // renderer queues explicit requests through its async construction; the
  // wrapper is an immediate fallback while that work finishes.
  $effect(() => {
    if (!attached || !active) return;
    const node = wrapper;
    if (!node) return;
    const requested = consumeSessionFocus(session.hostKey);
    // A soft request is navigation asking, not the user: it loses to a sacred
    // focus target (form fields, dialogs) but wins over a plain button, the
    // same contract renderer autofocus follows at creation.
    const granted =
      requested === "explicit" || (requested === "soft" && !focusIsSacred(document.activeElement));
    const unclaimed = document.activeElement === null || document.activeElement === document.body;
    if (!granted && !(ownsFocus && unclaimed)) return;
    const focusAtDecision = document.activeElement;
    void (async () => {
      // The active/attached state that admitted this effect also removes
      // inert. Wait for that DOM update before asking xterm to focus; browsers
      // silently ignore focus() inside an inert subtree.
      await tick();
      if (!attached || !active || wrapper !== node || node.inert) return;
      if (
        requested !== "explicit" &&
        document.activeElement !== focusAtDecision &&
        (focusIsSacred(document.activeElement) ||
          (document.activeElement !== null && document.activeElement !== document.body))
      ) {
        return;
      }
      terminalPane?.focus();
      if (!node.contains(document.activeElement)) node.focus();
    })();
  });

  // The wrapper is reparented out of this component's own fragment, so Svelte
  // cannot remove it on destroy. Without this an unmounted session leaves a dead
  // terminal sitting in whatever slot last held it.
  $effect(() => () => wrapper?.remove());
</script>

<svelte:document onfocusin={handleDocumentFocusIn} />

<!-- tabindex, like the workspace host's own wrapper: Focus Terminal on a session
     the user promoted has to put the keyboard somewhere inside its terminal, and
     this wrapper is the only node the store can reach for a pooled session. -->
<div
  class="session-host-wrapper"
  data-session-host={session.hostKey}
  bind:this={wrapper}
  tabindex="-1"
  inert={!active || !attached}
>
  <TerminalPane
    bind:this={terminalPane}
    websocketPath={session.websocketPath}
    reconnectOnExit={false}
    disabled={session.disabled ?? false}
    active={active && attached}
    autoFocus={false}
    initialStatus={session.status}
    onExit={(code) => onExit(code)}
  />
</div>

<style>
  .session-host-wrapper {
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    min-height: 0;
  }
</style>
