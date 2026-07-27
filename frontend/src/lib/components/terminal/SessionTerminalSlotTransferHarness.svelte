<script lang="ts">
  import SessionTerminalSlot from "./SessionTerminalSlot.svelte";
  import type { SessionHostKey } from "../../stores/session-host.svelte.ts";

  interface Props {
    hostKey: SessionHostKey;
    showSource: boolean;
    showDestination: boolean;
    /**
     * Which conditional block comes first in the template.
     *
     * Svelte processes blocks in template order, so this decides whether the
     * departing slot's cleanup runs before or after the arriving slot registers
     * — the two orders a real promotion can take. Registration must survive
     * both, or the source's teardown clears the destination and the terminal
     * stays parked.
     */
    order: "source-first" | "destination-first";
  }

  let { hostKey, showSource, showDestination, order }: Props = $props();
</script>

{#snippet source()}
  {#if showSource}
    <div class="transfer-slot" data-slot="source">
      <SessionTerminalSlot {hostKey} visible={true} />
    </div>
  {/if}
{/snippet}

{#snippet destination()}
  {#if showDestination}
    <div class="transfer-slot" data-slot="destination">
      <SessionTerminalSlot {hostKey} visible={true} />
    </div>
  {/if}
{/snippet}

{#if order === "source-first"}
  {@render source()}
  {@render destination()}
{:else}
  {@render destination()}
  {@render source()}
{/if}

<style>
  .transfer-slot {
    display: flex;
    width: 400px;
    height: 300px;
  }
</style>
