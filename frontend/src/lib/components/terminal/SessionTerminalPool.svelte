<script lang="ts">
  import { getStores } from "../../context.js";
  import {
    getSessionSlotElement,
    isSessionClaimed,
    isSessionSlotVisible,
    mountedSessions,
    noteSessionConnection,
    noteSessionExited,
    registerSessionParking,
    setRetainedSessionLimit,
    type MountedSession,
    type SessionHostKey,
  } from "../../stores/session-host.svelte.ts";
  import PooledSessionTerminal from "./PooledSessionTerminal.svelte";

  const { settings: settingsStore } = getStores();

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

  $effect(() => {
    setRetainedSessionLimit(settingsStore.getTerminalSettings().retained_sessions ?? 10);
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

  function handleConnectionChange(session: MountedSession, connected: boolean): void {
    noteSessionConnection(session.hostKey, connected);
  }
</script>

<div class="session-pool-parking" bind:this={parkingNode} aria-hidden="true"></div>

{#each sessions as session (session.hostKey)}
  <PooledSessionTerminal
    {session}
    parking={parkingNode}
    slotEl={slotFor(session.hostKey)}
    active={activeFor(session.hostKey)}
    retained={!isSessionClaimed(session.hostKey)}
    onExit={(code) => handleExit(session, code)}
    onConnectionChange={(connected) => handleConnectionChange(session, connected)}
  />
{/each}

<style>
  .session-pool-parking {
    display: none;
  }
</style>
