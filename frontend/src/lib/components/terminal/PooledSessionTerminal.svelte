<script lang="ts">
  import { tick } from "svelte";
  import TerminalPane from "./TerminalPane.svelte";
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

  // Parking to move between slots blurs xterm's helper textarea (the wrapper
  // passes through a display:none node and goes inert), so a pane move would
  // silently take the keyboard away from a terminal the user was typing in.
  // Remember that the wrapper held focus and give it back after attachment.
  // Not $state: the focus effect below already reruns on `attached`.
  let restoreFocus = false;

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
    if (node.contains(document.activeElement)) restoreFocus = true;
    park.appendChild(node);
    if (!destination || destination === park) {
      // Parked with nowhere to go: the pane closed. Like the workspace host's
      // clearPendingHostFocus, the intent must not fire on some later,
      // unrelated reveal.
      restoreFocus = false;
      return;
    }
    let cancelled = false;
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
  // attachment or after it was already visible) or to give back focus a
  // reparent took. The renderer queues the request through its async
  // construction; the wrapper is an immediate fallback while that work
  // finishes.
  $effect(() => {
    if (!attached || !active) return;
    const node = wrapper;
    if (!node) return;
    const requested = consumeSessionFocus(session.hostKey);
    if (!requested && !restoreFocus) return;
    restoreFocus = false;
    terminalPane?.focus();
    if (!node.contains(document.activeElement)) node.focus();
  });

  // The wrapper is reparented out of this component's own fragment, so Svelte
  // cannot remove it on destroy. Without this an unmounted session leaves a dead
  // terminal sitting in whatever slot last held it.
  $effect(() => () => wrapper?.remove());
</script>

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
