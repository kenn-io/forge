<script lang="ts">
  import {
    getSessionSlotElement,
    isSessionSlotVisible,
    mountedSessions,
    noteSessionExited,
    registerSessionParking,
    type MountedSession,
    type SessionHostKey,
  } from "../../stores/session-host.svelte.ts";
  import PooledSessionTerminal from "./PooledSessionTerminal.svelte";

  // Every workspace's live session terminals, rendered once here and reparented
  // into whichever slot shows them. This has to sit OUTSIDE the reparented
  // workspace wrapper: the wrapper is what gets parked when the workspace pane
  // closes, and a session promoted to its own detail pane must outlive that.
  let parkingNode = $state<HTMLElement | null>(null);

  const sessions = $derived(mountedSessions());

  $effect(() => {
    registerSessionParking(parkingNode);
    return () => registerSessionParking(null);
  });

  function slotFor(hostKey: SessionHostKey): HTMLElement | null {
    return getSessionSlotElement(hostKey);
  }

  function activeFor(hostKey: SessionHostKey): boolean {
    // Only the slot knows whether the session is on screen: an inactive tab
    // panel keeps its slot mounted under visibility:hidden.
    return isSessionSlotVisible(hostKey);
  }

  function handleExit(session: MountedSession, code: number): void {
    noteSessionExited(session.hostKey, code);
  }
</script>

<div class="session-pool-parking" bind:this={parkingNode} aria-hidden="true"></div>

{#each sessions as session (session.hostKey)}
  <PooledSessionTerminal
    {session}
    parking={parkingNode}
    slotEl={slotFor(session.hostKey)}
    active={activeFor(session.hostKey)}
    onExit={(code) => handleExit(session, code)}
  />
{/each}

<style>
  .session-pool-parking {
    display: none;
  }
</style>
