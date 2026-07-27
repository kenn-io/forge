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
  // Revealed only once the wrapper sits in its destination with non-zero
  // geometry, the same gate WorkspaceHost uses: terminal panes size themselves
  // on activation and a zero-height reveal renders a one-row terminal.
  let attached = $state(false);

  // Placement, mirroring WorkspaceHost's: park first, attach after a tick, then
  // reveal on real geometry. Parking rather than leaving the wrapper in place
  // matters because the previous slot may be unmounting in this same flush.
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
      const reveal = () => {
        if (cancelled) return;
        if (node.getBoundingClientRect().height > 0) {
          attached = true;
          return;
        }
        requestAnimationFrame(reveal);
      };
      requestAnimationFrame(reveal);
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
