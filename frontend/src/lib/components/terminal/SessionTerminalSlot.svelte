<script lang="ts">
  import {
    sessionSlotAttachment,
    setSessionSlotVisible,
    type SessionHostKey,
  } from "../../stores/session-host.svelte.ts";

  let node = $state<HTMLElement | null>(null);

  interface Props {
    hostKey: SessionHostKey;
    /**
     * Whether this slot is on screen. An inactive tab panel stays mounted under
     * `visibility: hidden`, so the pool cannot infer it from the slot existing —
     * and a terminal left active behind one competes for keystrokes with the
     * visible terminal.
     */
    visible: boolean;
  }

  let { hostKey, visible }: Props = $props();

  // Published separately from the element below: an attachment that read this
  // would re-run through its own cleanup on every tab switch, unregistering and
  // re-adopting a live terminal for a change that moved no DOM. Scoped to this
  // element so a slot superseded mid-promotion cannot hide the terminal now
  // showing somewhere else.
  $effect(() => {
    const el = node;
    if (el === null) return;
    setSessionSlotVisible(hostKey, el, visible);
    return () => setSessionSlotVisible(hostKey, el, false);
  });
</script>

<!-- Portal target only. The terminal itself lives in the app-level pool so it
     survives being promoted out of this tree and back. -->
<div
  class="session-terminal-slot"
  bind:this={node}
  {@attach sessionSlotAttachment(hostKey)}
></div>

<style>
  .session-terminal-slot {
    display: flex;
    flex: 1 1 auto;
    min-width: 0;
    min-height: 0;
  }
</style>
