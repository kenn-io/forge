<script lang="ts">
  import { Chip } from "@kenn-io/kit-ui";

  import {
    hasMergeQueuePosition,
    mergeQueueEntryDescription,
    mergeQueueRemovalDescription,
    mergeQueueState,
    type MergeQueueEntry,
    type MergeQueueRemoval,
  } from "./merge-queue.js";

  interface Props {
    entry?: MergeQueueEntry | undefined;
    removal?: MergeQueueRemoval | undefined;
  }

  const { entry, removal }: Props = $props();

  const state = $derived(entry ? mergeQueueState(entry.state) : undefined);
  const entryLabel = $derived.by(() => {
    if (!entry || !state) return "";
    const base = hasMergeQueuePosition(entry) ? `Queued #${entry.position}` : "Queued";
    return entry.state.trim().toLowerCase() === "queued" ? base : `${base} · ${state.label}`;
  });
  const removalTitle = $derived(removal ? mergeQueueRemovalDescription(removal) : undefined);
</script>

{#if entry && state}
  <Chip
    size="sm"
    tone={state.tone}
    class="merge-queue-chip"
    title={mergeQueueEntryDescription(entry)}
  >
    {entryLabel}
  </Chip>
{:else if removal}
  <Chip size="sm" tone="danger" class="merge-queue-chip" title={removalTitle}>
    Removed from queue
  </Chip>
{/if}
