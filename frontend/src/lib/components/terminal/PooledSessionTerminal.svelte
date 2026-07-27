<script lang="ts">
  import { tick } from "svelte";
  import TerminalPane from "./TerminalPane.svelte";
  import type { MountedSession } from "../../stores/session-host.svelte.ts";

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

  // The wrapper is reparented out of this component's own fragment, so Svelte
  // cannot remove it on destroy. Without this an unmounted session leaves a dead
  // terminal sitting in whatever slot last held it.
  $effect(() => () => wrapper?.remove());
</script>

<div
  class="session-host-wrapper"
  data-session-host={session.hostKey}
  bind:this={wrapper}
  inert={!active || !attached}
>
  <TerminalPane
    websocketPath={session.websocketPath}
    reconnectOnExit={false}
    disabled={session.disabled ?? false}
    active={active && attached}
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
