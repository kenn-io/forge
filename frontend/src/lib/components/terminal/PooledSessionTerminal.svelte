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
  // True only once the wrapper actually sits in its destination and the browser
  // has laid it out. Activating a terminal earlier makes the fit addon measure
  // the parking node and resize the real tmux pane to one row.
  let attached = $state(false);
  // A terminal renderer focuses only when it is first created. The pool itself
  // mounts while this wrapper is still parked and inactive, so constructing the
  // renderer immediately would spend that one focus opportunity off-screen.
  // Latch this once the session first reaches a visible slot, then keep the
  // renderer mounted through every later park, promotion, and demotion.
  let terminalReady = $state(false);

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
    if (!destination || destination === park) return;
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

  // Focus a terminal that was asked for before it existed. Focus Terminal on a
  // promoted session reveals its pane and the pool mounts the wrapper a tick and a
  // frame later, by which time the request would otherwise be lost: focusing an
  // inert, still-parked wrapper does nothing at all.
  $effect(() => {
    if (!attached || !active) return;
    const node = wrapper;
    if (!node || !consumeSessionFocus(session.hostKey)) return;
    node.focus();
  });

  $effect(() => {
    if (attached && active) terminalReady = true;
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
  {#if terminalReady}
    <TerminalPane
      websocketPath={session.websocketPath}
      reconnectOnExit={false}
      disabled={session.disabled ?? false}
      active={active && attached}
      initialStatus={session.status}
      onExit={(code) => onExit(code)}
    />
  {/if}
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
