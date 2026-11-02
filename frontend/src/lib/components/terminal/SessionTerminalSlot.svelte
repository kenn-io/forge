<script lang="ts">
  import { Spinner } from "@kenn-io/kit-ui";
  import {
    isSessionMounted,
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
     survives being promoted out of this tree and back. Until the owning view
     mounts the session there, for instance while it waits for a runtime read,
     the slot says so instead of sitting blank. -->
<div class="session-terminal-frame">
  <div
    class="session-terminal-slot"
    bind:this={node}
    {@attach sessionSlotAttachment(hostKey)}
  ></div>
  {#if !isSessionMounted(hostKey)}
    <div class="session-terminal-loading" role="status">
      <Spinner size={18} label="" />
      <span>Loading session…</span>
    </div>
  {/if}
</div>

<style>
  .session-terminal-frame {
    position: relative;
    display: flex;
    flex: 1 1 auto;
    min-width: 0;
    min-height: 0;
  }
  .session-terminal-slot {
    display: flex;
    flex: 1 1 auto;
    min-width: 0;
    min-height: 0;
  }
  /* Appears only once a load takes long enough to notice, so a fast mount
     does not flash. */
  .session-terminal-loading {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    animation: session-loading-appear 150ms ease-out 400ms both;
  }
  @keyframes session-loading-appear {
    from { opacity: 0; }
    to { opacity: 1; }
  }
  @media (prefers-reduced-motion: reduce) {
    .session-terminal-loading { animation-duration: 0s; }
  }
</style>
